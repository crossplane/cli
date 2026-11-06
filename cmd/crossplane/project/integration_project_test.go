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

package project_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	diffv1alpha1 "github.com/crossplane/upjet/v2/proto/diff/v1alpha1"
	"github.com/google/go-cmp/cmp"

	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	"github.com/crossplane/cli/v2/internal/plan"
	"github.com/crossplane/cli/v2/internal/simulate"
	"github.com/crossplane/cli/v2/internal/testutils/integration"
	"github.com/crossplane/cli/v2/internal/testutils/integration/simharness"
)

// projectTestCase is a `project simulate -f <project file>` run against a
// fixture project, exercising how a project varies rather than the XR and
// Composition logic the other tables cover.
type projectTestCase struct {
	reason string
	// setupFiles and crossplaneManagedResources set up the cluster, as in the
	// other tables.
	setupFiles                 []string
	crossplaneManagedResources []integration.HierarchicalOwnershipRelation
	// project is a directory under testdata/project, copied to a temp dir so
	// the build cannot dirty the tree.
	project string
	// extraArgs follow the project file argument.
	extraArgs        []string
	expectedExitCode int
	// expectedResult is the -o json output, reduced by summarize.
	expectedResult planShape
	// expectedChanges are the field changes of test-resource's XDownstreamResource plan.
	expectedChanges []plan.FieldChange
	// router, when set, routes provider plans instead of diff servers and
	// the run does not pass --skip-plan.
	router simulate.Router
}

func TestProjectSimulateIntegration(t *testing.T) {
	t.Parallel()

	region := &diffv1alpha1.FieldChange{Field: "spec.forProvider.region", Actual: strValue("us-east-1"), Planned: strValue("eu-west-1")}

	xnop := plan.ResourceRef{APIVersion: "ns.diff.example.org/v1alpha1", Kind: "XNopResource", Namespace: "default", Name: "test-resource"}
	another := xnop
	another.Name = "another-resource"
	comp := plan.ResourceRef{APIVersion: "apiextensions.crossplane.io/v1", Kind: "Composition", Name: "xnopresources.diff.example.org"}
	changed := func(refs ...plan.ResourceRef) []simulate.CompositeResult {
		out := make([]simulate.CompositeResult, 0, len(refs))
		for _, r := range refs {
			out = append(out, simulate.CompositeResult{Ref: r, Status: simulate.CompositeChanged, Composition: &comp})
		}
		return out
	}
	compositions := func(repointed int) []simulate.CompositionResult {
		return []simulate.CompositionResult{{Ref: comp, RevisionImpact: &simulate.RevisionImpact{ChangeScope: "spec", CreatesRevision: true, RepointedComposites: repointed}}}
	}
	// onePlans is the plans of a run over test-resource alone.
	onePlans := []planEntry{
		{Kind: "Composition", Name: comp.Name, Action: plan.ActionUpdate},
		{Kind: "XDownstreamResource", Name: "test-resource", Action: plan.ActionUpdate, Composite: "test-resource"},
		{Kind: "XNopResource", Name: "test-resource", Action: plan.ActionNoOp, Composite: "test-resource"},
	}
	val := func(v string) *plan.Value { return &plan.Value{Value: v} }

	cases := map[string]projectTestCase{
		"PlainProject": {
			reason: "A project with no dependencies or embedded functions simulates its Composition against cluster functions, like the same Composition in Composition mode.",
			setupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-1.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-1.yaml",
				),
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-2.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-2.yaml",
				),
			},
			project:          "plain",
			extraArgs:        []string{"-n", "default"},
			expectedExitCode: dp.ExitCodeDiffDetected,
			expectedResult: planShape{
				Summary:      plan.Summary{Update: 3, NoOp: 2},
				Composites:   changed(another, xnop),
				Compositions: compositions(2),
				Plans: []planEntry{
					{Kind: "Composition", Name: comp.Name, Action: plan.ActionUpdate},
					{Kind: "XDownstreamResource", Name: "another-resource", Action: plan.ActionUpdate, Composite: "another-resource"},
					{Kind: "XNopResource", Name: "another-resource", Action: plan.ActionNoOp, Composite: "another-resource"},
					{Kind: "XDownstreamResource", Name: "test-resource", Action: plan.ActionUpdate, Composite: "test-resource"},
					{Kind: "XNopResource", Name: "test-resource", Action: plan.ActionNoOp, Composite: "test-resource"},
				},
			},
			expectedChanges: []plan.FieldChange{
				{Path: "spec.forProvider.configData", Actual: val("existing-value"), Planned: val("updated-existing-value")},
				{Path: "spec.forProvider.resourceTier", Actual: val("basic"), Planned: val("premium")},
			},
		},
		"ProviderPlanJoinsChanges": {
			reason: "A provider plan routed for a composed resource joins that resource's changes.",
			setupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-1.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-1.yaml",
				),
			},
			project:          "providerdep",
			extraArgs:        []string{"-n", "default"},
			expectedExitCode: dp.ExitCodeDiffDetected,
			router: fakeProviderRouter{fn: func(*diffv1alpha1.PlanRequest) (*diffv1alpha1.PlanResponse, error) {
				return &diffv1alpha1.PlanResponse{Action: diffv1alpha1.Action_ACTION_UPDATE, Changes: []*diffv1alpha1.FieldChange{region}}, nil
			}},
			expectedResult: planShape{
				Summary:      plan.Summary{Update: 2, NoOp: 1},
				Composites:   changed(xnop),
				Compositions: compositions(1),
				Plans:        onePlans,
			},
			expectedChanges: []plan.FieldChange{
				{Path: "spec.forProvider.region", Actual: val("us-east-1"), Planned: val("eu-west-1")},
				{Path: "spec.forProvider.configData", Actual: val("existing-value"), Planned: val("updated-existing-value")},
				{Path: "spec.forProvider.resourceTier", Actual: val("basic"), Planned: val("premium")},
			},
		},
		"EmbeddedFunctionOverlay": {
			reason: "A Composition step naming a function embedded in the project runs the project's function, not a same-named cluster Function, which would fail without step input.",
			setupFiles: []string{
				"../../../internal/testutils/integration/testdata/comp/resources/xrd.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/original-composition.yaml",
				"../../../internal/testutils/integration/testdata/comp/resources/functions.yaml",
				"testdata/project/embedded/cluster-function.yaml",
			},
			crossplaneManagedResources: []integration.HierarchicalOwnershipRelation{
				integration.Composes(
					"../../../internal/testutils/integration/testdata/comp/resources/existing-xr-1.yaml",
					"../../../internal/testutils/integration/testdata/comp/resources/existing-downstream-1.yaml",
				),
			},
			project:          "embedded",
			extraArgs:        []string{"-n", "default"},
			expectedExitCode: dp.ExitCodeDiffDetected,
			expectedResult: planShape{
				Summary:      plan.Summary{Update: 2, NoOp: 1},
				Composites:   changed(xnop),
				Compositions: compositions(1),
				Plans:        onePlans,
			},
			expectedChanges: []plan.FieldChange{
				{Path: "spec.forProvider.configData", Actual: val("existing-value"), Planned: val("embedded-existing-value")},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if reason := integrationEnvGap(); reason != "" {
				t.Skip(reason)
			}

			cfg := integration.Start(t, integration.Setup{
				CRDDirs:                    []string{"../../../internal/testutils/integration/testdata/comp/crds"},
				SetupFiles:                 tc.setupFiles,
				CrossplaneManagedResources: tc.crossplaneManagedResources,
			}).Config

			argv := []string{
				"project", "simulate", "--timeout=" + simharness.DefaultTimeout.String(),
				"-f", copyProjectFixture(t, tc.project),
				"--cache-dir=" + t.TempDir(),
			}
			if tc.router == nil {
				argv = append(argv, "--skip-plan")
			}
			argv = append(argv, tc.extraArgs...)
			argv = append(argv, "-o", "json")

			var stdout, stderr bytes.Buffer
			code, err := simharness.RunSimulateArgvWith(t, cfg, simharness.WithRenderBinary(argv, simharness.LocalCrossplaneBinary(t)), &simulate.Hooks{Router: tc.router}, &stdout, &stderr)
			if err != nil {
				t.Fatalf("\n%s\nsimulate: unexpected error: %v\nstderr:\n%s", tc.reason, err, stderr.String())
			}
			if code != tc.expectedExitCode {
				t.Errorf("\n%s\nsimulate: want exit code %d, got %d", tc.reason, tc.expectedExitCode, code)
			}

			var res simulate.ProjectResult
			if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
				t.Fatalf("\n%s\nsimulate -o json: output is not a simulate.ProjectResult: %v\n%s", tc.reason, err, stdout.String())
			}
			if diff := cmp.Diff(tc.expectedResult, summarize(res)); diff != "" {
				t.Errorf("\n%s\nsimulate -o json: -want, +got:\n%s", tc.reason, diff)
			}
			var got []plan.FieldChange
			for _, p := range res.Plans {
				if p.Kind == "XDownstreamResource" && p.Name == "test-resource" {
					got = p.Changes
				}
			}
			if diff := cmp.Diff(tc.expectedChanges, got); diff != "" {
				t.Errorf("\n%s\nsimulate -o json: XDownstreamResource changes -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

// copyProjectFixture copies the project fixture under testdata/project to a
// temp dir, so the build cannot dirty the tree, and returns its project file.
func copyProjectFixture(t *testing.T, project string) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "project", project))); err != nil {
		t.Fatalf("cannot copy fixture %q: %v", project, err)
	}

	return filepath.Join(dir, "crossplane-project.yaml")
}
