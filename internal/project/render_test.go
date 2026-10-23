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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-containerregistry/pkg/name"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
)

// fakeResolver resolves a ref to itself, or fails for refs in errs.
type fakeResolver struct {
	errs map[string]bool
}

func (r fakeResolver) ResolveRef(ref string) (name.Reference, error) {
	if r.errs[ref] {
		return nil, errors.New("boom")
	}
	return name.ParseReference(ref)
}

func xpkgDep(kind, pkg string, apiOnly bool) devv1alpha1.Dependency {
	return devv1alpha1.Dependency{
		Type: devv1alpha1.DependencyTypeXpkg,
		Xpkg: &devv1alpha1.XpkgDependency{Kind: kind, Package: pkg, Version: "v1.0.0", APIOnly: apiOnly},
	}
}

func TestLoadProviderDependencies(t *testing.T) {
	t.Parallel()

	const digest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	digestDep := xpkgDep(pkgv1.ProviderKind, "xpkg.example.com/acme/provider-pinned", false)
	digestDep.Xpkg.Version = digest

	cases := map[string]struct {
		deps     []devv1alpha1.Dependency
		resolver fakeResolver
		want     []string
		wantErr  bool
	}{
		"ProvidersOnly": {
			deps: []devv1alpha1.Dependency{
				xpkgDep(pkgv1.ProviderKind, "xpkg.example.com/acme/provider-a", false),
				xpkgDep(pkgv1.FunctionKind, "xpkg.example.com/acme/function-a", false),
				xpkgDep(pkgv1.ConfigurationKind, "xpkg.example.com/acme/configuration-a", false),
				xpkgDep(pkgv1.ProviderKind, "xpkg.example.com/acme/provider-api", true),
				{Type: devv1alpha1.DependencyTypeXpkg},
				digestDep,
			},
			want: []string{
				"xpkg.example.com/acme/provider-a:v1.0.0",
				"xpkg.example.com/acme/provider-pinned@" + digest,
			},
		},
		"ResolveError": {
			deps:     []devv1alpha1.Dependency{xpkgDep(pkgv1.ProviderKind, "xpkg.example.com/acme/provider-a", false)},
			resolver: fakeResolver{errs: map[string]bool{"xpkg.example.com/acme/provider-a:v1.0.0": true}},
			wantErr:  true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			proj := &devv1alpha1.Project{Spec: devv1alpha1.ProjectSpec{Dependencies: tc.deps}}
			got, err := LoadProviderDependencies(tc.resolver, proj)
			if (err != nil) != tc.wantErr {
				t.Fatalf("LoadProviderDependencies(...): error = %v, wantErr %v", err, tc.wantErr)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("LoadProviderDependencies(...): -want, +got:\n%s", diff)
			}
		})
	}
}
