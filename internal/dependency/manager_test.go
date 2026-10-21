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
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/spf13/afero"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	runtimexpkg "github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg/parser"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/git"
	smanager "github.com/crossplane/cli/v2/internal/schemas/manager"
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

// configurationWithXRDPackageYAML is a Configuration package that bundles an
// XRD (rather than a raw CRD, like configurationPackageYAML above) - the
// shape that previously produced zero schemas.
const configurationWithXRDPackageYAML = `apiVersion: meta.pkg.crossplane.io/v1
kind: Configuration
metadata:
  name: example
spec:
  crossplane:
    version: ">=v1.14.0"
---
apiVersion: apiextensions.crossplane.io/v1
kind: CompositeResourceDefinition
metadata:
  name: xdatabases.acme.example.com
spec:
  group: acme.example.com
  names:
    kind: XDatabase
    plural: xdatabases
    singular: xdatabase
    listKind: XDatabaseList
  claimNames:
    kind: Database
    plural: databases
    singular: database
    listKind: DatabaseList
  scope: LegacyCluster
  versions:
  - name: v1alpha1
    served: true
    referenceable: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
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

// providerDependsOnPackageYAML is a provider meta with a new-style dependsOn
// entry; fill in the dependency repo and version.
const providerDependsOnPackageYAML = `apiVersion: meta.pkg.crossplane.io/v1
kind: Provider
metadata:
  name: example
spec:
  crossplane:
    version: ">=v1.14.0"
  dependsOn:
  - apiVersion: pkg.crossplane.io/v1
    kind: Provider
    package: %s
    version: %q
`

// providerDependsOnProviderYAML is a provider meta with a deprecated-style
// dependsOn entry; fill in the dependency repo and version.
const providerDependsOnProviderYAML = `apiVersion: meta.pkg.crossplane.io/v1
kind: Provider
metadata:
  name: example
spec:
  crossplane:
    version: ">=v1.14.0"
  dependsOn:
  - provider: %s
    version: %q
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

// fakeClient is a fake xpkg.Client. Get returns a pre-canned Package per ref;
// ListVersions returns the tags for the requested repo, falling back to a
// fixed tag list, so a real Resolver can be wired on top.
type fakeClient struct {
	packages   map[string]*runtimexpkg.Package
	tags       []string
	tagsByRepo map[string][]string

	// jitter delays every call by a random time, to shuffle the order in which
	// concurrent fetches finish.
	jitter bool

	mu   sync.Mutex
	gets map[string]int
}

func (f *fakeClient) delay() {
	if f.jitter {
		time.Sleep(time.Duration(rand.IntN(300)) * time.Microsecond)
	}
}

func (f *fakeClient) Get(_ context.Context, ref string, _ ...runtimexpkg.GetOption) (*runtimexpkg.Package, error) {
	f.delay()
	f.mu.Lock()
	if f.gets == nil {
		f.gets = make(map[string]int)
	}
	f.gets[ref]++
	f.mu.Unlock()

	pkg, ok := f.packages[ref]
	if !ok {
		return nil, errors.New("not found")
	}
	return pkg, nil
}

func (f *fakeClient) ListVersions(_ context.Context, repo string, _ ...runtimexpkg.GetOption) ([]string, error) {
	f.delay()
	if tags, ok := f.tagsByRepo[repo]; ok {
		return tags, nil
	}
	return f.tags, nil
}

func (f *fakeClient) getCount(ref string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets[ref]
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

func newTestManager(t *testing.T, fc *fakeClient) *Manager {
	t.Helper()
	return NewManager(
		&v1alpha1.Project{
			Spec: v1alpha1.ProjectSpec{
				Paths: &v1alpha1.ProjectPaths{Schemas: "schemas"},
			},
		},
		afero.NewMemMapFs(),
		WithXpkgClient(fc),
		WithResolver(clixpkg.NewResolver(fc)),
	)
}

// sourceKeys returns the keys of the sources.
func sourceKeys(sources []smanager.Source) []string {
	keys := make([]string, 0, len(sources))
	for _, src := range sources {
		keys = append(keys, src.Key())
	}
	return keys
}

// refDep builds an xpkg dependency from an OCI ref, whose version may be a tag,
// a constraint or a digest.
func refDep(ref string) *v1alpha1.Dependency {
	repo := refRepo(ref)
	return xpkgDep(repo, strings.TrimLeft(strings.TrimPrefix(ref, repo), ":@"))
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

func TestManager_Resolve(t *testing.T) {
	const (
		cfgXRDPkg = "xpkg.crossplane.io/crossplane-contrib/configuration-xrd"
		cfgXRDTag = "v0.1.0"
	)

	type args struct {
		ref     string
		tags    []string
		fetchAt string
		// body is the package YAML served by the fake client; empty
		// defaults to configurationPackageYAML.
		body string
	}
	type want struct {
		// key is the only schema source the package should yield.
		key string
		// crds is whether the source's resources contain CRD files.
		crds bool
		err  error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ConstraintCollapsesToResolvedVersion": {
			reason: "A semver constraint resolves to a tag, but the source key never contains a version.",
			args:   args{ref: "pkg.example/foo:>=v0.0.0", tags: []string{"v0.5.2"}, fetchAt: "pkg.example/foo:v0.5.2"},
			want:   want{key: "xpkg://pkg.example/foo", crds: true},
		},
		"ExactVersionMatchesResolved": {
			reason: "An exact tag yields the same key as a constraint on the same package.",
			args:   args{ref: "pkg.example/foo:v0.5.2", tags: []string{"v0.5.2"}, fetchAt: "pkg.example/foo:v0.5.2"},
			want:   want{key: "xpkg://pkg.example/foo", crds: true},
		},
		"DigestRefSharesKeyWithTags": {
			reason: "A digest-pinned ref yields the same key, so one lock entry tracks the package whatever the ref form.",
			args: args{
				ref:     "pkg.example/foo@sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
				fetchAt: "pkg.example/foo@sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03",
			},
			want: want{key: "xpkg://pkg.example/foo", crds: true},
		},
		// A Configuration that bundles XRDs (rather than raw CRDs) must yield
		// real CRD resources, not an empty source. Before
		// internal/xpkg.CRDFilesystem learned to convert XRDs to their derived
		// CRD form, this produced a source with zero schema content.
		"ConfigurationWithXRDYieldsCRDs": {
			reason: "XRDs bundled in a Configuration are converted to CRDs for schema generation.",
			args: args{
				ref:     cfgXRDPkg + ":" + cfgXRDTag,
				tags:    []string{cfgXRDTag},
				fetchAt: cfgXRDPkg + ":" + cfgXRDTag,
				body:    configurationWithXRDPackageYAML,
			},
			want: want{key: "xpkg://" + cfgXRDPkg, crds: true},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			body := tc.args.body
			if body == "" {
				body = configurationPackageYAML
			}
			fc := &fakeClient{
				packages: map[string]*runtimexpkg.Package{
					tc.args.fetchAt: makePackageWithBody(t, refRepo(tc.args.ref), "sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03", "", body),
				},
				tags: tc.args.tags,
			}
			m := newTestManager(t, fc)

			m.proj.Spec.Dependencies = []v1alpha1.Dependency{*refDep(tc.args.ref)}
			sources, _, err := m.Resolve(context.Background(), ResolveOptions{})
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("%s\nResolve(...): -want err, +got err:\n%s", tc.reason, diff)
			}

			if diff := cmp.Diff([]string{tc.want.key}, sourceKeys(sources)); diff != "" {
				t.Errorf("%s\nResolve(...): -want source keys, +got source keys:\n%s", tc.reason, diff)
			}
			resources, err := sources[0].Resources(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			files, err := afero.Glob(resources, "*.yaml")
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.want.crds, len(files) > 0); diff != "" {
				t.Errorf("%s\nResolve(...): -want CRD files, +got CRD files:\n%s\nfiles: %v", tc.reason, diff, files)
			}
		})
	}
}

// refRepo returns the repository portion of an OCI ref, stripping a digest or
// tag suffix.
func refRepo(ref string) string {
	if repo, _, ok := strings.Cut(ref, "@"); ok {
		return repo
	}
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		return ref[:i]
	}
	return ref
}

func TestManager_Resolve_TransitiveDeps(t *testing.T) {
	const (
		provA     = "xpkg.example/prov-a"
		provB     = "xpkg.example/prov-b"
		family    = "xpkg.example/family"
		digestVal = "sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
	)

	type pkgFixture struct {
		ref  string // exact ref the fake client serves.
		body string
	}

	type args struct {
		packages   []pkgFixture
		tagsByRepo map[string][]string
		addRefs    []string
	}
	type want struct {
		// keys are the source keys of the lock.
		keys []string
		// gets are the expected fetch counts, by ref.
		gets map[string]int
		err  error
		// errMsg is a substring the error must contain: it must name the
		// package that could not be fetched.
		errMsg string
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"TransitiveDepGeneratesSchemas": {
			reason: "A dependency of a package is resolved and gets its own source, so schemas are generated for it too.",
			args: args{
				packages: []pkgFixture{
					{ref: provA + ":v0.1.0", body: fmt.Sprintf(providerDependsOnPackageYAML, family, "v1.0.0")},
					{ref: family + ":v1.0.0", body: providerPackageYAML},
				},
				tagsByRepo: map[string][]string{
					provA:  {"v0.1.0"},
					family: {"v1.0.0"},
				},
				addRefs: []string{provA + ":v0.1.0"},
			},
			want: want{
				keys: []string{
					"xpkg://" + family,
					"xpkg://" + provA,
				},
			},
		},
		"TransitiveDepWithConstraint": {
			reason: "A version constraint on a dependency of a package resolves to the matching tag.",
			args: args{
				packages: []pkgFixture{
					{ref: provA + ":v0.1.0", body: fmt.Sprintf(providerDependsOnPackageYAML, family, ">=v1.0.0")},
					{ref: family + ":v1.2.3", body: providerPackageYAML},
				},
				tagsByRepo: map[string][]string{
					provA:  {"v0.1.0"},
					family: {"v1.2.3"},
				},
				addRefs: []string{provA + ":v0.1.0"},
			},
			want: want{
				keys: []string{
					"xpkg://" + family,
					"xpkg://" + provA,
				},
			},
		},
		"TransitiveDepByDigest": {
			reason: "A dependency of a package that is pinned by digest is fetched by that digest.",
			args: args{
				packages: []pkgFixture{
					{ref: provA + ":v0.1.0", body: fmt.Sprintf(providerDependsOnPackageYAML, family, digestVal)},
					{ref: family + "@" + digestVal, body: providerPackageYAML},
				},
				tagsByRepo: map[string][]string{
					provA: {"v0.1.0"},
				},
				addRefs: []string{provA + ":v0.1.0"},
			},
			want: want{
				keys: []string{
					"xpkg://" + family,
					"xpkg://" + provA,
				},
			},
		},
		"DeprecatedProviderField": {
			reason: "A dependency declared through the deprecated provider field is resolved like any other.",
			args: args{
				packages: []pkgFixture{
					{ref: provA + ":v0.1.0", body: fmt.Sprintf(providerDependsOnProviderYAML, family, "v1.0.0")},
					{ref: family + ":v1.0.0", body: providerPackageYAML},
				},
				tagsByRepo: map[string][]string{
					provA:  {"v0.1.0"},
					family: {"v1.0.0"},
				},
				addRefs: []string{provA + ":v0.1.0"},
			},
			want: want{
				keys: []string{
					"xpkg://" + family,
					"xpkg://" + provA,
				},
			},
		},
		"DiamondSharedDepFetchedOnce": {
			reason: "A dependency shared by two packages is fetched only once.",
			args: args{
				packages: []pkgFixture{
					{ref: provA + ":v0.1.0", body: fmt.Sprintf(providerDependsOnPackageYAML, family, "v1.0.0")},
					{ref: provB + ":v0.2.0", body: fmt.Sprintf(providerDependsOnPackageYAML, family, "v1.0.0")},
					{ref: family + ":v1.0.0", body: providerPackageYAML},
				},
				tagsByRepo: map[string][]string{
					provA:  {"v0.1.0"},
					provB:  {"v0.2.0"},
					family: {"v1.0.0"},
				},
				addRefs: []string{provA + ":v0.1.0", provB + ":v0.2.0"},
			},
			want: want{
				keys: []string{
					"xpkg://" + family,
					"xpkg://" + provA,
					"xpkg://" + provB,
				},
				gets: map[string]int{
					family + ":v1.0.0": 1,
				},
			},
		},
		"CycleTerminates": {
			reason: "Packages that depend on each other do not make resolution loop forever.",
			args: args{
				packages: []pkgFixture{
					{ref: provA + ":v0.1.0", body: fmt.Sprintf(providerDependsOnPackageYAML, provB, "v0.2.0")},
					{ref: provB + ":v0.2.0", body: fmt.Sprintf(providerDependsOnPackageYAML, provA, "v0.1.0")},
				},
				tagsByRepo: map[string][]string{
					provA: {"v0.1.0"},
					provB: {"v0.2.0"},
				},
				addRefs: []string{provA + ":v0.1.0"},
			},
			want: want{
				keys: []string{
					"xpkg://" + provA,
					"xpkg://" + provB,
				},
			},
		},
		"TransitiveFetchFailure": {
			reason: "A dependency of a package that cannot be fetched is an error naming that dependency.",
			args: args{
				packages: []pkgFixture{
					{ref: provA + ":v0.1.0", body: fmt.Sprintf(providerDependsOnPackageYAML, family, "v9.9.9")},
				},
				tagsByRepo: map[string][]string{
					provA:  {"v0.1.0"},
					family: {},
				},
				addRefs: []string{provA + ":v0.1.0"},
			},
			want: want{
				err: cmpopts.AnyError, errMsg: family,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fc := &fakeClient{
				packages:   map[string]*runtimexpkg.Package{},
				tagsByRepo: tc.args.tagsByRepo,
			}
			for _, p := range tc.args.packages {
				fc.packages[p.ref] = makePackageWithBody(t, refRepo(p.ref), "sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03", "", p.body)
			}
			m := newTestManager(t, fc)

			for _, ref := range tc.args.addRefs {
				m.proj.Spec.Dependencies = append(m.proj.Spec.Dependencies, *refDep(ref))
			}
			sources, _, err := m.Resolve(context.Background(), ResolveOptions{})
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("%s\nResolve(...): -want err, +got err:\n%s", tc.reason, diff)
			}
			if tc.want.err != nil {
				if !strings.Contains(err.Error(), tc.want.errMsg) {
					t.Errorf("%s\nResolve(...): error %q does not contain %q", tc.reason, err, tc.want.errMsg)
				}
				return
			}

			want := slices.Clone(tc.want.keys)
			slices.Sort(want)
			if diff := cmp.Diff(want, sourceKeys(sources)); diff != "" {
				t.Errorf("%s\nResolve(...): -want source keys, +got source keys:\n%s", tc.reason, diff)
			}

			for ref, wantCount := range tc.want.gets {
				if diff := cmp.Diff(wantCount, fc.getCount(ref)); diff != "" {
					t.Errorf("%s\nResolve(...): -want fetch count for %s, +got fetch count:\n%s", tc.reason, ref, diff)
				}
			}
		})
	}
}

func TestManager_Resolve_SharedTransitiveDep(t *testing.T) {
	const (
		provA  = "xpkg.example/prov-a"
		provB  = "xpkg.example/prov-b"
		family = "xpkg.example/family"
		digest = "sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
	)

	type args struct {
		packages   map[string]*runtimexpkg.Package
		tagsByRepo map[string][]string
		deps       []v1alpha1.Dependency
	}
	type want struct {
		// gets are the expected fetch counts, by ref.
		gets map[string]int
		keys []string
		err  error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"TwoPackagesShareDependency": {
			reason: "A dependency needed by several packages is fetched once, however many need it, and listed once.",
			args: args{
				packages: map[string]*runtimexpkg.Package{
					provA + ":v0.1.0":  makePackageWithBody(t, provA, digest, "", fmt.Sprintf(providerDependsOnPackageYAML, family, "v1.0.0")),
					provB + ":v0.2.0":  makePackageWithBody(t, provB, digest, "", fmt.Sprintf(providerDependsOnPackageYAML, family, "v1.0.0")),
					family + ":v1.0.0": makePackageWithBody(t, family, digest, "", providerPackageYAML),
				},
				tagsByRepo: map[string][]string{
					provA:  {"v0.1.0"},
					provB:  {"v0.2.0"},
					family: {"v1.0.0"},
				},
				deps: []v1alpha1.Dependency{*xpkgDep(provA, "v0.1.0"), *xpkgDep(provB, "v0.2.0")},
			},
			want: want{
				gets: map[string]int{family + ":v1.0.0": 1},
				keys: []string{"xpkg://" + family, "xpkg://" + provA, "xpkg://" + provB},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fc := &fakeClient{
				packages:   tc.args.packages,
				tagsByRepo: tc.args.tagsByRepo,
			}

			m := NewManager(
				&v1alpha1.Project{
					Spec: v1alpha1.ProjectSpec{
						Dependencies: tc.args.deps,
						Paths:        &v1alpha1.ProjectPaths{Schemas: "schemas"},
					},
				},
				afero.NewMemMapFs(),
				WithXpkgClient(fc),
				WithResolver(clixpkg.NewResolver(fc)),
			)

			sources, _, err := m.Resolve(context.Background(), ResolveOptions{})
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("%s\nResolve(...): -want err, +got err:\n%s", tc.reason, diff)
			}

			for ref, wantCount := range tc.want.gets {
				if diff := cmp.Diff(wantCount, fc.getCount(ref)); diff != "" {
					t.Errorf("%s\nResolve(...): -want fetch count for %s, +got fetch count:\n%s", tc.reason, ref, diff)
				}
			}
			if diff := cmp.Diff(tc.want.keys, sourceKeys(sources)); diff != "" {
				t.Errorf("%s\nResolve(...): -want source keys, +got source keys:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestManager_AddDependency_TransitiveNotPersisted(t *testing.T) {
	const (
		provA  = "xpkg.example/prov-a"
		family = "xpkg.example/family"
		digest = "sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
	)

	type args struct {
		packages   map[string]*runtimexpkg.Package
		tagsByRepo map[string][]string
		dep        *v1alpha1.Dependency
	}
	type want struct {
		// gets are the expected fetch counts, by ref.
		gets map[string]int
		// deps are the dependencies persisted to the project, in memory and on
		// disk.
		deps []v1alpha1.Dependency
		err  error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"OnlyDeclaredPersisted": {
			reason: "The transitive package is fetched into the cache, but only the declared dependency is persisted to the project.",
			args: args{
				packages: map[string]*runtimexpkg.Package{
					provA + ":v0.1.0":  makePackageWithBody(t, provA, digest, "", fmt.Sprintf(providerDependsOnPackageYAML, family, "v1.0.0")),
					family + ":v1.0.0": makePackageWithBody(t, family, digest, "", providerPackageYAML),
				},
				tagsByRepo: map[string][]string{
					provA:  {"v0.1.0"},
					family: {"v1.0.0"},
				},
				dep: xpkgDep(provA, "v0.1.0"),
			},
			want: want{
				gets: map[string]int{family + ":v1.0.0": 1},
				deps: []v1alpha1.Dependency{{
					Type: v1alpha1.DependencyTypeXpkg,
					Xpkg: &v1alpha1.XpkgDependency{
						APIVersion: "pkg.crossplane.io/v1",
						Kind:       "Provider",
						Package:    provA,
						Version:    "v0.1.0",
					},
				}},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fc := &fakeClient{
				packages:   tc.args.packages,
				tagsByRepo: tc.args.tagsByRepo,
			}

			projFS := afero.NewMemMapFs()
			proj := &v1alpha1.Project{
				TypeMeta: metav1.TypeMeta{
					APIVersion: "dev.crossplane.io/v1alpha1",
					Kind:       "Project",
				},
				ObjectMeta: metav1.ObjectMeta{Name: "test-project"},
				Spec: v1alpha1.ProjectSpec{
					Repository: "xpkg.crossplane.io/test/test",
					Paths:      &v1alpha1.ProjectPaths{Schemas: "schemas"},
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
				WithXpkgClient(fc),
				WithResolver(clixpkg.NewResolver(fc)),
			)

			err = m.AddDependency(context.Background(), tc.args.dep)
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("%s\nAddDependency(...): -want err, +got err:\n%s", tc.reason, diff)
			}

			for ref, wantCount := range tc.want.gets {
				if diff := cmp.Diff(wantCount, fc.getCount(ref)); diff != "" {
					t.Errorf("%s\nAddDependency(...): -want fetch count for %s, +got fetch count:\n%s", tc.reason, ref, diff)
				}
			}

			if diff := cmp.Diff(tc.want.deps, proj.Spec.Dependencies); diff != "" {
				t.Errorf("%s\nAddDependency(...): -want project deps, +got project deps:\n%s", tc.reason, diff)
			}
			gotBytes, err := afero.ReadFile(projFS, "crossplane-project.yaml")
			if err != nil {
				t.Fatal(err)
			}
			var gotProj v1alpha1.Project
			if err := yaml.Unmarshal(gotBytes, &gotProj); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.want.deps, gotProj.Spec.Dependencies); diff != "" {
				t.Errorf("%s\nAddDependency(...): -want on-disk project deps, +got on-disk project deps:\n%s", tc.reason, diff)
			}
		})
	}
}

const testDigest = "sha256:5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"

// newProjectManager returns a manager for a project with the given
// dependencies, with its project file on a memory filesystem.
func newProjectManager(t *testing.T, fc *fakeClient, deps []v1alpha1.Dependency) (*Manager, afero.Fs) {
	t.Helper()
	proj := &v1alpha1.Project{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "dev.crossplane.io/v1alpha1",
			Kind:       "Project",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "test-project"},
		Spec: v1alpha1.ProjectSpec{
			Repository:   "xpkg.crossplane.io/test/test",
			Dependencies: deps,
			Paths:        &v1alpha1.ProjectPaths{Schemas: "schemas"},
		},
	}
	bs, err := yaml.Marshal(proj)
	if err != nil {
		t.Fatal(err)
	}
	projFS := afero.NewMemMapFs()
	if err := afero.WriteFile(projFS, "crossplane-project.yaml", bs, 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(proj, projFS,
		WithProjectFile("crossplane-project.yaml"),
		WithXpkgClient(fc),
		WithResolver(clixpkg.NewResolver(fc)),
	)
	return m, projFS
}

// providerDep is an xpkg dependency as stored in a project file, which requires
// the package's apiVersion and kind.
func providerDep(pkg string) *v1alpha1.Dependency {
	dep := xpkgDep(pkg, "v0.1.0")
	dep.Xpkg.APIVersion = "pkg.crossplane.io/v1"
	dep.Xpkg.Kind = "Provider"
	return dep
}

// familyFixture is a registry with the given providers, which all depend on a
// shared provider package.
func familyFixture(t *testing.T, provs ...string) *fakeClient {
	t.Helper()
	const family = "xpkg.example/family"
	fc := &fakeClient{
		packages:   map[string]*runtimexpkg.Package{family + ":v1.0.0": makePackageWithBody(t, family, testDigest, "", providerPackageYAML)},
		tagsByRepo: map[string][]string{family: {"v1.0.0"}},
	}
	for _, p := range provs {
		fc.packages[p+":v0.1.0"] = makePackageWithBody(t, p, testDigest, "", fmt.Sprintf(providerDependsOnPackageYAML, family, "v1.0.0"))
		fc.tagsByRepo[p] = []string{"v0.1.0"}
	}
	return fc
}

func TestManager_Resolve_Sources(t *testing.T) {
	const (
		provA  = "xpkg.example/prov-a"
		provB  = "xpkg.example/prov-b"
		family = "xpkg.example/family"
	)

	type args struct {
		deps []v1alpha1.Dependency
		// provs are the providers available in the fake registry.
		provs []string
	}
	type want struct {
		keys []string
		err  error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"NoDependencies": {
			reason: "A project without dependencies has no sources.",
		},
		"IncludesTransitiveDependencies": {
			reason: "Sources of packages the dependency depends on are live too, otherwise reconciling would delete them.",
			args:   args{deps: []v1alpha1.Dependency{*xpkgDep(provA, "v0.1.0")}, provs: []string{provA}},
			want:   want{keys: []string{"xpkg://" + family, "xpkg://" + provA}},
		},
		"SharedTransitiveListedOnce": {
			reason: "Two providers sharing a dependency list it once.",
			args:   args{deps: []v1alpha1.Dependency{*xpkgDep(provA, "v0.1.0"), *xpkgDep(provB, "v0.1.0")}, provs: []string{provA, provB}},
			want:   want{keys: []string{"xpkg://" + family, "xpkg://" + provA, "xpkg://" + provB}},
		},
		"AllSourceTypes": {
			reason: "k8s, http and git dependencies are listed without being fetched.",
			args: args{deps: []v1alpha1.Dependency{
				{Type: v1alpha1.DependencyTypeK8s, K8s: &v1alpha1.K8sDependency{Version: "1.33.0"}},
				{Type: v1alpha1.DependencyTypeCRD, HTTP: &v1alpha1.HTTPDependency{URL: "https://example.com/crd.yaml"}},
				{Type: v1alpha1.DependencyTypeCRD, Git: &v1alpha1.GitDependency{Repository: "https://example.com/r.git", Path: "crds"}},
			}},
			want: want{keys: []string{"git://https://example.com/r.git/crds", "http://https://example.com/crd.yaml", "k8s://1.33.0"}},
		},
		"UnavailablePackage": {
			reason: "A dependency that can't be fetched is an error, not silently dropped from the live set.",
			args:   args{deps: []v1alpha1.Dependency{*xpkgDep("xpkg.example/missing", "v0.1.0")}},
			want:   want{err: cmpopts.AnyError},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fc := familyFixture(t, tc.args.provs...)
			m, _ := newProjectManager(t, fc, tc.args.deps)

			sources, _, err := m.Resolve(context.Background(), ResolveOptions{})
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("%s\nResolve(...): -want err, +got err:\n%s", tc.reason, diff)
			}
			got := make([]string, 0, len(sources))
			for _, s := range sources {
				got = append(got, s.Key())
			}
			if diff := cmp.Diff(tc.want.keys, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("%s\nResolve(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestManager_ResolveAndRefreshAll(t *testing.T) {
	const (
		provA  = "xpkg.example/prov-a"
		family = "xpkg.example/family"
	)

	type args struct {
		refresh bool
	}
	type want struct {
		// fetched are the refs that must have been fetched into the cache.
		fetched []string
		err     error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Resolve": {
			reason: "Resolve caches the declared package and its transitive dependency.",
			want:   want{fetched: []string{provA + ":v0.1.0", family + ":v1.0.0"}},
		},
		"RefreshAll": {
			reason: "RefreshAll fetches the same packages, asking the cache to refresh them.",
			args:   args{refresh: true},
			want:   want{fetched: []string{provA + ":v0.1.0", family + ":v1.0.0"}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fc := familyFixture(t, provA)
			m, _ := newProjectManager(t, fc, []v1alpha1.Dependency{*providerDep(provA)})

			var err error
			if tc.args.refresh {
				err = m.RefreshAll(context.Background(), nil)
			} else {
				_, _, err = m.Resolve(context.Background(), ResolveOptions{})
			}
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("%s\nResolve/RefreshAll(...): -want err, +got err:\n%s", tc.reason, diff)
			}

			for _, ref := range tc.want.fetched {
				if fc.getCount(ref) == 0 {
					t.Errorf("%s\nResolve/RefreshAll(...): %s was not fetched", tc.reason, ref)
				}
			}
		})
	}
}

func TestDependenciesHash(t *testing.T) {
	t.Parallel()

	xpkg := func(pkg, version string) v1alpha1.Dependency {
		return v1alpha1.Dependency{Type: v1alpha1.DependencyTypeXpkg, Xpkg: &v1alpha1.XpkgDependency{Package: pkg, Version: version}}
	}
	k8s := v1alpha1.Dependency{Type: v1alpha1.DependencyTypeK8s, K8s: &v1alpha1.K8sDependency{Version: "v1.33.0"}}
	proj := func(deps ...v1alpha1.Dependency) *v1alpha1.Project {
		return &v1alpha1.Project{Spec: v1alpha1.ProjectSpec{Dependencies: deps}}
	}

	a, b := xpkg("xpkg.example.com/a", "v1.0.0"), xpkg("xpkg.example.com/b", "v1.0.0")
	baseProj := proj(a, b, k8s)

	type args struct {
		// base is the project whose hash is the baseline.
		base *v1alpha1.Project
		proj *v1alpha1.Project
	}
	type want struct {
		// same is whether proj hashes the same as base.
		same bool
		err  error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Reordered": {
			reason: "The order of the dependencies does not change the hash.",
			args:   args{base: baseProj, proj: proj(k8s, b, a)},
			want:   want{same: true},
		},
		"ChangedVersion": {
			reason: "Changing a dependency changes the hash.",
			args:   args{base: baseProj, proj: proj(a, xpkg("xpkg.example.com/b", "v2.0.0"), k8s)},
		},
		"RemovedDependency": {
			reason: "Removing a dependency changes the hash.",
			args:   args{base: baseProj, proj: proj(a, k8s)},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			base, err := DependenciesHash(tc.args.base)
			if err != nil {
				t.Fatal(err)
			}
			got, err := DependenciesHash(tc.args.proj)
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("%s\nDependenciesHash(...): -want err, +got err:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.same, got == base); diff != "" {
				t.Errorf("%s\nDependenciesHash(...): -want same as base, +got same as base:\n%s", tc.reason, diff)
			}
		})
	}
}

// regPkg is a package in a fake registry.
type regPkg struct {
	repo    string
	version string // empty if the package is only available by digest.
	digest  string
	body    string
}

// digestN returns a valid digest that is different for each n.
func digestN(n int) string {
	return fmt.Sprintf("sha256:%064x", n)
}

// registry returns a fake client serving the packages.
func registry(t *testing.T, pkgs ...regPkg) *fakeClient {
	t.Helper()
	fc := &fakeClient{
		packages:   make(map[string]*runtimexpkg.Package),
		tagsByRepo: make(map[string][]string),
	}
	for _, p := range pkgs {
		ref := p.repo + "@" + p.digest
		if p.version != "" {
			ref = p.repo + ":" + p.version
			fc.tagsByRepo[p.repo] = append(fc.tagsByRepo[p.repo], p.version)
		}
		fc.packages[ref] = makePackageWithBody(t, p.repo, p.digest, p.version, p.body)
	}
	return fc
}

// dependsOn returns the body of a provider that depends on repo at version.
func dependsOn(repo, version string) string {
	return fmt.Sprintf(providerDependsOnPackageYAML, repo, version)
}

// pinsOf returns the pin of each source, by key.
func pinsOf(t *testing.T, sources []smanager.Source) map[string]string {
	t.Helper()
	pins := make(map[string]string)
	for _, src := range sources {
		pin, err := src.(smanager.Pinned).Pin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		pins[src.Key()] = pin
	}
	return pins
}

// versionsOf returns the resolved version of each source, by key.
func versionsOf(sources []smanager.Source) map[string]string {
	versions := make(map[string]string)
	for _, src := range sources {
		versions[src.Key()] = src.(smanager.Pinned).Version()
	}
	return versions
}

func TestManager_Resolve_Versions(t *testing.T) {
	const (
		provA  = "xpkg.example/prov-a"
		provB  = "xpkg.example/prov-b"
		provC  = "xpkg.example/prov-c"
		family = "xpkg.example/family"
		libOld = "xpkg.example/lib-old"
		libNew = "xpkg.example/lib-new"
	)

	familyVersions := func(versions ...string) []regPkg {
		pkgs := make([]regPkg, 0, len(versions))
		for i, v := range versions {
			pkgs = append(pkgs, regPkg{repo: family, version: v, digest: digestN(100 + i), body: providerPackageYAML})
		}
		return pkgs
	}

	type args struct {
		pkgs []regPkg
		deps []v1alpha1.Dependency
		opts ResolveOptions
	}
	type want struct {
		// versions are the resolved versions, by repository. Repositories that
		// are not listed must not be resolved at all.
		versions map[string]string
		warnings []Warning
		err      error
		// errMsgs are substrings the error must contain, for cases where the
		// message (e.g. naming the conflicting package) is the behaviour.
		errMsgs []string
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"DirectBeatsTransitive": {
			reason: "A direct dependency wins over what a package requires, and the violated requirement is reported.",
			args: args{
				pkgs: append(familyVersions("v1.0.0", "v2.0.0"),
					regPkg{repo: provA, version: "v0.1.0", digest: digestN(1), body: dependsOn(family, ">=v2.0.0")},
				),
				deps: []v1alpha1.Dependency{*xpkgDep(provA, "v0.1.0"), *xpkgDep(family, "v1.0.0")},
			},
			want: want{
				versions: map[string]string{provA: "v0.1.0", family: "v1.0.0"},
				warnings: []Warning{{Package: family, Version: "v1.0.0", Requirer: provA, Constraint: ">=v2.0.0"}},
			},
		},
		"DirectSatisfyingTransitive": {
			reason: "A direct dependency that meets the requirements of the packages does not cause a warning.",
			args: args{
				pkgs: append(familyVersions("v1.0.0", "v2.0.0"),
					regPkg{repo: provA, version: "v0.1.0", digest: digestN(1), body: dependsOn(family, ">=v1.0.0")},
				),
				deps: []v1alpha1.Dependency{*xpkgDep(provA, "v0.1.0"), *xpkgDep(family, "v1.0.0")},
			},
			want: want{versions: map[string]string{provA: "v0.1.0", family: "v1.0.0"}},
		},
		"HighestSatisfyingBothRequirers": {
			reason: "Two packages that require different ranges get the highest version that satisfies both.",
			args: args{
				pkgs: append(familyVersions("v1.0.0", "v1.1.0", "v1.2.0", "v2.0.0"),
					regPkg{repo: provA, version: "v0.1.0", digest: digestN(1), body: dependsOn(family, ">=v1.0.0")},
					regPkg{repo: provB, version: "v0.1.0", digest: digestN(2), body: dependsOn(family, "<v1.2.0")},
				),
				deps: []v1alpha1.Dependency{*xpkgDep(provA, "v0.1.0"), *xpkgDep(provB, "v0.1.0")},
			},
			want: want{versions: map[string]string{provA: "v0.1.0", provB: "v0.1.0", family: "v1.1.0"}},
		},
		"Unsatisfiable": {
			reason: "Requirements that no version satisfies together are an error that names the requirers.",
			args: args{
				pkgs: append(familyVersions("v1.0.0", "v2.0.0"),
					regPkg{repo: provA, version: "v0.1.0", digest: digestN(1), body: dependsOn(family, ">=v2.0.0")},
					regPkg{repo: provB, version: "v0.1.0", digest: digestN(2), body: dependsOn(family, "<v2.0.0, <v1.0.0")},
				),
				deps: []v1alpha1.Dependency{*xpkgDep(provA, "v0.1.0"), *xpkgDep(provB, "v0.1.0")},
			},
			want: want{err: cmpopts.AnyError, errMsgs: []string{family, provA, ">=v2.0.0", provB, "<v1.0.0"}},
		},
		"LockedVersionKept": {
			reason: "Without Refresh, the locked version is kept while it satisfies the requirements, even if there is a newer one.",
			args: args{
				pkgs: append(familyVersions("v1.0.0", "v1.1.0"),
					regPkg{repo: provA, version: "v0.1.0", digest: digestN(1), body: dependsOn(family, ">=v1.0.0")},
				),
				deps: []v1alpha1.Dependency{*xpkgDep(provA, "v0.1.0")},
				opts: ResolveOptions{Locked: map[string]smanager.LockedSource{
					"xpkg://" + family: {Version: "v1.0.0"},
				}},
			},
			want: want{versions: map[string]string{provA: "v0.1.0", family: "v1.0.0"}},
		},
		"LockedVersionReplacedByRefresh": {
			reason: "With Refresh, the lock is ignored and the highest version wins.",
			args: args{
				pkgs: append(familyVersions("v1.0.0", "v1.1.0"),
					regPkg{repo: provA, version: "v0.1.0", digest: digestN(1), body: dependsOn(family, ">=v1.0.0")},
				),
				deps: []v1alpha1.Dependency{*xpkgDep(provA, "v0.1.0")},
				opts: ResolveOptions{Refresh: true, Locked: map[string]smanager.LockedSource{
					"xpkg://" + family: {Version: "v1.0.0"},
				}},
			},
			want: want{versions: map[string]string{provA: "v0.1.0", family: "v1.1.0"}},
		},
		"LockedVersionNoLongerAllowed": {
			reason: "A locked version that violates the requirements is replaced.",
			args: args{
				pkgs: append(familyVersions("v1.0.0", "v1.1.0"),
					regPkg{repo: provA, version: "v0.1.0", digest: digestN(1), body: dependsOn(family, ">=v1.1.0")},
				),
				deps: []v1alpha1.Dependency{*xpkgDep(provA, "v0.1.0")},
				opts: ResolveOptions{Locked: map[string]smanager.LockedSource{
					"xpkg://" + family: {Version: "v1.0.0"},
				}},
			},
			want: want{versions: map[string]string{provA: "v0.1.0", family: "v1.1.0"}},
		},
		"ParentVersionChangeDropsTransitive": {
			reason: "When a package is moved to another version, the packages only the old version needed are dropped.",
			args: args{
				// provA is first chosen at v0.2.0 because of provB. Once provC
				// is found, which pins it below v0.2.0, it moves to v0.1.0.
				pkgs: []regPkg{
					{repo: provA, version: "v0.1.0", digest: digestN(1), body: dependsOn(libOld, "v1.0.0")},
					{repo: provA, version: "v0.2.0", digest: digestN(2), body: dependsOn(libNew, "v1.0.0")},
					{repo: libOld, version: "v1.0.0", digest: digestN(3), body: providerPackageYAML},
					{repo: libNew, version: "v1.0.0", digest: digestN(4), body: providerPackageYAML},
					{repo: provB, version: "v0.1.0", digest: digestN(5), body: dependsOn(provA, ">=v0.1.0")},
					{repo: provC, version: "v0.1.0", digest: digestN(6), body: dependsOn(provA, "<v0.2.0")},
					{repo: xpkgDepRepo, version: "v0.1.0", digest: digestN(7), body: dependsOn(provC, "v0.1.0")},
				},
				deps: []v1alpha1.Dependency{*xpkgDep(provB, "v0.1.0"), *xpkgDep(xpkgDepRepo, "v0.1.0")},
			},
			want: want{versions: map[string]string{
				provA: "v0.1.0", provB: "v0.1.0", provC: "v0.1.0", xpkgDepRepo: "v0.1.0", libOld: "v1.0.0",
			}},
		},
		"ConflictingDigests": {
			reason: "Packages that pin different digests of one package are an error.",
			args: args{
				pkgs: []regPkg{
					{repo: family, digest: digestN(100), body: providerPackageYAML},
					{repo: family, digest: digestN(101), body: providerPackageYAML},
					{repo: provA, version: "v0.1.0", digest: digestN(1), body: dependsOn(family, digestN(100))},
					{repo: provB, version: "v0.1.0", digest: digestN(2), body: dependsOn(family, digestN(101))},
				},
				deps: []v1alpha1.Dependency{*xpkgDep(provA, "v0.1.0"), *xpkgDep(provB, "v0.1.0")},
			},
			want: want{err: cmpopts.AnyError, errMsgs: []string{family, "conflicting digests"}},
		},
		"EqualDigests": {
			reason: "Packages that pin the same digest agree.",
			args: args{
				pkgs: []regPkg{
					{repo: family, digest: digestN(100), body: providerPackageYAML},
					{repo: provA, version: "v0.1.0", digest: digestN(1), body: dependsOn(family, digestN(100))},
					{repo: provB, version: "v0.1.0", digest: digestN(2), body: dependsOn(family, digestN(100))},
				},
				deps: []v1alpha1.Dependency{*xpkgDep(provA, "v0.1.0"), *xpkgDep(provB, "v0.1.0")},
			},
			want: want{versions: map[string]string{provA: "v0.1.0", provB: "v0.1.0", family: ""}},
		},
		"Oscillating": {
			reason: "Packages whose selections keep invalidating each other are an error, not an endless loop.",
			args: args{
				// Each version of prov-a and prov-b requires a version of the
				// other that rules out the version that required it.
				pkgs: []regPkg{
					{repo: provA, version: "v1.0.0", digest: digestN(1), body: dependsOn(provB, ">=v2.0.0")},
					{repo: provA, version: "v2.0.0", digest: digestN(2), body: dependsOn(provB, "<v2.0.0")},
					{repo: provB, version: "v1.0.0", digest: digestN(3), body: dependsOn(provA, "<v2.0.0")},
					{repo: provB, version: "v2.0.0", digest: digestN(4), body: dependsOn(provA, ">=v2.0.0")},
					{repo: provC, version: "v0.1.0", digest: digestN(5), body: dependsOn(provA, ">=v1.0.0")},
				},
				deps: []v1alpha1.Dependency{*xpkgDep(provC, "v0.1.0")},
			},
			want: want{err: cmpopts.AnyError, errMsgs: []string{"does not converge"}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m, _ := newProjectManager(t, registry(t, tc.args.pkgs...), tc.args.deps)

			sources, warnings, err := m.Resolve(context.Background(), tc.args.opts)
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("%s\nResolve(...): -want err, +got err:\n%s", tc.reason, diff)
			}
			if tc.want.err != nil {
				for _, s := range tc.want.errMsgs {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("%s\nResolve(...): error %q does not contain %q", tc.reason, err, s)
					}
				}
				return
			}

			got := make(map[string]string)
			for key, v := range versionsOf(sources) {
				got[strings.TrimPrefix(key, "xpkg://")] = v
			}
			if diff := cmp.Diff(tc.want.versions, got); diff != "" {
				t.Errorf("%s\nResolve(...): -want versions, +got versions:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.warnings, warnings, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("%s\nResolve(...): -want warnings, +got warnings:\n%s", tc.reason, diff)
			}
		})
	}
}

// xpkgDepRepo is a package that depends on a package that the project does not.
const xpkgDepRepo = "xpkg.example/prov-d"

func TestManager_Resolve_Deterministic(t *testing.T) {
	const (
		provA  = "xpkg.example/prov-a"
		provB  = "xpkg.example/prov-b"
		provC  = "xpkg.example/prov-c"
		family = "xpkg.example/family"
	)

	pkgs := make([]regPkg, 0, 8)
	pkgs = append(pkgs,
		regPkg{repo: provA, version: "v0.1.0", digest: digestN(1), body: dependsOn(family, ">=v1.0.0")},
		regPkg{repo: provB, version: "v0.1.0", digest: digestN(2), body: dependsOn(family, "<v1.3.0")},
		regPkg{repo: provC, version: "v0.1.0", digest: digestN(3), body: dependsOn(provA, ">=v0.1.0")},
	)
	for i, v := range []string{"v1.0.0", "v1.1.0", "v1.2.0", "v1.3.0", "v2.0.0"} {
		pkgs = append(pkgs, regPkg{repo: family, version: v, digest: digestN(100 + i), body: providerPackageYAML})
	}

	type args struct {
		pkgs []regPkg
		deps []v1alpha1.Dependency
		// runs is how many times to resolve against a registry with jittery
		// latency.
		runs int
	}
	type want struct {
		// pins are the pins that every run must produce, by source key.
		pins map[string]string
		err  error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ConcurrentFetchOrder": {
			reason: "Resolving yields the same pins however fetches interleave, and picks the highest version all requirements allow.",
			args: args{
				pkgs: pkgs,
				deps: []v1alpha1.Dependency{*xpkgDep(provC, "v0.1.0"), *xpkgDep(provB, "v0.1.0")},
				runs: 20,
			},
			want: want{pins: map[string]string{
				"xpkg://" + provA:  provA + "@" + digestN(1),
				"xpkg://" + provB:  provB + "@" + digestN(2),
				"xpkg://" + provC:  provC + "@" + digestN(3),
				"xpkg://" + family: family + "@" + digestN(102),
			}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for i := range tc.args.runs {
				fc := registry(t, tc.args.pkgs...)
				fc.jitter = true
				m, _ := newProjectManager(t, fc, tc.args.deps)

				sources, _, err := m.Resolve(context.Background(), ResolveOptions{})
				if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
					t.Fatalf("%s\nResolve(...) run %d: -want err, +got err:\n%s", tc.reason, i, diff)
				}
				if diff := cmp.Diff(tc.want.pins, pinsOf(t, sources)); diff != "" {
					t.Fatalf("%s\nResolve(...) run %d: -want pins, +got pins:\n%s", tc.reason, i, diff)
				}
			}
		})
	}
}

// fakeCloner is a git.Cloner that records the ref it was asked to clone.
type fakeCloner struct {
	refs []string
}

func (c *fakeCloner) CloneRepository(_ storage.Storer, fs billy.Filesystem, _ git.AuthProvider, opts git.CloneOptions) (*plumbing.Reference, error) {
	c.refs = append(c.refs, opts.RefName)
	f, err := fs.Create(opts.Path + "/crd.yaml")
	if err != nil {
		return nil, err
	}
	if _, err := f.Write([]byte(configurationPackageYAML)); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return plumbing.NewHashReference("refs/heads/main", plumbing.NewHash(strings.Repeat("a", 40))), nil
}

func TestManager_Resolve_LockedGit(t *testing.T) {
	sha := strings.Repeat("b", 40)
	dep := v1alpha1.Dependency{
		Type: v1alpha1.DependencyTypeCRD,
		Git:  &v1alpha1.GitDependency{Repository: "https://example.com/r.git", Ref: "main", Path: "crds"},
	}
	locked := map[string]smanager.LockedSource{"git://https://example.com/r.git/crds": {Pin: sha}}

	type args struct {
		opts ResolveOptions
	}
	type want struct {
		// ref is the single ref the cloner must be asked for.
		ref string
		err error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Locked": {
			reason: "The locked commit is cloned instead of the branch.",
			args:   args{opts: ResolveOptions{Locked: locked}},
			want:   want{ref: sha},
		},
		"Refresh": {
			reason: "With Refresh, the branch is cloned.",
			args:   args{opts: ResolveOptions{Refresh: true, Locked: locked}},
			want:   want{ref: "refs/heads/main"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m, _ := newProjectManager(t, registry(t), []v1alpha1.Dependency{dep})
			cloner := &fakeCloner{}
			m.gitCloner = cloner

			sources, _, err := m.Resolve(context.Background(), tc.args.opts)
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("%s\nResolve(...): -want err, +got err:\n%s", tc.reason, diff)
			}
			if _, err := sources[0].Hash(context.Background()); err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff([]string{tc.want.ref}, cloner.refs); diff != "" {
				t.Errorf("%s\nResolve(...): -want cloned refs, +got cloned refs:\n%s", tc.reason, diff)
			}
			if diff := cmp.Diff(dep.Git.Ref, sources[0].(smanager.Pinned).Version()); diff != "" {
				t.Errorf("%s\nResolve(...): -want version, +got version:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestManager_FetchPinned(t *testing.T) {
	const prov = "xpkg.example/prov-a"

	sha := strings.Repeat("c", 40)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(configurationPackageYAML))
	}))
	t.Cleanup(srv.Close)

	httpDep := v1alpha1.Dependency{Type: v1alpha1.DependencyTypeCRD, HTTP: &v1alpha1.HTTPDependency{URL: srv.URL + "/crd.yaml"}}
	gitDep := v1alpha1.Dependency{
		Type: v1alpha1.DependencyTypeCRD,
		Git:  &v1alpha1.GitDependency{Repository: "https://example.com/r.git", Ref: "main", Path: "crds"},
	}

	// The pin of the http source is the hash of its content.
	m, _ := newProjectManager(t, registry(t), []v1alpha1.Dependency{httpDep})
	httpSrc, err := m.source(httpDep)
	if err != nil {
		t.Fatal(err)
	}
	httpHash, err := httpSrc.Hash(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	type args struct {
		deps  []v1alpha1.Dependency
		key   string
		entry smanager.LockedSource
	}
	type want struct {
		// pin is the pin of the returned source.
		pin string
		err error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"XpkgByDigest": {
			reason: "A package is fetched by the digest in the lock, not by a tag.",
			args: args{
				key:   "xpkg://" + prov,
				entry: smanager.LockedSource{Pin: prov + "@" + digestN(1), Version: "v0.1.0"},
			},
			want: want{pin: prov + "@" + digestN(1)},
		},
		"XpkgMissing": {
			reason: "A package that cannot be fetched is an error.",
			args: args{
				key:   "xpkg://" + prov,
				entry: smanager.LockedSource{Pin: prov + "@" + digestN(2)},
			},
			want: want{err: cmpopts.AnyError},
		},
		"GitAtSHA": {
			reason: "A git dependency is cloned at the locked commit.",
			args: args{
				deps:  []v1alpha1.Dependency{gitDep},
				key:   "git://https://example.com/r.git/crds",
				entry: smanager.LockedSource{Pin: sha},
			},
			want: want{pin: sha},
		},
		"HTTPHashMatches": {
			reason: "An http dependency whose content is unchanged is returned.",
			args: args{
				deps:  []v1alpha1.Dependency{httpDep},
				key:   "http://" + srv.URL + "/crd.yaml",
				entry: smanager.LockedSource{Pin: httpHash},
			},
			want: want{pin: httpHash},
		},
		"HTTPHashMismatch": {
			reason: "An http dependency whose content changed is an error.",
			args: args{
				deps:  []v1alpha1.Dependency{httpDep},
				key:   "http://" + srv.URL + "/crd.yaml",
				entry: smanager.LockedSource{Pin: "not-the-hash"},
			},
			want: want{err: cmpopts.AnyError},
		},
		"NotInProject": {
			reason: "A source that no dependency provides is an error.",
			args: args{
				deps:  []v1alpha1.Dependency{httpDep},
				key:   "git://https://example.com/other.git",
				entry: smanager.LockedSource{Pin: sha},
			},
			want: want{err: cmpopts.AnyError},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fc := registry(t, regPkg{repo: prov, version: "v0.1.0", digest: digestN(1), body: providerPackageYAML})
			fc.packages[prov+"@"+digestN(1)] = fc.packages[prov+":v0.1.0"]
			m, _ := newProjectManager(t, fc, tc.args.deps)
			cloner := &fakeCloner{}
			m.gitCloner = cloner

			src, err := m.FetchPinned(context.Background(), tc.args.key, tc.args.entry)
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("%s\nFetchPinned(...): -want err, +got err:\n%s", tc.reason, diff)
			}
			if tc.want.err != nil {
				return
			}
			if diff := cmp.Diff(tc.args.key, src.Key()); diff != "" {
				t.Errorf("%s\nFetchPinned(...): -want key, +got key:\n%s", tc.reason, diff)
			}

			// A git source reports the commit it cloned; clone it first.
			if _, err := src.Resources(context.Background()); err != nil {
				t.Fatal(err)
			}
			pin, err := src.(smanager.Pinned).Pin(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(tc.args.key, "git://") {
				// The fake cloner reports its own commit; what matters is that the
				// pin was cloned, and that the version is still the branch.
				if diff := cmp.Diff([]string{tc.want.pin}, cloner.refs); diff != "" {
					t.Errorf("%s\nFetchPinned(...): -want cloned refs, +got cloned refs:\n%s", tc.reason, diff)
				}
				if diff := cmp.Diff(gitDep.Git.Ref, src.(smanager.Pinned).Version()); diff != "" {
					t.Errorf("%s\nFetchPinned(...): -want version, +got version:\n%s", tc.reason, diff)
				}
				return
			}
			if diff := cmp.Diff(tc.want.pin, pin); diff != "" {
				t.Errorf("%s\nFetchPinned(...): -want pin, +got pin:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestNewCachedManager(t *testing.T) {
	// t.Setenv makes these tests unsuitable for t.Parallel.
	explicit := filepath.Join(t.TempDir(), "explicit")

	type args struct {
		cacheDir string
		projFile string
	}
	type want struct {
		// projFile is the project file name the manager uses.
		projFile string
		// cacheDir returns the directory that must have been created.
		cacheDir func() string
		err      error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ExplicitCacheDir": {
			reason: "An explicit cache directory should be created and used instead of the default.",
			args:   args{cacheDir: explicit, projFile: "crossplane-project.yaml"},
			want:   want{projFile: "crossplane-project.yaml", cacheDir: func() string { return explicit }},
		},
		"DefaultCacheDir": {
			reason: "An empty cache directory should fall back to DefaultCacheDir.",
			args:   args{projFile: "crossplane-project.yaml"},
			want:   want{projFile: "crossplane-project.yaml", cacheDir: DefaultCacheDir},
		},
		"ProjectFileBaseName": {
			reason: "Only the base name of the project file should be used, because the project filesystem is rooted at the project directory.",
			args:   args{cacheDir: explicit, projFile: "/some/dir/my-project.yaml"},
			want:   want{projFile: "my-project.yaml", cacheDir: func() string { return explicit }},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Point the user cache dir at a temp dir, so DefaultCacheDir
			// doesn't touch the real cache.
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

			m, err := NewCachedManager(&v1alpha1.Project{}, afero.NewMemMapFs(), tc.args.projFile, tc.args.cacheDir)
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("%s\nNewCachedManager(...): -want err, +got err:\n%s", tc.reason, diff)
			}

			if diff := cmp.Diff(tc.want.projFile, m.projFile); diff != "" {
				t.Errorf("%s\nNewCachedManager(...): -want projFile, +got projFile:\n%s", tc.reason, diff)
			}

			dir := tc.want.cacheDir()
			if ok, err := afero.DirExists(afero.NewOsFs(), dir); err != nil || !ok {
				t.Errorf("%s\nNewCachedManager(...): cache dir %q not created (err: %v)", tc.reason, dir, err)
			}
		})
	}
}
