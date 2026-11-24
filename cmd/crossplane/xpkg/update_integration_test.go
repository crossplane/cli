//go:build integration

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
	"bytes"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/google/go-cmp/cmp"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	"github.com/crossplane/crossplane-runtime/v2/pkg/version"

	v1 "github.com/crossplane/crossplane/apis/v2/pkg/v1"
)

// runUpdate parses args as `crossplane xpkg update` would and runs it, returning its stdout.
func runUpdate(t *testing.T, args ...string) (string, error) {
	t.Helper()

	var c updateCmd

	parser, err := kong.New(&c)
	if err != nil {
		t.Fatalf("kong.New(): %v", err)
	}

	kongCtx, err := parser.Parse(args)
	if err != nil {
		t.Fatalf("Parse(%v): %v", args, err)
	}

	var stdout bytes.Buffer
	kongCtx.Stdout = &stdout

	err = c.Run(kongCtx, logging.NewNopLogger())

	return stdout.String(), err
}

// TestUpdateIntegration updates packages in a real API server and checks the source read back from it.
func TestUpdateIntegration(t *testing.T) {
	cluster := startInstallCluster(t)

	type args struct {
		existing v1.Package
		update   []string
	}

	type want struct {
		obj    v1.Package
		source string
		stdout string
		err    error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Provider": {
			reason: "A provider's package source is replaced with the new reference.",
			args: args{
				existing: &v1.Provider{Name: "provider-a", Spec: v1.ProviderSpec{PackageSpec: v1.PackageSpec{Package: "xpkg.crossplane.io/acme/prov:v1.0.0"}}},
				update:   []string{"provider", "xpkg.crossplane.io/acme/prov:v2.0.0", "provider-a"},
			},
			want: want{
				obj:    &v1.Provider{Name: "provider-a"},
				source: "xpkg.crossplane.io/acme/prov:v2.0.0",
				stdout: "provider/provider-a updated\n",
			},
		},
		"Configuration": {
			reason: "A configuration's package source is replaced with the new reference.",
			args: args{
				existing: &v1.Configuration{Name: "config-a", Spec: v1.ConfigurationSpec{PackageSpec: v1.PackageSpec{Package: "xpkg.crossplane.io/acme/config:v1.0.0"}}},
				update:   []string{"configuration", "xpkg.crossplane.io/acme/config:v1.1.0", "config-a"},
			},
			want: want{
				obj:    &v1.Configuration{Name: "config-a"},
				source: "xpkg.crossplane.io/acme/config:v1.1.0",
				stdout: "configuration/config-a updated\n",
			},
		},
		"DefaultName": {
			reason: "Without a name the function to update is found by its repository, as a DNS label.",
			args: args{
				existing: &v1.Function{Name: "acme-fn", Spec: v1.FunctionSpec{PackageSpec: v1.PackageSpec{Package: "xpkg.crossplane.io/acme/fn:v1.0.0"}}},
				update:   []string{"function", "xpkg.crossplane.io/acme/fn:v1.2.0"},
			},
			want: want{
				obj:    &v1.Function{Name: "acme-fn"},
				source: "xpkg.crossplane.io/acme/fn:v1.2.0",
				stdout: "function/acme-fn updated\n",
			},
		},
		"InvalidReference": {
			reason: "Without a name, a reference that doesn't parse fails before contacting the cluster.",
			args:   args{update: []string{"function", "Not A Reference"}},
			want:   want{err: errors.Wrap(errors.New("could not parse reference: Not A Reference"), errPkgIdentifier)},
		},
		"NotFound": {
			// The Get failed, so the package's name is still empty in the error.
			reason: "Updating a package that doesn't exist fails, hinting that the CLI might be out of date.",
			args:   args{update: []string{"provider", "xpkg.crossplane.io/acme/prov:v2.0.0", "provider-missing"}},
			want: want{err: errors.Wrap(errors.Wrap(
				errors.WithMessagef(kerrors.NewNotFound(schema.GroupResource{Group: "pkg.crossplane.io", Resource: "providers"}, "provider-missing"),
					"crossplane CLI (version %s) might be out of date", version.New().GetVersionString()),
				"cannot get package"), "cannot update provider/")},
		},
	}

	// Cases share a cluster but use distinct package names, so they don't depend on each other.
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.args.existing != nil {
				if err := cluster.Client.Create(cluster.Context, tc.args.existing); err != nil {
					t.Fatalf("cannot create existing package %s: %v", tc.args.existing.GetName(), err)
				}
			}

			stdout, err := runUpdate(t, tc.args.update...)

			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Fatalf("\n%s\nRun(): -want error, +got error:\n%s", tc.reason, diff)
			}

			if tc.want.err != nil {
				return
			}

			if diff := cmp.Diff(tc.want.stdout, stdout); diff != "" {
				t.Errorf("\n%s\nstdout: -want, +got:\n%s", tc.reason, diff)
			}

			got := tc.want.obj
			if err := cluster.Client.Get(cluster.Context, client.ObjectKeyFromObject(got), got); err != nil {
				t.Fatalf("\n%s\nGet(%s): %v", tc.reason, got.GetName(), err)
			}

			if diff := cmp.Diff(tc.want.source, got.GetSource()); diff != "" {
				t.Errorf("\n%s\nspec.package: -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
