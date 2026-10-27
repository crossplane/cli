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
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	tu "github.com/crossplane/cli/v2/internal/diff/testutils"
	"github.com/crossplane/cli/v2/internal/plan"
	"github.com/crossplane/cli/v2/internal/simulate"
)

// TestSimulatePlanOutput runs project simulate's own output formats, -o plan
// and -o json's simulate.ProjectResult, which crossplane-diff's cases cannot express.
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

Note: Provider plan not run: no provider diff servers serve these resources.
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

Note: Provider plan not run: no provider diff servers serve these resources.
`,
		},
		"XRPlanJSON": {
			reason:   "-o json emits simulate.ProjectResult: the XR as a changed composite, with both its plans grouped under it.",
			testType: XRDiffTest,
			tt:       xrCase,
			args:     append([]string{"-o", "json"}, xrArgs...),
			wantResult: &planShape{
				Summary:    plan.Summary{Update: 2},
				Composites: []simulate.CompositeResult{{Ref: xnop, Status: simulate.CompositeChanged}},
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

Note: Provider plan not run: no provider diff servers serve these resources.
`,
		},
		"CompPlanJSON": {
			reason:   "-o json emits simulate.ProjectResult: the Composition's revision impact, both affected XRs as changed composites, and their plans.",
			testType: CompositionDiffTest,
			tt:       compCase,
			args:     append([]string{"-o", "json"}, compArgs...),
			wantResult: &planShape{
				Summary:    plan.Summary{Update: 3, NoOp: 2},
				Composites: []simulate.CompositeResult{{Ref: another, Status: simulate.CompositeChanged, Composition: &comp}, {Ref: xnop, Status: simulate.CompositeChanged, Composition: &comp}},
				Compositions: []simulate.CompositionResult{{
					Ref:            comp,
					RevisionImpact: &simulate.RevisionImpact{ChangeScope: "spec", CreatesRevision: true, RepointedComposites: 2},
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
		"NameRepeatedScopesToEach": {
			reason:   "repeated --name with --namespace scopes the run to each named composite, as the unscoped run covers both.",
			testType: CompositionDiffTest,
			tt:       compCase,
			args:     []string{"-o", "json", "-f", "testdata/comp/updated-composition.yaml", "-n", "default", "--name=another-resource", "--name=test-resource"},
			wantResult: &planShape{
				Summary:    plan.Summary{Update: 3, NoOp: 2},
				Composites: []simulate.CompositeResult{{Ref: another, Status: simulate.CompositeChanged, Composition: &comp}, {Ref: xnop, Status: simulate.CompositeChanged, Composition: &comp}},
				Compositions: []simulate.CompositionResult{{
					Ref:            comp,
					RevisionImpact: &simulate.RevisionImpact{ChangeScope: "spec", CreatesRevision: true, RepointedComposites: 2},
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
		"NameNamespaceQualifiedScopesOne": {
			reason:   "a namespace/name --name without --namespace scopes the run to that one composite.",
			testType: CompositionDiffTest,
			tt:       compCase,
			args:     []string{"-o", "json", "-f", "testdata/comp/updated-composition.yaml", "--name=default/test-resource"},
			wantResult: &planShape{
				Summary:    plan.Summary{Update: 2, NoOp: 1},
				Composites: []simulate.CompositeResult{{Ref: xnop, Status: simulate.CompositeChanged, Composition: &comp}},
				Compositions: []simulate.CompositionResult{{
					Ref:            comp,
					RevisionImpact: &simulate.RevisionImpact{ChangeScope: "spec", CreatesRevision: true, RepointedComposites: 1},
				}},
				Plans: []planEntry{
					{Kind: "Composition", Name: comp.Name, Action: plan.ActionUpdate},
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

			var res simulate.ProjectResult
			if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
				t.Fatalf("\n%s\nsimulate -o json: output is not a simulate.ProjectResult: %v\n%s", tc.reason, err, stdout.String())
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

// planShape is the part of a simulate.ProjectResult these cases pin: everything but
// the field changes, dry-run details and notes, which the plan text cases and
// the mapper's unit tests cover.
type planShape struct {
	Summary      plan.Summary
	Composites   []simulate.CompositeResult
	Compositions []simulate.CompositionResult
	Plans        []planEntry
}

// planEntry is one plan's identity, action and composite name.
type planEntry struct {
	Kind, Name string
	Action     plan.Action
	Composite  string
}

func summarize(r simulate.ProjectResult) planShape {
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

// TestSimulateNameRejected runs the --name scopes simulate refuses: one
// naming a composite no supplied Composition reaches, and a namespace/name
// combined with --namespace.
// TestSimulatePinnedChangedFunction runs a Composition simulate whose Manual
// XR is pinned to a revision. Marking a function that revision calls as changed
// renders the XR against that revision; any other function leaves it deferred.
// The Hooks seam stands in for project mode's digest detection.
func TestSimulatePinnedChangedFunction(t *testing.T) {
	t.Parallel()

	tu.SetupKubeTestLogger(t)

	tt := IntegrationTestCase{
		setupFiles: []string{
			"testdata/comp/resources/xrd.yaml",
			"testdata/comp/resources/revision-templating-composition.yaml",
			"testdata/comp/resources/revision-templating-composition-revision.yaml",
			"testdata/comp/resources/revision-templating-composition-older-revision.yaml",
			"testdata/comp/resources/functions.yaml",
		},
		crossplaneManagedResources: []HierarchicalOwnershipRelation{
			composes(
				"testdata/comp/resources/existing-xr-pinned-revision-ref.yaml",
				"testdata/comp/resources/existing-downstream-pinned-revision-ref-stale.yaml",
			),
		},
	}
	const rev = "xrevisionrefs.diff.example.org-0d65896"

	cases := map[string]struct {
		reason  string
		changed []string
		// wantDeferred is whether the XR stays deferred; otherwise it is
		// rendered against its pinned revision.
		wantDeferred bool
	}{
		"PinnedXRWithChangedFunctionRendersPinnedRevision": {
			reason:  "A Manual XR whose pinned revision calls a changed function is rendered against that revision, not deferred.",
			changed: []string{"function-go-templating"},
		},
		"PinnedXRWithUnchangedFunctionDeferred": {
			reason:       "A Manual XR whose pinned revision calls no changed function stays deferred.",
			changed:      []string{"function-extra-resources"},
			wantDeferred: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if reason := integrationEnvGap(); reason != "" {
				t.Skip(reason)
			}

			cfg := startIntegrationCluster(t, CompositionDiffTest, tt, defaultTimeout).cfg
			base := []string{"project", "simulate", "--timeout=" + defaultTimeout.String(), "-f", "testdata/comp/revision-templating-updated-composition.yaml", "-n", "default"}

			var stdout, stderr bytes.Buffer
			code, err := runSimulateArgvHooked(t, cfg, append(slices.Clip(base), "-o", "json"), tc.changed, &stdout, &stderr)
			if err != nil {
				t.Fatalf("\n%s\nsimulate: unexpected error: %v\nstderr:\n%s", tc.reason, err, stderr.String())
			}
			var res simulate.ProjectResult
			if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
				t.Fatalf("\n%s\nsimulate -o json: output is not a simulate.ProjectResult: %v\n%s", tc.reason, err, stdout.String())
			}
			if len(res.Composites) != 1 {
				t.Fatalf("\n%s\nsimulate -o json: want 1 composite, got %+v", tc.reason, res.Composites)
			}
			c := res.Composites[0]

			if tc.wantDeferred {
				if c.Status != simulate.CompositeDeferred || strings.HasPrefix(c.Detail, "rendered against") {
					t.Errorf("\n%s\nsimulate -o json: want a deferred pinned composite, got %+v", tc.reason, c)
				}
				return
			}

			if code != dp.ExitCodeDiffDetected {
				t.Errorf("\n%s\nsimulate: want exit code %d, got %d", tc.reason, dp.ExitCodeDiffDetected, code)
			}
			if c.Status != simulate.CompositeChanged || !c.Pinned || c.Detail != "rendered against pinned revision "+rev+" with the project's functions" {
				t.Errorf("\n%s\nsimulate -o json: want a changed pinned composite rendered against %s, got %+v", tc.reason, rev, c)
			}
			// The plan is rendered from the pinned revision's pipeline: its
			// configData is the pinned revision's name, which the current
			// composition's predicted revision name would not be.
			var found bool
			for _, p := range res.Plans {
				for _, ch := range p.Changes {
					if ch.Path == "spec.forProvider.configData" && ch.Planned != nil && ch.Planned.Value == rev {
						found = true
					}
				}
			}
			if !found {
				t.Errorf("\n%s\nsimulate -o json: want a configData change to %q, got plans %+v", tc.reason, rev, res.Plans)
			}

			var text bytes.Buffer
			if _, err := runSimulateArgvHooked(t, cfg, base, tc.changed, &text, &stderr); err != nil {
				t.Fatalf("simulate -o plan: unexpected error: %v", err)
			}
			if os.Getenv("SIMULATE_DEMO") != "" {
				j, _ := json.MarshalIndent(c, "", "  ")
				t.Logf("-o plan:\n%s\n-o json composite:\n%s", text.String(), j)
			}
			if !strings.Contains(text.String(), "rendered against pinned revision "+rev) || strings.Contains(text.String(), "(deferred)") {
				t.Errorf("\n%s\nsimulate -o plan: want the rendered-against note and no deferral, got:\n%s", tc.reason, text.String())
			}
		})
	}
}

func TestSimulateNameRejected(t *testing.T) {
	t.Parallel()

	tu.SetupKubeTestLogger(t)

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
	base := []string{"-f", "testdata/comp/updated-composition.yaml"}

	// exitUsageError is kong's exit status for parse and Validate errors.
	const exitUsageError = 80

	cases := map[string]struct {
		reason   string
		args     []string
		wantErr  string
		wantCode int
	}{
		"NameQualifiedWithNamespaceFlagRejected": {
			reason:   "namespace/name with --namespace is ambiguous and is rejected.",
			args:     append([]string{"--namespace=default", "--name=default/test-resource"}, base...),
			wantErr:  "can't be combined with --namespace",
			wantCode: exitUsageError,
		},
		"NamePartiallyUnmatchedFailsBeforeRendering": {
			reason:   "one --name no composite matches fails the run, and no plan is rendered.",
			args:     append([]string{"--name=default/test-resource", "--name=default/missing"}, base...),
			wantErr:  "resource ref(s) not relevant to any supplied composition: [default/missing]",
			wantCode: dp.ExitCodeToolError,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if reason := integrationEnvGap(); reason != "" {
				t.Skip(reason)
			}

			cfg := startIntegrationCluster(t, CompositionDiffTest, compCase, defaultTimeout).cfg

			var stdout, stderr bytes.Buffer
			argv := append([]string{"project", "simulate", "--timeout=" + defaultTimeout.String()}, tc.args...)
			code, err := runSimulateArgv(t, cfg, argv, localCrossplaneBinary(t), &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("\n%s\nsimulate: want error containing %q, got %v (exit %d)", tc.reason, tc.wantErr, err, code)
			}
			if code != tc.wantCode {
				t.Errorf("\n%s\nsimulate: want exit code %d, got %d", tc.reason, tc.wantCode, code)
			}
			if stdout.Len() != 0 {
				t.Errorf("\n%s\nsimulate: want no plan output, got:\n%s", tc.reason, stdout.String())
			}
		})
	}
}
