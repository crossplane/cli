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
	"errors"
	"os"
	"testing"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
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

			gotVersion, err := tc.source.Hash(t.Context())

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

func TestNewPinnedGitSource(t *testing.T) {
	t.Parallel()

	const sha = "abc123def456789012345678901234567890abcd"

	type args struct {
		pin string
	}
	type want struct {
		cloned  string
		version string
		err     error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Pinned": {
			reason: "A pinned source clones the commit but still reports the branch as its version.",
			args:   args{pin: sha},
			want:   want{cloned: sha, version: "main"},
		},
		"Unpinned": {
			reason: "Without a pin the dependency's ref is cloned.",
			want:   want{cloned: "refs/heads/main", version: "main"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cloner := &recordingCloner{mockCloner: mockCloner{ref: plumbing.NewHashReference("refs/heads/main", plumbing.NewHash(sha))}}
			dep := v1alpha1.Dependency{Git: &v1alpha1.GitDependency{Repository: "https://example.com/r.git", Ref: "main"}}
			src := NewPinnedGitSource(dep, tc.args.pin, cloner, &mockAuthProvider{})

			_, err := src.Hash(t.Context())
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("%s\nHash(...): -want err, +got err:\n%s", tc.reason, diff)
			}
			got := want{cloned: cloner.refName, version: src.(Pinned).Version()}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(want{}), cmpopts.EquateErrors()); diff != "" {
				t.Errorf("%s\nNewPinnedGitSource(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

// recordingCloner remembers the ref it was asked to clone.
type recordingCloner struct {
	mockCloner
	refName string
}

func (r *recordingCloner) CloneRepository(st storage.Storer, fs billy.Filesystem, auth git.AuthProvider, opts git.CloneOptions) (*plumbing.Reference, error) {
	r.refName = opts.RefName
	return r.mockCloner.CloneRepository(st, fs, auth, opts)
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

			gotID := tc.source.Key()
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

func TestSourceVersionAndHash(t *testing.T) {
	t.Parallel()

	openAPIFS := afero.NewMemMapFs()
	_ = afero.WriteFile(openAPIFS, "api__v1_openapi.json", []byte(`{"a":1}`), 0o600)

	type args struct {
		source Source
	}
	type want struct {
		key     string
		version string
		hash    string
		err     error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"XpkgHashIsDigestNotTag": {
			reason: "The lock must pin the resolved digest; the tag is only the version shown to people.",
			args:   args{source: NewXpkgSource("xpkg.upbound.io/acme/provider", "v1.2.3", "sha256:abc", nil)},
			want:   want{key: "xpkg://xpkg.upbound.io/acme/provider", version: "v1.2.3", hash: "sha256:abc"},
		},
		"K8sHashIsContentNotRelease": {
			reason: "A k8s source is hashed by its fetched OpenAPI bytes so a changed spec is noticed without a version bump.",
			args:   args{source: &k8sOpenAPISource{k8sRef: &v1alpha1.K8sDependency{Version: "1.33.0"}, fs: openAPIFS}},
			want: want{
				key:     "k8s://1.33.0",
				version: "1.33.0",
				hash:    "015abd7f5cc57a2dd94b7590f04ad8084273905ee33ec5cebeae62276a97f862",
			},
		},
		"GitRefIsBranchHashIsCommit": {
			reason: "A git source's version is the configured branch or tag; its hash is the commit.",
			args: args{source: &gitSource{
				gitRef:    &v1alpha1.GitDependency{Repository: "https://example.com/r.git", Ref: "main"},
				commitSHA: "deadbeef",
			}},
			want: want{key: "git://https://example.com/r.git", version: "main", hash: "deadbeef"},
		},
		"PinnedGitKeepsBranchAsVersion": {
			reason: "A git source cloned from a locked commit still reports the branch it was resolved from, so the lock keeps it.",
			args: args{source: &gitSource{
				gitRef:    &v1alpha1.GitDependency{Repository: "https://example.com/r.git", Ref: "main"},
				cloneRef:  "abc123def456789012345678901234567890abcd",
				commitSHA: "abc123def456789012345678901234567890abcd",
			}},
			want: want{key: "git://https://example.com/r.git", version: "main", hash: "abc123def456789012345678901234567890abcd"},
		},
		"FSHasNoVersion": {
			reason: "A filesystem source has no version.",
			args:   args{source: NewFSSource("apis", afero.NewMemMapFs())},
			want: want{
				key:  "fs://apis",
				hash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			gotVersion := ""
			if p, ok := tc.args.source.(Pinned); ok {
				gotVersion = p.Version()
			}
			gotHash, err := tc.args.source.Hash(t.Context())
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("%s\nHash(...): -want err, +got err:\n%s", tc.reason, diff)
			}
			got := want{key: tc.args.source.Key(), version: gotVersion, hash: gotHash}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(want{}), cmpopts.EquateErrors()); diff != "" {
				t.Errorf("%s\nKey(), Version(), Hash(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestFSSourceHash(t *testing.T) {
	t.Parallel()

	const (
		xrd       = "apiVersion: apiextensions.crossplane.io/v2\nkind: CompositeResourceDefinition\nmetadata:\n  name: a\n"
		xrdEdited = "apiVersion: apiextensions.crossplane.io/v2\nkind: CompositeResourceDefinition\nmetadata:\n  name: b\n"
		crd       = "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: c\n"
		other     = "apiVersion: apiextensions.crossplane.io/v1\nkind: Composition\nmetadata:\n  name: x\n"
	)

	type args struct {
		a map[string]string
		b map[string]string
	}
	type want struct {
		same bool
		err  error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"AddingCompositionIsIgnored": {
			reason: "A Composition is not an API definition, so adding one must not change the hash.",
			args: args{
				a: map[string]string{"xrd.yaml": xrd},
				b: map[string]string{"xrd.yaml": xrd, "composition.yaml": other},
			},
			want: want{same: true},
		},
		"EditingExampleAndWorkflowIsIgnored": {
			reason: "Examples, workflows and function YAML are not API definitions.",
			args: args{
				a: map[string]string{"xrd.yaml": xrd, "examples/xr.yaml": "kind: XR\nspec: {a: 1}\n", ".github/ci.yaml": "name: ci\n", "fn/fn.yaml": "kind: Function\n"},
				b: map[string]string{"xrd.yaml": xrd, "examples/xr.yaml": "kind: XR\nspec: {a: 2}\n", ".github/ci.yaml": "name: ci2\n", "fn/fn.yaml": "kind: Function\nx: y\n"},
			},
			want: want{same: true},
		},
		"NonYAMLIsIgnored": {
			reason: "Only YAML files are read.",
			args: args{
				a: map[string]string{"xrd.yaml": xrd},
				b: map[string]string{"xrd.yaml": xrd, "README.md": xrd},
			},
			want: want{same: true},
		},
		"EditingXRDChangesHash": {
			reason: "Editing an XRD must change the hash.",
			args: args{
				a: map[string]string{"xrd.yaml": xrd},
				b: map[string]string{"xrd.yaml": xrdEdited},
			},
			want: want{same: false},
		},
		"CRDCountsAnyGroup": {
			reason: "A CRD counts regardless of its apiVersion group.",
			args: args{
				a: map[string]string{"crd.yaml": crd},
				b: map[string]string{},
			},
			want: want{same: false},
		},
		"RenamingFileChangesHash": {
			reason: "Renaming a file with an XRD must change the hash.",
			args: args{
				a: map[string]string{"a.yaml": xrd},
				b: map[string]string{"b.yaml": xrd},
			},
			want: want{same: false},
		},
		"SplittingFileChangesHash": {
			reason: "Splitting one multi-document file into two files must change the hash.",
			args: args{
				a: map[string]string{"all.yaml": xrd + "---\n" + crd},
				b: map[string]string{"xrd.yaml": xrd, "crd.yaml": crd},
			},
			want: want{same: false},
		},
		"SameContentSameHash": {
			reason: "The hash is deterministic.",
			args: args{
				a: map[string]string{"b.yaml": crd, "a.yaml": xrd},
				b: map[string]string{"a.yaml": xrd, "b.yaml": crd},
			},
			want: want{same: true},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			hash := func(files map[string]string) (string, error) {
				fs := afero.NewMemMapFs()
				for p, c := range files {
					_ = afero.WriteFile(fs, p, []byte(c), 0o600)
				}
				return NewFSSource("apis", fs).Hash(t.Context())
			}

			ha, errA := hash(tc.args.a)
			hb, errB := hash(tc.args.b)
			err := errors.Join(errA, errB)
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("%s\nHash(...): -want err, +got err:\n%s", tc.reason, diff)
			}
			got := want{same: ha == hb}
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(want{}), cmpopts.EquateErrors()); diff != "" {
				t.Errorf("%s\nHash(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}

func TestFSSourceResources(t *testing.T) {
	t.Parallel()

	const (
		xrd   = "apiVersion: apiextensions.crossplane.io/v2\nkind: CompositeResourceDefinition\nmetadata:\n  name: a\n"
		crd   = "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: c\n"
		other = "apiVersion: apiextensions.crossplane.io/v1\nkind: Composition\nmetadata:\n  name: x\n"
	)

	type args struct {
		files map[string]string
	}
	type want struct {
		files map[string]string
		err   error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"DropsOtherDocuments": {
			reason: "Only XRDs and CRDs are returned; files without any are omitted.",
			args: args{files: map[string]string{
				"xrd.yaml":         xrd,
				"composition.yaml": other,
				"README.md":        "hi",
				".github/ci.yaml":  "name: ci\n",
			}},
			want: want{files: map[string]string{"xrd.yaml": xrd}},
		},
		"KeepsMultiDocumentFiles": {
			reason: "Definitions of a multi-document file stay together, other documents are dropped.",
			args: args{files: map[string]string{
				"sub/all.yml": xrd + "---\n" + other + "---\n" + crd,
			}},
			want: want{files: map[string]string{"sub/all.yml": xrd + "---\n" + crd}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fs := afero.NewMemMapFs()
			for p, c := range tc.args.files {
				_ = afero.WriteFile(fs, p, []byte(c), 0o600)
			}
			out, err := NewFSSource("apis", fs).Resources(t.Context())
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("%s\nResources(...): -want err, +got err:\n%s", tc.reason, diff)
			}

			got := want{files: map[string]string{}}
			_ = afero.Walk(out, ".", func(p string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return err
				}
				b, _ := afero.ReadFile(out, p)
				got.files[p] = string(b)
				return nil
			})
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(want{})); diff != "" {
				t.Errorf("%s\nResources(...): -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
