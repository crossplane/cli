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
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
)

func TestParseSource(t *testing.T) {
	t.Parallel()

	const digest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	tcs := map[string]struct {
		source  string
		gitRef  string
		gitPath string
		want    v1alpha1.Dependency
		wantErr bool
	}{
		"K8s": {
			source: "k8s:v1.33.0",
			want:   v1alpha1.Dependency{Type: v1alpha1.DependencyTypeK8s, K8s: &v1alpha1.K8sDependency{Version: "v1.33.0"}},
		},
		"K8sNoVersion": {
			source:  "k8s:",
			wantErr: true,
		},
		"Git": {
			source:  "https://github.com/example/repo",
			gitRef:  "main",
			gitPath: "crds",
			want: v1alpha1.Dependency{Type: v1alpha1.DependencyTypeCRD, Git: &v1alpha1.GitDependency{
				Repository: "https://github.com/example/repo", Ref: "main", Path: "crds",
			}},
		},
		"HTTP": {
			source: "https://example.com/crd.yaml",
			want:   v1alpha1.Dependency{Type: v1alpha1.DependencyTypeCRD, HTTP: &v1alpha1.HTTPDependency{URL: "https://example.com/crd.yaml"}},
		},
		"XpkgNoVersion": {
			source: "xpkg.crossplane.io/org/pkg",
			want:   v1alpha1.Dependency{Type: v1alpha1.DependencyTypeXpkg, Xpkg: &v1alpha1.XpkgDependency{Package: "xpkg.crossplane.io/org/pkg", Version: ">=v0.0.0"}},
		},
		"XpkgConstraint": {
			source: "xpkg.crossplane.io/org/pkg:>v1.1.0",
			want:   v1alpha1.Dependency{Type: v1alpha1.DependencyTypeXpkg, Xpkg: &v1alpha1.XpkgDependency{Package: "xpkg.crossplane.io/org/pkg", Version: ">v1.1.0"}},
		},
		"XpkgDigest": {
			source: "xpkg.crossplane.io/org/pkg@" + digest,
			want:   v1alpha1.Dependency{Type: v1alpha1.DependencyTypeXpkg, Xpkg: &v1alpha1.XpkgDependency{Package: "xpkg.crossplane.io/org/pkg", Version: digest}},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseSource(tc.source, tc.gitRef, tc.gitPath)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ParseSource (-want +got):\n%s", diff)
			}
		})
	}
}
