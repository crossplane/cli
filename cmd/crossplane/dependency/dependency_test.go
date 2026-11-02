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

package dependency

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"

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
  schemas:
    languages: [go]
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
		content       string
		schemasDir    string
		languages     []string
		wantSchemas   string
		wantLanguages []string
		wantDeps      []v1alpha1.Dependency
		wantErr       bool
	}{
		"Meta": {
			content:     testMeta,
			wantSchemas: "schemas",
			wantDeps:    metaDeps,
		},
		"MetaProvider": {
			content:     strings.Replace(testMeta, "kind: Configuration", "kind: Provider", 1),
			wantSchemas: "schemas",
			wantDeps:    metaDeps,
		},
		"MetaFunction": {
			content:     strings.Replace(testMeta, "kind: Configuration", "kind: Function", 1),
			wantSchemas: "schemas",
			wantDeps:    metaDeps,
		},
		"MetaFunctionV1beta1": {
			content: strings.NewReplacer(
				"meta.pkg.crossplane.io/v1", "meta.pkg.crossplane.io/v1beta1",
				"kind: Configuration", "kind: Function",
			).Replace(testMeta),
			wantSchemas: "schemas",
			wantDeps:    metaDeps,
		},
		"MetaOverrides": {
			content:       testMeta,
			schemasDir:    "gen/schemas",
			languages:     []string{"python"},
			wantSchemas:   "gen/schemas",
			wantLanguages: []string{"python"},
			wantDeps:      metaDeps,
		},
		"Project": {
			content:       testProject,
			wantSchemas:   "my-schemas",
			wantLanguages: []string{"go"},
		},
		"ProjectOverrides": {
			content:       testProject,
			schemasDir:    "other",
			languages:     []string{"python", "json"},
			wantSchemas:   "other",
			wantLanguages: []string{"python", "json"},
		},
		"SchemasDirEscapes": {
			content:    testMeta,
			schemasDir: "../schemas",
			wantErr:    true,
		},
		"SchemasDirIsRoot": {
			content:    testMeta,
			schemasDir: "./",
			wantErr:    true,
		},
		"UnsupportedLanguage": {
			content:   testMeta,
			languages: []string{"cobol"},
			wantErr:   true,
		},
		"UnsupportedMetaKind": {
			content: "apiVersion: meta.pkg.crossplane.io/v1\nkind: Bogus\n",
			wantErr: true,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fs := afero.NewMemMapFs()
			if err := afero.WriteFile(fs, "crossplane.yaml", []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}

			proj, _, err := load(fs, "crossplane.yaml", Overrides{SchemasDir: tc.schemasDir, SchemaLanguages: tc.languages})
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
