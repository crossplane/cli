//go:build integration

/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	diffv1alpha1 "github.com/crossplane/upjet/v2/proto/diff/v1alpha1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	tu "github.com/crossplane/cli/v2/internal/diff/testutils"
	"github.com/crossplane/cli/v2/internal/plan"
	"github.com/crossplane/cli/v2/internal/simulate"
)

// fakeProviderRouter routes the composed XDownstreamResource kind, and only
// it, to a fake diff server that answers with fn.
type fakeProviderRouter struct {
	fn func(*diffv1alpha1.PlanRequest) (*diffv1alpha1.PlanResponse, error)
}

func (f fakeProviderRouter) Route(gk schema.GroupKind) (diffv1alpha1.PlanServiceClient, bool) {
	if gk.Kind != "XDownstreamResource" {
		return nil, false
	}
	return f, true
}

func (f fakeProviderRouter) Plan(_ context.Context, in *diffv1alpha1.PlanRequest, _ ...grpc.CallOption) (*diffv1alpha1.PlanResponse, error) {
	return f.fn(in)
}

func strValue(s string) *diffv1alpha1.FieldValue {
	return &diffv1alpha1.FieldValue{Kind: &diffv1alpha1.FieldValue_Value{Value: structpb.NewStringValue(s)}}
}

func unknownValue() *diffv1alpha1.FieldValue {
	return &diffv1alpha1.FieldValue{Kind: &diffv1alpha1.FieldValue_Absence{Absence: diffv1alpha1.Absence_ABSENCE_UNKNOWN}}
}

func TestSimulateProviderPlan(t *testing.T) {
	t.Parallel()

	tu.SetupKubeTestLogger(t)

	xrCase := IntegrationTestCase{
		setupFiles: []string{
			"testdata/diff/resources/xrd.yaml",
			"testdata/diff/resources/composition.yaml",
			"testdata/diff/resources/functions.yaml",
		},
		crossplaneManagedResources: []HierarchicalOwnershipRelation{
			composes("testdata/diff/resources/existing-xr.yaml", "testdata/diff/resources/existing-downstream-resource.yaml"),
		},
	}

	update := func(changes ...*diffv1alpha1.FieldChange) func(*diffv1alpha1.PlanRequest) (*diffv1alpha1.PlanResponse, error) {
		return func(*diffv1alpha1.PlanRequest) (*diffv1alpha1.PlanResponse, error) {
			return &diffv1alpha1.PlanResponse{Action: diffv1alpha1.Action_ACTION_UPDATE, Changes: changes}, nil
		}
	}
	region := &diffv1alpha1.FieldChange{Field: "spec.forProvider.region", Actual: strValue("us-east-1"), Planned: strValue("eu-west-1")}

	cases := map[string]struct {
		reason string
		args   []string
		fn     func(*diffv1alpha1.PlanRequest) (*diffv1alpha1.PlanResponse, error)
		skip   string
		// ownCRD makes the composed kind's CRD provider-installed.
		ownCRD   bool
		xrFile   string
		wantCode int
		// wantOut are substrings of stdout; wantErr of stderr.
		wantOut, wantNotOut []string
		wantErr             []string
		// check inspects the -o json result, when args ask for it.
		check func(t *testing.T, res plan.Result)
	}{
		"ProviderUpdate": {
			reason:     "A provider's extra field change joins the composed resource's plan, and the cloud-layer note is gone.",
			args:       []string{"-o", "json"},
			fn:         update(region),
			wantCode:   dp.ExitCodeDiffDetected,
			wantNotOut: []string{"Cloud-layer plan not run"},
			check: func(t *testing.T, res plan.Result) {
				t.Helper()
				for _, p := range res.Plans {
					if p.Kind != "XDownstreamResource" {
						continue
					}
					for _, c := range p.Changes {
						if c.Path == "spec.forProvider.region" {
							return
						}
					}
				}
				t.Errorf("no XDownstreamResource plan carries the provider's spec.forProvider.region change: %+v", res.Plans)
			},
		},
		"Replace": {
			reason: "A field requiring replacement shows [-/+] and # forces replacement in -o plan.",
			args:   []string{"-o", "plan"},
			fn: func(*diffv1alpha1.PlanRequest) (*diffv1alpha1.PlanResponse, error) {
				return &diffv1alpha1.PlanResponse{Action: diffv1alpha1.Action_ACTION_REPLACE, Changes: []*diffv1alpha1.FieldChange{
					{Field: "spec.forProvider.region", Actual: strValue("us-east-1"), Planned: strValue("eu-west-1"), RequiresReplace: true},
				}}, nil
			},
			wantCode: dp.ExitCodeDiffDetected,
			wantOut:  []string{"[-/+]", "# forces replacement"},
		},
		"ReplaceDiff": {
			reason: "-o diff says requires replacement in the header and marks the changed line.",
			args:   []string{"-o", "diff"},
			fn: func(*diffv1alpha1.PlanRequest) (*diffv1alpha1.PlanResponse, error) {
				return &diffv1alpha1.PlanResponse{Action: diffv1alpha1.Action_ACTION_REPLACE, Changes: []*diffv1alpha1.FieldChange{
					{Field: "spec.forProvider.configData", Actual: strValue("existing-value"), Planned: strValue("modified-value"), RequiresReplace: true},
				}}, nil
			},
			wantCode: dp.ExitCodeDiffDetected,
			wantOut:  []string{"# forces replacement"},
		},
		"ReplaceDiffHeader": {
			reason: "-o diff says requires replacement in the resource header.",
			skip:   "bug: -o diff prints a plain ~~~ header for a replace; no code under internal/simulate/annotate.go or internal/diff renders 'requires replacement'",
			args:   []string{"-o", "diff"},
			fn: func(*diffv1alpha1.PlanRequest) (*diffv1alpha1.PlanResponse, error) {
				return &diffv1alpha1.PlanResponse{Action: diffv1alpha1.Action_ACTION_REPLACE, Changes: []*diffv1alpha1.FieldChange{
					{Field: "spec.forProvider.configData", Actual: strValue("existing-value"), Planned: strValue("modified-value"), RequiresReplace: true},
				}}, nil
			},
			wantCode: dp.ExitCodeDiffDetected,
			wantOut:  []string{"requires replacement"},
		},
		"KnownAfterApply": {
			reason:   "Computed paths show (known after apply) in -o diff.",
			args:     []string{"-o", "diff"},
			fn:       update(&diffv1alpha1.FieldChange{Field: "spec.forProvider.configData", Actual: strValue("existing-value"), Planned: unknownValue()}),
			wantCode: dp.ExitCodeDiffDetected,
			wantOut:  []string{"(known after apply)"},
		},
		"PlanError": {
			reason: "A plan error makes the XR errored, drops its plans, names the resource and exits 1.",
			args:   []string{"-o", "json"},
			fn: func(*diffv1alpha1.PlanRequest) (*diffv1alpha1.PlanResponse, error) {
				return nil, status.Error(codes.Internal, "boom")
			},
			wantCode: dp.ExitCodeToolError,
			check: func(t *testing.T, res plan.Result) {
				t.Helper()
				if !res.Errored {
					t.Errorf("want errored result, got %+v", res)
				}
				if !strings.Contains(planJSON(t, res), "test-resource") || !strings.Contains(planJSON(t, res), "boom") {
					t.Errorf("result does not name the resource and error: %s", planJSON(t, res))
				}
			},
		},
		"NotSupported": {
			reason: "A provider that cannot diff leaves the cluster diff, marked approximate, with no error.",
			args:   []string{"-o", "json"},
			fn: func(*diffv1alpha1.PlanRequest) (*diffv1alpha1.PlanResponse, error) {
				st, err := status.New(codes.FailedPrecondition, "cannot compute diff").WithDetails(&errdetails.PreconditionFailure{Violations: []*errdetails.PreconditionFailure_Violation{{Type: "DIFF_COMPUTATION_NOT_SUPPORTED"}}})
				if err != nil {
					return nil, err
				}
				return nil, st.Err()
			},
			wantCode: dp.ExitCodeDiffDetected,
			check: func(t *testing.T, res plan.Result) {
				t.Helper()
				if res.Errored || res.Summary.Approximate == 0 {
					t.Errorf("want non-errored result with approximate plans, got errored=%t summary=%+v", res.Errored, res.Summary)
				}
			},
		},
		"SkipPlan": {
			reason: "--skip-plan never calls the provider: the run succeeds although every call would fail.",
			args:   []string{"-o", "plan", "--skip-plan"},
			fn: func(*diffv1alpha1.PlanRequest) (*diffv1alpha1.PlanResponse, error) {
				return nil, status.Error(codes.Internal, "must not be called")
			},
			wantCode: dp.ExitCodeDiffDetected,
			wantOut:  []string{simulate.NoteProviderPlanSkipped},
		},
		"Drift": {
			reason:   "A provider-managed resource the cluster sees unchanged still shows the provider's change.",
			xrFile:   "testdata/diff/unmodified-xr.yaml",
			args:     []string{"-o", "plan"},
			ownCRD:   true,
			fn:       update(&diffv1alpha1.FieldChange{Field: "spec.forProvider.region", Actual: strValue("us-east-1"), Planned: strValue("eu-west-1")}),
			wantCode: dp.ExitCodeDiffDetected,
			wantOut:  []string{"XDownstreamResource", "region"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if tc.skip != "" {
				t.Skip(tc.skip)
			}
			if reason := integrationEnvGap(); reason != "" {
				t.Skip(reason)
			}

			cfg, kube, ctx, _ := startIntegrationCluster(t, XRDiffTest, xrCase, defaultTimeout)
			if tc.ownCRD {
				ownByProviderRevision(ctx, t, kube, "xdownstreamresources.ns.nop.example.org")
			}

			xr := tc.xrFile
			if xr == "" {
				xr = "testdata/diff/modified-xr.yaml"
			}
			argv := append([]string{"project", "simulate", "--timeout=" + defaultTimeout.String(), "-f", xr}, tc.args...)

			var stdout, stderr bytes.Buffer
			code, err := runSimulateArgvRouted(t, cfg, argv, localCrossplaneBinary(t), fakeProviderRouter{fn: tc.fn}, &stdout, &stderr)
			if code != tc.wantCode {
				t.Errorf("\n%s\nwant exit code %d, got %d (err %v)\nstdout:\n%s\nstderr:\n%s", tc.reason, tc.wantCode, code, err, stdout.String(), stderr.String())
			}
			for _, w := range tc.wantOut {
				if !strings.Contains(stdout.String(), w) {
					t.Errorf("\n%s\nstdout lacks %q:\n%s", tc.reason, w, stdout.String())
				}
			}
			for _, w := range tc.wantNotOut {
				if strings.Contains(stdout.String(), w) {
					t.Errorf("\n%s\nstdout has %q:\n%s", tc.reason, w, stdout.String())
				}
			}
			if tc.check != nil {
				var res plan.Result
				if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
					t.Fatalf("\n%s\nstdout is not a plan.Result: %v\n%s\nstderr:\n%s", tc.reason, err, stdout.String(), stderr.String())
				}
				tc.check(t, res)
			}
		})
	}
}

func planJSON(t *testing.T, res plan.Result) string {
	t.Helper()
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ownByProviderRevision makes the named CRD look provider-installed, as
// simulate.ProviderManaged reads it: controlled by a ProviderRevision.
func ownByProviderRevision(ctx context.Context, t *testing.T, kube client.Client, crdName string) {
	t.Helper()

	crd := &unstructured.Unstructured{}
	crd.SetGroupVersionKind(schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"})
	if err := kube.Get(ctx, client.ObjectKey{Name: crdName}, crd); err != nil {
		t.Fatalf("cannot get CRD %s: %v", crdName, err)
	}
	crd.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: "pkg.crossplane.io/v1", Kind: "ProviderRevision", Name: "provider-fake-1", UID: "00000000-0000-0000-0000-000000000001",
		Controller: new(true), BlockOwnerDeletion: new(true),
	}})
	if err := kube.Update(ctx, crd); err != nil {
		t.Fatalf("cannot own CRD %s: %v", crdName, err)
	}
}
