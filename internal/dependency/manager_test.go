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
	"context"
	"encoding/json"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	runtimexpkg "github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg/parser"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

const configurationPackageYAML = `apiVersion: meta.pkg.crossplane.io/v1
kind: Configuration
metadata:
  name: example
spec:
  crossplane:
    version: ">=v1.14.0"
---
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: things.example.com
spec:
  group: example.com
  names:
    plural: things
    kind: Thing
    listKind: ThingList
    singular: thing
  scope: Namespaced
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
`

const providerPackageYAML = `apiVersion: meta.pkg.crossplane.io/v1
kind: Provider
metadata:
  name: example
spec:
  crossplane:
    version: ">=v1.14.0"
`

const functionPackageYAML = `apiVersion: meta.pkg.crossplane.io/v1beta1
kind: Function
metadata:
  name: example
spec:
  crossplane:
    version: ">=v1.14.0"
`

// parsedPackage parses the given package YAML into a *parser.Package that the
// fake client can hand back from Get.
func parsedPackage(t *testing.T, body string) *parser.Package {
	t.Helper()
	metaScheme, err := runtimexpkg.BuildMetaScheme()
	if err != nil {
		t.Fatalf("build meta scheme: %v", err)
	}
	objScheme, err := runtimexpkg.BuildObjectScheme()
	if err != nil {
		t.Fatalf("build object scheme: %v", err)
	}
	pkg, err := parser.New(metaScheme, objScheme).Parse(context.Background(), io.NopCloser(strings.NewReader(body)))
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	return pkg
}

// parsedTestPackage parses configurationPackageYAML once into a *parser.Package
// that the fake client can hand back from Get.
func parsedTestPackage(t *testing.T) *parser.Package {
	t.Helper()
	return parsedPackage(t, configurationPackageYAML)
}

// fakeClient is a fake xpkg.Client. Get returns a pre-canned Package per ref;
// ListVersions returns a fixed tag list, so a real Resolver can be wired on
// top.
type fakeClient struct {
	packages map[string]*runtimexpkg.Package
	tags     []string
}

func (f *fakeClient) Get(_ context.Context, ref string, _ ...runtimexpkg.GetOption) (*runtimexpkg.Package, error) {
	pkg, ok := f.packages[ref]
	if !ok {
		return nil, errors.New("not found")
	}
	return pkg, nil
}

func (f *fakeClient) ListVersions(_ context.Context, _ string, _ ...runtimexpkg.GetOption) ([]string, error) {
	return f.tags, nil
}

func makePackage(t *testing.T, source, digest, version string) *runtimexpkg.Package {
	t.Helper()
	return &runtimexpkg.Package{
		Package: parsedTestPackage(t),
		Source:  source,
		Digest:  digest,
		Version: version,
	}
}

func makePackageWithBody(t *testing.T, source, digest, version, body string) *runtimexpkg.Package {
	t.Helper()
	return &runtimexpkg.Package{
		Package: parsedPackage(t, body),
		Source:  source,
		Digest:  digest,
		Version: version,
	}
}

func newTestManager(t *testing.T, fc *fakeClient) (*Manager, afero.Fs) {
	t.Helper()
	schemaFS := afero.NewMemMapFs()
	m := NewManager(
		&v1alpha1.Project{
			Spec: v1alpha1.ProjectSpec{
				Paths: &v1alpha1.ProjectPaths{Schemas: "schemas"},
			},
		},
		afero.NewMemMapFs(),
		WithSchemaFS(schemaFS),
		WithSchemaGenerators([]generator.Interface{}),
		WithXpkgClient(fc),
		WithResolver(clixpkg.NewResolver(fc)),
	)
	return m, schemaFS
}

// xpkgDep builds an xpkg dependency the same way cmd/crossplane/dependency/add.go
// does for the given package string.
func xpkgDep(pkg, version string) *v1alpha1.Dependency {
	return &v1alpha1.Dependency{
		Type: v1alpha1.DependencyTypeXpkg,
		Xpkg: &v1alpha1.XpkgDependency{
			Package: pkg,
			Version: version,
		},
	}
}

func TestManager_AddDependency(t *testing.T) {
	const (
		fnPkg     = "xpkg.crossplane.io/crossplane-contrib/function-auto-ready"
		provPkg   = "xpkg.crossplane.io/crossplane-contrib/provider-nop"
		cfgPkg    = "xpkg.crossplane.io/crossplane-contrib/configuration-empty"
		fnTag     = "v0.2.1"
		provTag   = "v0.2.1"
		cfgTag    = "v0.1.0"
		digestVal = "sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
	)

	type pkgInfo struct {
		body string
		// expectedAPIVersion / expectedKind that AddDependency should
		// populate on the runtime Xpkg fields.
		apiVersion string
		kind       string
	}
	fnInfo := pkgInfo{body: functionPackageYAML, apiVersion: "pkg.crossplane.io/v1beta1", kind: "Function"}
	provInfo := pkgInfo{body: providerPackageYAML, apiVersion: "pkg.crossplane.io/v1", kind: "Provider"}
	cfgInfo := pkgInfo{body: configurationPackageYAML, apiVersion: "pkg.crossplane.io/v1", kind: "Configuration"}

	tcs := map[string]struct {
		inputDeps    []v1alpha1.Dependency
		dep          *v1alpha1.Dependency
		pkg          pkgInfo
		fetchAt      string   // exact ref the fake client should serve.
		tags         []string // tags ListVersions returns.
		expectedDeps []v1alpha1.Dependency
		expectError  bool
	}{
		"AddFunctionWithoutVersion": {
			dep:     xpkgDep(fnPkg, ">=v0.0.0"),
			pkg:     fnInfo,
			fetchAt: fnPkg + ":" + fnTag,
			tags:    []string{fnTag},
			expectedDeps: []v1alpha1.Dependency{{
				Type: v1alpha1.DependencyTypeXpkg,
				Xpkg: &v1alpha1.XpkgDependency{
					APIVersion: fnInfo.apiVersion,
					Kind:       fnInfo.kind,
					Package:    fnPkg,
					Version:    ">=v0.0.0",
				},
			}},
		},
		"AddProviderWithoutVersion": {
			dep:     xpkgDep(provPkg, ">=v0.0.0"),
			pkg:     provInfo,
			fetchAt: provPkg + ":" + provTag,
			tags:    []string{provTag},
			expectedDeps: []v1alpha1.Dependency{{
				Type: v1alpha1.DependencyTypeXpkg,
				Xpkg: &v1alpha1.XpkgDependency{
					APIVersion: provInfo.apiVersion,
					Kind:       provInfo.kind,
					Package:    provPkg,
					Version:    ">=v0.0.0",
				},
			}},
		},
		"AddConfigurationWithoutVersion": {
			dep:     xpkgDep(cfgPkg, ">=v0.0.0"),
			pkg:     cfgInfo,
			fetchAt: cfgPkg + ":" + cfgTag,
			tags:    []string{cfgTag},
			expectedDeps: []v1alpha1.Dependency{{
				Type: v1alpha1.DependencyTypeXpkg,
				Xpkg: &v1alpha1.XpkgDependency{
					APIVersion: cfgInfo.apiVersion,
					Kind:       cfgInfo.kind,
					Package:    cfgPkg,
					Version:    ">=v0.0.0",
				},
			}},
		},
		"AddFunctionWithVersion": {
			dep:     xpkgDep(fnPkg, fnTag),
			pkg:     fnInfo,
			fetchAt: fnPkg + ":" + fnTag,
			tags:    []string{fnTag},
			expectedDeps: []v1alpha1.Dependency{{
				Type: v1alpha1.DependencyTypeXpkg,
				Xpkg: &v1alpha1.XpkgDependency{
					APIVersion: fnInfo.apiVersion,
					Kind:       fnInfo.kind,
					Package:    fnPkg,
					Version:    fnTag,
				},
			}},
		},
		"AddFunctionWithSemVersion": {
			dep:     xpkgDep(fnPkg, ">=v0.1.0"),
			pkg:     fnInfo,
			fetchAt: fnPkg + ":" + fnTag,
			tags:    []string{fnTag},
			expectedDeps: []v1alpha1.Dependency{{
				Type: v1alpha1.DependencyTypeXpkg,
				Xpkg: &v1alpha1.XpkgDependency{
					APIVersion: fnInfo.apiVersion,
					Kind:       fnInfo.kind,
					Package:    fnPkg,
					Version:    ">=v0.1.0",
				},
			}},
		},
		"AddFunctionWithSemVersionGreaterThan": {
			dep:     xpkgDep(fnPkg, ">v0.1.0"),
			pkg:     fnInfo,
			fetchAt: fnPkg + ":" + fnTag,
			tags:    []string{fnTag},
			expectedDeps: []v1alpha1.Dependency{{
				Type: v1alpha1.DependencyTypeXpkg,
				Xpkg: &v1alpha1.XpkgDependency{
					APIVersion: fnInfo.apiVersion,
					Kind:       fnInfo.kind,
					Package:    fnPkg,
					Version:    ">v0.1.0",
				},
			}},
		},
		"AddFunctionWithSemVersionLessThan": {
			dep:     xpkgDep(fnPkg, "<v0.3.0"),
			pkg:     fnInfo,
			fetchAt: fnPkg + ":" + fnTag,
			tags:    []string{fnTag},
			expectedDeps: []v1alpha1.Dependency{{
				Type: v1alpha1.DependencyTypeXpkg,
				Xpkg: &v1alpha1.XpkgDependency{
					APIVersion: fnInfo.apiVersion,
					Kind:       fnInfo.kind,
					Package:    fnPkg,
					Version:    "<v0.3.0",
				},
			}},
		},
		"AddFunctionWithSemVersionLessThanError": {
			dep:         xpkgDep(fnPkg, "<v0.2.0"),
			pkg:         fnInfo,
			tags:        []string{fnTag}, // only v0.2.1 available; <v0.2.0 has no match.
			expectError: true,
		},
		"AddProviderWithSemVersion": {
			dep:     xpkgDep(provPkg, "<=v0.3.0"),
			pkg:     provInfo,
			fetchAt: provPkg + ":" + provTag,
			tags:    []string{provTag},
			expectedDeps: []v1alpha1.Dependency{{
				Type: v1alpha1.DependencyTypeXpkg,
				Xpkg: &v1alpha1.XpkgDependency{
					APIVersion: provInfo.apiVersion,
					Kind:       provInfo.kind,
					Package:    provPkg,
					Version:    "<=v0.3.0",
				},
			}},
		},
		"AddConfigurationWithSemVersionNotAvailable": {
			dep:         xpkgDep(cfgPkg, ">=v1.0.0"),
			pkg:         cfgInfo,
			tags:        []string{cfgTag},
			expectError: true,
		},
		"AddProviderWithExistingDeps": {
			inputDeps: []v1alpha1.Dependency{{
				Type: v1alpha1.DependencyTypeXpkg,
				Xpkg: &v1alpha1.XpkgDependency{
					APIVersion: fnInfo.apiVersion,
					Kind:       fnInfo.kind,
					Package:    fnPkg,
					Version:    fnTag,
				},
			}},
			dep:     xpkgDep(provPkg, provTag),
			pkg:     provInfo,
			fetchAt: provPkg + ":" + provTag,
			tags:    []string{provTag},
			expectedDeps: []v1alpha1.Dependency{
				{
					Type: v1alpha1.DependencyTypeXpkg,
					Xpkg: &v1alpha1.XpkgDependency{
						APIVersion: fnInfo.apiVersion,
						Kind:       fnInfo.kind,
						Package:    fnPkg,
						Version:    fnTag,
					},
				},
				{
					Type: v1alpha1.DependencyTypeXpkg,
					Xpkg: &v1alpha1.XpkgDependency{
						APIVersion: provInfo.apiVersion,
						Kind:       provInfo.kind,
						Package:    provPkg,
						Version:    provTag,
					},
				},
			},
		},
		"UpdateFunction": {
			inputDeps: []v1alpha1.Dependency{{
				Type: v1alpha1.DependencyTypeXpkg,
				Xpkg: &v1alpha1.XpkgDependency{
					APIVersion: fnInfo.apiVersion,
					Kind:       fnInfo.kind,
					Package:    fnPkg,
					Version:    "v0.1.0",
				},
			}},
			dep:     xpkgDep(fnPkg, fnTag),
			pkg:     fnInfo,
			fetchAt: fnPkg + ":" + fnTag,
			tags:    []string{fnTag},
			expectedDeps: []v1alpha1.Dependency{{
				Type: v1alpha1.DependencyTypeXpkg,
				Xpkg: &v1alpha1.XpkgDependency{
					APIVersion: fnInfo.apiVersion,
					Kind:       fnInfo.kind,
					Package:    fnPkg,
					Version:    fnTag,
				},
			}},
		},
		"AddByDigest": {
			dep:     xpkgDep(fnPkg, digestVal),
			pkg:     fnInfo,
			fetchAt: fnPkg + "@" + digestVal,
			tags:    nil,
			expectedDeps: []v1alpha1.Dependency{{
				Type: v1alpha1.DependencyTypeXpkg,
				Xpkg: &v1alpha1.XpkgDependency{
					APIVersion: fnInfo.apiVersion,
					Kind:       fnInfo.kind,
					Package:    fnPkg,
					Version:    digestVal,
				},
			}},
		},
	}

	for tname, tc := range tcs {
		t.Run(tname, func(t *testing.T) {
			fc := &fakeClient{
				packages: map[string]*runtimexpkg.Package{},
				tags:     tc.tags,
			}
			if tc.fetchAt != "" {
				fc.packages[tc.fetchAt] = makePackageWithBody(t, tc.dep.Xpkg.Package, digestVal, "", tc.pkg.body)
			}

			projFS := afero.NewMemMapFs()
			proj := &v1alpha1.Project{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "dev.crossplane.io/v1alpha1",
					Kind:       "Project",
				},
				ObjectMeta: metav1.ObjectMeta{Name: "test-project"},
				Spec: v1alpha1.ProjectSpec{
					Repository:   "xpkg.crossplane.io/test/test",
					Dependencies: tc.inputDeps,
					Paths:        &v1alpha1.ProjectPaths{Schemas: "schemas"},
				},
			}
			bs, err := yaml.Marshal(proj)
			if err != nil {
				t.Fatal(err)
			}
			if err := afero.WriteFile(projFS, "crossplane-project.yaml", bs, 0o644); err != nil {
				t.Fatal(err)
			}

			m := NewManager(proj, projFS,
				WithProjectFile("crossplane-project.yaml"),
				WithSchemaFS(afero.NewMemMapFs()),
				WithSchemaGenerators([]generator.Interface{}),
				WithXpkgClient(fc),
				WithResolver(clixpkg.NewResolver(fc)),
			)

			err = m.AddDependency(context.Background(), tc.dep)
			if tc.expectError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("AddDependency: %v", err)
			}

			if diff := cmp.Diff(tc.expectedDeps, proj.Spec.Dependencies); diff != "" {
				t.Errorf("project deps (-want +got):\n%s", diff)
			}

			// Verify the project file on disk reflects the updated deps.
			gotBytes, err := afero.ReadFile(projFS, "crossplane-project.yaml")
			if err != nil {
				t.Fatal(err)
			}
			var gotProj v1alpha1.Project
			if err := yaml.Unmarshal(gotBytes, &gotProj); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.expectedDeps, gotProj.Spec.Dependencies); diff != "" {
				t.Errorf("on-disk project deps (-want +got):\n%s", diff)
			}
		})
	}
}

func TestManager_AddPackage(t *testing.T) {
	tests := map[string]struct {
		ref     string
		tags    []string
		fetchAt string
		wantKey string
	}{
		"ConstraintCollapsesToResolvedVersion": {
			ref:     "pkg.example/foo:>=v0.0.0",
			tags:    []string{"v0.5.2"},
			fetchAt: "pkg.example/foo:v0.5.2",
			wantKey: "xpkg://pkg.example/foo:v0.5.2",
		},
		"ExactVersionMatchesResolved": {
			ref:     "pkg.example/foo:v0.5.2",
			tags:    []string{"v0.5.2"},
			fetchAt: "pkg.example/foo:v0.5.2",
			wantKey: "xpkg://pkg.example/foo:v0.5.2",
		},
		"DigestRefUsesDigestForm": {
			ref:     "pkg.example/foo@sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
			fetchAt: "pkg.example/foo@sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
			wantKey: "xpkg://pkg.example/foo@sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fc := &fakeClient{
				packages: map[string]*runtimexpkg.Package{
					tc.fetchAt: makePackage(t, "pkg.example/foo", "sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03", ""),
				},
				tags: tc.tags,
			}
			m, schemaFS := newTestManager(t, fc)

			if _, err := m.AddPackage(context.Background(), tc.ref, false); err != nil {
				t.Fatalf("AddPackage: %v", err)
			}

			bs, err := afero.ReadFile(schemaFS, ".lock.json")
			if err != nil {
				t.Fatalf("read lock: %v", err)
			}
			var got struct {
				Packages map[string]string `json:"packages"`
			}
			if err := json.Unmarshal(bs, &got); err != nil {
				t.Fatalf("unmarshal lock: %v", err)
			}
			if _, ok := got.Packages[tc.wantKey]; !ok {
				t.Errorf("lock has no entry for %q; keys = %v", tc.wantKey, slices.Collect(maps.Keys(got.Packages)))
			}
			if len(got.Packages) != 1 {
				t.Errorf("lock packages = %d, want 1; got %v", len(got.Packages), got.Packages)
			}
		})
	}
}
