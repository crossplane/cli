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

package projectfile

import (
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
)

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setupFs       func(t *testing.T, fs afero.Fs)
		projectFile   string
		expectErr     bool
		expectedPaths *v1alpha1.ProjectPaths
	}{
		{
			name: "ValidProjectFileAllPaths",
			setupFs: func(t *testing.T, fs afero.Fs) {
				t.Helper()
				yamlContent := `
apiVersion: dev.crossplane.io/v1alpha1
kind: Project
metadata:
  name: ValidProjectFileAllPaths
spec:
  repository: xpkg.crossplane.io/test/test
  paths:
    apis: "test"
    examples: "example"
    functions: "funcs"
`
				if err := afero.WriteFile(fs, "/project.yaml", []byte(yamlContent), os.ModePerm); err != nil {
					t.Fatal(err)
				}
			},
			projectFile: "/project.yaml",
			// Defaults fill in tests, operations, schemas; user-set paths win.
			expectedPaths: &v1alpha1.ProjectPaths{
				APIs:       "test",
				Functions:  "funcs",
				Examples:   "example",
				Tests:      "tests",
				Operations: "operations",
				Schemas:    "schemas",
			},
		},
		{
			name: "ValidProjectFileSomePaths",
			setupFs: func(t *testing.T, fs afero.Fs) {
				t.Helper()
				yamlContent := `
apiVersion: dev.crossplane.io/v1alpha1
kind: Project
metadata:
  name: ValidProjectFileSomePaths
spec:
  repository: xpkg.crossplane.io/test/test
  paths:
    functions: "funcs"
`
				if err := afero.WriteFile(fs, "/project.yaml", []byte(yamlContent), os.ModePerm); err != nil {
					t.Fatal(err)
				}
			},
			projectFile: "/project.yaml",
			expectedPaths: &v1alpha1.ProjectPaths{
				APIs:       "apis",
				Functions:  "funcs",
				Examples:   "examples",
				Tests:      "tests",
				Operations: "operations",
				Schemas:    "schemas",
			},
		},
		{
			name: "InvalidProjectFileYAML",
			setupFs: func(t *testing.T, fs afero.Fs) {
				t.Helper()
				if err := afero.WriteFile(fs, "/project.yaml", []byte("invalid yaml content"), os.ModePerm); err != nil {
					t.Fatal(err)
				}
			},
			projectFile: "/project.yaml",
			expectErr:   true,
		},
		{
			name: "ProjectFileWithNoPaths",
			setupFs: func(t *testing.T, fs afero.Fs) {
				t.Helper()
				yamlContent := `
apiVersion: dev.crossplane.io/v1alpha1
kind: Project
metadata:
  name: ProjectFileWithNoPaths
spec:
  repository: xpkg.crossplane.io/test/test
`
				if err := afero.WriteFile(fs, "/project.yaml", []byte(yamlContent), os.ModePerm); err != nil {
					t.Fatal(err)
				}
			},
			projectFile: "/project.yaml",
			// All defaults applied.
			expectedPaths: &v1alpha1.ProjectPaths{
				APIs:       "apis",
				Functions:  "functions",
				Examples:   "examples",
				Tests:      "tests",
				Operations: "operations",
				Schemas:    "schemas",
			},
		},
		{
			name: "ProjectFileNotFound",
			setupFs: func(_ *testing.T, _ afero.Fs) {
			},
			projectFile: "/nonexistent.yaml",
			expectErr:   true,
		},
		{
			name: "WrongAPIVersion",
			setupFs: func(t *testing.T, fs afero.Fs) {
				t.Helper()
				yamlContent := `
apiVersion: foo.example.com/v1
kind: Project
metadata:
  name: WrongAPIVersion
spec:
  repository: xpkg.crossplane.io/test/test
`
				if err := afero.WriteFile(fs, "/project.yaml", []byte(yamlContent), os.ModePerm); err != nil {
					t.Fatal(err)
				}
			},
			projectFile: "/project.yaml",
			expectErr:   true,
		},
		{
			name: "MissingRepository",
			setupFs: func(t *testing.T, fs afero.Fs) {
				t.Helper()
				yamlContent := `
apiVersion: dev.crossplane.io/v1alpha1
kind: Project
metadata:
  name: MissingRepository
spec: {}
`
				if err := afero.WriteFile(fs, "/project.yaml", []byte(yamlContent), os.ModePerm); err != nil {
					t.Fatal(err)
				}
			},
			projectFile: "/project.yaml",
			expectErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fs := afero.NewMemMapFs()
			tt.setupFs(t, fs)

			proj, err := Parse(fs, tt.projectFile)

			if tt.expectErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}

			if diff := cmp.Diff(tt.expectedPaths, proj.Spec.Paths); diff != "" {
				t.Errorf("paths (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseWithoutDefaults(t *testing.T) {
	t.Parallel()

	yamlContent := `
apiVersion: dev.crossplane.io/v1alpha1
kind: Project
metadata:
  name: NoDefaults
spec:
  repository: xpkg.crossplane.io/test/test
`
	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, "/project.yaml", []byte(yamlContent), 0o644); err != nil {
		t.Fatal(err)
	}

	proj, err := ParseWithoutDefaults(fs, "/project.yaml")
	if err != nil {
		t.Fatal(err)
	}

	// Defaults must NOT be applied — Update relies on this so user-omitted
	// fields aren't persisted back to disk.
	if proj.Spec.Paths != nil {
		t.Errorf("Spec.Paths = %+v, want nil", proj.Spec.Paths)
	}
	if len(proj.Spec.Architectures) != 0 {
		t.Errorf("Spec.Architectures = %v, want empty", proj.Spec.Architectures)
	}
}

func TestUpdate(t *testing.T) {
	t.Parallel()

	proj := &v1alpha1.Project{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "dev.crossplane.io/v1alpha1",
			Kind:       "Project",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-project",
		},
		Spec: v1alpha1.ProjectSpec{
			Repository: "xpkg.crossplane.io/foo/bar",
		},
	}
	bs, err := yaml.Marshal(proj)
	if err != nil {
		t.Fatal(err)
	}
	projFS := afero.NewMemMapFs()
	if err := afero.WriteFile(projFS, "crossplane-project.yaml", bs, 0o644); err != nil {
		t.Fatal(err)
	}

	var want []byte
	err = Update(projFS, "crossplane-project.yaml", func(p *v1alpha1.Project) {
		p.Spec.Architectures = []string{"arch1", "arch2"}
		p.Spec.Paths = &v1alpha1.ProjectPaths{
			APIs:      "my-cool-apis",
			Functions: "my-cool-functions",
			Examples:  "my-cool-examples",
			Tests:     "my-cool-tests",
		}
		p.Spec.Dependencies = []v1alpha1.Dependency{{
			Type: v1alpha1.DependencyTypeXpkg,
			Xpkg: &v1alpha1.XpkgDependency{
				APIVersion: "pkg.crossplane.io/v1",
				Kind:       "Provider",
				Package:    "xpkg.crossplane.io/crossplane-contrib/provider-nop",
				Version:    "v0.2.1",
			},
		}}
		want, err = yaml.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := afero.ReadFile(projFS, "crossplane-project.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("on-disk contents (-want +got):\n%s", diff)
	}
}
