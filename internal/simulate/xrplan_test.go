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

package simulate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	diffv1alpha1 "github.com/crossplane/upjet/v2/proto/diff/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	dt "github.com/crossplane/cli/v2/internal/diff/result"
	"github.com/crossplane/cli/v2/internal/plan"
)

// funcPlanClient answers Plan per resource name.
type funcPlanClient struct {
	fakePlanClient
	fail map[string]bool
}

func (f *funcPlanClient) Plan(_ context.Context, in *diffv1alpha1.PlanRequest, _ ...grpc.CallOption) (*diffv1alpha1.PlanResponse, error) {
	if f.fail[in.GetDesiredResource().GetFields()["metadata"].GetStructValue().GetFields()["name"].GetStringValue()] {
		return nil, status.Error(codes.Internal, "boom")
	}
	return f.rsp, nil
}

func added(r *unstructured.Unstructured) *dt.ResourceDiff {
	d := &dt.ResourceDiff{Gvk: vpcGVK, DiffType: dt.DiffTypeAdded}
	d.Desired.Raw = r
	return d
}

func TestXRPlanner(t *testing.T) {
	s := runtime.NewScheme()
	s.AddKnownTypeWithName(vpcGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(vpcGVK.GroupVersion().WithKind("VPCList"), &unstructured.UnstructuredList{})
	rm := meta.NewDefaultRESTMapper([]schema.GroupVersion{vpcGVK.GroupVersion()})
	rm.Add(vpcGVK, meta.RESTScopeRoot)

	pc := &funcPlanClient{
		fakePlanClient: fakePlanClient{rsp: &diffv1alpha1.PlanResponse{Action: diffv1alpha1.Action_ACTION_CREATE}},
		fail:           map[string]bool{"bad-a": true, "bad-b": true},
	}
	planner := NewPlanner(fake.NewClientBuilder().WithScheme(s).Build(), rm, "default", &fakeRouter{client: pc}, time.Second)

	removed := added(vpc("gone", "10.0.0.0/16"))
	removed.DiffType = dt.DiffTypeRemoved
	nilDesired := &dt.ResourceDiff{Gvk: vpcGVK, DiffType: dt.DiffTypeAdded}

	cases := map[string]struct {
		diffs       map[string]*dt.ResourceDiff
		ensureErr   error
		wantErr     string
		wantPlans   []string
		wantEnsured []string
	}{
		"AllOK": {
			diffs:       map[string]*dt.ResourceDiff{"a": added(vpc("a", "10.0.0.0/16")), "b": added(vpc("b", "10.0.0.0/16"))},
			wantPlans:   []string{"a", "b"},
			wantEnsured: []string{"a", "b"},
		},
		"OnePlanErrors": {
			diffs:       map[string]*dt.ResourceDiff{"a": added(vpc("a", "10.0.0.0/16")), "bad-a": added(vpc("bad-a", "10.0.0.0/16"))},
			wantErr:     "cannot plan 1 resources with their diff servers: VPC/bad-a: ",
			wantEnsured: []string{"a", "bad-a"},
		},
		"ErrorsAreSorted": {
			diffs:       map[string]*dt.ResourceDiff{"bad-b": added(vpc("bad-b", "10.0.0.0/16")), "bad-a": added(vpc("bad-a", "10.0.0.0/16"))},
			wantErr:     "cannot plan 2 resources",
			wantEnsured: []string{"bad-a", "bad-b"},
		},
		"EnsureError": {
			diffs:       map[string]*dt.ResourceDiff{"a": added(vpc("a", "10.0.0.0/16"))},
			ensureErr:   errors.New("pull failed"),
			wantErr:     "cannot start provider diff servers: pull failed",
			wantEnsured: []string{"a"},
		},
		"SkipsRemovedAndNilDesired": {
			diffs:       map[string]*dt.ResourceDiff{"r": removed, "n": nilDesired, "a": added(vpc("a", "10.0.0.0/16"))},
			wantPlans:   []string{"a"},
			wantEnsured: []string{"a"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var ensured []string
			var ensureCtx context.Context
			ensure := func(ctx context.Context, rs []*unstructured.Unstructured) error {
				ensureCtx := ctx
				for _, r := range rs {
					ensured = append(ensured, r.GetName())
				}
				return tc.ensureErr
			}
			// An earlier XR's plans stay whatever this XR does.
			x := NewXRPlanner(planner, nil, ensure)
			x.plans["earlier"] = &plan.ResourcePlan{Name: "earlier"}

			err := x.AfterXRDiff(context.Background(), tc.diffs)

			if tc.wantErr == "" && err != nil {
				t.Fatalf("AfterXRDiff(...): unexpected error %v", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("AfterXRDiff(...): want error containing %q, got %v", tc.wantErr, err)
			}
			if name == "ErrorsAreSorted" {
				a, b := strings.Index(err.Error(), "VPC/bad-a: "), strings.Index(err.Error(), "VPC/bad-b: ")
				if a < 0 || b < 0 || a >= b {
					t.Errorf("AfterXRDiff(...): want bad-a before bad-b, got %v", err)
				}
			}
			if len(ensured) != len(tc.wantEnsured) {
				t.Errorf("ensure got %v, want %v", ensured, tc.wantEnsured)
			}
			if len(tc.wantEnsured) > 0 && (ensureCtx == nil || ensureCtx.Err() != nil) {
				t.Errorf("ensure ctx missing or ended: %v", ensureCtx)
			}
			got := x.Plans()
			if len(got) != len(tc.wantPlans)+1 || got["earlier"] == nil {
				t.Errorf("Plans() has %d entries, want %v plus the earlier XR's", len(got), tc.wantPlans)
			}
			for _, k := range tc.wantPlans {
				if got[k] == nil {
					t.Errorf("Plans()[%q] missing", k)
				}
			}
		})
	}

	t.Run("EnsureRunsOutsideXRDeadline", func(t *testing.T) {
		var ensureCtx context.Context
		ensure := func(ctx context.Context, _ []*unstructured.Unstructured) error {
			ensureCtx := ctx
			return errors.New("stop before planning")
		}
		xrCtx, cancel := diffprocessor.ContextWithXRDeadline(context.Background(), time.Nanosecond)
		defer cancel()
		<-xrCtx.Done()

		x := NewXRPlanner(planner, nil, ensure)
		_ = x.AfterXRDiff(xrCtx, map[string]*dt.ResourceDiff{"a": added(vpc("a", "10.0.0.0/16"))})

		if ensureCtx == nil || ensureCtx.Err() != nil {
			t.Errorf("ensure ctx missing or ended: %v", ensureCtx)
		}
	})

	t.Run("EmptyPlansNonNil", func(t *testing.T) {
		if NewXRPlanner(planner, nil, nil).Plans() == nil {
			t.Errorf("Plans() = nil, want an empty non-nil map")
		}
	})
}
