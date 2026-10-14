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

package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/schemas/runner"
)

// mockSource is a Source over canned resources and versions.
type mockSource struct {
	id         string
	version    string
	resources  map[string]string
	sourceType SourceType
}

func (s *mockSource) ID() string { return s.id }

func (s *mockSource) Version(_ context.Context) (string, error) { return s.version, nil }

func (s *mockSource) Resources(_ context.Context) (afero.Fs, error) {
	fsys := afero.NewMemMapFs()
	for path, contents := range s.resources {
		if err := afero.WriteFile(fsys, path, []byte(contents), 0o600); err != nil {
			return nil, err
		}
	}
	return fsys, nil
}

func (s *mockSource) Type() SourceType {
	if s.sourceType == "" {
		return SourceTypeCRD
	}
	return s.sourceType
}

// walkInputFiles returns the sorted file paths (not directories) under in.
func walkInputFiles(in afero.Fs) ([]string, error) {
	var names []string
	err := afero.Walk(in, ".", func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			names = append(names, p)
		}
		return nil
	})
	return names, err
}

// kindGenerator models a per-kind generator (the shape of the Go, JSON, KCL
// and Python generators): one output file per input resource, so two
// groups' outputs are disjoint -- plus a constant shared root file, which
// some real generators also emit (KCL's kcl.mod) and which assembly must
// accept when its content is identical from both groups.
type kindGenerator struct {
	mu      sync.Mutex // the manager runs groups and source types concurrently
	lang    string
	fail    bool
	finger  string
	runs    int      // every generation run, both groups
	inputs  []string // every input file this generator has ever seen
	shared  string   // content of the shared root file; constant across groups
	noshare bool     // suppress the shared root file
	shareFn func(in afero.Fs) (string, error)
}

// emitSharedDefault is the default shared root file content, standing in for
// files some real generators write from either group identically (KCL's
// kcl.mod, Go's apimachinery models).
const emitSharedDefault = "shared from either group"

func (g *kindGenerator) Language() string {
	if g.lang == "" {
		return "mock"
	}
	return g.lang
}

func (g *kindGenerator) Fingerprint() string { return g.finger }

func (g *kindGenerator) GenerateFromCRD(_ context.Context, in afero.Fs, _ runner.SchemaRunner) (afero.Fs, error) {
	g.mu.Lock()
	fail := g.fail
	if !fail {
		g.runs++
	}
	g.mu.Unlock()
	if fail {
		return nil, errors.New("mock generation failed")
	}

	names, err := walkInputFiles(in)
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	g.inputs = append(g.inputs, names...)
	g.mu.Unlock()

	out := afero.NewMemMapFs()
	for _, name := range names {
		bs, err := afero.ReadFile(in, name)
		if err != nil {
			return nil, err
		}
		if err := afero.WriteFile(out, name, bs, 0o600); err != nil {
			return nil, err
		}
	}

	if !g.noshare {
		shared := g.shared
		if shared == "" {
			shared = emitSharedDefault
		}
		if g.shareFn != nil {
			shared, err = g.shareFn(in)
			if err != nil {
				return nil, err
			}
		}
		if shared != "" {
			if err := afero.WriteFile(out, "shared-root", []byte(shared), 0o600); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func (g *kindGenerator) GenerateFromOpenAPI(_ context.Context, _ afero.Fs, _ runner.SchemaRunner) (afero.Fs, error) {
	return nil, nil
}

// runLevelGenerator models a generator whose output describes its entire
// input (the TypeScript shape: a root index naming everything the run saw)
// and which implements schemaMerger to combine groups.
type runLevelGenerator struct {
	kindGenerator
}

func (g *runLevelGenerator) GenerateFromOpenAPI(ctx context.Context, in afero.Fs, _ runner.SchemaRunner) (afero.Fs, error) {
	return g.GenerateFromCRD(ctx, in, nil)
}

func (g *runLevelGenerator) GenerateFromCRD(_ context.Context, in afero.Fs, _ runner.SchemaRunner) (afero.Fs, error) {
	g.mu.Lock()
	fail := g.fail
	if !fail {
		g.runs++
	}
	g.mu.Unlock()
	if fail {
		return nil, errors.New("mock generation failed")
	}

	names, err := walkInputFiles(in)
	if err != nil {
		return nil, err
	}
	slices.Sort(names)

	out := afero.NewMemMapFs()
	if err := afero.WriteFile(out, "index", []byte(strings.Join(names, ",")), 0o600); err != nil {
		return nil, err
	}
	return out, nil
}

func (g *runLevelGenerator) MergeGeneratedSchemas(parts []afero.Fs) (afero.Fs, error) {
	var names []string
	for _, part := range parts {
		bs, err := afero.ReadFile(part, "index")
		if err != nil {
			return nil, err
		}
		if s := string(bs); s != "" {
			names = append(names, strings.Split(s, ",")...)
		}
	}
	slices.Sort(names)

	out := afero.NewMemMapFs()
	if err := afero.WriteFile(out, "index", []byte(strings.Join(names, ",")), 0o600); err != nil {
		return nil, err
	}
	return out, nil
}

func readFile(t *testing.T, fsys afero.Fs, p string) string {
	t.Helper()
	bs, err := afero.ReadFile(fsys, p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(bs)
}

// assertFilesPresent fails the test if any of the given paths does not exist
// under fsys.
func assertFilesPresent(t *testing.T, fsys afero.Fs, paths ...string) {
	t.Helper()
	for _, p := range paths {
		ok, err := afero.Exists(fsys, p)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("%s is missing", p)
		}
	}
}

// assertFilesAbsent fails the test if any of the given paths exists.
func assertFilesAbsent(t *testing.T, fsys afero.Fs, paths ...string) {
	t.Helper()
	for _, p := range paths {
		ok, err := afero.Exists(fsys, p)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Errorf("%s should have been removed", p)
		}
	}
}

// depSrc builds a dependency-source shorthand.
func depSrc(name, version, file string) *mockSource {
	return &mockSource{id: "xpkg://" + name, version: version, resources: map[string]string{file: file}}
}

// treeDepPath is where a dependency source's file lands in the assembled
// tree: sources are merged under a positional prefix (see
// collectSourceResources), and per-kind generators preserve input paths in
// their output.
func treeDepPath(i int, name string, file string) string {
	return fmt.Sprintf("mock/%04d_xpkg_%s/%s", i, strings.ReplaceAll(strings.ReplaceAll(name, ":", "_"), "/", "_"), file)
}

// treeAPIPath is where the apis source's file lands in the assembled tree.
func treeAPIPath(i int, file string) string {
	return fmt.Sprintf("mock/%04d_fs_apis/%s", i, file)
}

// apiSrc builds the project's apis source shorthand.
func apiSrc(version, file string) *mockSource {
	return &mockSource{id: "fs://apis", version: version, resources: map[string]string{file: file}}
}

// One full pass assembles the tree from both groups, and a second pass over
// unchanged sources generates nothing.
func TestGenerateFromMultipleSources(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	testFS := afero.NewMemMapFs()
	g := &kindGenerator{lang: "mock"}
	m := New(testFS, []generator.Interface{g}, nil)

	dep := depSrc("prov-a:v1", "v1", "dep.yaml")
	api := apiSrc("v1", "xrd.yaml")

	if err := m.GenerateFromMultipleSources(ctx, []Source{dep}, api); err != nil {
		t.Fatal(err)
	}
	assertFilesPresent(t, testFS, treeDepPath(0, "prov-a:v1", "dep.yaml"), treeAPIPath(0, "xrd.yaml"), "mock/shared-root")

	// Both groups ran once.
	if got, want := g.runs, 2; got != want {
		t.Errorf("generation ran %d times, want %d (once per group)", got, want)
	}

	// A second pass over unchanged sources regenerates nothing.
	if err := m.GenerateFromMultipleSources(ctx, []Source{dep}, api); err != nil {
		t.Fatal(err)
	}
	if got, want := g.runs, 2; got != want {
		t.Errorf("generation ran %d times after an unchanged pass, want still %d", g.runs, want)
	}
}

// An edit to the project's own XRDs must not re-run the dependency group's
// generation: the dependencies cache holds, and only the apis group
// regenerates. On main this regenerates every dependency on every XRD edit.
func TestXRDEditRegeneratesOnlyAPIsGroup(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	testFS := afero.NewMemMapFs()
	g := &kindGenerator{lang: "mock"}
	m := New(testFS, []generator.Interface{g}, nil)

	dep := depSrc("prov-a:v1", "v1", "dep.yaml")
	api := apiSrc("v1", "xrd.yaml")

	if err := m.GenerateFromMultipleSources(ctx, []Source{dep}, api); err != nil {
		t.Fatal(err)
	}

	// The XRD changed; the dependency did not.
	api2 := apiSrc("v2", "xrd.yaml")
	if err := m.GenerateFromMultipleSources(ctx, []Source{dep}, api2); err != nil {
		t.Fatal(err)
	}

	// Only the apis group ran: 2 runs before, 1 more now.
	if got, want := g.runs, 3; got != want {
		t.Errorf("generation ran %d times after an XRD edit, want %d (the dependency group must reuse its cache)", g.runs, want)
	}

	// The assembled tree still carries both groups' files, the dependency's
	// straight from its cache.
	assertFilesPresent(t, testFS, treeDepPath(0, "prov-a:v1", "dep.yaml"), treeAPIPath(0, "xrd.yaml"))
}

// A dependency change regenerates the dependency group and reuses the apis
// cache.
func TestDependencyChangeRegeneratesOnlyDependenciesGroup(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	testFS := afero.NewMemMapFs()
	g := &kindGenerator{lang: "mock"}
	m := New(testFS, []generator.Interface{g}, nil)

	dep := depSrc("prov-a:v1", "v1", "dep.yaml")
	api := apiSrc("v1", "xrd.yaml")
	if err := m.GenerateFromMultipleSources(ctx, []Source{dep}, api); err != nil {
		t.Fatal(err)
	}

	dep2 := depSrc("prov-a:v2", "v2", "dep-v2.yaml")
	if err := m.GenerateFromMultipleSources(ctx, []Source{dep2}, api); err != nil {
		t.Fatal(err)
	}

	// Only the dependency group ran.
	if got, want := g.runs, 3; got != want {
		t.Errorf("generation ran %d times after a dependency change, want %d (the apis group must reuse its cache)", g.runs, want)
	}
	assertFilesPresent(t, testFS, treeDepPath(0, "prov-a:v2", "dep-v2.yaml"), treeAPIPath(0, "xrd.yaml"))
	assertFilesAbsent(t, testFS, treeDepPath(0, "prov-a:v1", "dep.yaml"))
}

// Removing a dependency from the source list must remove its models from the
// tree: the assembled tree describes exactly the current sources.
func TestRemovedDependencyLeavesNoStaleModels(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	testFS := afero.NewMemMapFs()
	m := New(testFS, []generator.Interface{&kindGenerator{lang: "mock"}}, nil)

	a := depSrc("prov-a:v1", "v1", "a.yaml")
	b := depSrc("prov-b:v1", "v1", "b.yaml")

	if err := m.GenerateFromMultipleSources(ctx, []Source{a, b}, nil); err != nil {
		t.Fatal(err)
	}
	assertFilesPresent(t, testFS, treeDepPath(0, "prov-a:v1", "a.yaml"), treeDepPath(1, "prov-b:v1", "b.yaml"))

	// prov-b removed from the project.
	if err := m.GenerateFromMultipleSources(ctx, []Source{a}, nil); err != nil {
		t.Fatal(err)
	}
	assertFilesPresent(t, testFS, treeDepPath(0, "prov-a:v1", "a.yaml"))
	assertFilesAbsent(t, testFS, treeDepPath(1, "prov-b:v1", "b.yaml"))
}

// A changed language set must regenerate: the caches and the tree were
// produced for a different generator set. A dropped language must not leave
// its tree behind.
func TestLanguageChangeRegeneratesAndClearsDroppedLanguage(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	testFS := afero.NewMemMapFs()

	dep := depSrc("prov-a:v1", "v1", "dep.yaml")
	api := apiSrc("v1", "xrd.yaml")

	two := New(testFS, []generator.Interface{&kindGenerator{}, &kindGenerator{lang: "other"}}, nil)
	if err := two.GenerateFromMultipleSources(ctx, []Source{dep}, api); err != nil {
		t.Fatal(err)
	}
	assertFilesPresent(t, testFS, "mock/shared-root", "other/shared-root")

	// The same project with "other" dropped from the language set.
	one := New(testFS, []generator.Interface{&kindGenerator{}}, nil)
	if err := one.GenerateFromMultipleSources(ctx, []Source{dep}, api); err != nil {
		t.Fatal(err)
	}
	assertFilesPresent(t, testFS, "mock/shared-root")
	assertFilesAbsent(t, testFS, "other/shared-root")
	if ok, _ := afero.DirExists(testFS, "other"); ok {
		t.Error("schemas for the removed language are still on disk")
	}
}

// A changed generator fingerprint must regenerate, even with identical
// sources and languages: the CLI or a toolchain image changed, so the cached
// output may not be what this CLI would produce.
func TestGeneratorFingerprintChangeRegenerates(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	testFS := afero.NewMemMapFs()

	g := &kindGenerator{lang: "mock", finger: "img:v1"}
	m := New(testFS, []generator.Interface{g}, nil)

	dep := depSrc("prov-a:v1", "v1", "dep.yaml")
	api := apiSrc("v1", "xrd.yaml")

	if err := m.GenerateFromMultipleSources(ctx, []Source{dep}, api); err != nil {
		t.Fatal(err)
	}

	// The toolchain was upgraded.
	g.finger = "img:v2"
	if err := m.GenerateFromMultipleSources(ctx, []Source{dep}, api); err != nil {
		t.Fatal(err)
	}

	if got, want := g.runs, 4; got != want {
		t.Errorf("generation ran %d times after a fingerprint change, want %d (both groups regenerate)", g.runs, want)
	}
}

// A hand-deleted language directory must not read as fresh: the lock can
// outlive its output, and the next pass must reassemble from the caches.
func TestDeletedLanguageDirIsReassembled(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	testFS := afero.NewMemMapFs()
	m := New(testFS, []generator.Interface{&kindGenerator{lang: "mock"}}, nil)

	dep := depSrc("prov-a:v1", "v1", "dep.yaml")
	api := apiSrc("v1", "xrd.yaml")

	if err := m.GenerateFromMultipleSources(ctx, []Source{dep}, api); err != nil {
		t.Fatal(err)
	}

	if err := testFS.RemoveAll("mock"); err != nil {
		t.Fatal(err)
	}

	if err := m.GenerateFromMultipleSources(ctx, []Source{dep}, api); err != nil {
		t.Fatal(err)
	}
	assertFilesPresent(t, testFS, treeDepPath(0, "prov-a:v1", "dep.yaml"), treeAPIPath(0, "xrd.yaml"))
}

// A damaged or old-format lock must read as stale and cause regeneration,
// not fail the build.
func TestDamagedLockRegenerates(t *testing.T) {
	tcs := map[string]struct{ lockContent string }{
		"Truncated":    {lockContent: `{"version": 2, "groups": {"de`},
		"OldFormat":    {lockContent: `{"packages": {"xpkg://a": "v1"}}`},
		"EmptyNotJSON": {lockContent: ""},
	}
	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			testFS := afero.NewMemMapFs()
			if err := afero.WriteFile(testFS, lockFileName, []byte(tc.lockContent), 0o600); err != nil {
				t.Fatal(err)
			}

			m := New(testFS, []generator.Interface{&kindGenerator{lang: "mock"}}, nil)
			if err := m.GenerateFromMultipleSources(t.Context(), []Source{depSrc("prov-a:v1", "v1", "dep.yaml")}, nil); err != nil {
				t.Fatalf("pass with a damaged lock failed: %v", err)
			}
			assertFilesPresent(t, testFS, treeDepPath(0, "prov-a:v1", "dep.yaml"))

			// And the lock was rewritten into the current format.
			l := m.readLock()
			if l.Version != lockVersion {
				t.Errorf("lock version = %d, want %d", l.Version, lockVersion)
			}
		})
	}
}

// A generation failure must leave the previous tree and lock in place, and
// recovery on the next pass must work.
func TestGenerationFailureKeepsPreviousTree(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	testFS := afero.NewMemMapFs()
	g := &kindGenerator{lang: "mock"}
	m := New(testFS, []generator.Interface{g}, nil)

	dep := depSrc("prov-a:v1", "v1", "dep.yaml")
	api := apiSrc("v1", "xrd.yaml")

	if err := m.GenerateFromMultipleSources(ctx, []Source{dep}, api); err != nil {
		t.Fatal(err)
	}

	// The apis source changes and the generator starts failing.
	g.fail = true
	api2 := apiSrc("v2", "xrd.yaml")
	if err := m.GenerateFromMultipleSources(ctx, []Source{dep}, api2); err == nil {
		t.Fatal("expected the failing pass to error")
	}

	// The previous tree survived untouched.
	assertFilesPresent(t, testFS, treeDepPath(0, "prov-a:v1", "dep.yaml"), treeAPIPath(0, "xrd.yaml"))

	// So did the lock: the failed pass must not record anything.
	l := m.readLock()
	if got, want := l.Groups[sourceGroupAPIs]["fs://apis"], "v1"; got != want {
		t.Errorf("lock apis version = %q, want %q", got, want)
	}

	// Recovery: the generator stops failing; the pass regenerates and
	// assembles the new tree.
	g.fail = false
	if err := m.GenerateFromMultipleSources(ctx, []Source{dep}, api2); err != nil {
		t.Fatal(err)
	}
	l = m.readLock()
	if got, want := l.Groups[sourceGroupAPIs]["fs://apis"], "v2"; got != want {
		t.Errorf("lock apis version after recovery = %q, want %q", got, want)
	}
	assertFilesPresent(t, testFS, treeDepPath(0, "prov-a:v1", "dep.yaml"), treeAPIPath(0, "xrd.yaml"))
}

// A same-path, different-content collision between the two groups must be an
// error, not a silent last-write-wins.
func TestConflictingGeneratedFileErrors(t *testing.T) {
	t.Parallel()

	testFS := afero.NewMemMapFs()

	// The shared root file's content depends on which group's input the
	// generator saw, so the two groups' copies differ.
	g := &kindGenerator{lang: "mock"}
	g.shareFn = func(in afero.Fs) (string, error) {
		names, err := walkInputFiles(in)
		if err != nil {
			return "", err
		}
		for _, n := range names {
			if strings.Contains(n, "dep.yaml") {
				return "from-dependencies", nil
			}
		}
		return "from-apis", nil
	}

	m := New(testFS, []generator.Interface{g}, nil)

	dep := depSrc("prov-a:v1", "v1", "dep.yaml")
	api := apiSrc("v1", "xrd.yaml")

	err := m.GenerateFromMultipleSources(t.Context(), []Source{dep}, api)
	if err == nil {
		t.Fatal("expected a conflict error when both groups emit a different root file")
	}
	if !strings.Contains(err.Error(), "conflicting generated file") {
		t.Errorf("error does not name the conflict: %v", err)
	}
}

// A run-level generator (schemaMerger, TypeScript's shape) must see both
// groups' output merged, not whichever group was copied last.
func TestRunLevelLanguageMergesGroups(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	testFS := afero.NewMemMapFs()
	g := &runLevelGenerator{kindGenerator{lang: "mock"}}
	m := New(testFS, []generator.Interface{g}, nil)

	dep := depSrc("prov-a:v1", "v1", "dep.yaml")
	api := apiSrc("v1", "xrd.yaml")

	if err := m.GenerateFromMultipleSources(ctx, []Source{dep}, api); err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, testFS, "mock/index"), "0000_fs_apis/xrd.yaml,0000_xpkg_prov-a_v1/dep.yaml"; got != want {
		t.Errorf("merged index = %q, want %q (both groups' resources, merged)", got, want)
	}

	// And an unchanged second pass does not regenerate.
	if err := m.GenerateFromMultipleSources(ctx, []Source{dep}, api); err != nil {
		t.Fatal(err)
	}
	if got, want := g.runs, 2; got != want {
		t.Errorf("generation ran %d times, want %d", g.runs, want)
	}
}

// The lock file is written indented, one entry per line, so it is
// human-readable and diff-friendly. See issue #168.
func TestLockIsIndented(t *testing.T) {
	t.Parallel()

	testFS := afero.NewMemMapFs()
	m := New(testFS, []generator.Interface{&kindGenerator{lang: "mock"}}, nil)

	if err := m.GenerateFromMultipleSources(t.Context(), []Source{depSrc("pkg.example/foo:v0.5.2", "sha256:aaa", "a.yaml")}, nil); err != nil {
		t.Fatal(err)
	}

	got, err := afero.ReadFile(testFS, lockFileName)
	if err != nil {
		t.Fatal(err)
	}

	// Map keys are marshalled in sorted order, so this golden is
	// deterministic.
	want := `{
  "version": 2,
  "generator": "` + m.generatorFingerprint() + `",
  "languages": [
    "mock"
  ],
  "groups": {
    "apis": {},
    "dependencies": {
      "xpkg://pkg.example/foo:v0.5.2": "sha256:aaa"
    }
  }
}
`
	if diff := cmp.Diff(want, string(got)); diff != "" {
		t.Errorf("lock file (-want +got):\n%s", diff)
	}
}

// A pass that regenerates the dependency group and then fails on the apis
// group leaves the new dependency cache behind with the old lock. A later
// pass must not trust that cache as the old sources' output: it is judged by
// its own stamp, so going back to the old dependency regenerates it.
func TestFailedPassDoesNotPoisonGroupCache(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	testFS := afero.NewMemMapFs()
	g := &kindGenerator{lang: "mock"}
	g.shareFn = func(in afero.Fs) (string, error) {
		names, err := walkInputFiles(in)
		if err != nil {
			return "", err
		}
		for _, n := range names {
			if strings.Contains(n, "broken.yaml") {
				return "", errors.New("mock generation failed on a broken XRD")
			}
		}
		return emitSharedDefault, nil
	}
	m := New(testFS, []generator.Interface{g}, nil)

	depV1 := depSrc("prov-a", "v1", "dep-v1.yaml")
	if err := m.GenerateFromMultipleSources(ctx, []Source{depV1}, apiSrc("v1", "xrd.yaml")); err != nil {
		t.Fatal(err)
	}

	// The dependency moves to v2 and the XRD is broken: the dependency group
	// regenerates into its cache, then the apis group fails.
	depV2 := depSrc("prov-a", "v2", "dep-v2.yaml")
	if err := m.GenerateFromMultipleSources(ctx, []Source{depV2}, apiSrc("v2", "broken.yaml")); err == nil {
		t.Fatal("expected the pass with a broken XRD to fail")
	}

	// The dependency is reverted to v1 and the XRD fixed. The tree must hold
	// v1's models, not the v2 output the failed pass cached.
	if err := m.GenerateFromMultipleSources(ctx, []Source{depV1}, apiSrc("v3", "xrd-fixed.yaml")); err != nil {
		t.Fatal(err)
	}
	assertFilesPresent(t, testFS, treeDepPath(0, "prov-a", "dep-v1.yaml"), treeAPIPath(0, "xrd-fixed.yaml"))
	assertFilesAbsent(t, testFS, treeDepPath(0, "prov-a", "dep-v2.yaml"))
}

// openAPIRunLevelSrc is a dependency source of OpenAPI type.
func openAPIRunLevelSrc(name, version, file string) *mockSource {
	s := depSrc(name, version, file)
	s.sourceType = SourceTypeOpenAPI
	return s
}

// A run-level generator must also merge within a group: a project with both
// CRD and OpenAPI dependencies (an xpkg plus k8s) gives the dependency group
// two parts, whose run-level files describe different inputs.
func TestRunLevelLanguageMergesWithinGroup(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	testFS := afero.NewMemMapFs()
	g := &runLevelGenerator{kindGenerator{lang: "mock"}}
	m := New(testFS, []generator.Interface{g}, nil)

	crd := depSrc("prov-a:v1", "v1", "dep.yaml")
	openAPI := openAPIRunLevelSrc("k8s:v1.35.0", "v1.35.0", "k8s.json")

	if err := m.GenerateFromMultipleSources(ctx, []Source{crd, openAPI}, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, testFS, "mock/index"), "0000_xpkg_k8s_v1.35.0/k8s.json,0000_xpkg_prov-a_v1/dep.yaml"; got != want {
		t.Errorf("merged index = %q, want %q (both source types' resources)", got, want)
	}
}

// A per-kind generator whose CRD and OpenAPI output disagree on a shared file
// fails with an error that names the source types inside the group, not the
// two groups.
func TestConflictWithinGroupNamesSourceTypes(t *testing.T) {
	t.Parallel()

	testFS := afero.NewMemMapFs()
	g := &conflictingTypesGenerator{kindGenerator{lang: "mock"}}
	m := New(testFS, []generator.Interface{g}, nil)

	crd := depSrc("prov-a:v1", "v1", "dep.yaml")
	openAPI := openAPIRunLevelSrc("k8s:v1.35.0", "v1.35.0", "k8s.json")

	err := m.GenerateFromMultipleSources(t.Context(), []Source{crd, openAPI}, nil)
	if err == nil {
		t.Fatal("expected a conflict between the CRD and OpenAPI output")
	}
	for _, want := range []string{"conflicting generated file shared-root", "CRD and OpenAPI", "dependencies"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "dependency and API groups") {
		t.Errorf("error blames the groups for a conflict inside one group: %v", err)
	}
}

// conflictingTypesGenerator writes a shared root file whose content depends
// on the source type, like KCL's kcl.mod did before its edition was pinned.
type conflictingTypesGenerator struct {
	kindGenerator
}

func (g *conflictingTypesGenerator) GenerateFromOpenAPI(_ context.Context, _ afero.Fs, _ runner.SchemaRunner) (afero.Fs, error) {
	out := afero.NewMemMapFs()
	if err := afero.WriteFile(out, "shared-root", []byte("from openapi"), 0o600); err != nil {
		return nil, err
	}
	return out, nil
}

// modelsGenerator mimics the Python generator's layout: its output is
// models/{pyproject.toml, models/<package>/...}.
type modelsGenerator struct {
	kindGenerator
}

func (g *modelsGenerator) GenerateFromCRD(_ context.Context, in afero.Fs, _ runner.SchemaRunner) (afero.Fs, error) {
	names, err := walkInputFiles(in)
	if err != nil {
		return nil, err
	}
	out := afero.NewMemMapFs()
	if err := afero.WriteFile(out, "models/pyproject.toml", []byte("[project]"), 0o600); err != nil {
		return nil, err
	}
	for _, n := range names {
		if err := afero.WriteFile(out, "models/models/"+n+".py", []byte(n), 0o600); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// The generator's outer models/ directory is removed exactly once, at
// assembly. The cache keeps the generator output verbatim, so the inner
// models/ package directory survives; removing the prefix twice flattened
// python/models/... to python/....
func TestModelsPrefixStrippedOnce(t *testing.T) {
	t.Parallel()

	testFS := afero.NewMemMapFs()
	m := New(testFS, []generator.Interface{&modelsGenerator{kindGenerator{lang: "python"}}}, nil)

	dep := depSrc("prov-a:v1", "v1", "dep.yaml")
	api := apiSrc("v1", "xrd.yaml")
	if err := m.GenerateFromMultipleSources(t.Context(), []Source{dep}, api); err != nil {
		t.Fatal(err)
	}

	assertFilesPresent(t, testFS,
		"python/pyproject.toml",
		"python/models/0000_xpkg_prov-a_v1/dep.yaml.py",
		"python/models/0000_fs_apis/xrd.yaml.py",
		".cache/dependencies/python/models/pyproject.toml",
	)
	assertFilesAbsent(t, testFS, "python/0000_xpkg_prov-a_v1/dep.yaml.py")
}

// Errors name a source the way the user wrote it, not by its lock ID: an HTTP
// source's ID is http://<url>, which doubles the scheme.
func TestSourceErrorsUseReadableNames(t *testing.T) {
	t.Parallel()

	m := New(afero.NewMemMapFs(), []generator.Interface{&kindGenerator{lang: "mock"}}, nil)
	src := NewHTTPSource(v1alpha1.Dependency{
		Type: v1alpha1.DependencyTypeCRD,
		HTTP: &v1alpha1.HTTPDependency{URL: "http://127.0.0.1:1/crds.yaml"},
	})

	err := m.GenerateFromMultipleSources(t.Context(), []Source{src}, nil)
	if err == nil {
		t.Fatal("expected an error for an unreachable HTTP source")
	}
	if !strings.Contains(err.Error(), "cannot load http://127.0.0.1:1/crds.yaml") {
		t.Errorf("error does not name the URL: %v", err)
	}
	if strings.Contains(err.Error(), "http://http://") {
		t.Errorf("error uses the source's lock ID: %v", err)
	}
}

// A generator failure names the language and the group in plain words.
func TestGeneratorErrorNamesLanguageAndGroup(t *testing.T) {
	t.Parallel()

	m := New(afero.NewMemMapFs(), []generator.Interface{&kindGenerator{lang: "python", fail: true}}, nil)
	err := m.GenerateFromMultipleSources(t.Context(), nil, apiSrc("v1", "xrd.yaml"))
	if err == nil {
		t.Fatal("expected the failing generator to fail the pass")
	}
	if want := "cannot generate schemas for the project's own APIs: python generator: mock generation failed"; err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestDescribeSource(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		src  Source
		want string
	}{
		"APIsDir":  {src: NewFSSource("apis", afero.NewMemMapFs()), want: "apis/"},
		"APIsRoot": {src: NewFSSource(".", afero.NewMemMapFs()), want: "the project root"},
		"Xpkg":     {src: NewXpkgSource("xpkg.example/prov-a:v1.0.0", "sha256:abc", afero.NewMemMapFs()), want: "xpkg.example/prov-a:v1.0.0"},
		"HTTP": {src: NewHTTPSource(v1alpha1.Dependency{
			HTTP: &v1alpha1.HTTPDependency{URL: "https://example.com/crds.yaml"},
		}), want: "https://example.com/crds.yaml"},
		"K8s": {src: NewK8sSource(v1alpha1.Dependency{
			K8s: &v1alpha1.K8sDependency{Version: "v1.35.0"},
		}), want: "Kubernetes API v1.35.0"},
		"NoDescriber": {src: depSrc("prov-a", "v1", "a.yaml"), want: "xpkg://prov-a"},
	}
	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := describeSource(tc.src); got != tc.want {
				t.Errorf("describeSource() = %q, want %q", got, tc.want)
			}
		})
	}
}

// failRenameFs fails the rename that swaps the staged tree of one language
// into place, once armed.
type failRenameFs struct {
	afero.Fs
	mu       sync.Mutex
	failLang string
}

func (f *failRenameFs) arm(lang string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failLang = lang
}

func (f *failRenameFs) Rename(oldname, newname string) error {
	f.mu.Lock()
	lang := f.failLang
	f.mu.Unlock()
	if lang != "" && strings.TrimPrefix(oldname, "/") == path.Join(stagingDirName, "tree", lang) {
		return errors.New("mock rename failure")
	}
	return f.Fs.Rename(oldname, newname)
}

// A failure while swapping in the second language must put the first
// language's previous tree back: the tree on disk is never half old, half
// new.
func TestFailedSwapRestoresPreviousTree(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	testFS := &failRenameFs{Fs: afero.NewMemMapFs()}
	m := New(testFS, []generator.Interface{&kindGenerator{lang: "aaa"}, &kindGenerator{lang: "bbb"}}, nil)

	dep := depSrc("prov-a:v1", "v1", "dep.yaml")
	if err := m.GenerateFromMultipleSources(ctx, []Source{dep}, apiSrc("v1", "xrd-v1.yaml")); err != nil {
		t.Fatal(err)
	}

	// The XRD changes, and swapping in bbb's new tree fails after aaa's has
	// already been swapped.
	testFS.arm("bbb")
	if err := m.GenerateFromMultipleSources(ctx, []Source{dep}, apiSrc("v2", "xrd-v2.yaml")); err == nil {
		t.Fatal("expected the failed swap to fail the pass")
	}
	for _, lang := range []string{"aaa", "bbb"} {
		assertFilesPresent(t, testFS, lang+"/0000_fs_apis/xrd-v1.yaml")
		assertFilesAbsent(t, testFS, lang+"/0000_fs_apis/xrd-v2.yaml")
	}
	if ok, _ := afero.Exists(testFS, stagingDirName); ok {
		t.Errorf("%s left behind after a failed pass", stagingDirName)
	}

	// Once renames work again, the next pass completes the update.
	testFS.arm("")
	if err := m.GenerateFromMultipleSources(ctx, []Source{dep}, apiSrc("v2", "xrd-v2.yaml")); err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"aaa", "bbb"} {
		assertFilesPresent(t, testFS, lang+"/0000_fs_apis/xrd-v2.yaml")
		assertFilesAbsent(t, testFS, lang+"/0000_fs_apis/xrd-v1.yaml")
	}
}

// Sources are grouped by the argument they are passed in, not by their IDs:
// a dependency whose ID happens to look like a local path is still a
// dependency.
func TestGroupsComeFromArguments(t *testing.T) {
	t.Parallel()

	testFS := afero.NewMemMapFs()
	m := New(testFS, []generator.Interface{&kindGenerator{lang: "mock"}}, nil)

	vendored := &mockSource{id: "fs://vendored-crds", version: "v1", resources: map[string]string{"crd.yaml": "crd"}}
	if err := m.GenerateFromMultipleSources(t.Context(), []Source{vendored}, apiSrc("v1", "xrd.yaml")); err != nil {
		t.Fatal(err)
	}

	l := m.readLock()
	if _, ok := l.Groups[sourceGroupDependencies]["fs://vendored-crds"]; !ok {
		t.Errorf("dependency group = %v, want it to hold fs://vendored-crds", l.Groups[sourceGroupDependencies])
	}
	if diff := cmp.Diff(map[string]string{"fs://apis": "v1"}, l.Groups[sourceGroupAPIs]); diff != "" {
		t.Errorf("apis group (-want +got):\n%s", diff)
	}
}

// A pass that changes nothing does not rewrite the lock file. The lock is
// re-saved in a different (compact) format between the passes; an untouched
// file keeps that format.
func TestUnchangedPassLeavesLockFileAlone(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	testFS := afero.NewMemMapFs()
	m := New(testFS, []generator.Interface{&kindGenerator{lang: "mock"}}, nil)
	dep := depSrc("prov-a:v1", "v1", "dep.yaml")
	api := apiSrc("v1", "xrd.yaml")

	if err := m.GenerateFromMultipleSources(ctx, []Source{dep}, api); err != nil {
		t.Fatal(err)
	}
	compact, err := json.Marshal(m.readLock())
	if err != nil {
		t.Fatal(err)
	}
	if err := afero.WriteFile(testFS, lockFileName, compact, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := m.GenerateFromMultipleSources(ctx, []Source{dep}, api); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, testFS, lockFileName); got != string(compact) {
		t.Errorf("an unchanged pass rewrote the lock file:\n%s", got)
	}

	// A change still writes it.
	if err := m.GenerateFromMultipleSources(ctx, []Source{dep}, apiSrc("v2", "xrd.yaml")); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, testFS, lockFileName); got == string(compact) {
		t.Error("a changed pass did not rewrite the lock file")
	}
}

// Each source is its own generator run, so a group of many dependencies
// generates in parallel instead of in one long toolchain process; the parts
// still assemble into one tree.
func TestEachSourceIsOneGeneratorRun(t *testing.T) {
	t.Parallel()

	testFS := afero.NewMemMapFs()
	g := &kindGenerator{lang: "mock"}
	m := New(testFS, []generator.Interface{g}, nil)

	deps := []Source{
		depSrc("prov-a:v1", "v1", "a.yaml"),
		depSrc("prov-b:v1", "v1", "b.yaml"),
		depSrc("prov-c:v1", "v1", "c.yaml"),
	}
	if err := m.GenerateFromMultipleSources(t.Context(), deps, apiSrc("v1", "xrd.yaml")); err != nil {
		t.Fatal(err)
	}

	if got, want := g.runs, 4; got != want {
		t.Errorf("generator ran %d times, want %d (one per source)", got, want)
	}
	assertFilesPresent(t, testFS,
		treeDepPath(0, "prov-a:v1", "a.yaml"),
		treeDepPath(1, "prov-b:v1", "b.yaml"),
		treeDepPath(2, "prov-c:v1", "c.yaml"),
		treeAPIPath(0, "xrd.yaml"),
		"mock/shared-root",
	)
}

// The hidden directories ignore themselves for git, so projects that check
// schemas/ in do not pick up the cache, and staging is gone once a pass ends,
// including a pass that replaced a previous tree.
func TestHiddenDirectoriesStayOutOfGit(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	testFS := afero.NewMemMapFs()
	m := New(testFS, []generator.Interface{&kindGenerator{lang: "mock"}}, nil)

	dep := depSrc("prov-a:v1", "v1", "dep.yaml")
	for _, xrd := range []string{"v1", "v2"} {
		if err := m.GenerateFromMultipleSources(ctx, []Source{dep}, apiSrc(xrd, "xrd-"+xrd+".yaml")); err != nil {
			t.Fatal(err)
		}
		if ok, _ := afero.Exists(testFS, stagingDirName); ok {
			t.Errorf("after the %s pass: %s left behind", xrd, stagingDirName)
		}
	}

	bs, err := afero.ReadFile(testFS, path.Join(cacheDirName, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(ignoreAllGitignore, string(bs)); diff != "" {
		t.Errorf("%s/.gitignore (-want +got):\n%s", cacheDirName, diff)
	}
}
