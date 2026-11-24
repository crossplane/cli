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
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/project"
	"github.com/crossplane/cli/v2/internal/terminal"
)

// stalledRegistry returns the host of a server that accepts every request and
// never writes response headers, like a registry connection gone silent.
func stalledRegistry(t *testing.T) string {
	t.Helper()
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-stop:
		case <-r.Context().Done():
		}
	}))
	// Cleanups run last-in first-out: unblock the handlers before Close waits
	// for them.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(stop) })
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestPushStalledRegistry(t *testing.T) {
	t.Parallel()

	type args struct {
		responseHeaderTimeout time.Duration
		timeout               time.Duration // Run's ctx deadline; 0 means none.
	}
	type want struct {
		errContains string
		errIs       error
		within      time.Duration
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ResponseHeaderTimeout": {
			reason: "Pushing a project to a registry that never sends response headers should fail once the transport's header timeout expires.",
			args:   args{responseHeaderTimeout: 100 * time.Millisecond},
			want: want{
				errContains: "timeout awaiting response headers",
				// httptest serves HTTP/1.1, where ggcr does not retry the
				// timeout; allow for its backoff anyway.
				within: 10 * time.Second,
			},
		},
		"ContextDeadline": {
			reason: "A deadline on Run's context should end a stalled push before the transport's header timeout.",
			args:   args{responseHeaderTimeout: time.Hour, timeout: 200 * time.Millisecond},
			want:   want{errIs: context.DeadlineExceeded, within: 2 * time.Second},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			repo := stalledRegistry(t) + "/it-project"
			dir := t.TempDir()
			projectFile := filepath.Join(dir, "crossplane-project.yaml")
			proj := fmt.Sprintf("apiVersion: dev.crossplane.io/v1alpha1\nkind: Project\nmetadata:\n  name: it-project\nspec:\n  repository: %s\n", repo)
			if err := os.WriteFile(projectFile, []byte(proj), 0o600); err != nil {
				t.Fatal(err)
			}
			pkgFile := filepath.Join(dir, "it-project.xpkg")
			writeConfigurationPackage(t, pkgFile, repo)

			c := &PushCmd{
				ProjectFile:           projectFile,
				PackageFile:           pkgFile,
				MaxConcurrency:        1,
				responseHeaderTimeout: tc.args.responseHeaderTimeout,
			}
			if err := c.AfterApply(); err != nil {
				t.Fatalf("AfterApply(): %v", err)
			}

			ctx := t.Context()
			if tc.args.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.args.timeout)
				defer cancel()
			}

			start := time.Now()
			err := c.Run(ctx, logging.NewNopLogger(), terminal.NewNopSpinnerPrinter())
			elapsed := time.Since(start)

			if err == nil || !strings.Contains(err.Error(), tc.want.errContains) {
				t.Errorf("\n%s\nRun(...): want error containing %q, got %v", tc.reason, tc.want.errContains, err)
			}
			if tc.want.errIs != nil && !errors.Is(err, tc.want.errIs) {
				t.Errorf("\n%s\nRun(...): want errors.Is(err, %v), got %v", tc.reason, tc.want.errIs, err)
			}
			if elapsed > tc.want.within {
				t.Errorf("\n%s\nRun(...): took %s, want within %s", tc.reason, elapsed, tc.want.within)
			}
		})
	}
}

// writeConfigurationPackage writes a random image tagged as repo's
// configuration package to path.
func writeConfigurationPackage(t *testing.T, path, repo string) {
	t.Helper()
	img, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := name.NewTag(repo + ":configuration")
	if err != nil {
		t.Fatal(err)
	}
	if err := tarball.WriteToFile(path, tag, img); err != nil {
		t.Fatal(err)
	}
}

func TestPushCmdRun(t *testing.T) {
	// Use an empty Docker config so the default keychain doesn't consult the
	// host's credential helpers.
	t.Setenv("DOCKER_CONFIG", t.TempDir())

	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	// go-containerregistry talks plain HTTP to loopback registries.
	host := strings.TrimPrefix(srv.URL, "http://")

	type args struct {
		// tags written into the package file, relative to the repository.
		tags        []string
		repository  string
		packageFile string
		tag         string
	}
	type want struct {
		// refs that must exist in the registry after the push.
		refs []string
		err  string
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ConfigurationAndFunction": {
			reason: "The configuration and its embedded function should be pushed from <output-dir>/<name>.xpkg under the requested tag.",
			args: args{
				tags: []string{"REPO:" + project.ConfigurationTag, "REPO_fn:amd64"},
				tag:  "v1.0.0",
			},
			want: want{refs: []string{"REPO:v1.0.0", "REPO_fn:v1.0.0"}},
		},
		"RepositoryOverride": {
			reason: "--repository should replace the project's repository, and an explicit package file path should be read.",
			args: args{
				tags:        []string{"REPO:" + project.ConfigurationTag},
				repository:  "REPO",
				packageFile: "custom.xpkg",
				tag:         "v2.0.0",
			},
			want: want{refs: []string{"REPO:v2.0.0"}},
		},
		"MissingConfiguration": {
			reason: "A package file without a configuration image should fail to push.",
			args:   args{tags: []string{"REPO_fn:amd64"}, tag: "v1.0.0"},
			want:   want{err: "failed to find configuration image"},
		},
		"InvalidRepository": {
			reason: "An invalid --repository should be rejected.",
			args:   args{tags: []string{"REPO:" + project.ConfigurationTag}, repository: "Not A Repo"},
			want:   want{err: "failed to parse repository"},
		},
		"MissingPackageFile": {
			reason: "A missing package file should fail to load.",
			want:   want{err: "failed to read package file manifest"},
		},
	}

	for caseName, tc := range cases {
		t.Run(caseName, func(t *testing.T) {
			repo := host + "/unittest/" + strings.ToLower(caseName)
			expand := func(s string) string { return strings.ReplaceAll(s, "REPO", repo) }

			dir := t.TempDir()
			projRepo := repo
			if tc.args.repository != "" {
				// The project file points elsewhere; the flag must win.
				projRepo = "xpkg.example.org/wrong/repo"
			}
			if err := os.WriteFile(filepath.Join(dir, "crossplane-project.yaml"), fmt.Appendf(nil, `apiVersion: dev.crossplane.io/v1alpha1
kind: Project
metadata:
  name: demo
spec:
  repository: %s
`, projRepo), 0o600); err != nil {
				t.Fatal(err)
			}

			pkgPath := filepath.Join(dir, "_output", "demo.xpkg")
			if tc.args.packageFile != "" {
				pkgPath = filepath.Join(dir, tc.args.packageFile)
			}
			if len(tc.args.tags) > 0 {
				refs := make(map[name.Reference]v1.Image, len(tc.args.tags))
				for _, s := range tc.args.tags {
					img, err := random.Image(64, 1)
					if err != nil {
						t.Fatal(err)
					}
					ref, err := name.ParseReference(expand(s))
					if err != nil {
						t.Fatal(err)
					}
					refs[ref] = img
				}
				if err := os.MkdirAll(filepath.Dir(pkgPath), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := tarball.MultiRefWriteToFile(pkgPath, refs); err != nil {
					t.Fatal(err)
				}
			}

			c := &PushCmd{
				ProjectFile:    filepath.Join(dir, "crossplane-project.yaml"),
				OutputDir:      "_output",
				Tag:            tc.args.tag,
				MaxConcurrency: 2,
			}
			if tc.args.repository != "" {
				c.Repository = expand(tc.args.repository)
			}
			if tc.args.packageFile != "" {
				c.PackageFile = pkgPath
			}
			if err := c.AfterApply(); err != nil {
				t.Fatal(err)
			}

			err := c.Run(t.Context(), logging.NewNopLogger(), terminal.NewNopSpinnerPrinter())
			if tc.want.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want.err) {
					t.Fatalf("\n%s\nRun(...): want error containing %q, got %v", tc.reason, tc.want.err, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("\n%s\nRun(...): %v", tc.reason, err)
			}

			var got []string
			for _, r := range tc.want.refs {
				ref, err := name.ParseReference(expand(r))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := remote.Head(ref); err == nil {
					got = append(got, r)
				}
			}
			if diff := cmp.Diff(tc.want.refs, got); diff != "" {
				t.Errorf("\n%s\nRun(...): refs in registry -want, +got:\n%s", tc.reason, diff)
			}
		})
	}
}
