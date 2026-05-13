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

package composition

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"
	"sigs.k8s.io/yaml"

	apiextv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/terminal"
)

const testProjectYAML = `apiVersion: dev.crossplane.io/v1alpha1
kind: Project
metadata:
  name: test-project
spec:
  paths:
    apis: apis
`

const testXRDYAML = `apiVersion: apiextensions.crossplane.io/v2
kind: CompositeResourceDefinition
metadata:
  name: xexamples.example.org
spec:
  group: example.org
  names:
    kind: XExample
    plural: xexamples
  versions:
  - name: v1alpha1
    served: true
    referenceable: true
    schema:
      openAPIV3Schema:
        type: object
`

// testProjectWithAutoReady returns a Project that already has
// function-auto-ready in dependencies, so ensureFunctionAutoReady is a no-op
// without needing a real dependency manager.
func testProjectWithAutoReady() *v1alpha1.Project {
	return &v1alpha1.Project{
		Spec: v1alpha1.ProjectSpec{
			Paths: &v1alpha1.ProjectPaths{
				APIs: "apis",
			},
			Dependencies: []v1alpha1.Dependency{
				{
					Type: v1alpha1.DependencyTypeXpkg,
					Xpkg: &v1alpha1.XpkgDependency{
						Package: functionAutoReadyPackage,
						Version: ">=v0.0.0",
					},
				},
			},
		},
	}
}

func setupTestFS(t *testing.T) (afero.Fs, afero.Fs) {
	t.Helper()
	fs := afero.NewMemMapFs()
	_ = afero.WriteFile(fs, "crossplane-project.yaml", []byte(testProjectYAML), 0o644)
	_ = fs.MkdirAll("apis/xexamples", 0o755)
	_ = afero.WriteFile(fs, "apis/xexamples/definition.yaml", []byte(testXRDYAML), 0o644)
	return fs, afero.NewBasePathFs(fs, "apis")
}

func TestGenerateComposition(t *testing.T) {
	type want struct {
		file         string
		compName     string
		apiVersion   string
		kind         string
		mode         apiextv1.CompositionMode
		pipelineLen  int
		firstStep    string
		errSubstring string
	}

	cases := map[string]struct {
		name        string
		plural      string
		preExisting map[string]string
		want        want
	}{
		"Default": {
			want: want{
				file:        "xexamples/composition.yaml",
				compName:    "xexamples.example.org",
				apiVersion:  "example.org/v1alpha1",
				kind:        "XExample",
				mode:        apiextv1.CompositionModePipeline,
				pipelineLen: 1,
				firstStep:   functionAutoReadyName,
			},
		},
		"WithName": {
			name: "aws",
			want: want{
				file:     "xexamples/composition-aws.yaml",
				compName: "aws.xexamples.example.org",
			},
		},
		"WithCustomPlural": {
			plural: "xthings",
			want: want{
				file:     "xthings/composition.yaml",
				compName: "xthings.example.org",
			},
		},
		"FileAlreadyExists": {
			preExisting: map[string]string{
				"apis/xexamples/composition.yaml": "existing",
			},
			want: want{
				errSubstring: "already exists",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fs, apisFS := setupTestFS(t)
			for path, content := range tc.preExisting {
				_ = afero.WriteFile(fs, path, []byte(content), 0o644)
			}

			cmd := &generateCmd{
				XRD:    "apis/xexamples/definition.yaml",
				Name:   tc.name,
				Plural: tc.plural,
				projFS: fs,
				apisFS: apisFS,
				proj:   testProjectWithAutoReady(),
			}

			err := cmd.Run(terminal.NewSpinnerPrinter(io.Discard, false))
			if tc.want.errSubstring != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.want.errSubstring)
				}
				if !strings.Contains(err.Error(), tc.want.errSubstring) {
					t.Errorf("error = %q, want substring %q", err.Error(), tc.want.errSubstring)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}

			exists, err := afero.Exists(apisFS, tc.want.file)
			if err != nil {
				t.Fatal(err)
			}
			if !exists {
				t.Fatalf("expected %q to be created", tc.want.file)
			}

			data, err := afero.ReadFile(apisFS, tc.want.file)
			if err != nil {
				t.Fatal(err)
			}

			var comp apiextv1.Composition
			if err := yaml.Unmarshal(data, &comp); err != nil {
				t.Fatal(err)
			}

			if diff := cmp.Diff(tc.want.compName, comp.Name); diff != "" {
				t.Errorf("name mismatch (-want +got):\n%s", diff)
			}
			if tc.want.apiVersion != "" {
				if diff := cmp.Diff(tc.want.apiVersion, comp.Spec.CompositeTypeRef.APIVersion); diff != "" {
					t.Errorf("apiVersion mismatch (-want +got):\n%s", diff)
				}
			}
			if tc.want.kind != "" {
				if diff := cmp.Diff(tc.want.kind, comp.Spec.CompositeTypeRef.Kind); diff != "" {
					t.Errorf("kind mismatch (-want +got):\n%s", diff)
				}
			}
			if tc.want.mode != "" {
				if diff := cmp.Diff(tc.want.mode, comp.Spec.Mode); diff != "" {
					t.Errorf("mode mismatch (-want +got):\n%s", diff)
				}
			}
			if tc.want.pipelineLen > 0 {
				if len(comp.Spec.Pipeline) != tc.want.pipelineLen {
					t.Fatalf("pipeline len = %d, want %d", len(comp.Spec.Pipeline), tc.want.pipelineLen)
				}
				if diff := cmp.Diff(tc.want.firstStep, comp.Spec.Pipeline[0].Step); diff != "" {
					t.Errorf("first step mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestEnsureFunctionAutoReady(t *testing.T) {
	cases := map[string]struct {
		proj *v1alpha1.Project
	}{
		// depManager is nil — if ensureFunctionAutoReady doesn't short-circuit,
		// it will panic, which is the desired failure mode for this test.
		"AlreadyExists": {
			proj: testProjectWithAutoReady(),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := &generateCmd{proj: tc.proj}
			if err := cmd.ensureFunctionAutoReady(context.Background()); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
