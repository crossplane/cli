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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/go-containerregistry/pkg/name"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"
)

var errBoom = errors.New("boom")

// fakeClient is a fake xpkg.Client whose ListVersions returns a fixed
// list of tags and whose Get is unused by the resolver.
type fakeClient struct {
	tags    []string
	listErr error
}

func (f *fakeClient) Get(_ context.Context, _ string, _ ...xpkg.GetOption) (*xpkg.Package, error) {
	return nil, nil
}

func (f *fakeClient) ListVersions(_ context.Context, _ string, _ ...xpkg.GetOption) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.tags, nil
}

func TestResolver_Resolve(t *testing.T) {
	tags := []string{"v1.0.0", "v1.1.0", "v2.0.0", "latest", "invalid"}

	type args struct {
		ref  string
		tags []string
	}

	type want struct {
		ref     name.Reference
		version string
		err     error
	}

	tests := map[string]struct {
		args args
		want want
	}{
		"DigestRef": {
			args: args{
				ref: "pkg.example/foo@sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
			},
			want: want{
				ref: name.MustParseReference("pkg.example/foo@sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"),
			},
		},
		"ExactSemver": {
			args: args{
				ref:  "pkg.example/foo:v1.0.0",
				tags: tags,
			},
			want: want{
				ref:     name.MustParseReference("pkg.example/foo:v1.0.0"),
				version: "v1.0.0",
			},
		},
		"OpaqueTag": {
			args: args{
				ref: "pkg.example/foo:latest",
			},
			want: want{
				ref:     name.MustParseReference("pkg.example/foo:latest"),
				version: "latest",
			},
		},
		"ConstraintRange": {
			args: args{
				ref:  "pkg.example/foo:>=v1.0.0, <v2.0.0",
				tags: tags,
			},
			want: want{
				ref:     name.MustParseReference("pkg.example/foo:v1.1.0"),
				version: "v1.1.0",
			},
		},
		"ConstraintCaret": {
			args: args{
				ref:  "pkg.example/foo:^v1.0.0",
				tags: tags,
			},
			want: want{
				ref:     name.MustParseReference("pkg.example/foo:v1.1.0"),
				version: "v1.1.0",
			},
		},
		"MissingVersion": {
			args: args{
				ref: "pkg.example/foo",
			},
			want: want{
				err: cmpopts.AnyError,
			},
		},
		"NoMatch": {
			args: args{
				ref:  "pkg.example/foo:>=v3.0.0",
				tags: tags,
			},
			want: want{
				err: cmpopts.AnyError,
			},
		},
	}

	for tname, tc := range tests {
		t.Run(tname, func(t *testing.T) {
			r := NewResolver(&fakeClient{tags: tc.args.tags})
			gotRef, gotVer, err := r.Resolve(context.Background(), tc.args.ref)
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Resolve(...), -want error, +got error:\n%s", diff)
			}

			// The name types have some embedded unexported fields that aren't
			// comparable, so we have to ignore them.
			ignoreUnexported := cmpopts.IgnoreUnexported(name.Registry{}, name.Repository{}, name.Tag{}, name.Digest{})
			if diff := cmp.Diff(tc.want.ref, gotRef, ignoreUnexported); diff != "" {
				t.Errorf("Resolve(...), -want ref +got ref:\n%s", diff)
			}
			if diff := cmp.Diff(tc.want.version, gotVer); diff != "" {
				t.Errorf("Resolve(...), -want verfsion +got versfion:\n%s", diff)
			}
		})
	}
}

func TestResolver_ResolveConstraints(t *testing.T) {
	tags := []string{"v1.0.0", "v1.1.0", "v1.2.0", "v2.0.0", "latest"}

	type args struct {
		repo        string
		constraints []string
		listErr     error
	}

	type want struct {
		ref     name.Reference
		version string
		err     error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"SingleConstraint": {
			reason: "A single constraint resolves to the highest matching version.",
			args:   args{repo: "pkg.example/foo", constraints: []string{">=v1.0.0, <v2.0.0"}},
			want: want{
				ref:     name.MustParseReference("pkg.example/foo:v1.2.0"),
				version: "v1.2.0",
			},
		},
		"IntersectionOfConstraints": {
			reason: "Every constraint must hold, so the result is the highest version in the intersection.",
			args:   args{repo: "pkg.example/foo", constraints: []string{">=v1.0.0", "<v1.2.0"}},
			want: want{
				ref:     name.MustParseReference("pkg.example/foo:v1.1.0"),
				version: "v1.1.0",
			},
		},
		"EmptyIntersection": {
			reason: "Constraints that no version satisfies together are an error.",
			args:   args{repo: "pkg.example/foo", constraints: []string{">=v2.0.0", "<v1.2.0"}},
			want:   want{err: cmpopts.AnyError},
		},
		"NoConstraints": {
			reason: "Resolving without constraints is an error.",
			args:   args{repo: "pkg.example/foo"},
			want:   want{err: cmpopts.AnyError},
		},
		"InvalidConstraint": {
			reason: "A constraint that does not parse is an error.",
			args:   args{repo: "pkg.example/foo", constraints: []string{"not-a-constraint"}},
			want:   want{err: cmpopts.AnyError},
		},
		"ListError": {
			reason: "A registry failure is returned.",
			args:   args{repo: "pkg.example/foo", constraints: []string{">=v1.0.0"}, listErr: errBoom},
			want:   want{err: cmpopts.AnyError},
		},
	}

	for tname, tc := range cases {
		t.Run(tname, func(t *testing.T) {
			r := NewResolver(&fakeClient{tags: tags, listErr: tc.args.listErr})
			gotRef, gotVer, err := r.ResolveConstraints(context.Background(), tc.args.repo, tc.args.constraints)
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("%s\nResolveConstraints(...), -want error, +got error:\n%s", tc.reason, diff)
			}

			ignoreUnexported := cmpopts.IgnoreUnexported(name.Registry{}, name.Repository{}, name.Tag{}, name.Digest{})
			if diff := cmp.Diff(tc.want.ref, gotRef, ignoreUnexported); diff != "" {
				t.Errorf("%s\nResolveConstraints(...), -want ref +got ref:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.version, gotVer); diff != "" {
				t.Errorf("%s\nResolveConstraints(...), -want version +got version:\n%s", tc.reason, diff)
			}
		})
	}
}
