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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crossplane/cli/v2/cmd/crossplane/project"
	dp "github.com/crossplane/cli/v2/internal/diff/diffprocessor"
	tu "github.com/crossplane/cli/v2/internal/diff/testutils"
	"github.com/crossplane/cli/v2/internal/simulate/simtest"
)

// projectTestCase is a `project simulate -f <project file>` run against a
// fixture project, exercising how a project varies rather than the XR and
// Composition logic the other tables cover.
type projectTestCase struct {
	reason string
	// setupFiles and crossplaneManagedResources set up the cluster, as in the
	// other tables. Cases that fail before the cluster is used leave them empty.
	setupFiles                 []string
	crossplaneManagedResources []HierarchicalOwnershipRelation
	// project is a directory under testdata/project, copied to a temp dir so
	// the build cannot dirty the tree.
	project string
	// extraArgs follow the project file argument.
	extraArgs             []string
	expectedExitCode      int
	expectedErrorContains string
	expectedStructured    simtest.Expectation
	// slow cases are skipped under testing.Short (registry pulls, builds).
	slow bool
}

func TestProjectSimulateIntegration(t *testing.T) {
	t.Parallel()

	tu.SetupKubeTestLogger(t)

	cases := map[string]projectTestCase{
		"MalformedProjectFile": {
			reason:                "A project file that does not parse fails the run and the error names the file.",
			project:               "malformed",
			expectedExitCode:      dp.ExitCodeToolError,
			expectedErrorContains: "crossplane-project.yaml",
		},
		"NoCompositions": {
			reason:                "A valid project whose APIs path holds no Composition has nothing to simulate.",
			project:               "nocomps",
			expectedExitCode:      dp.ExitCodeToolError,
			expectedErrorContains: "no Compositions found",
		},
		"ProjectFileWithOtherInputs": {
			reason:                "A project file must be the only -f input, so a second input is rejected before the project is built.",
			project:               "plain",
			extraArgs:             []string{"-f", "testdata/comp/updated-composition.yaml"},
			expectedExitCode:      dp.ExitCodeToolError,
			expectedErrorContains: "a project file must be the only -f input",
		},
		"PlainProject": {
			reason: "A project with no dependencies or embedded functions simulates its Composition against cluster functions, like the same Composition in Composition mode.",
			setupFiles: []string{
				"testdata/comp/resources/xrd.yaml",
				"testdata/comp/resources/original-composition.yaml",
				"testdata/comp/resources/functions.yaml",
			},
			crossplaneManagedResources: []HierarchicalOwnershipRelation{
				composes(
					"testdata/comp/resources/existing-xr-1.yaml",
					"testdata/comp/resources/existing-downstream-1.yaml",
				),
				composes(
					"testdata/comp/resources/existing-xr-2.yaml",
					"testdata/comp/resources/existing-downstream-2.yaml",
				),
			},
			project:          "plain",
			extraArgs:        []string{"-n", "default"},
			expectedExitCode: dp.ExitCodeDiffDetected,
			expectedStructured: simtest.Expect().
				WithComposition("xnopresources.diff.example.org").
				WithCompositionModified().
				WithAffectedResources(2, 2, 0, 0).
				WithXRImpact("XNopResource", "test-resource", "default", "changed").
				AndComp().
				WithXRImpact("XNopResource", "another-resource", "default", "changed"),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if tc.slow && testing.Short() {
				t.Skip("slow: pulls or builds against a registry")
			}

			if reason := integrationEnvGap(); reason != "" {
				t.Skip(reason)
			}

			dir := t.TempDir()
			if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "project", tc.project))); err != nil {
				t.Fatalf("\n%s\ncannot copy fixture %q: %v", tc.reason, tc.project, err)
			}

			cfg := startIntegrationCluster(t, CompositionDiffTest, IntegrationTestCase{
				setupFiles:                 tc.setupFiles,
				crossplaneManagedResources: tc.crossplaneManagedResources,
			}, defaultTimeout).cfg

			argv := []string{
				"project", "simulate", "--timeout=" + defaultTimeout.String(),
				"-f", filepath.Join(dir, "crossplane-project.yaml"),
				"--cache-dir=" + t.TempDir(), "--skip-plan",
			}
			argv = append(argv, tc.extraArgs...)
			if tc.expectedStructured != nil {
				argv = append(argv, "-o", "json")
			}

			var stdout, stderr bytes.Buffer
			code, err := runSimulateArgvWith(t, cfg, argv, &project.SimulateHooks{RenderBinary: localCrossplaneBinary(t)}, &stdout, &stderr)

			if tc.expectedErrorContains != "" {
				if err == nil || !strings.Contains(err.Error(), tc.expectedErrorContains) {
					t.Fatalf("\n%s\nsimulate: want error containing %q, got %v\nstderr:\n%s", tc.reason, tc.expectedErrorContains, err, stderr.String())
				}
			} else if err != nil {
				t.Fatalf("\n%s\nsimulate: unexpected error: %v\nstderr:\n%s", tc.reason, err, stderr.String())
			}
			if code != tc.expectedExitCode {
				t.Errorf("\n%s\nsimulate: want exit code %d, got %d", tc.reason, tc.expectedExitCode, code)
			}
			if tc.expectedStructured != nil {
				simtest.AssertStructured(t, stdout.String(), tc.expectedStructured)
			}
		})
	}
}
