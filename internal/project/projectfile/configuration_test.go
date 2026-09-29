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
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
)

const testMeta = `apiVersion: meta.pkg.crossplane.io/v1
kind: Configuration
metadata:
  name: my-config
spec:
  dependsOn:
  - apiVersion: pkg.crossplane.io/v1
    kind: Provider
    package: xpkg.crossplane.io/crossplane-contrib/provider-nop
    version: ">=v0.2.0"
  - function: xpkg.crossplane.io/crossplane-contrib/function-auto-ready
    version: v0.5.0
  - configuration: xpkg.crossplane.io/crossplane-contrib/configuration-foo
`

const testProject = `apiVersion: dev.crossplane.io/v1alpha1
kind: Project
metadata:
  name: my-project
spec:
  repository: xpkg.crossplane.io/foo/bar
  paths:
    schemas: my-schemas
`

func TestLoad(t *testing.T) {
	t.Parallel()

	metaDeps := []v1alpha1.Dependency{
		{Type: v1alpha1.DependencyTypeXpkg, Xpkg: &v1alpha1.XpkgDependency{
			APIVersion: "pkg.crossplane.io/v1", Kind: "Provider",
			Package: "xpkg.crossplane.io/crossplane-contrib/provider-nop", Version: ">=v0.2.0",
		}},
		{Type: v1alpha1.DependencyTypeXpkg, Xpkg: &v1alpha1.XpkgDependency{
			APIVersion: "pkg.crossplane.io/v1", Kind: "Function",
			Package: "xpkg.crossplane.io/crossplane-contrib/function-auto-ready", Version: "v0.5.0",
		}},
		{Type: v1alpha1.DependencyTypeXpkg, Xpkg: &v1alpha1.XpkgDependency{
			APIVersion: "pkg.crossplane.io/v1", Kind: "Configuration",
			Package: "xpkg.crossplane.io/crossplane-contrib/configuration-foo", Version: ">=v0.0.0",
		}},
	}

	tcs := map[string]struct {
		file          string
		content       string
		schemasDir    string
		languages     []string
		k8sVersion    string
		wantSchemas   string
		wantLanguages []string
		wantDeps      []v1alpha1.Dependency
		wantErr       bool
	}{
		"MetaDefaultSchemas": {
			file:        "crossplane.yaml",
			content:     testMeta,
			wantSchemas: "schemas",
			wantDeps:    metaDeps,
		},
		"MetaSchemasFlag": {
			file:        "crossplane.yaml",
			content:     testMeta,
			schemasDir:  "gen/schemas",
			wantSchemas: "gen/schemas",
			wantDeps:    metaDeps,
		},
		"ProjectKeepsSchemasPath": {
			file:        "crossplane-project.yaml",
			content:     testProject,
			wantSchemas: "my-schemas",
		},
		"ProjectSchemasFlagOverrides": {
			file:        "crossplane-project.yaml",
			content:     testProject,
			schemasDir:  "other",
			wantSchemas: "other",
		},
		"SchemasDirEscapes": {
			file:       "crossplane.yaml",
			content:    testMeta,
			schemasDir: "../schemas",
			wantErr:    true,
		},
		"SchemasDirAbsolute": {
			file:       "crossplane.yaml",
			content:    testMeta,
			schemasDir: "/tmp/schemas",
			wantErr:    true,
		},
		"SchemasDirIsRoot": {
			file:       "crossplane.yaml",
			content:    testMeta,
			schemasDir: "./",
			wantErr:    true,
		},
		"MetaLanguagesFlag": {
			file:          "crossplane.yaml",
			content:       testMeta,
			languages:     []string{"python"},
			wantSchemas:   "schemas",
			wantLanguages: []string{"python"},
			wantDeps:      metaDeps,
		},
		"ProjectLanguagesFlagOverrides": {
			file:          "crossplane-project.yaml",
			content:       testProject + "  schemas:\n    languages: [go]\n",
			languages:     []string{"python", "json"},
			wantSchemas:   "my-schemas",
			wantLanguages: []string{"python", "json"},
		},
		"ProjectKeepsLanguages": {
			file:          "crossplane-project.yaml",
			content:       testProject + "  schemas:\n    languages: [go]\n",
			wantSchemas:   "my-schemas",
			wantLanguages: []string{"go"},
		},
		"MetaK8sVersionAdds": {
			file:        "crossplane.yaml",
			content:     testMeta,
			k8sVersion:  "v1.33.0",
			wantSchemas: "schemas",
			wantDeps: append(slices.Clone(metaDeps), v1alpha1.Dependency{
				Type: v1alpha1.DependencyTypeK8s, K8s: &v1alpha1.K8sDependency{Version: "v1.33.0"},
			}),
		},
		"ProjectK8sVersionReplaces": {
			file: "crossplane-project.yaml",
			content: testProject + `  dependencies:
  - type: k8s
    k8s:
      version: v1.31.0
  - type: xpkg
    xpkg:
      apiVersion: pkg.crossplane.io/v1
      kind: Provider
      package: xpkg.crossplane.io/crossplane-contrib/provider-nop
      version: v0.4.0
`,
			k8sVersion:  "v1.33.0",
			wantSchemas: "my-schemas",
			wantDeps: []v1alpha1.Dependency{
				{Type: v1alpha1.DependencyTypeXpkg, Xpkg: &v1alpha1.XpkgDependency{
					APIVersion: "pkg.crossplane.io/v1", Kind: "Provider",
					Package: "xpkg.crossplane.io/crossplane-contrib/provider-nop", Version: "v0.4.0",
				}},
				{Type: v1alpha1.DependencyTypeK8s, K8s: &v1alpha1.K8sDependency{Version: "v1.33.0"}},
			},
		},
		"UnsupportedLanguage": {
			file:      "crossplane.yaml",
			content:   testMeta,
			languages: []string{"rust"},
			wantErr:   true,
		},
		"NotAConfiguration": {
			file:    "crossplane.yaml",
			content: "apiVersion: meta.pkg.crossplane.io/v1\nkind: Provider\n",
			wantErr: true,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fs := afero.NewMemMapFs()
			if err := afero.WriteFile(fs, tc.file, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}

			proj, err := Load(fs, tc.file, Overrides{SchemasDir: tc.schemasDir, SchemaLanguages: tc.languages, K8sVersion: tc.k8sVersion})
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}

			if got := proj.Spec.Paths.Schemas; got != tc.wantSchemas {
				t.Errorf("schemas path: want %q, got %q", tc.wantSchemas, got)
			}
			if diff := cmp.Diff(tc.wantLanguages, proj.Spec.Schemas.GetLanguages()); diff != "" {
				t.Errorf("schema languages (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantDeps, proj.Spec.Dependencies); diff != "" {
				t.Errorf("dependencies (-want +got):\n%s", diff)
			}
		})
	}
}

func TestUpsertConfigurationDependency(t *testing.T) {
	t.Parallel()

	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, "crossplane.yaml", []byte(testMeta), 0o644); err != nil {
		t.Fatal(err)
	}

	// Replaces the deprecated-style function entry in place.
	if err := UpsertConfigurationDependency(fs, "crossplane.yaml", v1alpha1.XpkgDependency{
		APIVersion: "pkg.crossplane.io/v1", Kind: "Function",
		Package: "xpkg.crossplane.io/crossplane-contrib/function-auto-ready", Version: "v0.6.0",
	}); err != nil {
		t.Fatal(err)
	}
	// Appends a new entry.
	if err := UpsertConfigurationDependency(fs, "crossplane.yaml", v1alpha1.XpkgDependency{
		APIVersion: "pkg.crossplane.io/v1", Kind: "Provider",
		Package: "xpkg.crossplane.io/crossplane-contrib/provider-new", Version: "v1.0.0",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := afero.ReadFile(fs, "crossplane.yaml")
	if err != nil {
		t.Fatal(err)
	}

	want := `apiVersion: meta.pkg.crossplane.io/v1
kind: Configuration
metadata:
  name: my-config
spec:
  dependsOn:
  - apiVersion: pkg.crossplane.io/v1
    kind: Provider
    package: xpkg.crossplane.io/crossplane-contrib/provider-nop
    version: '>=v0.2.0'
  - apiVersion: pkg.crossplane.io/v1
    kind: Function
    package: xpkg.crossplane.io/crossplane-contrib/function-auto-ready
    version: v0.6.0
  - configuration: xpkg.crossplane.io/crossplane-contrib/configuration-foo
  - apiVersion: pkg.crossplane.io/v1
    kind: Provider
    package: xpkg.crossplane.io/crossplane-contrib/provider-new
    version: v1.0.0
`
	if diff := cmp.Diff(want, string(got)); diff != "" {
		t.Errorf("on-disk contents (-want +got):\n%s", diff)
	}

	// The result must still parse as a Configuration.
	var doc map[string]any
	if err := yaml.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(fs, "crossplane.yaml", Overrides{}); err != nil {
		t.Fatalf("updated file no longer loads: %v", err)
	}
}
