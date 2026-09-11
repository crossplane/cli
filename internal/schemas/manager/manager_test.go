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
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"

	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/schemas/runner"
)

func TestManager_Add(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		lock *lock
		gen  generator.Interface
		src  Source

		expectedLock  *lock
		expectedFiles map[string]string
		expectErr     bool
	}{
		// Version already matches: skip generation.
		"AlreadyGenerated": {
			lock: &lock{
				Packages: map[string]string{
					"xpkg.upbound.io/my-org/my-pkg": "v1.0.0",
				},
			},
			gen: &mockGenerator{
				files: map[string]string{
					"should-not-exist": "does not get created",
				},
			},
			src: &mockSource{
				id:      "xpkg.upbound.io/my-org/my-pkg",
				version: "v1.0.0",
			},
			expectedLock: &lock{
				Packages: map[string]string{
					"xpkg.upbound.io/my-org/my-pkg": "v1.0.0",
				},
			},
		},
		// No lock at all: generate and record version.
		"EmptyLock": {
			gen: &mockGenerator{
				files: map[string]string{
					"should-exist": "does get created",
				},
			},
			src: &mockSource{
				id:      "xpkg.upbound.io/my-org/my-pkg",
				version: "v1.0.0",
			},
			expectedLock: &lock{
				Packages: map[string]string{
					"xpkg.upbound.io/my-org/my-pkg": "v1.0.0",
				},
			},
			expectedFiles: map[string]string{
				"mock/should-exist": "does get created",
			},
		},
		// Version changed: regenerate and update lock.
		"VersionUpdated": {
			lock: &lock{
				Packages: map[string]string{
					"xpkg.upbound.io/my-org/my-pkg": "v1.0.0",
				},
			},
			gen: &mockGenerator{
				files: map[string]string{
					"should-exist": "does get created",
				},
			},
			src: &mockSource{
				id:      "xpkg.upbound.io/my-org/my-pkg",
				version: "v1.1.0",
			},
			expectedLock: &lock{
				Packages: map[string]string{
					"xpkg.upbound.io/my-org/my-pkg": "v1.1.0",
				},
			},
			expectedFiles: map[string]string{
				"mock/should-exist": "does get created",
			},
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			testFS := afero.NewMemMapFs()

			m := New(testFS, []generator.Interface{tc.gen}, nil)
			if tc.lock != nil {
				if err := m.updateLock(tc.lock); err != nil {
					t.Fatal(err)
				}
			}

			err := m.Add(t.Context(), tc.src)
			if tc.expectErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}

			_ = afero.Walk(testFS, ".", func(path string, info fs.FileInfo, err error) error {
				if err != nil {
					t.Fatal(err)
				}
				if info.Name() == lockFileName {
					return nil
				}
				if info.IsDir() {
					return nil
				}

				want, ok := tc.expectedFiles[path]
				if !ok {
					t.Errorf("unexpected file %q generated", path)
				}

				got, err := afero.ReadFile(testFS, path)
				if err != nil {
					t.Fatal(err)
				}
				if diff := cmp.Diff(want, string(got)); diff != "" {
					t.Errorf("file %q content (-want +got):\n%s", path, diff)
				}

				return nil
			})

			gotLock, err := m.getLock()
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.expectedLock, gotLock); diff != "" {
				t.Errorf("lock (-want +got):\n%s", diff)
			}
		})
	}
}

func TestUpdateLockIsIndented(t *testing.T) {
	// The lock file must be written with one entry per line (indented) so it is
	// human-readable and avoids spurious merge conflicts. See issue #168.
	fs := afero.NewMemMapFs()
	m := New(fs, nil, nil)

	l := newLock()
	l.Packages["xpkg://pkg.example/bar:v1.0.0"] = "sha256:bbb"
	l.Packages["xpkg://pkg.example/foo:v0.5.2"] = "sha256:aaa"

	if err := m.updateLock(l); err != nil {
		t.Fatalf("updateLock: %v", err)
	}

	got, err := afero.ReadFile(fs, lockFileName)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}

	// Map keys are marshalled in sorted order, so this golden is deterministic.
	want := `{
  "packages": {
    "xpkg://pkg.example/bar:v1.0.0": "sha256:bbb",
    "xpkg://pkg.example/foo:v0.5.2": "sha256:aaa"
  }
}
`
	if diff := cmp.Diff(want, string(got)); diff != "" {
		t.Errorf("lock file is not indented as expected (-want +got):\n%s", diff)
	}
}

type mockGenerator struct {
	files map[string]string
}

func (g *mockGenerator) Language() string {
	return "mock"
}

func (g *mockGenerator) GenerateFromCRD(_ context.Context, _ afero.Fs, _ runner.SchemaRunner) (afero.Fs, error) {
	fs := afero.NewMemMapFs()
	for path, contents := range g.files {
		if err := afero.WriteFile(fs, path, []byte(contents), 0o600); err != nil {
			return nil, err
		}
	}
	return fs, nil
}

func (g *mockGenerator) GenerateFromOpenAPI(_ context.Context, _ afero.Fs, _ runner.SchemaRunner) (afero.Fs, error) {
	return nil, nil
}

type mockSource struct {
	id        string
	version   string
	resources map[string]string
}

func (s *mockSource) ID() string {
	return s.id
}

func (s *mockSource) Version(_ context.Context) (string, error) {
	return s.version, nil
}

func (s *mockSource) Resources(_ context.Context) (afero.Fs, error) {
	if s.resources == nil {
		return nil, nil
	}
	fs := afero.NewMemMapFs()
	for path, contents := range s.resources {
		if err := afero.WriteFile(fs, path, []byte(contents), 0o600); err != nil {
			return nil, err
		}
	}
	return fs, nil
}

func (s *mockSource) Type() SourceType {
	return SourceTypeCRD
}

// indexingGenerator writes one index file naming every resource it was handed.
// That is what makes a merged pass distinguishable from a single-source one: the
// merged pass sees every source at once so its index names them all, while a
// single-source pass overwrites that same file with only its own. The real
// TypeScript generator has exactly this shape - a root index enumerating every
// group - which is why the bug below is visible there and not in JSON.
type indexingGenerator struct{ lang string }

func (g *indexingGenerator) Language() string {
	if g.lang == "" {
		return "mock"
	}
	return g.lang
}

func (g *indexingGenerator) GenerateFromCRD(_ context.Context, in afero.Fs, _ runner.SchemaRunner) (afero.Fs, error) {
	var names []string
	if in != nil {
		err := afero.Walk(in, ".", func(_ string, info fs.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				names = append(names, info.Name())
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	slices.Sort(names)

	out := afero.NewMemMapFs()
	if err := afero.WriteFile(out, "index", []byte(strings.Join(names, ",")), 0o600); err != nil {
		return nil, err
	}
	return out, nil
}

func (g *indexingGenerator) GenerateFromOpenAPI(_ context.Context, _ afero.Fs, _ runner.SchemaRunner) (afero.Fs, error) {
	return nil, nil
}

func readMockIndex(t *testing.T, testFS afero.Fs) string {
	t.Helper()

	bs, err := afero.ReadFile(testFS, "mock/index")
	if err != nil {
		t.Fatalf("read generated index: %v", err)
	}
	return string(bs)
}

// A single-source write must not leave a stale tree reading as fresh.
//
// lock.Packages serves both callers: the merged pass replaces the whole map,
// while Add writes one entry into it. So after a dependency is added, every
// recorded version matches its source while the tree on disk is the one the
// single-source pass overwrote. A freshness check that trusts Packages alone
// skips the next merged pass and leaves the user with the clobbered tree, with
// no way back short of deleting the lock by hand.
func TestMergedPassRegeneratesAfterSingleSourceWrite(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	testFS := afero.NewMemMapFs()
	m := New(testFS, []generator.Interface{&indexingGenerator{}}, nil)

	a := &mockSource{id: "xpkg://a", version: "v1", resources: map[string]string{"a.yaml": "a"}}
	b := &mockSource{id: "xpkg://b", version: "v1", resources: map[string]string{"b.yaml": "b"}}
	c := &mockSource{id: "xpkg://c", version: "v1", resources: map[string]string{"c.yaml": "c"}}

	if err := m.GenerateFromMultipleSources(ctx, []Source{a, b}); err != nil {
		t.Fatal(err)
	}
	if got, want := readMockIndex(t, testFS), "a.yaml,b.yaml"; got != want {
		t.Fatalf("after the merged pass, index = %q, want %q", got, want)
	}

	// What `crossplane dependency add` does: one source, straight through
	// Generate, overwriting the merged index with only its own entry.
	if err := m.Add(ctx, c); err != nil {
		t.Fatal(err)
	}
	if got, want := readMockIndex(t, testFS), "c.yaml"; got != want {
		t.Fatalf("after the single-source add, index = %q, want %q; this test's premise no longer holds", got, want)
	}

	// The build that follows has to rebuild the index rather than trust the lock.
	if err := m.GenerateFromMultipleSources(ctx, []Source{a, b, c}); err != nil {
		t.Fatal(err)
	}
	if got, want := readMockIndex(t, testFS), "a.yaml,b.yaml,c.yaml"; got != want {
		t.Errorf("after the merged pass that follows a single-source write, index = %q, want %q", got, want)
	}
}

// A language dropped from spec.schemas.languages must not leave its tree
// behind. Nothing else would ever remove it: it is gone from the generator set,
// so it is absent from m.languages(), which is what the clearing iterated.
//
// That is not cosmetic. The TypeScript function builder gates on whether the
// language directory exists, so an orphaned tree stays load-bearing - a project
// with a hand-added function keeps building against models no pass will update
// again, exit 0 and no warning.
func TestRemovedLanguageDirIsCleared(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	testFS := afero.NewMemMapFs()
	src := &mockSource{id: "xpkg://a", version: "v1", resources: map[string]string{"a.yaml": "a"}}

	both := New(testFS, []generator.Interface{&indexingGenerator{}, &indexingGenerator{lang: "other"}}, nil)
	if err := both.GenerateFromMultipleSources(ctx, []Source{src}); err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"mock", "other"} {
		ok, err := afero.DirExists(testFS, lang)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("%s schemas were not generated, so this test proves nothing", lang)
		}
	}

	// The same project with "other" removed from spec.schemas.languages.
	one := New(testFS, []generator.Interface{&indexingGenerator{}}, nil)
	if err := one.GenerateFromMultipleSources(ctx, []Source{src}); err != nil {
		t.Fatal(err)
	}

	orphaned, err := afero.DirExists(testFS, "other")
	if err != nil {
		t.Fatal(err)
	}
	if orphaned {
		t.Error("schemas for the removed language are still on disk")
	}

	// The language the project still generates for is intact.
	if got, want := readMockIndex(t, testFS), "a.yaml"; got != want {
		t.Errorf("index for the remaining language = %q, want %q", got, want)
	}

	l, err := one.currentLock()
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"mock"}, l.Languages); diff != "" {
		t.Errorf("recorded languages (-want +got):\n%s", diff)
	}
}
