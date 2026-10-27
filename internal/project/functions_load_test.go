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
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	runtimexpkg "github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg/parser"

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// fakeXpkgClient serves one package and lists fixed versions.
type fakeXpkgClient struct {
	pkg  *runtimexpkg.Package
	tags []string
}

func (f *fakeXpkgClient) Get(_ context.Context, ref string, _ ...runtimexpkg.GetOption) (*runtimexpkg.Package, error) {
	if !strings.HasPrefix(ref, f.pkg.Source+":") {
		return nil, errors.Errorf("not found: %s", ref)
	}
	return f.pkg, nil
}

func (f *fakeXpkgClient) ListVersions(context.Context, string, ...runtimexpkg.GetOption) ([]string, error) {
	return f.tags, nil
}

// TestLoadFunctions verifies LoadFunctions resolves function dependencies
// through the dependency manager it is given.
func TestLoadFunctions(t *testing.T) {
	t.Parallel()

	const src = "xpkg.example.com/acme/function-a"
	metaScheme, err := runtimexpkg.BuildMetaScheme()
	if err != nil {
		t.Fatal(err)
	}
	objScheme, err := runtimexpkg.BuildObjectScheme()
	if err != nil {
		t.Fatal(err)
	}
	body := "apiVersion: meta.pkg.crossplane.io/v1\nkind: Function\nmetadata:\n  name: function-a\n"
	pkg, err := parser.New(metaScheme, objScheme).Parse(t.Context(), io.NopCloser(strings.NewReader(body)))
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeXpkgClient{
		pkg:  &runtimexpkg.Package{Package: pkg, Source: src, Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000"},
		tags: []string{"v1.0.0", "v1.2.0", "v2.0.0"},
	}

	projFS := afero.NewMemMapFs()
	writeProject(t, projFS, map[string]string{"db.yaml": xrdYAML("acme.example.com", "xdatabases", "xdatabase", "XDatabase")}, nil)
	dep := xpkgDep("Function", src, false)
	dep.Xpkg.Version = "<v2.0.0"
	proj := &devv1alpha1.Project{Spec: devv1alpha1.ProjectSpec{
		Repository:   "xpkg.example.com/acme/test",
		Dependencies: []devv1alpha1.Dependency{dep},
	}}
	proj.Default()

	depMgr := dependency.NewManager(proj, projFS,
		dependency.WithXpkgClient(fc),
		dependency.WithResolver(clixpkg.NewResolver(fc)),
	)

	fns, digests, err := LoadFunctions(t.Context(), proj, projFS, depMgr, LoadFunctionsOptions{
		Logger:  logging.NewNopLogger(),
		Spinner: terminal.NewNopSpinnerPrinter(),
	})
	if err != nil {
		t.Fatalf("LoadFunctions(...): %v", err)
	}

	got := make([]string, 0, len(fns))
	for _, fn := range fns {
		got = append(got, fn.Spec.Package)
	}
	if diff := cmp.Diff([]string{src + ":v1.2.0"}, got); diff != "" {
		t.Errorf("LoadFunctions(...): function packages -want, +got:\n%s", diff)
	}
	if len(digests) != 0 {
		t.Errorf("LoadFunctions(...): want no embedded function digests, got %v", digests)
	}
}
