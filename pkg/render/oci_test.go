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

package render

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	pkgv1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
)

const (
	testDigest  = "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"
	testRef     = "xpkg.crossplane.io/crossplane-contrib/function-dummy@" + testDigest
	testTagRef  = "xpkg.crossplane.io/crossplane-contrib/function-dummy:v1.0.0@" + testDigest
	testOtherFn = "xpkg.crossplane.io/crossplane-contrib/function-auto-ready@" + testDigest
)

func TestOCIRefsToFunctions(t *testing.T) {
	type want struct {
		fns []pkgv1.Function
		err error
	}

	cases := map[string]struct {
		reason string
		refs   []string
		want   want
	}{
		"NoRefs": {
			reason: "With no OCI references no Functions should be returned.",
			want:   want{},
		},
		"SingleRef": {
			reason: "An OCI reference should produce a Function named after, and running, that reference.",
			refs:   []string{testRef},
			want: want{
				fns: []pkgv1.Function{{
					APIVersion: pkgv1.FunctionGroupVersionKind.GroupVersion().String(),
					Kind:       pkgv1.FunctionKind,
					Name:       testRef,
					Spec: pkgv1.FunctionSpec{
						PackageSpec: pkgv1.PackageSpec{Package: testRef},
					},
				}},
			},
		},
		"TagAndDigestRef": {
			reason: "An OCI reference with both a tag and a digest should be used verbatim as the Function's name and package.",
			refs:   []string{testTagRef},
			want: want{
				fns: []pkgv1.Function{{
					APIVersion: pkgv1.FunctionGroupVersionKind.GroupVersion().String(),
					Kind:       pkgv1.FunctionKind,
					Name:       testRef,
					Spec: pkgv1.FunctionSpec{
						PackageSpec: pkgv1.PackageSpec{Package: testTagRef},
					},
				}},
			},
		},
		"MultipleRefs": {
			reason: "Each OCI reference should produce a Function, in the order the references were supplied.",
			refs:   []string{testOtherFn, testRef},
			want: want{
				fns: []pkgv1.Function{
					{
						APIVersion: pkgv1.FunctionGroupVersionKind.GroupVersion().String(),
						Kind:       pkgv1.FunctionKind,
						Name:       testOtherFn,
						Spec: pkgv1.FunctionSpec{
							PackageSpec: pkgv1.PackageSpec{Package: testOtherFn},
						},
					},
					{
						APIVersion: pkgv1.FunctionGroupVersionKind.GroupVersion().String(),
						Kind:       pkgv1.FunctionKind,
						Name:       testRef,
						Spec: pkgv1.FunctionSpec{
							PackageSpec: pkgv1.PackageSpec{Package: testRef},
						},
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := OCIRefsToFunctions(tc.refs)
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nOCIRefsToFunctions(...): -want error, +got error:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.fns, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("\n%s\nOCIRefsToFunctions(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestCheckOCIFunctionSupport(t *testing.T) {
	errBoom := errors.New("boom")

	cases := map[string]struct {
		reason  string
		version string
		err     error
		wantErr bool
	}{
		"MinimumVersion": {
			reason:  "The first version that supports OCI function references should be supported.",
			version: "v2.5.0",
		},
		"NewerVersion": {
			reason:  "Versions after the first that supports OCI function references should be supported.",
			version: "v2.6.1",
		},
		"NoLeadingV": {
			reason:  "Versions without a leading v should be parsed.",
			version: "2.5.0",
		},
		"DevelopmentBuild": {
			reason:  "Development builds of a supported version should be supported.",
			version: "v2.6.0-rc.0.123.gabcdef0",
		},
		"UnknownVersion": {
			reason:  "An engine that doesn't report a parseable version should be assumed to be supported.",
			version: "",
		},
		"OldVersion": {
			reason:  "Versions before the first that supports OCI function references should return an error.",
			version: "v2.4.3",
			wantErr: true,
		},
		"OldPreRelease": {
			reason:  "Pre-releases of unsupported versions should return an error.",
			version: "v2.4.0-rc.1",
			wantErr: true,
		},
		"OldMajorVersion": {
			reason:  "Older major versions should return an error.",
			version: "v1.20.0",
			wantErr: true,
		},
		"VersionError": {
			reason:  "An error getting the engine's version should be returned.",
			err:     errBoom,
			wantErr: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := &MockEngine{MockVersion: func(context.Context) (string, error) { return tc.version, tc.err }}

			err := CheckOCIFunctionSupport(t.Context(), e, logging.NewNopLogger())
			if (err != nil) != tc.wantErr {
				t.Errorf("\n%s\nCheckOCIFunctionSupport(...): want error %t, got %v", tc.reason, tc.wantErr, err)
			}
		})
	}
}
