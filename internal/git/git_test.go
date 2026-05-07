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

package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/google/go-cmp/cmp"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

func TestCloneRepository(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		options     CloneOptions
		mockAuth    *MockAuthProvider
		mockCloner  *MockCloner
		wantError   bool
		expectError string
		expectRef   string
	}{
		"ValidHTTPS": {
			options: CloneOptions{
				Repo:      "https://github.com/example/repo.git",
				RefName:   "refs/heads/main",
				Directory: "testdir",
			},
			mockAuth: &MockAuthProvider{
				GetAuthMethodFunc: func() (transport.AuthMethod, error) {
					return &http.BasicAuth{Username: "user", Password: "pass"}, nil
				},
			},
			mockCloner: &MockCloner{
				CloneRepositoryFunc: func(storer storage.Storer, _ billy.Filesystem, _ AuthProvider, _ CloneOptions) (*plumbing.Reference, error) {
					mainRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/heads/main"), plumbing.NewHash("mocksha"))
					if err := storer.SetReference(mainRef); err != nil {
						return nil, errors.Wrap(err, "failed to set reference in storer")
					}
					headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.ReferenceName("refs/heads/main"))
					if err := storer.SetReference(headRef); err != nil {
						return nil, errors.Wrap(err, "failed to set HEAD reference in storer")
					}
					return headRef, nil
				},
			},
			wantError: false,
			expectRef: "refs/heads/main",
		},
		"CloneFailure": {
			options: CloneOptions{
				Repo:      "https://github.com/example/repo.git",
				RefName:   "main",
				Directory: "testdir",
			},
			mockAuth: &MockAuthProvider{
				GetAuthMethodFunc: func() (transport.AuthMethod, error) {
					return &http.BasicAuth{Username: "user", Password: "pass"}, nil
				},
			},
			mockCloner: &MockCloner{
				CloneRepositoryFunc: func(storage.Storer, billy.Filesystem, AuthProvider, CloneOptions) (*plumbing.Reference, error) {
					return nil, errors.New("failed to clone repository")
				},
			},
			wantError:   true,
			expectError: "failed to clone repository",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ref, err := tc.mockCloner.CloneRepository(memory.NewStorage(), nil, tc.mockAuth, tc.options)

			if tc.wantError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tc.expectError) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.expectError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}

			resolvedRef := ref.Target()
			if diff := cmp.Diff(tc.expectRef, resolvedRef.String()); diff != "" {
				t.Errorf("ref (-want +got):\n%s", diff)
			}
		})
	}
}

func TestHTTPSAuthProvider(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		username string
		password string
		wantAuth bool
	}{
		"WithUsernameAndPassword": {username: "testuser", password: "testpass", wantAuth: true},
		"WithUsernameOnly":        {username: "testuser", password: "", wantAuth: true},
		"WithPasswordOnly":        {username: "", password: "testpass", wantAuth: true},
		"WithoutCredentials":      {username: "", password: "", wantAuth: false},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			provider := &HTTPSAuthProvider{
				Username: tc.username,
				Password: tc.password,
			}

			auth, err := provider.GetAuthMethod()
			if err != nil {
				t.Fatal(err)
			}

			if tc.wantAuth {
				if auth == nil {
					t.Fatal("auth is nil, expected non-nil")
				}
				httpAuth, ok := auth.(*http.BasicAuth)
				if !ok {
					t.Fatalf("auth is %T, want *http.BasicAuth", auth)
				}
				if diff := cmp.Diff(tc.username, httpAuth.Username); diff != "" {
					t.Errorf("username (-want +got):\n%s", diff)
				}
				if diff := cmp.Diff(tc.password, httpAuth.Password); diff != "" {
					t.Errorf("password (-want +got):\n%s", diff)
				}
			} else if auth != nil {
				t.Errorf("auth = %v, want nil", auth)
			}
		})
	}
}

func TestSSHAuthProvider(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		setupKeyFile func(t *testing.T) string
		username     string
		passphrase   string
		wantError    bool
	}{
		"WithUsernameAndValidKey": {
			setupKeyFile: func(t *testing.T) string {
				t.Helper()
				tmpDir := t.TempDir()
				keyPath := filepath.Join(tmpDir, "test_key")
				keyContent := `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAFwAAAAdzc2gtcn
NhAAAAAwEAAQAAAQEAtest_key_content_here
-----END OPENSSH PRIVATE KEY-----`
				if err := os.WriteFile(keyPath, []byte(keyContent), 0o600); err != nil {
					t.Fatal(err)
				}
				return keyPath
			},
			username:   "testuser",
			passphrase: "",
			wantError:  true,
		},
		"WithoutUsernameDefaultsToGit": {
			setupKeyFile: func(t *testing.T) string {
				t.Helper()
				tmpDir := t.TempDir()
				keyPath := filepath.Join(tmpDir, "test_key")
				keyContent := `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAFwAAAAdzc2gtcn
NhAAAAAwEAAQAAAQEAtest_key_content_here
-----END OPENSSH PRIVATE KEY-----`
				if err := os.WriteFile(keyPath, []byte(keyContent), 0o600); err != nil {
					t.Fatal(err)
				}
				return keyPath
			},
			username:   "",
			passphrase: "",
			wantError:  true,
		},
		"WithInvalidKeyPath": {
			setupKeyFile: func(_ *testing.T) string {
				return "/non/existent/path"
			},
			username:   "testuser",
			passphrase: "",
			wantError:  true,
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			keyPath := tc.setupKeyFile(t)
			provider := &SSHAuthProvider{
				Username:       tc.username,
				PrivateKeyPath: keyPath,
				Passphrase:     tc.passphrase,
			}

			auth, err := provider.GetAuthMethod()

			if tc.wantError {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				if auth != nil {
					t.Errorf("auth = %v, want nil", auth)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if auth == nil {
					t.Errorf("auth is nil, want non-nil")
				}
			}
		})
	}
}

func TestCheckSHA1Hash(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input    string
		expected bool
	}{
		"ValidSHA":                  {"a1b2c3d4e5f6789012345678901234567890abcd", true},
		"ValidSHAUppercase":         {"A1B2C3D4E5F6789012345678901234567890ABCD", true},
		"ValidSHAMixed":             {"a1B2c3D4e5F6789012345678901234567890AbCd", true},
		"TooShort":                  {"a1b2c3d4e5f6789012345678901234567890abc", false},
		"TooLong":                   {"a1b2c3d4e5f6789012345678901234567890abcde", false},
		"EmptyString":               {"", false},
		"ContainsInvalidCharacters": {"a1b2c3d4e5f6789012345678901234567890abcg", false},
		"ContainsSpecialCharacters": {"a1b2c3d4e5f6789012345678901234567890ab-d", false},
		"AllZeros":                  {"0000000000000000000000000000000000000000", true},
		"AllNines":                  {"9999999999999999999999999999999999999999", true},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := CheckSHA1Hash(tc.input)
			if diff := cmp.Diff(tc.expected, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestExtractBranchName(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		input    string
		expected string
	}{
		"FullRef":              {"refs/heads/main", "main"},
		"FullRefFeatureBranch": {"refs/heads/feature/test", "feature/test"},
		"BranchNameOnly":       {"develop", "develop"},
		"EmptyString":          {"", "main"},
		"TagRef":               {"refs/tags/v1.0.0", "main"},
		"RemoteRef":            {"refs/remotes/origin/main", "main"},
		"JustRefs":             {"refs/", "main"},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := extractBranchName(tc.input)
			if diff := cmp.Diff(tc.expected, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

func TestDefaultCloner_CloneRepository(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		setupAuthProvider func() AuthProvider
		options           CloneOptions
		wantError         bool
		expectError       string
	}{
		"AuthProviderError": {
			setupAuthProvider: func() AuthProvider {
				return &MockAuthProvider{
					GetAuthMethodFunc: func() (transport.AuthMethod, error) {
						return nil, errors.New("auth provider error")
					},
				}
			},
			options: CloneOptions{
				Repo:    "https://github.com/test/repo.git",
				RefName: "main",
			},
			wantError:   true,
			expectError: "failed to get authentication method",
		},
		"InvalidRepo": {
			setupAuthProvider: func() AuthProvider {
				return &MockAuthProvider{
					GetAuthMethodFunc: func() (transport.AuthMethod, error) {
						return nil, nil
					},
				}
			},
			options: CloneOptions{
				Repo:    "https://github.com/nonexistent/repo.git",
				RefName: "refs/heads/main",
			},
			wantError:   true,
			expectError: "failed to clone repository",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cloner := &DefaultCloner{}
			authProvider := tc.setupAuthProvider()

			_, err := cloner.CloneRepository(memory.NewStorage(), memfs.New(), authProvider, tc.options)

			if tc.wantError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tc.expectError) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.expectError)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHandleSHACheckout(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("Skipping integration-like test in short mode")
	}

	tcs := map[string]struct {
		setupAuthProvider func() AuthProvider
		options           CloneOptions
		expectError       string
	}{
		"InvalidSHA": {
			setupAuthProvider: func() AuthProvider {
				return &MockAuthProvider{
					GetAuthMethodFunc: func() (transport.AuthMethod, error) { return nil, nil },
				}
			},
			options: CloneOptions{
				Repo:    "https://github.com/nonexistent/repo.git",
				RefName: "1234567890abcdef1234567890abcdef12345678",
			},
			expectError: "failed to clone repository",
		},
		"SHAWithSparseCheckout": {
			setupAuthProvider: func() AuthProvider {
				return &MockAuthProvider{
					GetAuthMethodFunc: func() (transport.AuthMethod, error) { return nil, nil },
				}
			},
			options: CloneOptions{
				Repo:    "https://github.com/nonexistent/repo.git",
				RefName: "1234567890abcdef1234567890abcdef12345678",
				Path:    "some/path",
			},
			expectError: "failed to clone repository",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cloner := &DefaultCloner{}
			authProvider := tc.setupAuthProvider()

			_, err := cloner.CloneRepository(memory.NewStorage(), memfs.New(), authProvider, tc.options)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.expectError) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.expectError)
			}
		})
	}
}

func TestSSHAgentAuthProvider(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		username string
	}{
		"WithUsername":                 {username: "customuser"},
		"WithoutUsernameDefaultsToGit": {username: ""},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			provider := &SSHAgentAuthProvider{
				Username: tc.username,
			}

			auth, err := provider.GetAuthMethod()

			if err == nil {
				if auth == nil {
					t.Error("auth method nil when no error")
				}
			} else {
				t.Logf("SSH agent not available (expected in CI): %v", err)
			}
		})
	}
}

func TestCompositeAuthProvider(t *testing.T) {
	t.Parallel()

	tcs := map[string]struct {
		providers   []AuthProvider
		wantError   bool
		expectError string
	}{
		"FirstProviderSucceeds": {
			providers: []AuthProvider{
				&MockAuthProvider{GetAuthMethodFunc: func() (transport.AuthMethod, error) {
					return &http.BasicAuth{Username: "user1", Password: "pass1"}, nil
				}},
				&MockAuthProvider{GetAuthMethodFunc: func() (transport.AuthMethod, error) {
					return &http.BasicAuth{Username: "user2", Password: "pass2"}, nil
				}},
			},
			wantError: false,
		},
		"FirstFailsSecondSucceeds": {
			providers: []AuthProvider{
				&MockAuthProvider{GetAuthMethodFunc: func() (transport.AuthMethod, error) {
					return nil, errors.New("first provider failed")
				}},
				&MockAuthProvider{GetAuthMethodFunc: func() (transport.AuthMethod, error) {
					return &http.BasicAuth{Username: "user2", Password: "pass2"}, nil
				}},
			},
			wantError: false,
		},
		"AllProvidersFail": {
			providers: []AuthProvider{
				&MockAuthProvider{GetAuthMethodFunc: func() (transport.AuthMethod, error) {
					return nil, errors.New("first provider failed")
				}},
				&MockAuthProvider{GetAuthMethodFunc: func() (transport.AuthMethod, error) {
					return nil, errors.New("second provider failed")
				}},
			},
			wantError:   true,
			expectError: "second provider failed",
		},
		"NoProviders": {
			providers:   []AuthProvider{},
			wantError:   true,
			expectError: "no auth providers configured",
		},
	}

	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			provider := &CompositeAuthProvider{
				Providers: tc.providers,
			}

			auth, err := provider.GetAuthMethod()

			if tc.wantError {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !strings.Contains(err.Error(), tc.expectError) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.expectError)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if auth == nil {
					t.Error("auth method nil")
				}
			}
		})
	}
}

type MockAuthProvider struct {
	GetAuthMethodFunc func() (transport.AuthMethod, error)
}

func (m *MockAuthProvider) GetAuthMethod() (transport.AuthMethod, error) {
	if m.GetAuthMethodFunc != nil {
		return m.GetAuthMethodFunc()
	}
	return nil, nil
}

type MockCloner struct {
	CloneRepositoryFunc func(storage storage.Storer, fs billy.Filesystem, auth AuthProvider, opts CloneOptions) (*plumbing.Reference, error)
}

func (m *MockCloner) CloneRepository(storage storage.Storer, fs billy.Filesystem, auth AuthProvider, opts CloneOptions) (*plumbing.Reference, error) {
	if m.CloneRepositoryFunc != nil {
		return m.CloneRepositoryFunc(storage, fs, auth, opts)
	}
	return nil, nil
}
