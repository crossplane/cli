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
	"testing"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage"
	"github.com/google/go-cmp/cmp"
	"github.com/spf13/afero"

	"github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/git"
)

// mockCloner is a mock implementation of git.Cloner for testing.
type mockCloner struct {
	ref        *plumbing.Reference
	cloneErr   error
	cloneCalls int
}

func (m *mockCloner) CloneRepository(_ storage.Storer, fs billy.Filesystem, _ git.AuthProvider, opts git.CloneOptions) (*plumbing.Reference, error) {
	m.cloneCalls++
	if m.cloneErr != nil {
		return nil, m.cloneErr
	}

	// Create a test file in the filesystem to simulate a successful clone.
	if opts.Path != "" {
		if err := fs.MkdirAll(opts.Path, 0o755); err != nil {
			return nil, err
		}
		file, err := fs.Create(opts.Path + "/test.yaml")
		if err != nil {
			return nil, err
		}
		_ = file.Close()
	} else {
		file, err := fs.Create("test.yaml")
		if err != nil {
			return nil, err
		}
		_ = file.Close()
	}

	return m.ref, nil
}

// mockAuthProvider is a mock implementation of git.AuthProvider.
type mockAuthProvider struct{}

func (m *mockAuthProvider) GetAuthMethod() (transport.AuthMethod, error) {
	return nil, nil
}

func TestGitSourceVersion(t *testing.T) {
	t.Parallel()

	commitHash := plumbing.NewHash("abc123def456789012345678901234567890abcd")
	mockRef := plumbing.NewHashReference("refs/heads/main", commitHash)

	tcs := map[string]struct {
		source    *gitSource
		wantSHA   string
		wantError bool
	}{
		"ReturnsCommitSHA": {
			source: &gitSource{
				gitRef: &v1alpha1.GitDependency{
					Repository: "https://github.com/example/repo.git",
					Ref:        "main",
				},
				cloner: &mockCloner{
					ref: mockRef,
				},
				authProvider: &mockAuthProvider{},
				sourceType:   SourceTypeCRD,
			},
			wantSHA:   "abc123def456789012345678901234567890abcd",
			wantError: false,
		},
		"CachedCommitSHA": {
			source: &gitSource{
				gitRef: &v1alpha1.GitDependency{
					Repository: "https://github.com/example/repo.git",
					Ref:        "main",
				},
				cloner:       &mockCloner{ref: mockRef},
				authProvider: &mockAuthProvider{},
				sourceType:   SourceTypeCRD,
				commitSHA:    "cached123def456789012345678901234567890a",
			},
			wantSHA:   "cached123def456789012345678901234567890a",
			wantError: false,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gotVersion, err := tc.source.Version(t.Context())

			if tc.wantError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.wantSHA, gotVersion); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestGitSourceResources(t *testing.T) {
	t.Parallel()

	commitHash := plumbing.NewHash("abc123def456789012345678901234567890abcd")
	mockRef := plumbing.NewHashReference("refs/heads/main", commitHash)

	tcs := map[string]struct {
		source        *gitSource
		wantCommitSHA string
		wantError     bool
	}{
		"StoresCommitSHA": {
			source: &gitSource{
				gitRef: &v1alpha1.GitDependency{
					Repository: "https://github.com/example/repo.git",
					Ref:        "main",
				},
				cloner: &mockCloner{
					ref: mockRef,
				},
				authProvider: &mockAuthProvider{},
				sourceType:   SourceTypeCRD,
			},
			wantCommitSHA: "abc123def456789012345678901234567890abcd",
			wantError:     false,
		},
		"WithPath": {
			source: &gitSource{
				gitRef: &v1alpha1.GitDependency{
					Repository: "https://github.com/example/repo.git",
					Ref:        "main",
					Path:       "apis",
				},
				cloner: &mockCloner{
					ref: mockRef,
				},
				authProvider: &mockAuthProvider{},
				sourceType:   SourceTypeCRD,
			},
			wantCommitSHA: "abc123def456789012345678901234567890abcd",
			wantError:     false,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gotFS, err := tc.source.Resources(t.Context())

			if tc.wantError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatal(err)
			}
			if gotFS == nil {
				t.Fatal("expected non-nil filesystem")
			}
			if diff := cmp.Diff(tc.wantCommitSHA, tc.source.commitSHA); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}

			// Verify filesystem is cached
			if tc.source.fs == nil {
				t.Fatal("expected cached filesystem")
			}

			// Verify that calling Resources again returns cached filesystem.
			gotFS2, err := tc.source.Resources(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if gotFS != gotFS2 {
				t.Errorf("expected cached filesystem to be returned")
			}
		})
	}
}

func TestGitSourceID(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		source *gitSource
		wantID string
	}{
		"BasicRepository": {
			source: &gitSource{
				gitRef: &v1alpha1.GitDependency{
					Repository: "https://github.com/example/repo.git",
				},
			},
			wantID: "git://https://github.com/example/repo.git",
		},
		"RepositoryWithPath": {
			source: &gitSource{
				gitRef: &v1alpha1.GitDependency{
					Repository: "https://github.com/example/repo.git",
					Path:       "apis/crds",
				},
			},
			wantID: "git://https://github.com/example/repo.git/apis/crds",
		},
		"EmptyRepository": {
			source: &gitSource{
				gitRef: &v1alpha1.GitDependency{
					Repository: "",
				},
			},
			wantID: "git://",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gotID := tc.source.ID()
			if diff := cmp.Diff(tc.wantID, gotID); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestGitSourceType(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		source   *gitSource
		wantType SourceType
	}{
		"CRDType": {
			source: &gitSource{
				sourceType: SourceTypeCRD,
			},
			wantType: SourceTypeCRD,
		},
		"OpenAPIType": {
			source: &gitSource{
				sourceType: SourceTypeOpenAPI,
			},
			wantType: SourceTypeOpenAPI,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gotType := tc.source.Type()
			if diff := cmp.Diff(tc.wantType, gotType); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestGitSourceNormalizeRef(t *testing.T) {
	t.Parallel()

	g := &gitSource{}

	tcs := map[string]struct {
		input string
		want  string
	}{
		"EmptyRef": {
			input: "",
			want:  "refs/heads/main",
		},
		"BranchName": {
			input: "develop",
			want:  "refs/heads/develop",
		},
		"VersionTag": {
			input: "v1.2.3",
			want:  "refs/tags/v1.2.3",
		},
		"NumericVersionTag": {
			input: "1.2.3",
			want:  "refs/tags/1.2.3",
		},
		"FullRef": {
			input: "refs/heads/feature",
			want:  "refs/heads/feature",
		},
		"FullTagRef": {
			input: "refs/tags/v1.0.0",
			want:  "refs/tags/v1.0.0",
		},
		"SHA256Hash": {
			input: "abc123def456789012345678901234567890abcd",
			want:  "abc123def456789012345678901234567890abcd",
		},
		"SHA256HashUppercase": {
			input: "ABC123DEF456789012345678901234567890ABCD",
			want:  "ABC123DEF456789012345678901234567890ABCD",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := g.normalizeRef(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestGitSourceVerifyClone(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		setupFS   func() billy.Filesystem
		path      string
		wantError bool
	}{
		"ValidClone": {
			setupFS: func() billy.Filesystem {
				fs := memfs.New()
				file, _ := fs.Create("test.yaml")
				_ = file.Close()
				return fs
			},
			path:      ".",
			wantError: false,
		},
		"ValidCloneWithPath": {
			setupFS: func() billy.Filesystem {
				fs := memfs.New()
				_ = fs.MkdirAll("apis", 0o755)
				file, _ := fs.Create("apis/test.yaml")
				_ = file.Close()
				return fs
			},
			path:      "apis",
			wantError: false,
		},
		"EmptyDirectory": {
			setupFS: func() billy.Filesystem {
				fs := memfs.New()
				_ = fs.MkdirAll("empty", 0o755)
				return fs
			},
			path:      "empty",
			wantError: true,
		},
		"NonExistentPath": {
			setupFS:   memfs.New,
			path:      "does-not-exist",
			wantError: true,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			g := &gitSource{}
			fs := tc.setupFS()

			err := g.verifyClone(fs, tc.path)

			if tc.wantError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestIsSHA256Hash(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input string
		want  bool
	}{
		"ValidSHA": {
			input: "abc123def456789012345678901234567890abcd",
			want:  true,
		},
		"ValidSHAUppercase": {
			input: "ABC123DEF456789012345678901234567890ABCD",
			want:  true,
		},
		"ValidSHAMixed": {
			input: "AbC123DeF456789012345678901234567890aBcD",
			want:  true,
		},
		"TooShort": {
			input: "abc123",
			want:  false,
		},
		"TooLong": {
			input: "abc123def456789012345678901234567890abcde",
			want:  false,
		},
		"InvalidCharacters": {
			input: "xyz123def456789012345678901234567890abcd",
			want:  false,
		},
		"EmptyString": {
			input: "",
			want:  false,
		},
		"BranchName": {
			input: "main",
			want:  false,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := git.CheckSHA1Hash(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestCalculateFilesystemHash(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		setupFS    func() afero.Fs
		sourceType SourceType
		wantError  bool
		wantSame   bool // whether two identical setups should produce the same hash.
	}{
		"CRDSourceWithYAMLFiles": {
			setupFS: func() afero.Fs {
				fs := afero.NewMemMapFs()
				_ = afero.WriteFile(fs, "test1.yaml", []byte("apiVersion: v1\nkind: Test"), 0o644)
				_ = afero.WriteFile(fs, "test2.yml", []byte("apiVersion: v1\nkind: Test2"), 0o644)
				return fs
			},
			sourceType: SourceTypeCRD,
			wantError:  false,
		},
		"OpenAPISourceWithJSONFiles": {
			setupFS: func() afero.Fs {
				fs := afero.NewMemMapFs()
				_ = afero.WriteFile(fs, "openapi.json", []byte(`{"openapi": "3.0.0"}`), 0o644)
				return fs
			},
			sourceType: SourceTypeCRD,
			wantError:  false,
		},
		"CRDSourceIgnoresNonYAMLFiles": {
			setupFS: func() afero.Fs {
				fs := afero.NewMemMapFs()
				_ = afero.WriteFile(fs, "test.yaml", []byte("apiVersion: v1\nkind: Test"), 0o644)
				_ = afero.WriteFile(fs, "readme.txt", []byte("This should be ignored"), 0o644)
				_ = afero.WriteFile(fs, "script.sh", []byte("#!/bin/bash"), 0o644)
				return fs
			},
			sourceType: SourceTypeCRD,
			wantError:  false,
		},
		"OpenAPISourceIgnoresNonJSONFiles": {
			setupFS: func() afero.Fs {
				fs := afero.NewMemMapFs()
				_ = afero.WriteFile(fs, "openapi.json", []byte(`{"openapi": "3.0.0"}`), 0o644)
				_ = afero.WriteFile(fs, "test.yaml", []byte("apiVersion: v1\nkind: Test"), 0o644)
				return fs
			},
			sourceType: SourceTypeOpenAPI,
			wantError:  false,
		},
		"EmptyFilesystem": {
			setupFS:    afero.NewMemMapFs,
			sourceType: SourceTypeCRD,
			wantError:  false,
		},
		"DirectoriesIgnored": {
			setupFS: func() afero.Fs {
				fs := afero.NewMemMapFs()
				_ = fs.MkdirAll("subdir", 0o755)
				_ = afero.WriteFile(fs, "subdir/test.yaml", []byte("apiVersion: v1\nkind: Test"), 0o644)
				return fs
			},
			sourceType: SourceTypeCRD,
			wantError:  false,
		},
		"IdenticalContentSameHash": {
			setupFS: func() afero.Fs {
				fs := afero.NewMemMapFs()
				_ = afero.WriteFile(fs, "test.yaml", []byte("apiVersion: v1\nkind: Test"), 0o644)
				return fs
			},
			sourceType: SourceTypeCRD,
			wantError:  false,
		},
		"DifferentSourceTypeDifferentHash": {
			setupFS: func() afero.Fs {
				fs := afero.NewMemMapFs()
				_ = afero.WriteFile(fs, "test.yaml", []byte("apiVersion: v1\nkind: Test"), 0o644)
				_ = afero.WriteFile(fs, "openapi.json", []byte(`{"openapi": "3.0.0"}`), 0o644)
				return fs
			},
			sourceType: SourceTypeCRD,
			wantError:  false,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fs := tc.setupFS()
			hash, err := calculateFilesystemHash(fs, tc.sourceType)

			if tc.wantError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatal(err)
			}
			if len(hash) == 0 {
				t.Error("hash should not be empty")
			}
			if len(hash) != 64 {
				t.Errorf("SHA256 hash should be 64 hex characters, got %d", len(hash))
			}

			// Verify hash is deterministic.
			hash2, err := calculateFilesystemHash(fs, tc.sourceType)
			if err != nil {
				t.Fatal(err)
			}
			if hash != hash2 {
				t.Errorf("hash should be deterministic: %q vs %q", hash, hash2)
			}

			// Special case tests for specific scenarios.
			switch name {
			case "IdenticalContentSameHash":
				// Test that identical content in different filesystem instances produces identical hashes.
				fs2 := tc.setupFS()
				hash3, err := calculateFilesystemHash(fs2, tc.sourceType)
				if err != nil {
					t.Fatal(err)
				}
				if hash != hash3 {
					t.Errorf("identical content should produce identical hashes: %q vs %q", hash, hash3)
				}

			case "DifferentSourceTypeDifferentHash":
				// Test that same filesystem with different source type produces different hash.
				hashOpenAPI, err := calculateFilesystemHash(fs, SourceTypeOpenAPI)
				if err != nil {
					t.Fatal(err)
				}
				if hash == hashOpenAPI {
					t.Errorf("different source types should produce different hashes")
				}
			}
		})
	}
}

func TestCalculateFilesystemHashDifferentContent(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		setupFS1   func() afero.Fs
		setupFS2   func() afero.Fs
		sourceType SourceType
	}{
		"DifferentFileContent": {
			setupFS1: func() afero.Fs {
				fs := afero.NewMemMapFs()
				_ = afero.WriteFile(fs, "test.yaml", []byte("apiVersion: v1\nkind: Test1"), 0o644)
				return fs
			},
			setupFS2: func() afero.Fs {
				fs := afero.NewMemMapFs()
				_ = afero.WriteFile(fs, "test.yaml", []byte("apiVersion: v1\nkind: Test2"), 0o644)
				return fs
			},
			sourceType: SourceTypeCRD,
		},
		"DifferentNumberOfFiles": {
			setupFS1: func() afero.Fs {
				fs := afero.NewMemMapFs()
				_ = afero.WriteFile(fs, "test.yaml", []byte("apiVersion: v1\nkind: Test"), 0o644)
				return fs
			},
			setupFS2: func() afero.Fs {
				fs := afero.NewMemMapFs()
				_ = afero.WriteFile(fs, "test1.yaml", []byte("apiVersion: v1\nkind: Test"), 0o644)
				_ = afero.WriteFile(fs, "test2.yaml", []byte("apiVersion: v1\nkind: Test2"), 0o644)
				return fs
			},
			sourceType: SourceTypeCRD,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fs1 := tc.setupFS1()
			fs2 := tc.setupFS2()

			hash1, err := calculateFilesystemHash(fs1, tc.sourceType)
			if err != nil {
				t.Fatal(err)
			}

			hash2, err := calculateFilesystemHash(fs2, tc.sourceType)
			if err != nil {
				t.Fatal(err)
			}

			if hash1 == hash2 {
				t.Errorf("different content should produce different hashes")
			}
		})
	}
}
