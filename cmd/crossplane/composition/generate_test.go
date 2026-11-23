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
	pkgv1beta1 "github.com/crossplane/crossplane/apis/v2/pkg/v1beta1"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/terminal"
	"github.com/crossplane/cli/v2/pkg/images"
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
						Package: images.ToolImage(images.ToolFunctionAutoReady),
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
				firstStep:   "crossplane-contrib-function-auto-ready",
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
			if err := cmd.resolveAutoReady(context.Background()); err != nil {
				t.Fatalf("resolveAutoReady() failed: %v", err)
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
			if err := cmd.resolveAutoReady(context.Background()); err != nil {
				t.Fatalf("resolveAutoReady() failed: %v", err)
			}
			if err := cmd.ensureFunctionAutoReady(context.Background()); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestResolveAutoReady pins how the function-auto-ready dependency is
// resolved: the image catalog's default by default, and the project's image
// config rewrites applied on top, so a mirrored (e.g. air-gapped) project
// depends on the mirror's path. The pipeline step's functionRef is derived
// from the resolved package the way the package manager names installed
// Functions, so it keeps matching after a rewrite.
func TestResolveAutoReady(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		reason       string
		imageConfigs []pkgv1beta1.ImageConfig
		wantPackage  string
		wantName     string
	}{
		"Default": {
			reason:      "Without image configs the catalog default is used as-is.",
			wantPackage: "xpkg.crossplane.io/crossplane-contrib/function-auto-ready",
			wantName:    "crossplane-contrib-function-auto-ready",
		},
		"RewrittenToMirror": {
			reason: "An image config rewriting the default registry must rewrite the dependency; the functionRef name follows the rewritten path, since that's how the package manager names the installed Function.",
			imageConfigs: []pkgv1beta1.ImageConfig{{
				Spec: pkgv1beta1.ImageConfigSpec{
					MatchImages: []pkgv1beta1.ImageMatch{{Type: pkgv1beta1.Prefix, Prefix: "xpkg.crossplane.io"}},
					RewriteImage: &pkgv1beta1.ImageRewrite{
						Prefix: "registry.example.org/mirror",
					},
				},
			}},
			wantPackage: "registry.example.org/mirror/crossplane-contrib/function-auto-ready",
			wantName:    "mirror-crossplane-contrib-function-auto-ready",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			proj := &v1alpha1.Project{Spec: v1alpha1.ProjectSpec{ImageConfigs: tc.imageConfigs}}
			cmd := &generateCmd{proj: proj}
			if err := cmd.resolveAutoReady(context.Background()); err != nil {
				t.Fatalf("\n%s\nresolveAutoReady() failed: %v", tc.reason, err)
			}
			if cmd.autoReadyPackage != tc.wantPackage {
				t.Errorf("\n%s\nautoReadyPackage = %q, want %q", tc.reason, cmd.autoReadyPackage, tc.wantPackage)
			}
			if cmd.autoReadyName != tc.wantName {
				t.Errorf("\n%s\nautoReadyName = %q, want %q", tc.reason, cmd.autoReadyName, tc.wantName)
			}
		})
	}
}
