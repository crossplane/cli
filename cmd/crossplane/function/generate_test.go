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

package function

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	apiextv1 "github.com/crossplane/crossplane/apis/v2/apiextensions/v1"

	v1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/terminal"
)

func testProject() *v1alpha1.Project {
	return &v1alpha1.Project{
		Spec: v1alpha1.ProjectSpec{
			Paths: &v1alpha1.ProjectPaths{
				Functions: "functions",
				Schemas:   "schemas",
			},
		},
	}
}

// seedFS returns a fresh MemMapFs populated with the given files. A nil byte
// slice creates an empty file.
func seedFS(t *testing.T, files map[string][]byte) afero.Fs {
	t.Helper()
	fs := afero.NewMemMapFs()
	for path, content := range files {
		dir := path[:strings.LastIndex(path, "/")+1]
		if dir != "" {
			_ = fs.MkdirAll(strings.TrimSuffix(dir, "/"), 0o755)
		}
		_ = afero.WriteFile(fs, path, content, 0o644)
	}
	return fs
}

func assertFiles(t *testing.T, fs afero.Fs, paths []string) {
	t.Helper()
	for _, f := range paths {
		exists, err := afero.Exists(fs, f)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("expected file %q to exist", f)
		}
	}
}

func assertContains(t *testing.T, fs afero.Fs, contains, notContains map[string][]byte) {
	t.Helper()
	for path, want := range contains {
		data, err := afero.ReadFile(fs, path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !bytes.Contains(data, want) {
			t.Errorf("%s: missing %q\ngot:\n%s", path, want, data)
		}
	}
	for path, want := range notContains {
		data, err := afero.ReadFile(fs, path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if bytes.Contains(data, want) {
			t.Errorf("%s: should not contain %q\ngot:\n%s", path, want, data)
		}
	}
}

func TestGenerateGoTemplatingFiles(t *testing.T) {
	cases := map[string]struct {
		seedSchemas     map[string][]byte
		wantFiles       []string
		wantContains    map[string][]byte
		wantNotContains map[string][]byte
	}{
		"NoSchema": {
			wantFiles: []string{"00-prelude.yaml.gotmpl", "01-compose.yaml.gotmpl"},
		},
		"WithSchema": {
			seedSchemas: map[string][]byte{
				"json/index.schema.json": []byte("{}"),
			},
			wantFiles: []string{"00-prelude.yaml.gotmpl", "01-compose.yaml.gotmpl"},
			wantContains: map[string][]byte{
				"01-compose.yaml.gotmpl": []byte("yaml-language-server"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &generateCmd{
				Name:      "my-func",
				schemasFS: seedFS(t, tc.seedSchemas),
				fsPath:    "functions/my-func",
			}
			fs := afero.NewMemMapFs()
			if err := c.generateGoTemplatingFiles(fs); err != nil {
				t.Fatal(err)
			}
			assertFiles(t, fs, tc.wantFiles)
			assertContains(t, fs, tc.wantContains, tc.wantNotContains)
		})
	}
}

func TestGenerateKCLFiles(t *testing.T) {
	cases := map[string]struct {
		seedSchemas     map[string][]byte
		wantFiles       []string
		wantContains    map[string][]byte
		wantNotContains map[string][]byte
	}{
		"NoSchemas": {
			wantFiles: []string{"main.k", "kcl.mod", "kcl.mod.lock"},
			wantContains: map[string][]byte{
				"kcl.mod": []byte(`name = "my-func"`),
			},
			wantNotContains: map[string][]byte{
				"kcl.mod": []byte("[dependencies]"),
			},
		},
		"WithSchemas": {
			seedSchemas: map[string][]byte{
				"kcl/io/example/aws/ec2/v1beta1/res.k": []byte("schema Bucket:"),
				"kcl/io/example/aws/s3/v1beta2/res.k":  []byte("schema Bucket:"),
			},
			wantFiles: []string{"main.k", "kcl.mod", "kcl.mod.lock"},
			wantContains: map[string][]byte{
				"main.k":  []byte("import models.io.example.aws.ec2.v1beta1 as ec2v1beta1"),
				"kcl.mod": []byte(`models = { path = "./model" }`),
			},
		},
		"WithSchemasS3Import": {
			seedSchemas: map[string][]byte{
				"kcl/io/example/aws/s3/v1beta2/res.k": []byte("schema Bucket:"),
			},
			wantContains: map[string][]byte{
				"main.k": []byte("import models.io.example.aws.s3.v1beta2 as s3v1beta2"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &generateCmd{
				Name:      "my-func",
				schemasFS: seedFS(t, tc.seedSchemas),
			}
			fs := afero.NewMemMapFs()
			if err := c.generateKCLFiles(fs); err != nil {
				t.Fatal(err)
			}
			assertFiles(t, fs, tc.wantFiles)
			assertContains(t, fs, tc.wantContains, tc.wantNotContains)
		})
	}
}

func TestGeneratePythonFiles(t *testing.T) {
	cases := map[string]struct {
		seedSchemas     map[string][]byte
		wantFiles       []string
		wantContains    map[string][]byte
		wantNotContains map[string][]byte
	}{
		"NoSchemas": {
			wantFiles: []string{
				"README.md",
				"pyproject.toml",
				"function/__init__.py",
				"function/__version__.py",
				"function/main.py",
				"function/fn.py",
			},
			wantNotContains: map[string][]byte{
				"pyproject.toml": []byte("crossplane-models"),
			},
		},
		"WithSchemas": {
			seedSchemas: map[string][]byte{
				"python/io/example/aws/v1beta1/__init__.py": nil,
			},
			wantContains: map[string][]byte{
				"pyproject.toml": []byte("crossplane-models @ file:./../../schemas/python"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &generateCmd{
				Name:      "my-func",
				schemasFS: seedFS(t, tc.seedSchemas),
				proj:      testProject(),
			}
			fs := afero.NewMemMapFs()
			if err := c.generatePythonFiles(fs); err != nil {
				t.Fatal(err)
			}
			assertFiles(t, fs, tc.wantFiles)
			assertContains(t, fs, tc.wantContains, tc.wantNotContains)
		})
	}
}

func TestGenerateGoFiles(t *testing.T) {
	cases := map[string]struct {
		seedSchemas  map[string][]byte
		wantFiles    []string
		wantContains map[string][]byte
	}{
		"NoSchemas": {
			wantFiles: []string{"main.go", "fn.go", "fn_test.go", "go.mod", "go.sum"},
			wantContains: map[string][]byte{
				"go.mod": []byte("github.com/example/my-project"),
			},
		},
		"WithSchemas": {
			seedSchemas: map[string][]byte{
				"go/.keep": nil,
			},
			wantContains: map[string][]byte{
				"go.mod": []byte("dev.crossplane.io/models"),
			},
		},
		"WithSchemasReplace": {
			seedSchemas: map[string][]byte{
				"go/.keep": nil,
			},
			wantContains: map[string][]byte{
				"go.mod": []byte("replace dev.crossplane.io/models"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			schemasFS := seedFS(t, tc.seedSchemas)
			// Tests that probe for the schemas dir need it to exist; an empty
			// MemMapFs has no entries, so create the dir explicitly.
			if len(tc.seedSchemas) > 0 {
				_ = schemasFS.MkdirAll("go", 0o755)
			}

			c := &generateCmd{
				Name:          "my-func",
				projectSource: "github.com/example/my-project",
				schemasFS:     schemasFS,
				proj:          testProject(),
			}
			fs := afero.NewMemMapFs()
			if err := c.generateGoFiles(fs); err != nil {
				t.Fatal(err)
			}
			assertFiles(t, fs, tc.wantFiles)
			assertContains(t, fs, tc.wantContains, nil)
		})
	}
}

func TestRunErrors(t *testing.T) {
	cases := map[string]struct {
		name             string
		language         string
		seedFunctionsFS  map[string][]byte
		stage            string // "afterApply" or "run"
		wantErrSubstring string
	}{
		"InvalidName": {
			name:             "INVALID_NAME",
			stage:            "afterApply",
			wantErrSubstring: "invalid function name",
		},
		"DirectoryNotEmpty": {
			name:     "my-func",
			language: "go-templating",
			seedFunctionsFS: map[string][]byte{
				"my-func/existing.txt": []byte("data"),
			},
			stage:            "run",
			wantErrSubstring: "already exists and is not empty",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &generateCmd{
				Name:        tc.name,
				Language:    tc.language,
				functionsFS: seedFS(t, tc.seedFunctionsFS),
				projFS:      afero.NewMemMapFs(),
			}
			var err error
			switch tc.stage {
			case "afterApply":
				err = c.AfterApply()
			case "run":
				err = c.Run(terminal.NewSpinnerPrinter(io.Discard, false))
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErrSubstring)
			}
			if !strings.Contains(err.Error(), tc.wantErrSubstring) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.wantErrSubstring)
			}
		})
	}
}

func TestAddCompositionStep(t *testing.T) {
	cases := map[string]struct {
		start         []apiextv1.PipelineStep
		step          string
		fnRef         string
		wantLen       int
		wantFirstStep string
		wantFirstFn   string
	}{
		"PrependsToExisting": {
			start: []apiextv1.PipelineStep{
				{
					Step:        "existing",
					FunctionRef: apiextv1.FunctionReference{Name: "existing-fn"},
				},
			},
			step:          "my-func",
			fnRef:         "my-fn-ref",
			wantLen:       2,
			wantFirstStep: "my-func",
			wantFirstFn:   "my-fn-ref",
		},
		"DedupsWhenAlreadyPresent": {
			start: []apiextv1.PipelineStep{
				{
					Step:        "my-func",
					FunctionRef: apiextv1.FunctionReference{Name: "my-fn-ref"},
				},
			},
			step:          "my-func",
			fnRef:         "my-fn-ref",
			wantLen:       1,
			wantFirstStep: "my-func",
			wantFirstFn:   "my-fn-ref",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			comp := &apiextv1.Composition{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "apiextensions.crossplane.io/v1",
					Kind:       "Composition",
				},
				Spec: apiextv1.CompositionSpec{
					Pipeline: tc.start,
				},
			}
			if err := addCompositionStep(comp, tc.step, tc.fnRef); err != nil {
				t.Fatal(err)
			}
			if len(comp.Spec.Pipeline) != tc.wantLen {
				t.Fatalf("pipeline len = %d, want %d", len(comp.Spec.Pipeline), tc.wantLen)
			}
			if diff := cmp.Diff(tc.wantFirstStep, comp.Spec.Pipeline[0].Step); diff != "" {
				t.Errorf("first step mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantFirstFn, comp.Spec.Pipeline[0].FunctionRef.Name); diff != "" {
				t.Errorf("first functionRef mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAddStepToComposition(t *testing.T) {
	cases := map[string]struct {
		comp          *apiextv1.Composition
		step          string
		fnRef         string
		wantLen       int
		wantFirstStep string
	}{
		"PrependsAndPersists": {
			comp: &apiextv1.Composition{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "apiextensions.crossplane.io/v1",
					Kind:       "Composition",
				},
				ObjectMeta: metav1.ObjectMeta{Name: "test-comp"},
				Spec: apiextv1.CompositionSpec{
					CompositeTypeRef: apiextv1.TypeReference{
						APIVersion: "example.org/v1",
						Kind:       "XExample",
					},
					Mode: apiextv1.CompositionModePipeline,
					Pipeline: []apiextv1.PipelineStep{
						{
							Step:        "auto-ready",
							FunctionRef: apiextv1.FunctionReference{Name: "crossplane-contrib-function-auto-ready"},
						},
					},
				},
			},
			step:          "my-func",
			fnRef:         "my-fn-ref",
			wantLen:       2,
			wantFirstStep: "my-func",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			compYAML, err := yaml.Marshal(tc.comp)
			if err != nil {
				t.Fatal(err)
			}

			fs := afero.NewMemMapFs()
			if err := afero.WriteFile(fs, "composition.yaml", compYAML, 0o644); err != nil {
				t.Fatal(err)
			}

			if err := addStepToComposition(fs, "composition.yaml", tc.step, tc.fnRef); err != nil {
				t.Fatal(err)
			}

			data, err := afero.ReadFile(fs, "composition.yaml")
			if err != nil {
				t.Fatal(err)
			}

			var result apiextv1.Composition
			if err := yaml.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}

			if len(result.Spec.Pipeline) != tc.wantLen {
				t.Fatalf("pipeline len = %d, want %d", len(result.Spec.Pipeline), tc.wantLen)
			}
			if diff := cmp.Diff(tc.wantFirstStep, result.Spec.Pipeline[0].Step); diff != "" {
				t.Errorf("first step mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
