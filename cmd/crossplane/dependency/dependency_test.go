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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/config"
	"github.com/crossplane/cli/v2/internal/terminal"
)

func TestAddGitAuth(t *testing.T) {
	testDependencyGitAuth(t, func(serverURL, projectFile, cacheDir, token string) error {
		c := &addCmd{Package: serverURL + "/repo.git", GitRef: "main", ProjectFile: projectFile, CacheDir: cacheDir, GitToken: token, GitUsername: "test-user"}
		return c.Run(logging.NewNopLogger(), terminal.NewSpinnerPrinter(io.Discard, false), &config.Config{})
	})
}

func TestUpdateCacheGitAuth(t *testing.T) {
	testDependencyGitAuth(t, func(_, projectFile, cacheDir, token string) error {
		c := &updateCacheCmd{ProjectFile: projectFile, CacheDir: cacheDir, GitToken: token, GitUsername: "test-user"}
		return c.Run(logging.NewNopLogger(), terminal.NewSpinnerPrinter(io.Discard, false), &config.Config{})
	})
}

// testDependencyGitAuth verifies that a command passes its token to a Git
// clone. It replaces the global HTTPS transport, so callers must run serially.
func testDependencyGitAuth(t *testing.T, run func(serverURL, projectFile, cacheDir, token string) error) {
	t.Helper()
	const token = "test-token"
	credentials := make(chan string, 10)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, _ := r.BasicAuth()
		credentials <- user + ":" + password
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	original := gitclient.Protocols["https"]
	gitclient.InstallProtocol("https", githttp.NewClient(server.Client()))
	t.Cleanup(func() { gitclient.InstallProtocol("https", original) })
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "apis"), 0o700); err != nil {
		t.Fatal(err)
	}
	projectFile := filepath.Join(dir, "crossplane-project.yaml")
	project := fmt.Sprintf(`apiVersion: dev.crossplane.io/v1alpha1
kind: Project
metadata:
  name: test
spec:
  repository: example.com/test
  dependencies:
    - type: crd
      git:
        repository: %s/repo.git
        ref: main
`, server.URL)
	if err := os.WriteFile(projectFile, []byte(project), 0o600); err != nil {
		t.Fatal(err)
	}

	err := run(server.URL, projectFile, filepath.Join(dir, "cache"), token)

	if err == nil {
		t.Fatal("expected unauthorized clone to fail")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatal("clone error exposes token")
	}
	select {
	case got := <-credentials:
		if got != "test-user:"+token {
			t.Fatal("clone received unexpected credentials")
		}
	default:
		t.Fatalf("command did not reach git server: %v", err)
	}
}
