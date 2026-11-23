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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

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
