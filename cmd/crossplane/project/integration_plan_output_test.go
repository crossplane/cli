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

package project

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	"github.com/crossplane/cli/v2/internal/plan"
	"github.com/crossplane/cli/v2/internal/simulate"
	"github.com/crossplane/cli/v2/internal/testutils/integration"
)

// TestSimulatePlanOutput runs project simulate's -o json simulate.ProjectResult
// against the plain project fixture, which the diff cases cannot
// express. Each case passes simulate args directly.
func TestSimulatePlanOutput(t *testing.T) {
	t.Parallel()

	compCase := integration.Setup{
		CRDDirs: []string{"testdata/comp/crds"},
		SetupFiles: []string{
			"testdata/comp/resources/xrd.yaml",
			"testdata/comp/resources/original-composition.yaml",
			"testdata/comp/resources/functions.yaml",
		},
		CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
			integration.Composes("testdata/comp/resources/existing-xr-1.yaml", "testdata/comp/resources/existing-downstream-1.yaml"),
			integration.Composes("testdata/comp/resources/existing-xr-2.yaml", "testdata/comp/resources/existing-downstream-2.yaml"),
		},
	}

	xnop := plan.ResourceRef{APIVersion: "ns.diff.example.org/v1alpha1", Kind: "XNopResource", Namespace: "default", Name: "test-resource"}
	another := xnop
	another.Name = "another-resource"
	comp := plan.ResourceRef{APIVersion: "apiextensions.crossplane.io/v1", Kind: "Composition", Name: "xnopresources.diff.example.org"}

	compArgs := []string{"-f", "testdata/comp/updated-composition.yaml", "--namespace=default"}

	cases := map[string]struct {
		reason string
		// args follow the plain project's -f, or with compArgv set, `composition simulate`.
		args []string
		// compArgv runs `composition simulate` with args instead.
		compArgv bool
		// wantText is the -o plan output; empty for a json case.
		wantText string
		// wantResult is the -o json output, reduced by summarize.
		wantResult *planShape
	}{
		"CompPlanText": {
			reason:   "-o plan prints the Composition's plan with its revision impact, then each affected XR's plans under it.",
			compArgv: true,
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

Note: Provider plan not run: no provider diff server planned these resources.
`,
		},
		"CompPlanJSON": {
			reason:   "-o json emits simulate.ProjectResult: the Composition's revision impact, both affected XRs as changed composites, and their plans.",
			compArgv: true,
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
			reason: "repeated --name with --namespace scopes the run to each named composite, as the unscoped run covers both.",
			args:   []string{"-o", "json", "-n", "default", "--name=another-resource", "--name=test-resource"},
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
			reason: "a namespace/name --name without --namespace scopes the run to that one composite.",
			args:   []string{"-o", "json", "--name=default/test-resource"},
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

			cfg := integration.Start(t, compCase).Config

			var stdout, stderr bytes.Buffer
			argv := append(plainProjectArgv(t), tc.args...)
			if tc.compArgv {
				argv = append([]string{"composition", "simulate", "--timeout=" + defaultTimeout.String()}, tc.args...)
			}
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
// case, as an env: skip, or "" when it can.
func integrationEnvGap() string {
	// integration.Start fails, rather than skips, a run without envtest's
	// binaries or Crossplane's CRDs.
	if !dockerAvailable() {
		return "env: no Docker daemon for the function render engine"
	}
	return ""
}

// TestSimulatePinnedChangedFunction runs a project simulate whose Manual XR
// is pinned to its Composition's current revision. The project embeds
// example-itprojectrender, and the cluster's Function of that name is another
// package, so the build marks it changed: a pinned revision that calls it
// renders the XR against that revision; one that does not leaves it deferred.
func TestSimulatePinnedChangedFunction(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		reason string
		// composition is the cluster's copy of the project's Composition,
		// whose current revision the XR is pinned to.
		composition string
		// wantDeferred is whether the XR stays deferred; otherwise it is
		// rendered against its pinned revision.
		wantDeferred bool
	}{
		"PinnedXRWithChangedFunctionRendersPinnedRevision": {
			reason:      "A Manual XR whose pinned revision calls a changed function is rendered against that revision, not deferred.",
			composition: "testdata/project/embedded/apis/composition.yaml",
		},
		"PinnedXRWithUnchangedFunctionDeferred": {
			reason:       "A Manual XR whose pinned revision calls no changed function stays deferred.",
			composition:  "testdata/comp/resources/original-composition.yaml",
			wantDeferred: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if reason := integrationEnvGap(); reason != "" {
				t.Skip(reason)
			}

			cluster := integration.Start(t, integration.Setup{
				CRDDirs: []string{"testdata/comp/crds"},
				SetupFiles: []string{
					"testdata/comp/resources/xrd.yaml",
					tc.composition,
					"testdata/comp/resources/functions.yaml",
					"testdata/project/embedded/cluster-function.yaml",
				},
				CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
					integration.Composes(
						"testdata/comp/resources/existing-xr-manual.yaml",
						"testdata/comp/resources/existing-downstream-manual.yaml",
					),
				},
			})
			cfg := cluster.Config

			// The harness pins the XR to its Composition's current revision.
			xr := &un.Unstructured{}
			xr.SetAPIVersion("ns.diff.example.org/v1alpha1")
			xr.SetKind("XNopResource")
			if err := cluster.Client.Get(cluster.Context, client.ObjectKey{Namespace: "default", Name: "manual-resource"}, xr); err != nil {
				t.Fatalf("cannot get the pinned XR: %v", err)
			}
			rev, _, _ := un.NestedString(xr.Object, "spec", "crossplane", "compositionRevisionRef", "name")

			base := withRenderBinary([]string{
				"project", "simulate", "--timeout=" + defaultTimeout.String(),
				"-f", copyProjectFixture(t, "embedded"),
				"--cache-dir=" + t.TempDir(), "--skip-plan", "-n", "default",
			}, localCrossplaneBinary(t))

			var stdout, stderr bytes.Buffer
			code, err := runSimulateArgvWith(t, cfg, append(slices.Clip(base), "-o", "json"), &simulate.Hooks{}, &stdout, &stderr)
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
			if c.Status != simulate.CompositeChanged || !c.Pinned || c.Detail != "rendered against pinned revision "+rev+" with the changed functions" {
				t.Errorf("\n%s\nsimulate -o json: want a changed pinned composite rendered against %s, got %+v", tc.reason, rev, c)
			}
			// The plan is rendered by the project's build of the function the
			// pinned revision calls, which prefixes configData with "embedded-".
			var found bool
			for _, p := range res.Plans {
				for _, ch := range p.Changes {
					if ch.Path == "spec.forProvider.configData" && ch.Planned != nil && ch.Planned.Value == "embedded-manual-existing-value" {
						found = true
					}
				}
			}
			if !found {
				t.Errorf("\n%s\nsimulate -o json: want a configData change to the embedded function's output, got plans %+v", tc.reason, res.Plans)
			}

			var text bytes.Buffer
			if _, err := runSimulateArgvWith(t, cfg, base, &simulate.Hooks{}, &text, &stderr); err != nil {
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

// TestSimulateNameRejected runs the --name scopes simulate refuses: one
// naming a composite no supplied Composition reaches, and a namespace/name
// combined with --namespace.
func TestSimulateNameRejected(t *testing.T) {
	t.Parallel()

	compCase := integration.Setup{
		CRDDirs: []string{"testdata/comp/crds"},
		SetupFiles: []string{
			"testdata/comp/resources/xrd.yaml",
			"testdata/comp/resources/original-composition.yaml",
			"testdata/comp/resources/functions.yaml",
		},
		CrossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
			integration.Composes("testdata/comp/resources/existing-xr-1.yaml", "testdata/comp/resources/existing-downstream-1.yaml"),
			integration.Composes("testdata/comp/resources/existing-xr-2.yaml", "testdata/comp/resources/existing-downstream-2.yaml"),
		},
	}
	cases := map[string]struct {
		reason   string
		args     []string
		wantErr  string
		wantCode int
	}{
		"NameQualifiedWithNamespaceFlagRejected": {
			reason:   "namespace/name with --namespace is ambiguous and is rejected.",
			args:     []string{"--namespace=default", "--name=default/test-resource"},
			wantErr:  "can't be combined with --namespace",
			wantCode: simulate.ExitCodeUsage,
		},
		"NamePartiallyUnmatchedFailsBeforeRendering": {
			reason:   "one --name no composite matches fails the run, and no plan is rendered.",
			args:     []string{"--name=default/test-resource", "--name=default/missing"},
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

			cfg := integration.Start(t, compCase).Config

			var stdout, stderr bytes.Buffer
			argv := append(plainProjectArgv(t), tc.args...)
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

// plainProjectArgv returns `project simulate` argv for a temp copy of the
// plain project fixture, whose Composition is the cluster's, updated.
func plainProjectArgv(t *testing.T) []string {
	t.Helper()

	return []string{
		"project", "simulate", "--timeout=" + defaultTimeout.String(),
		"-f", copyProjectFixture(t, "plain"),
		"--cache-dir=" + t.TempDir(),
	}
}
