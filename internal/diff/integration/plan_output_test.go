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
	"encoding/json"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"

	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	tu "github.com/crossplane/cli/v2/internal/diff/testutils"
	"github.com/crossplane/cli/v2/internal/plan"
)

// TestSimulatePlanOutput runs project simulate's own output formats, -o plan
// and -o json's plan.Result, which crossplane-diff's cases cannot express.
// Each case passes simulate args directly rather than crossplane-diff's.
func TestSimulatePlanOutput(t *testing.T) {
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
	compCase := IntegrationTestCase{
		setupFiles: []string{
			"testdata/comp/resources/xrd.yaml",
			"testdata/comp/resources/original-composition.yaml",
			"testdata/comp/resources/functions.yaml",
		},
		crossplaneManagedResources: []HierarchicalOwnershipRelation{
			composes("testdata/comp/resources/existing-xr-1.yaml", "testdata/comp/resources/existing-downstream-1.yaml"),
			composes("testdata/comp/resources/existing-xr-2.yaml", "testdata/comp/resources/existing-downstream-2.yaml"),
		},
	}
	xrArgs := []string{"-f", "testdata/diff/modified-xr.yaml"}
	compArgs := []string{"-f", "testdata/comp/updated-composition.yaml", "--namespace=default"}

	xnop := plan.ResourceRef{APIVersion: "ns.diff.example.org/v1alpha1", Kind: "XNopResource", Namespace: "default", Name: "test-resource"}
	another := xnop
	another.Name = "another-resource"
	comp := plan.ResourceRef{APIVersion: "apiextensions.crossplane.io/v1", Kind: "Composition", Name: "xnopresources.diff.example.org"}

	cases := map[string]struct {
		reason   string
		testType DiffTestType
		tt       IntegrationTestCase
		args     []string
		// wantText is the -o plan output; empty for a json case.
		wantText string
		// wantResult is the -o json output, reduced by summarize.
		wantResult *planShape
	}{
		"XRPlanText": {
			reason:   "-o plan, the default, prints an XR's updates grouped under it, then the summary and notes.",
			testType: XRDiffTest,
			tt:       xrCase,
			args:     xrArgs,
			wantText: `XR: XNopResource.ns.diff.example.org default/test-resource
  [~] XDownstreamResource.ns.nop.example.org default/test-resource (update)
      spec:
        forProvider:
          ~ configData: "existing-value" -> "modified-value"

  [~] XNopResource.ns.diff.example.org default/test-resource (update)
      spec:
        ~ coolField: "existing-value" -> "modified-value"

Plan: 0 to add, 2 to change, 0 to destroy.

Note: Cloud-layer plan not run: no provider diff servers serve these resources.
`,
		},
		"XRCreatePlanText": {
			reason:   "-o plan prints a dotted annotation or label key, such as crossplane.io/composite, whole as one level.",
			testType: XRDiffTest,
			tt:       IntegrationTestCase{setupFiles: xrCase.setupFiles},
			args:     []string{"-f", "testdata/diff/new-xr.yaml"},
			wantText: `XR: XNopResource.ns.diff.example.org default/test-resource
  [+] XDownstreamResource.ns.nop.example.org default/test-resource (create)
      metadata:
        annotations:
          + crossplane.io/composition-resource-name: "nop-resource"
        labels:
          + crossplane.io/composite: "test-resource"
      spec:
        forProvider:
          + configData: "new-value"

  [+] XNopResource.ns.diff.example.org default/test-resource (create)
      spec:
        + coolField: "new-value"

Plan: 2 to add, 0 to change, 0 to destroy.

Note: Cloud-layer plan not run: no provider diff servers serve these resources.
`,
		},
		"XRPlanJSON": {
			reason:   "-o json emits plan.Result: the XR as a changed composite, with both its plans grouped under it.",
			testType: XRDiffTest,
			tt:       xrCase,
			args:     append([]string{"-o", "json"}, xrArgs...),
			wantResult: &planShape{
				Summary:    plan.Summary{Update: 2},
				Composites: []plan.CompositeResult{{Ref: xnop, Status: plan.CompositeChanged}},
				Plans: []planEntry{
					{Kind: "XDownstreamResource", Name: "test-resource", Action: plan.ActionUpdate, Composite: "test-resource"},
					{Kind: "XNopResource", Name: "test-resource", Action: plan.ActionUpdate, Composite: "test-resource"},
				},
			},
		},
		"CompPlanText": {
			reason:   "-o plan prints the Composition's plan with its revision impact, then each affected XR's plans under it.",
			testType: CompositionDiffTest,
			tt:       compCase,
			args:     compArgs,
			wantText: `[~] Composition.apiextensions.crossplane.io xnopresources.diff.example.org (update)
    Applying this creates a new CompositionRevision, which 2 composites would adopt.
    spec:
      pipeline[0]:
        input:
          inline:
            ~ template: "apiVersion: ns.nop.example.org/v1alpha1\nkind: XDownstreamResource\nmetadata:\n  name: {{ .observed.composite.resource.metadata.name }}\n  namespace: {{ .observed.composite.resource.metadata.namespace }}\n  annotations:\n    gotemplating.fn.crossplane.io/composition-resource-name: nop-resource\nspec:\n  forProvider:\n    configData: {{ .observed.composite.resource.spec.coolField }}\n    resourceTier: basic\n" -> "apiVersion: ns.nop.example.org/v1alpha1\nkind: XDownstreamResource\nmetadata:\n  name: {{ .observed.composite.resource.metadata.name }}\n  namespace: {{ .observed.composite.resource.metadata.namespace }}\n  annotations:\n    gotemplating.fn.crossplane.io/composition-resource-name: nop-resource\nspec:\n  forProvider:\n    configData: updated-{{ .observed.composite.resource.spec.coolField }}\n    resourceTier: premium\n"

XR: XNopResource.ns.diff.example.org default/another-resource
  [~] XDownstreamResource.ns.nop.example.org default/another-resource (update)
      spec:
        forProvider:
          ~ configData: "another-existing-value" -> "updated-another-existing-value"
          ~ resourceTier: "basic" -> "premium"

XR: XNopResource.ns.diff.example.org default/test-resource
  [~] XDownstreamResource.ns.nop.example.org default/test-resource (update)
      spec:
        forProvider:
          ~ configData: "existing-value" -> "updated-existing-value"
          ~ resourceTier: "basic" -> "premium"

Plan: 0 to add, 3 to change, 0 to destroy.

Note: Cloud-layer plan not run: no provider diff servers serve these resources.
`,
		},
		"CompPlanJSON": {
			reason:   "-o json emits plan.Result: the Composition's revision impact, both affected XRs as changed composites, and their plans.",
			testType: CompositionDiffTest,
			tt:       compCase,
			args:     append([]string{"-o", "json"}, compArgs...),
			wantResult: &planShape{
				Summary:    plan.Summary{Update: 3, NoOp: 2},
				Composites: []plan.CompositeResult{{Ref: another, Status: plan.CompositeChanged, Composition: &comp}, {Ref: xnop, Status: plan.CompositeChanged, Composition: &comp}},
				Compositions: []plan.CompositionResult{{
					Ref:            comp,
					RevisionImpact: &plan.RevisionImpact{ChangeScope: "spec", CreatesRevision: true, RepointedComposites: 2},
				}},
				Plans: []planEntry{
					{Kind: "Composition", Name: comp.Name, Action: plan.ActionUpdate},
					{Kind: "XDownstreamResource", Name: "another-resource", Action: plan.ActionUpdate, Composite: "another-resource"},
					{Kind: "XNopResource", Name: "another-resource", Action: plan.ActionNoOp, Composite: "another-resource"},
					{Kind: "XDownstreamResource", Name: "test-resource", Action: plan.ActionUpdate, Composite: "test-resource"},
					{Kind: "XNopResource", Name: "test-resource", Action: plan.ActionNoOp, Composite: "test-resource"},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if reason := integrationEnvGap(); reason != "" {
				t.Skip(reason)
			}

			cfg := startIntegrationCluster(t, tc.testType, tc.tt, defaultTimeout).cfg

			var stdout, stderr bytes.Buffer
			argv := append([]string{"project", "simulate", "--timeout=" + defaultTimeout.String()}, tc.args...)
			code, err := runSimulateArgv(t, cfg, argv, localCrossplaneBinary(t), &stdout, &stderr)
			if err != nil {
				t.Fatalf("\n%s\nsimulate: unexpected error: %v\nstderr:\n%s", tc.reason, err, stderr.String())
			}
			if code != dp.ExitCodeDiffDetected {
				t.Errorf("\n%s\nsimulate: want exit code %d, got %d", tc.reason, dp.ExitCodeDiffDetected, code)
			}

			if tc.wantResult == nil {
				if diff := cmp.Diff(tc.wantText, stdout.String()); diff != "" {
					t.Errorf("\n%s\nsimulate -o plan: -want, +got:\n%s", tc.reason, diff)
				}
				return
			}

			var res plan.Result
			if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
				t.Fatalf("\n%s\nsimulate -o json: output is not a plan.Result: %v\n%s", tc.reason, err, stdout.String())
			}
			if res.FormatVersion != plan.ResultFormatVersion || res.Errored {
				t.Errorf("\n%s\nsimulate -o json: want formatVersion %q and not errored, got %q, errored %t", tc.reason, plan.ResultFormatVersion, res.FormatVersion, res.Errored)
			}
			if diff := cmp.Diff(*tc.wantResult, summarize(res)); diff != "" {
				t.Errorf("\n%s\nsimulate -o json: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

// planShape is the part of a plan.Result these cases pin: everything but
// the field changes, dry-run details and notes, which the plan text cases and
// the mapper's unit tests cover.
type planShape struct {
	Summary      plan.Summary
	Composites   []plan.CompositeResult
	Compositions []plan.CompositionResult
	Plans        []planEntry
}

// planEntry is one plan's identity, action and composite name.
type planEntry struct {
	Kind, Name string
	Action     plan.Action
	Composite  string
}

func summarize(r plan.Result) planShape {
	s := planShape{Summary: r.Summary, Composites: r.Composites, Compositions: r.Compositions}
	for _, p := range r.Plans {
		e := planEntry{Kind: p.Kind, Name: p.Name, Action: p.Action}
		if p.Composite != nil {
			e.Composite = p.Composite.Name
		}
		s.Plans = append(s.Plans, e)
	}
	// The revision's predicted name is a hash of the Composition's content.
	for i := range s.Compositions {
		if ri := s.Compositions[i].RevisionImpact; ri != nil {
			c := *ri
			c.PredictedRevision = ""
			s.Compositions[i].RevisionImpact = &c
		}
	}
	return s
}

// integrationEnvGap returns why the environment cannot run an integration
// case, as runIntegrationTest's env: skips do, or "" when it can.
func integrationEnvGap() string {
	switch {
	case !dockerAvailable():
		return "env: no Docker daemon for the function render engine"
	case !envtestBinariesAvailable():
		return "env: no envtest binaries (KUBEBUILDER_ASSETS unset)"
	case os.Getenv("CROSSPLANE_CRDS_DIR") == "":
		return "env: CROSSPLANE_CRDS_DIR unset (run via nix run .#test or set it to Crossplane's cluster/crds)"
	}
	return ""
}
