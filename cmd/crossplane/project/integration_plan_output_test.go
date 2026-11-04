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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	"github.com/crossplane/cli/v2/internal/plan"
	"github.com/crossplane/cli/v2/internal/simulate"
	"github.com/crossplane/cli/v2/internal/testutils/integration"
)

// TestSimulatePlanOutput runs project simulate's -o json simulate.ProjectResult
// against the plain project fixture, which crossplane-diff's cases cannot
// express. Each case passes simulate args directly rather than crossplane-diff's.
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

	cases := map[string]struct {
		reason string
		// args follow the plain project's -f.
		args []string
		// wantResult is the -o json output, reduced by summarize.
		wantResult *planShape
	}{
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
			code, err := runSimulateArgv(t, cfg, argv, localCrossplaneBinary(t), &stdout, &stderr)
			if err != nil {
				t.Fatalf("\n%s\nsimulate: unexpected error: %v\nstderr:\n%s", tc.reason, err, stderr.String())
			}
			if code != dp.ExitCodeDiffDetected {
				t.Errorf("\n%s\nsimulate: want exit code %d, got %d", tc.reason, dp.ExitCodeDiffDetected, code)
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
