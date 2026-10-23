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
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	un "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"

	"github.com/crossplane/cli/v2/internal/diff/testutils"
	"github.com/crossplane/cli/v2/internal/project"
)

func TestChangedFunctions(t *testing.T) {
	local := v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("a", 64)}
	other := v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("b", 64)}
	repo, err := name.NewRepository("xpkg.example.com/acme/proj_fn")
	if err != nil {
		t.Fatal(err)
	}
	const fnName = "acme-proj-fn"
	digests := map[string]project.EmbeddedDigest{fnName: {Repository: repo, Digest: local}}

	fnWithPackage := func(pkg string) func(context.Context, schema.GroupVersionKind, string, string) (*un.Unstructured, error) {
		return func(_ context.Context, gvk schema.GroupVersionKind, _, n string) (*un.Unstructured, error) {
			if gvk != pkgv1.FunctionGroupVersionKind || n != fnName {
				t.Errorf("GetResource(%v, %q): want Function %q", gvk, n, fnName)
			}
			return &un.Unstructured{Object: map[string]any{"spec": map[string]any{"package": pkg}}}, nil
		}
	}
	errBoom := errors.New("boom")

	cases := map[string]struct {
		get     func(context.Context, schema.GroupVersionKind, string, string) (*un.Unstructured, error)
		want    []string
		wantErr bool
	}{
		"EqualDigest":     {get: fnWithPackage("xpkg.example.com/acme/proj_fn@" + local.String())},
		"DifferentDigest": {get: fnWithPackage("xpkg.example.com/acme/proj_fn@" + other.String()), want: []string{fnName}},
		"TagOnly":         {get: fnWithPackage("xpkg.example.com/acme/proj_fn:v1"), want: []string{fnName}},
		"RepoMismatch":    {get: fnWithPackage("xpkg.example.com/other/proj_fn@" + local.String()), want: []string{fnName}},
		"NotFound": {get: func(context.Context, schema.GroupVersionKind, string, string) (*un.Unstructured, error) {
			return nil, kerrors.NewNotFound(schema.GroupResource{Group: "pkg.crossplane.io", Resource: "functions"}, fnName)
		}, want: []string{fnName}},
		"GetError": {get: func(context.Context, schema.GroupVersionKind, string, string) (*un.Unstructured, error) {
			return nil, errBoom
		}, wantErr: true},
	}
	for n, tc := range cases {
		t.Run(n, func(t *testing.T) {
			got, err := changedFunctions(t.Context(), &testutils.MockResourceClient{GetResourceFn: tc.get}, digests)
			if (err != nil) != tc.wantErr || (tc.wantErr && !errors.Is(err, errBoom)) {
				t.Fatalf("changedFunctions(...): err = %v, wantErr %t", err, tc.wantErr)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("changedFunctions(...): -want, +got:\n%s", diff)
			}
		})
	}
}
