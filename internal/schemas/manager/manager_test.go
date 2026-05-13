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
	id      string
	version string
}

func (s *mockSource) ID() string {
	return s.id
}

func (s *mockSource) Version(_ context.Context) (string, error) {
	return s.version, nil
}

func (s *mockSource) Resources(_ context.Context) (afero.Fs, error) {
	return nil, nil
}

func (s *mockSource) Type() SourceType {
	return SourceTypeCRD
}
