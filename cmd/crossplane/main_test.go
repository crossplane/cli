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

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/config"
	"github.com/crossplane/cli/v2/internal/terminal"
)

func TestConfigFlag(t *testing.T) {
	type want struct {
		path string
		err  error
	}

	cases := map[string]struct {
		reason string
		args   []string
		want   want
	}{
		"Empty": {
			reason: "No args should yield no path and no error.",
			args:   nil,
		},
		"NoConfigFlag": {
			reason: "Argv without --config should yield no path and no error.",
			args:   []string{"version"},
		},
		"EqualsForm": {
			reason: "--config=PATH should return PATH.",
			args:   []string{"--config=/tmp/config.yaml"},
			want:   want{path: "/tmp/config.yaml"},
		},
		"SpaceForm": {
			reason: "--config PATH should return PATH from the next argv.",
			args:   []string{"--config", "/tmp/config.yaml"},
			want:   want{path: "/tmp/config.yaml"},
		},
		"EmptyEquals": {
			reason: "--config= is an explicitly empty value and should return an error.",
			args:   []string{"--config="},
			want:   want{err: cmpopts.AnyError},
		},
		"EmptySpaceValue": {
			reason: "--config followed by an empty argument should return an error, as --config= does.",
			args:   []string{"--config", ""},
			want:   want{err: cmpopts.AnyError},
		},
		"MissingValue": {
			reason: "Trailing --config with no following argv should return an error.",
			args:   []string{"--config"},
			want:   want{err: cmpopts.AnyError},
		},
		"FirstWins": {
			reason: "If --config appears more than once, the first occurrence wins.",
			args:   []string{"--config=/first.yaml", "--config=/second.yaml"},
			want:   want{path: "/first.yaml"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := configFlag(tc.args)
			if diff := cmp.Diff(tc.want.err, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nconfigFlag(%v): -want err, +got err:\n%s", tc.reason, tc.args, diff)
			}
			if diff := cmp.Diff(tc.want.path, got); diff != "" {
				t.Errorf("\n%s\nconfigFlag(%v): -want, +got:\n%s", tc.reason, tc.args, diff)
			}
		})
	}
}

func TestGitHTTPSAuthFlags(t *testing.T) {
	t.Setenv("CROSSPLANE_GIT_TOKEN", "env-token")
	t.Setenv("CROSSPLANE_GIT_USERNAME", "env-user")

	projectFile := filepath.Join(t.TempDir(), "crossplane-project.yaml")
	if err := os.WriteFile(projectFile, []byte(`apiVersion: dev.crossplane.io/v1alpha1
kind: Project
metadata:
  name: test
spec:
  repository: example.com/test
`), 0o600); err != nil {
		t.Fatal(err)
	}

	type credentials func(*cli) (string, string)
	cases := map[string]struct {
		args         []string
		clearEnv     bool
		get          credentials
		wantToken    string
		wantUsername string
	}{
		"DependencyAddWithoutToken": {
			args:         []string{"dependency", "add", "https://example.com/repo.git", "--git-ref=main"},
			clearEnv:     true,
			get:          func(c *cli) (string, string) { return c.Dependency.Add.GitToken, c.Dependency.Add.GitUsername },
			wantToken:    "",
			wantUsername: "x-access-token",
		},
		"DependencyAddDefaultUsername": {
			args:         []string{"dependency", "add", "https://example.com/repo.git", "--git-ref=main", "--git-token=test-token"},
			clearEnv:     true,
			get:          func(c *cli) (string, string) { return c.Dependency.Add.GitToken, c.Dependency.Add.GitUsername },
			wantToken:    "test-token",
			wantUsername: "x-access-token",
		},
		"ProjectBuildWithoutToken": {
			args:         []string{"project", "build", "-f", projectFile},
			clearEnv:     true,
			get:          func(c *cli) (string, string) { return c.Project.Build.GitToken, c.Project.Build.GitUsername },
			wantToken:    "",
			wantUsername: "x-access-token",
		},
		"ProjectBuildDefaultUsername": {
			args:         []string{"project", "build", "-f", projectFile, "--git-token=test-token"},
			clearEnv:     true,
			get:          func(c *cli) (string, string) { return c.Project.Build.GitToken, c.Project.Build.GitUsername },
			wantToken:    "test-token",
			wantUsername: "x-access-token",
		},
		"DependencyUpdateCacheWithoutToken": {
			args:     []string{"dependency", "update-cache"},
			clearEnv: true,
			get: func(c *cli) (string, string) {
				return c.Dependency.UpdateCache.GitToken, c.Dependency.UpdateCache.GitUsername
			},
			wantToken:    "",
			wantUsername: "x-access-token",
		},
		"DependencyUpdateCacheDefaultUsername": {
			args:     []string{"dependency", "update-cache", "--git-token=test-token"},
			clearEnv: true,
			get: func(c *cli) (string, string) {
				return c.Dependency.UpdateCache.GitToken, c.Dependency.UpdateCache.GitUsername
			},
			wantToken:    "test-token",
			wantUsername: "x-access-token",
		},
		"DependencyAddFlags": {
			args: []string{"dependency", "add", "https://example.com/repo.git", "--git-ref=main", "--git-token=test-token", "--git-username=test-user"},
			get: func(c *cli) (string, string) {
				return c.Dependency.Add.GitToken, c.Dependency.Add.GitUsername
			},
			wantToken:    "test-token",
			wantUsername: "test-user",
		},
		"DependencyAddEnvironment": {
			args: []string{"dependency", "add", "https://example.com/repo.git", "--git-ref=main"},
			get: func(c *cli) (string, string) {
				return c.Dependency.Add.GitToken, c.Dependency.Add.GitUsername
			},
			wantToken:    "env-token",
			wantUsername: "env-user",
		},
		"DependencyUpdateCacheFlags": {
			args: []string{"dependency", "update-cache", "--git-token=test-token", "--git-username=test-user"},
			get: func(c *cli) (string, string) {
				return c.Dependency.UpdateCache.GitToken, c.Dependency.UpdateCache.GitUsername
			},
			wantToken:    "test-token",
			wantUsername: "test-user",
		},
		"DependencyUpdateCacheEnvironment": {
			args: []string{"dependency", "update-cache"},
			get: func(c *cli) (string, string) {
				return c.Dependency.UpdateCache.GitToken, c.Dependency.UpdateCache.GitUsername
			},
			wantToken:    "env-token",
			wantUsername: "env-user",
		},
		"ProjectBuildFlags": {
			args: []string{"project", "build", "-f", projectFile, "--git-token=test-token", "--git-username=test-user"},
			get: func(c *cli) (string, string) {
				return c.Project.Build.GitToken, c.Project.Build.GitUsername
			},
			wantToken:    "test-token",
			wantUsername: "test-user",
		},
		"ProjectBuildEnvironment": {
			args: []string{"project", "build", "-f", projectFile},
			get: func(c *cli) (string, string) {
				return c.Project.Build.GitToken, c.Project.Build.GitUsername
			},
			wantToken:    "env-token",
			wantUsername: "env-user",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.clearEnv {
				t.Setenv("CROSSPLANE_GIT_TOKEN", "")
				t.Setenv("CROSSPLANE_GIT_USERNAME", "")
				for _, key := range []string{"CROSSPLANE_GIT_TOKEN", "CROSSPLANE_GIT_USERNAME"} {
					if err := os.Unsetenv(key); err != nil {
						t.Fatal(err)
					}
				}
			}
			var c cli
			parser, err := kong.New(&c, kongVars)
			if err != nil {
				t.Fatalf("kong.New(): %v", err)
			}
			if _, err := parser.Parse(tc.args); err != nil {
				t.Fatalf("kong.Parse(%v): %v", tc.args, err)
			}

			gotToken, gotUsername := tc.get(&c)
			if diff := cmp.Diff(tc.wantToken, gotToken); diff != "" {
				t.Errorf("git token (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantUsername, gotUsername); diff != "" {
				t.Errorf("git username (-want +got):\n%s", diff)
			}
		})
	}
}

// TestGitHTTPSAuthReachesClone exercises the commands through their real managers
// and cloner. The server rejects the clone after recording its credentials.
// This test runs serially because it temporarily replaces the HTTP transports.
func TestGitHTTPSAuthReachesClone(t *testing.T) {
	for _, scheme := range []string{"https", "http"} {
		for _, command := range []string{"add", "build", "update-cache"} {
			for _, token := range []string{"test-token", ""} {
				t.Run(fmt.Sprintf("%s/%s/auth=%t", scheme, command, token != ""), func(t *testing.T) {
					t.Setenv("CROSSPLANE_GIT_TOKEN", token)
					t.Setenv("CROSSPLANE_GIT_USERNAME", "test-user")
					credentials := make(chan string, 10)
					server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						user, password, _ := r.BasicAuth()
						credentials <- user + ":" + password
						w.WriteHeader(http.StatusUnauthorized)
					}))
					if scheme == "https" {
						server.StartTLS()
					} else {
						server.Start()
					}
					defer server.Close()
					original := gitclient.Protocols[scheme]
					gitclient.InstallProtocol(scheme, githttp.NewClient(server.Client()))
					t.Cleanup(func() { gitclient.InstallProtocol(scheme, original) })
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
					args := []string{"dependency", command}
					if command == "build" {
						args = []string{"project", "build"}
					}
					if command == "add" {
						args = append(args, server.URL+"/repo.git", "--git-ref=main")
					}
					args = append(args, "--project-file="+projectFile, "--cache-dir="+filepath.Join(dir, "cache"))
					var c cli
					parser, err := kong.New(&c, kongVars)
					if err != nil {
						t.Fatal(err)
					}
					ctx, err := parser.Parse(args)
					if err != nil {
						t.Fatal(err)
					}
					ctx.BindTo(logging.NewNopLogger(), (*logging.Logger)(nil))
					ctx.BindTo(terminal.NewSpinnerPrinter(io.Discard, false), (*terminal.SpinnerPrinter)(nil))
					ctx.Bind(&config.Config{})
					err = ctx.Run()
					if err == nil {
						t.Fatal("expected unauthorized clone to fail")
					}
					if token != "" && strings.Contains(err.Error(), token) {
						t.Fatal("clone error exposes token")
					}
					want := ":"
					if token != "" {
						want = "test-user:" + token
					}
					select {
					case got := <-credentials:
						if got != want {
							t.Fatal("clone received unexpected credentials")
						}
					default:
						t.Fatalf("command did not reach git server: %v", err)
					}
				})
			}
		}
	}
}
