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

package xpkg

import (
	"context"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	pkgmetav1 "github.com/crossplane/crossplane/apis/v2/pkg/meta/v1"
	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
)

func TestParseConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		content   string
		expectErr bool
	}{
		{
			name: "ValidConfiguration",
			content: `
apiVersion: meta.pkg.crossplane.io/v1
kind: Configuration
metadata:
  name: my-config
spec:
  dependsOn:
  - function: ghcr.io/example/function-a
    version: "v1.0.0"
`,
		},
		{
			name:      "WrongAPIVersion",
			content:   "apiVersion: wrong.api/v1\nkind: Configuration\nspec: {}",
			expectErr: true,
		},
		{
			name:      "WrongKind",
			content:   "apiVersion: meta.pkg.crossplane.io/v1\nkind: Provider\nspec: {}",
			expectErr: true,
		},
		{
			name:      "InvalidYAML",
			content:   "not: valid: yaml: [",
			expectErr: true,
		},
		{
			name:      "FileNotFound",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fs := afero.NewMemMapFs()
			if tt.content != "" {
				if err := afero.WriteFile(fs, "/crossplane.yaml", []byte(tt.content), os.ModePerm); err != nil {
					t.Fatal(err)
				}
			}

			cfg, err := ParseConfiguration(fs, "/crossplane.yaml")
			if (err != nil) != tt.expectErr {
				t.Fatalf("ParseConfiguration() error = %v, expectErr %v", err, tt.expectErr)
			}
			if err == nil && cfg.Name != "my-config" {
				t.Errorf("name = %q, want %q", cfg.Name, "my-config")
			}
		})
	}
}

func TestResolveConfigurationFunctions(t *testing.T) {
	t.Parallel()

	fnA := "ghcr.io/example/function-a"
	fnB := "ghcr.io/example/function-b"
	provider := "ghcr.io/example/provider-x"

	tests := []struct {
		name string
		deps []pkgmetav1.Dependency
		want []pkgv1.Function
	}{
		{
			name: "FiltersFunctionsOnly",
			deps: []pkgmetav1.Dependency{
				{Function: &fnA, Version: "v1.0.0"},
				{Provider: &provider, Version: "v2.0.0"},
				{Function: &fnB, Version: "v0.5.0"},
			},
			want: []pkgv1.Function{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "function-a"},
					Spec:       pkgv1.FunctionSpec{PackageSpec: pkgv1.PackageSpec{Package: "ghcr.io/example/function-a:v1.0.0"}},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "function-b"},
					Spec:       pkgv1.FunctionSpec{PackageSpec: pkgv1.PackageSpec{Package: "ghcr.io/example/function-b:v0.5.0"}},
				},
			},
		},
		{
			name: "Empty",
			deps: nil,
			want: []pkgv1.Function{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &pkgmetav1.Configuration{
				Spec: pkgmetav1.ConfigurationSpec{
					MetaSpec: pkgmetav1.MetaSpec{DependsOn: tt.deps},
				},
			}

			resolver := NewResolver(&fakeClient{tags: []string{"v1.0.0", "v0.5.0", "latest"}})
			got, err := ResolveConfigurationFunctions(context.Background(), cfg, resolver)
			if err != nil {
				t.Fatal(err)
			}

			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("ResolveConfigurationFunctions (-want +got):\n%s", diff)
			}
		})
	}
}
