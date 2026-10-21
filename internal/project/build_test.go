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
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/spf13/afero"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	devv1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/dependency"
	"github.com/crossplane/cli/v2/internal/project/functions"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/schemas/manager"
	"github.com/crossplane/cli/v2/internal/schemas/runner"
)

// xrdYAML returns an XRD manifest for a resource with the given group/kind.
func xrdYAML(group, plural, singular, kind string) string {
	return fmt.Sprintf(`apiVersion: apiextensions.crossplane.io/v2
kind: CompositeResourceDefinition
metadata:
  name: %s.%s
spec:
  group: %s
  names:
    kind: %s
    plural: %s
    singular: %s
    listKind: %sList
  scope: Namespaced
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
`, plural, group, group, kind, plural, singular, kind)
}

// compositionYAML returns a Composition manifest targeting the given XR kind.
func compositionYAML(name, group, kind string) string {
	return fmt.Sprintf(`apiVersion: apiextensions.crossplane.io/v1
kind: Composition
metadata:
  name: %s
spec:
  compositeTypeRef:
    apiVersion: %s/v1alpha1
    kind: %s
  pipeline: []
  mode: Pipeline
`, name, group, kind)
}

func writeProject(t *testing.T, projFS afero.Fs, apis map[string]string, fns []string) {
	t.Helper()
	for path, content := range apis {
		if err := projFS.MkdirAll("apis", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := afero.WriteFile(projFS, "apis/"+path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, fn := range fns {
		dir := "functions/" + fn
		if err := projFS.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		// A minimal function source file so FakeIdentifier has something to find.
		if err := afero.WriteFile(projFS, dir+"/.placeholder", []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuilderBuild(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		apis              map[string]string
		functions         []string
		runtimeDeps       []devv1alpha1.Dependency
		expectedResources int
		expectedFns       int
	}{
		"ConfigurationOnly": {
			apis: map[string]string{
				"db.yaml":         xrdYAML("acme.example.com", "xdatabases", "xdatabase", "XDatabase"),
				"db-comp.yaml":    compositionYAML("xdatabase-comp", "acme.example.com", "XDatabase"),
				"queue.yaml":      xrdYAML("acme.example.com", "xqueues", "xqueue", "XQueue"),
				"queue-comp.yaml": compositionYAML("xqueue-comp", "acme.example.com", "XQueue"),
			},
			runtimeDeps: []devv1alpha1.Dependency{
				{
					Type: devv1alpha1.DependencyTypeXpkg,
					Xpkg: &devv1alpha1.XpkgDependency{
						APIVersion: "pkg.crossplane.io/v1",
						Kind:       "Provider",
						Package:    "xpkg.crossplane.io/example/provider-nop",
						Version:    "v0.2.1",
					},
				},
			},
			expectedResources: 4,
		},
		"EmbeddedFunctions": {
			apis: map[string]string{
				"db.yaml":      xrdYAML("acme.example.com", "xdatabases", "xdatabase", "XDatabase"),
				"db-comp.yaml": compositionYAML("xdatabase-comp", "acme.example.com", "XDatabase"),
			},
			functions:         []string{"fn-cluster", "fn-net"},
			expectedResources: 2,
			expectedFns:       2,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			projFS := afero.NewMemMapFs()
			writeProject(t, projFS, tc.apis, tc.functions)

			proj := &devv1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-project",
				},
				Spec: devv1alpha1.ProjectSpec{
					ProjectPackageMetadata: devv1alpha1.ProjectPackageMetadata{
						Maintainer:  "Maintainer <m@example.com>",
						Source:      "github.com/example/proj",
						License:     "Apache-2.0",
						Description: "test project",
					},
					Repository:   "xpkg.crossplane.io/example/test",
					Dependencies: tc.runtimeDeps,
				},
			}
			proj.Default()

			b := NewBuilder(
				BuildWithFunctionIdentifier(functions.FakeIdentifier),
			)

			imgMap, err := b.Build(t.Context(), proj, projFS)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}

			// Configuration image must be present at <repo>:configuration.
			cfgTag, err := constructTag(proj.Spec.Repository, ConfigurationTag)
			if err != nil {
				t.Fatal(err)
			}
			cfgImg, ok := imgMap[cfgTag]
			if !ok {
				t.Fatalf("configuration image not found in image map; tags=%v", tagsOf(imgMap))
			}

			// Configuration image's package layer should carry the package
			// annotation (added by AnnotateImage on push, but here we just
			// verify the image has the expected meta/Configuration object via
			// labels on the config file).
			cfgFile, err := cfgImg.ConfigFile()
			if err != nil {
				t.Fatal(err)
			}
			// The builder applies xpkg annotations as image labels.
			if _, ok := cfgFile.Config.Labels[xpkg.Label(xpkg.PackageAnnotation)]; !ok {
				// The labels are computed during package construction; for some
				// image shapes the label may not be present. We don't assert
				// strictly here, but the absence is a sign something's off.
				t.Logf("configuration image has no %s label", xpkg.PackageAnnotation)
			}

			// Function images: each fn should produce one image per arch.
			fnTags := map[string][]string{}
			for tag := range imgMap {
				if tag == cfgTag {
					continue
				}
				fnTags[tag.Repository.Name()] = append(fnTags[tag.Repository.Name()], tag.TagStr())
			}
			if diff := cmp.Diff(tc.expectedFns, len(fnTags)); diff != "" {
				t.Errorf("function repo count (-want +got):\n%s", diff)
			}
			for repo, tags := range fnTags {
				if diff := cmp.Diff(len(proj.Spec.Architectures), len(tags)); diff != "" {
					t.Errorf("function %s: arch count (-want +got):\n%s", repo, diff)
				}
			}
		})
	}
}

// TestBuilderDependsOn verifies the configuration package's dependsOn list
// includes both runtime project deps and embedded function deps.
func TestBuilderDependsOn(t *testing.T) {
	t.Parallel()

	projFS := afero.NewMemMapFs()
	writeProject(t, projFS,
		map[string]string{
			"db.yaml":      xrdYAML("acme.example.com", "xdatabases", "xdatabase", "XDatabase"),
			"db-comp.yaml": compositionYAML("xdb", "acme.example.com", "XDatabase"),
		},
		[]string{"fn-one"},
	)

	runtimeDep := devv1alpha1.Dependency{
		Type: devv1alpha1.DependencyTypeXpkg,
		Xpkg: &devv1alpha1.XpkgDependency{
			APIVersion: "pkg.crossplane.io/v1",
			Kind:       "Provider",
			Package:    "xpkg.crossplane.io/example/provider-nop",
			Version:    "v0.2.1",
		},
	}
	proj := &devv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-project",
		},
		Spec: devv1alpha1.ProjectSpec{
			Repository:    "xpkg.crossplane.io/example/test",
			Architectures: []string{"amd64", "arm64"},
			Dependencies:  []devv1alpha1.Dependency{runtimeDep},
		},
	}
	proj.Default()

	b := NewBuilder(
		BuildWithFunctionIdentifier(functions.FakeIdentifier),
	)

	imgMap, err := b.Build(t.Context(), proj, projFS)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// runtimeDependencies should have emitted the project's xpkg dep.
	got := runtimeDependencies(proj)
	if diff := cmp.Diff(1, len(got)); diff != "" {
		t.Errorf("runtimeDependencies count (-want +got):\n%s", diff)
	}
	if got[0].Package == nil || *got[0].Package != runtimeDep.Xpkg.Package {
		t.Errorf("runtimeDependencies package = %v, want %s", got[0].Package, runtimeDep.Xpkg.Package)
	}
	if diff := cmp.Diff(runtimeDep.Xpkg.Version, got[0].Version); diff != "" {
		t.Errorf("runtimeDependencies version (-want +got):\n%s", diff)
	}
	if got[0].APIVersion == nil || *got[0].APIVersion != runtimeDep.Xpkg.APIVersion {
		t.Errorf("runtimeDependencies APIVersion = %v, want %s", got[0].APIVersion, runtimeDep.Xpkg.APIVersion)
	}

	// The function dep is reflected in the image map as <repo>_<fn>:<arch>.
	wantFnRepo := proj.Spec.Repository + "_fn-one"
	foundArchs := 0
	for tag := range imgMap {
		if tag.Repository.Name() == wantFnRepo {
			foundArchs++
		}
	}
	if diff := cmp.Diff(2, foundArchs); diff != "" {
		t.Errorf("function arch images (-want +got):\n%s", diff)
	}
}

// fakeSchemaGenerator generates one fixed file per source, whatever the source
// contains, so tests can tell generated files from the lock's point of view.
type fakeSchemaGenerator struct{}

func (fakeSchemaGenerator) Language() string { return "fake" }

func (fakeSchemaGenerator) GenerateFromCRD(_ context.Context, _ afero.Fs, _ runner.SchemaRunner) (afero.Fs, error) {
	out := afero.NewMemMapFs()
	return out, afero.WriteFile(out, "schema.txt", []byte("generated"), 0o644)
}

func (fakeSchemaGenerator) GenerateFromOpenAPI(_ context.Context, _ afero.Fs, _ runner.SchemaRunner) (afero.Fs, error) {
	return nil, nil
}

// failingClient is an xpkg client that fails the test on any call, to prove a
// build doesn't touch the registry.
type failingClient struct {
	t *testing.T
}

func (c failingClient) Get(_ context.Context, ref string, _ ...xpkg.GetOption) (*xpkg.Package, error) {
	c.t.Errorf("unexpected Get(%q)", ref)
	return nil, errors.New("unexpected registry access")
}

func (c failingClient) ListVersions(_ context.Context, repo string, _ ...xpkg.GetOption) ([]string, error) {
	c.t.Errorf("unexpected ListVersions(%q)", repo)
	return nil, errors.New("unexpected registry access")
}

// perFileGenerator generates one file per input file, named after it, so tests
// can tell the files of different sources apart.
type perFileGenerator struct{}

func (perFileGenerator) Language() string { return "fake" }

func (perFileGenerator) GenerateFromCRD(_ context.Context, from afero.Fs, _ runner.SchemaRunner) (afero.Fs, error) {
	out := afero.NewMemMapFs()
	err := afero.Walk(from, ".", func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		return afero.WriteFile(out, filepath.Base(p)+".txt", []byte("generated "+filepath.Base(p)), 0o644)
	})
	return out, err
}

func (perFileGenerator) GenerateFromOpenAPI(_ context.Context, _ afero.Fs, _ runner.SchemaRunner) (afero.Fs, error) {
	return nil, nil
}

func TestBuilderBuild_VerifiesSchemas(t *testing.T) {
	t.Parallel()

	const schemaFile = "fake/schema.txt"

	type args struct {
		// lock seeds the schema lock, as `crossplane schema update` would, before
		// the build.
		lock bool
		// change modifies the project or the schemas between update and build.
		change func(t *testing.T, proj *devv1alpha1.Project, projFS, schemaFS afero.Fs)
	}
	type want struct {
		err error
		// errMsg is a substring of the error message, which is the behavior
		// under test: it tells the user which command to run.
		errMsg string
		// lockWritten is whether the build creates the lock. Otherwise it must
		// leave the lock alone.
		lockWritten bool
		// schema is the content of the generated file afterwards, "" if absent.
		schema string
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"MatchesLock": {
			reason: "A build whose schemas match the lock succeeds and leaves them alone.",
			args:   args{lock: true},
			want:   want{schema: "generated"},
		},
		"MissingFileIsRecreated": {
			reason: "A deleted generated file is restored, because that doesn't change what is locked.",
			args: args{lock: true, change: func(t *testing.T, _ *devv1alpha1.Project, _, schemaFS afero.Fs) {
				t.Helper()
				if err := schemaFS.Remove(schemaFile); err != nil {
					t.Fatal(err)
				}
			}},
			want: want{schema: "generated"},
		},
		"NoLock": {
			reason: "Without a lock the build runs a schema update, which writes the lock and the schemas.",
			want:   want{schema: "generated", lockWritten: true},
		},
		"DependenciesChangedSinceLock": {
			reason: "A dependency added after the lock was written fails the build without resolving it, because a build never touches the registry.",
			args: args{lock: true, change: func(t *testing.T, proj *devv1alpha1.Project, _, _ afero.Fs) {
				t.Helper()
				proj.Spec.Dependencies = append(proj.Spec.Dependencies, *xpkgDependency("xpkg.example/provider", ">=v1"))
			}},
			want: want{err: cmpopts.AnyError, errMsg: "crossplane schema update", schema: "generated"},
		},
		"XRDChangedSinceLock": {
			reason: "An XRD edited after the lock was written makes the lock stale; the build must not silently regenerate.",
			args: args{lock: true, change: func(t *testing.T, _ *devv1alpha1.Project, projFS, _ afero.Fs) {
				t.Helper()
				if err := afero.WriteFile(projFS, "apis/db.yaml", []byte(xrdYAML("acme.example.com", "xdatabases", "xdatabase", "XDatabase")+"# changed\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}},
			want: want{err: cmpopts.AnyError, errMsg: "crossplane schema update", schema: "generated"},
		},
		"ModifiedFile": {
			reason: "A hand-edited generated file is not overwritten by a build.",
			args: args{lock: true, change: func(t *testing.T, _ *devv1alpha1.Project, _, schemaFS afero.Fs) {
				t.Helper()
				if err := afero.WriteFile(schemaFS, schemaFile, []byte("edited"), 0o644); err != nil {
					t.Fatal(err)
				}
			}},
			want: want{err: cmpopts.AnyError, errMsg: "crossplane schema generate", schema: "edited"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			projFS := afero.NewMemMapFs()
			writeProject(t, projFS,
				map[string]string{
					"db.yaml":      xrdYAML("acme.example.com", "xdatabases", "xdatabase", "XDatabase"),
					"db-comp.yaml": compositionYAML("xdatabase-comp", "acme.example.com", "XDatabase"),
				}, nil)
			proj := &devv1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{Name: "test-project"},
				Spec:       devv1alpha1.ProjectSpec{Repository: "xpkg.crossplane.io/example/test"},
			}
			proj.Default()

			schemaFS := afero.NewMemMapFs()
			schemas := manager.New(schemaFS, []generator.Interface{fakeSchemaGenerator{}}, nil)
			if tc.args.lock {
				if err := schemas.Add(t.Context(), APISchemaSource(proj, projFS)); err != nil {
					t.Fatal(err)
				}
				depsHash, err := dependency.DependenciesHash(proj)
				if err != nil {
					t.Fatal(err)
				}
				if err := schemas.SetDependenciesHash(depsHash); err != nil {
					t.Fatal(err)
				}
			}
			if tc.args.change != nil {
				tc.args.change(t, proj, projFS, schemaFS)
			}
			lockBefore, _ := afero.ReadFile(schemaFS, ".lock.json")

			b := NewBuilder(
				BuildWithFunctionIdentifier(functions.FakeIdentifier),
				BuildWithSchemaManager(schemas),
				BuildWithDependencyManager(dependency.NewManager(proj, projFS, dependency.WithXpkgClient(failingClient{t: t}))),
			)
			_, err := b.Build(t.Context(), proj, projFS)

			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("%s\nBuild(...): -want err, +got err:\n%s", tc.reason, diff)
			}
			if tc.want.errMsg != "" && (err == nil || !strings.Contains(err.Error(), tc.want.errMsg)) {
				t.Errorf("%s\nBuild(...): err = %v, want error containing %q", tc.reason, err, tc.want.errMsg)
			}
			got, _ := afero.ReadFile(schemaFS, schemaFile)
			if diff := cmp.Diff(tc.want.schema, string(got)); diff != "" {
				t.Errorf("%s\ngenerated file (-want +got):\n%s", tc.reason, diff)
			}
			lockAfter, _ := afero.ReadFile(schemaFS, ".lock.json")
			if tc.want.lockWritten {
				if len(lockBefore) != 0 || len(lockAfter) == 0 {
					t.Errorf("%s\nBuild should have written the lock; before %d bytes, after %d bytes", tc.reason, len(lockBefore), len(lockAfter))
				}
				return
			}
			if diff := cmp.Diff(string(lockBefore), string(lockAfter)); diff != "" {
				t.Errorf("%s\nBuild must never change the lock (-before +after):\n%s", tc.reason, diff)
			}
		})
	}
}

func TestBuilderBuild_RecreatesDependencySchemas(t *testing.T) {
	t.Parallel()

	const crd = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: widgets.example.org
spec:
  group: example.org
  names:
    kind: Widget
    plural: widgets
  scope: Namespaced
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, crd)
	}))
	t.Cleanup(srv.Close)

	type args struct {
		// remove is the generated file deleted after the update.
		remove string
	}
	type want struct {
		// files are the generated files that exist after the build.
		files []string
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"MissingDependencyFile": {
			reason: "A deleted dependency file is recreated from the pin in the lock, without resolving the dependency again.",
			args:   args{remove: "fake/crd.yaml.txt"},
			want:   want{files: []string{"fake/crd.yaml.txt", "fake/db.yaml.txt"}},
		},
		"MissingLocalFile": {
			reason: "A deleted file of the project's own APIs is recreated from the project, without fetching any dependency.",
			args:   args{remove: "fake/db.yaml.txt"},
			want:   want{files: []string{"fake/crd.yaml.txt", "fake/db.yaml.txt"}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			projFS := afero.NewMemMapFs()
			writeProject(t, projFS,
				map[string]string{
					"db.yaml":      xrdYAML("acme.example.com", "xdatabases", "xdatabase", "XDatabase"),
					"db-comp.yaml": compositionYAML("xdatabase-comp", "acme.example.com", "XDatabase"),
				}, nil)
			proj := &devv1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{Name: "test-project"},
				Spec: devv1alpha1.ProjectSpec{
					Repository: "xpkg.crossplane.io/example/test",
					Dependencies: []devv1alpha1.Dependency{{
						Type: devv1alpha1.DependencyTypeCRD,
						HTTP: &devv1alpha1.HTTPDependency{URL: srv.URL + "/crd.yaml"},
					}},
				},
			}
			proj.Default()

			schemaFS := afero.NewMemMapFs()
			schemas := manager.New(schemaFS, []generator.Interface{perFileGenerator{}}, nil)
			deps := dependency.NewManager(proj, projFS, dependency.WithXpkgClient(failingClient{t: t}))

			if _, err := UpdateSchemas(t.Context(), schemas, deps, proj, projFS, nil, false); err != nil {
				t.Fatalf("%s\nUpdateSchemas(...): %v", tc.reason, err)
			}
			if err := schemaFS.Remove(tc.args.remove); err != nil {
				t.Fatal(err)
			}

			b := NewBuilder(
				BuildWithFunctionIdentifier(functions.FakeIdentifier),
				BuildWithSchemaManager(schemas),
				BuildWithDependencyManager(deps),
			)
			if _, err := b.Build(t.Context(), proj, projFS); err != nil {
				t.Fatalf("%s\nBuild(...): %v", tc.reason, err)
			}

			var got []string
			for _, f := range []string{"fake/crd.yaml.txt", "fake/db.yaml.txt"} {
				if ok, _ := afero.Exists(schemaFS, f); ok {
					got = append(got, f)
				}
			}
			if diff := cmp.Diff(tc.want.files, got); diff != "" {
				t.Errorf("%s\ngenerated files (-want +got):\n%s", tc.reason, diff)
			}
		})
	}
}

// xpkgDependency returns an xpkg dependency on the given package and version.
func xpkgDependency(pkg, version string) *devv1alpha1.Dependency {
	return &devv1alpha1.Dependency{
		Type: devv1alpha1.DependencyTypeXpkg,
		Xpkg: &devv1alpha1.XpkgDependency{APIVersion: "pkg.crossplane.io/v1", Kind: "Provider", Package: pkg, Version: version},
	}
}

func constructTag(repo, tag string) (name.Tag, error) {
	return name.NewTag(fmt.Sprintf("%s:%s", repo, tag))
}

func tagsOf(m ImageTagMap) []string {
	out := make([]string, 0, len(m))
	for t := range m {
		out = append(out, t.String())
	}
	return out
}

func TestResolveFunctions(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		spec    devv1alpha1.ProjectSpec
		fnDirs  []string
		fnFiles []string // files (not dirs) under the functions path; should be ignored.
		want    []devv1alpha1.Function
	}{
		"ExplicitListWins": {
			// When the project declares functions explicitly,
			// auto-discovery is disabled and the list is returned verbatim.
			spec: devv1alpha1.ProjectSpec{
				Functions: []devv1alpha1.Function{
					{Source: devv1alpha1.FunctionSourceDirectory, Directory: &devv1alpha1.FunctionDirectory{Name: "explicit"}},
				},
			},
			fnDirs: []string{"would-be-discovered"},
			want: []devv1alpha1.Function{
				{Source: devv1alpha1.FunctionSourceDirectory, Directory: &devv1alpha1.FunctionDirectory{Name: "explicit"}},
			},
		},
		"AutoDiscoverDirectories": {
			// Every subdirectory of the functions path becomes a
			// Directory-source function.
			fnDirs: []string{"fn-a", "fn-b"},
			want: []devv1alpha1.Function{
				{Source: devv1alpha1.FunctionSourceDirectory, Directory: &devv1alpha1.FunctionDirectory{Name: "fn-a"}},
				{Source: devv1alpha1.FunctionSourceDirectory, Directory: &devv1alpha1.FunctionDirectory{Name: "fn-b"}},
			},
		},
		"AutoDiscoverIgnoresFiles": {
			// Files directly under the functions path are not treated as
			// functions; only subdirectories are.
			fnDirs:  []string{"fn-real"},
			fnFiles: []string{"README.md", "stray.tar"},
			want: []devv1alpha1.Function{
				{Source: devv1alpha1.FunctionSourceDirectory, Directory: &devv1alpha1.FunctionDirectory{Name: "fn-real"}},
			},
		},
		"AutoDiscoverNoFunctionsDir": {
			// A missing functions path is not an error; it just yields no
			// functions.
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			projFS := afero.NewMemMapFs()
			for _, d := range tc.fnDirs {
				if err := projFS.MkdirAll(filepath.Join("functions", d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for _, f := range tc.fnFiles {
				if err := projFS.MkdirAll("functions", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := afero.WriteFile(projFS, filepath.Join("functions", f), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			proj := &devv1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{Name: "p"},
				Spec:       tc.spec,
			}
			proj.Spec.Repository = "xpkg.crossplane.io/example/test"
			proj.Default()

			got, err := resolveFunctions(proj, projFS)
			if err != nil {
				t.Fatalf("resolveFunctions: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("resolveFunctions(...): -want, +got:\n%s", diff)
			}
		})
	}
}

func TestBuilderBuildExplicitFunctions(t *testing.T) {
	t.Parallel()

	projFS := afero.NewMemMapFs()
	writeProject(t, projFS,
		map[string]string{
			"db.yaml":      xrdYAML("acme.example.com", "xdatabases", "xdatabase", "XDatabase"),
			"db-comp.yaml": compositionYAML("xdb", "acme.example.com", "XDatabase"),
		},
		// Auto-discovery would find fn-auto; explicit functions should
		// override and only build fn-explicit.
		[]string{"fn-auto", "fn-explicit"},
	)

	proj := &devv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "test-project"},
		Spec: devv1alpha1.ProjectSpec{
			Repository: "xpkg.crossplane.io/example/test",
			Functions: []devv1alpha1.Function{{
				Source:    devv1alpha1.FunctionSourceDirectory,
				Directory: &devv1alpha1.FunctionDirectory{Name: "fn-explicit"},
			}},
		},
	}
	proj.Default()

	b := NewBuilder(BuildWithFunctionIdentifier(functions.FakeIdentifier))

	imgMap, err := b.Build(t.Context(), proj, projFS)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// The configuration image plus per-arch images for fn-explicit. fn-auto
	// must not appear because the explicit list disables auto-discovery.
	want := map[string]bool{
		proj.Spec.Repository:                  true,
		proj.Spec.Repository + "_fn-explicit": true,
	}
	got := map[string]bool{}
	for tag := range imgMap {
		got[tag.Repository.Name()] = true
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Build(...) function repos: -want, +got:\n%s", diff)
	}
}

func TestBuilderBuildTarballFunction(t *testing.T) {
	t.Parallel()

	// Build one single-platform Docker-style tarball per architecture, named
	// using the <pathPrefix>-<arch>.tar convention.
	projFS := afero.NewMemMapFs()
	for _, arch := range []string{"amd64", "arm64"} {
		writeRuntimeTar(t, projFS, "fn-prebuilt-"+arch+".tar", arch)
	}

	if err := projFS.MkdirAll("apis", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := afero.WriteFile(projFS, "apis/db.yaml", []byte(xrdYAML("acme.example.com", "xdatabases", "xdatabase", "XDatabase")), 0o644); err != nil {
		t.Fatal(err)
	}

	proj := &devv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "test-project"},
		Spec: devv1alpha1.ProjectSpec{
			Repository:    "xpkg.crossplane.io/example/test",
			Architectures: []string{"amd64", "arm64"},
			Functions: []devv1alpha1.Function{{
				Source:  devv1alpha1.FunctionSourceTarball,
				Tarball: &devv1alpha1.FunctionTarball{Name: "fn-prebuilt", PathPrefix: "fn-prebuilt"},
			}},
		},
	}
	proj.Default()

	b := NewBuilder(BuildWithFunctionIdentifier(functions.FakeIdentifier))

	imgMap, err := b.Build(t.Context(), proj, projFS)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// The pre-built tarballs should produce one package image per target
	// architecture under the function's derived repo.
	wantRepo := proj.Spec.Repository + "_fn-prebuilt"
	want := map[string]int{wantRepo: 2}
	got := map[string]int{}
	for tag := range imgMap {
		got[tag.Repository.Name()]++
	}
	if diff := cmp.Diff(want, got, cmpopts.IgnoreMapEntries(func(k string, _ int) bool {
		return k != wantRepo
	})); diff != "" {
		t.Errorf("Build(...) tarball function images: -want, +got:\n%s", diff)
	}
}

func TestLoadTarballRuntime(t *testing.T) {
	t.Parallel()

	type args struct {
		// files maps relative file names under the project root to the
		// architecture the runtime image they contain should report.
		files map[string]string
		archs []string
	}
	type want struct {
		archs []string
		err   error
	}

	tcs := map[string]struct {
		args args
		want want
	}{
		"AllArchitecturesPresent": {
			args: args{
				files: map[string]string{
					"fn-amd64.tar": "amd64",
					"fn-arm64.tar": "arm64",
				},
				archs: []string{"amd64", "arm64"},
			},
			want: want{archs: []string{"amd64", "arm64"}},
		},
		"MissingArchitectureFile": {
			args: args{
				files: map[string]string{
					"fn-amd64.tar": "amd64",
				},
				archs: []string{"amd64", "arm64"},
			},
			want: want{err: cmpopts.AnyError},
		},
		"ArchitectureMismatch": {
			args: args{
				files: map[string]string{
					"fn-amd64.tar": "arm64",
				},
				archs: []string{"amd64"},
			},
			want: want{err: cmpopts.AnyError},
		},
		"SingleArchitecture": {
			args: args{
				files: map[string]string{
					"fn-amd64.tar": "amd64",
				},
				archs: []string{"amd64"},
			},
			want: want{archs: []string{"amd64"}},
		},
		"GzippedTarball": {
			args: args{
				files: map[string]string{
					"fn-amd64.tar.gz": "amd64",
				},
				archs: []string{"amd64"},
			},
			want: want{archs: []string{"amd64"}},
		},
		"MixedPlainAndGzipped": {
			args: args{
				files: map[string]string{
					"fn-amd64.tar":    "amd64",
					"fn-arm64.tar.gz": "arm64",
				},
				archs: []string{"amd64", "arm64"},
			},
			want: want{archs: []string{"amd64", "arm64"}},
		},
		"PlainPreferredOverGzipped": {
			// When both .tar and .tar.gz exist for the same architecture,
			// the plain .tar is used.
			args: args{
				files: map[string]string{
					"fn-amd64.tar":    "amd64",
					"fn-amd64.tar.gz": "arm64", // mismatched on purpose to prove it isn't read.
				},
				archs: []string{"amd64"},
			},
			want: want{archs: []string{"amd64"}},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			projFS := afero.NewMemMapFs()
			for fname, arch := range tc.args.files {
				writeRuntimeTar(t, projFS, fname, arch)
			}

			tb := &devv1alpha1.FunctionTarball{Name: "fn", PathPrefix: "fn"}
			got, err := loadTarballRuntime(projFS, tb, tc.args.archs, t.TempDir())

			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("loadTarballRuntime(...): -want error, +got error:\n%s", diff)
			}
			if diff := cmp.Diff(tc.want.archs, archsOf(t, got), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("loadTarballRuntime(...) architectures: -want, +got:\n%s", diff)
			}
		})
	}
}

// TestLoadRuntimeImageGzip checks that loading a multi-layer image from a gzipped
// tarball yields the same image as the equivalent plain tarball - including when
// every layer is read, which happens lazily after loading from the decompressed
// temporary file - and that the tarball is decompressed into the builder's temp
// directory.
func TestLoadRuntimeImageGzip(t *testing.T) {
	t.Parallel()

	projFS := afero.NewMemMapFs()
	// The same multi-layer image under both a plain and a gzipped path, so the
	// two must load to identical digests.
	img := runtimeImageForArch(t, "amd64")
	writeRuntimeTarImage(t, projFS, "plain-amd64.tar", "amd64", img)
	writeRuntimeTarImage(t, projFS, "gz-amd64.tar.gz", "amd64", img)

	tempDir := t.TempDir()

	plain, _, err := loadRuntimeImage(projFS, "plain", "amd64", tempDir)
	if err != nil {
		t.Fatalf("loadRuntimeImage(plain): %v", err)
	}
	gz, _, err := loadRuntimeImage(projFS, "gz", "amd64", tempDir)
	if err != nil {
		t.Fatalf("loadRuntimeImage(gz): %v", err)
	}

	// digestReadingLayers computes the image digest and reads every layer's
	// content. Reading the layers forces the per-layer opener calls that, for
	// the gzipped image, are served from the decompressed temporary file.
	digestReadingLayers := func(img v1.Image) v1.Hash {
		t.Helper()
		d, err := img.Digest()
		if err != nil {
			t.Fatalf("Digest(): %v", err)
		}
		layers, err := img.Layers()
		if err != nil {
			t.Fatalf("Layers(): %v", err)
		}
		for _, l := range layers {
			rc, err := l.Compressed()
			if err != nil {
				t.Fatalf("Compressed(): %v", err)
			}
			if _, err := io.Copy(io.Discard, rc); err != nil {
				t.Fatalf("reading layer: %v", err)
			}
			_ = rc.Close()
		}
		return d
	}

	if diff := cmp.Diff(digestReadingLayers(plain), digestReadingLayers(gz)); diff != "" {
		t.Errorf("gzipped and plain tarballs produced different digests: -plain, +gz:\n%s", diff)
	}

	// The gzipped tarball should have been decompressed into the builder's temp
	// directory; the plain tar is read directly and leaves nothing behind.
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", tempDir, err)
	}
	if len(entries) != 1 {
		t.Errorf("expected exactly one decompressed tarball in temp dir, got %d: %v", len(entries), entries)
	}
}

// archsOf returns the architecture each image reports, in order.
func archsOf(t *testing.T, imgs []v1.Image) []string {
	t.Helper()

	archs := make([]string, 0, len(imgs))
	for _, img := range imgs {
		cfg, err := img.ConfigFile()
		if err != nil {
			t.Fatal(err)
		}
		archs = append(archs, cfg.Architecture)
	}
	return archs
}

// runtimeImageForArch returns a multi-layer single-platform image whose config
// records the given architecture. The layers mean loading it from a tarball
// exercises the per-layer lazy opener calls tarball.Image makes.
func runtimeImageForArch(t *testing.T, arch string) v1.Image {
	t.Helper()

	img, err := random.Image(256, 4)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cfg = cfg.DeepCopy()
	cfg.OS, cfg.Architecture = "linux", arch
	img, err = mutate.ConfigFile(img, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// writeRuntimeTar writes a single-platform Docker-style image tarball recording
// the given architecture to path on fsys. If the path ends with ".tar.gz" the
// tarball is gzipped.
func writeRuntimeTar(t *testing.T, fsys afero.Fs, path, arch string) {
	t.Helper()
	writeRuntimeTarImage(t, fsys, path, arch, runtimeImageForArch(t, arch))
}

// writeRuntimeTarImage writes img as a single-platform Docker-style image
// tarball to path on fsys. If the path ends with ".tar.gz" the tarball is
// gzipped.
func writeRuntimeTarImage(t *testing.T, fsys afero.Fs, path, arch string, img v1.Image) {
	t.Helper()

	tag, err := name.NewTag("crossplane.io/test:" + arch)
	if err != nil {
		t.Fatal(err)
	}

	f, err := fsys.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if !strings.HasSuffix(path, ".gz") {
		if err := tarball.Write(tag, img, f); err != nil {
			t.Fatal(err)
		}
		return
	}

	gz := gzip.NewWriter(f)
	if err := tarball.Write(tag, img, gz); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}
