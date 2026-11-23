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

package xpkg

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/spf13/afero"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"

	"github.com/crossplane/cli/v2/internal/terminal"
	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
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
		packages              int
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
		"SinglePackageResponseHeaderTimeout": {
			reason: "Pushing one package to a registry that never sends response headers should fail once the transport's header timeout expires.",
			args:   args{packages: 1, responseHeaderTimeout: 100 * time.Millisecond},
			want: want{
				errContains: "timeout awaiting response headers",
				// httptest serves HTTP/1.1, where ggcr does not retry the
				// timeout; allow for its backoff anyway.
				within: 10 * time.Second,
			},
		},
		"MultiPackageResponseHeaderTimeout": {
			reason: "Pushing several packages to a registry that never sends response headers should fail once the transport's header timeout expires.",
			args:   args{packages: 2, responseHeaderTimeout: 100 * time.Millisecond},
			want: want{
				errContains: "timeout awaiting response headers",
				within:      10 * time.Second,
			},
		},
		"SinglePackageContextDeadline": {
			reason: "A deadline on Run's context should end a stalled single-package push before the transport's header timeout.",
			args:   args{packages: 1, responseHeaderTimeout: time.Hour, timeout: 200 * time.Millisecond},
			want:   want{errIs: context.DeadlineExceeded, within: 2 * time.Second},
		},
		"MultiPackageContextDeadline": {
			reason: "A deadline on Run's context should end a stalled multi-package push before the transport's header timeout.",
			args:   args{packages: 2, responseHeaderTimeout: time.Hour, timeout: 200 * time.Millisecond},
			want:   want{errIs: context.DeadlineExceeded, within: 2 * time.Second},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			files := make([]string, tc.args.packages)
			for i := range files {
				files[i] = writePackage(t)
			}

			c := &pushCmd{
				Package:               stalledRegistry(t) + "/pkg:v1",
				PackageFiles:          files,
				fs:                    afero.NewOsFs(),
				responseHeaderTimeout: tc.args.responseHeaderTimeout,
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

// writePackage writes a random single-layer image to a tarball and returns
// its path.
func writePackage(t *testing.T) string {
	t.Helper()
	img, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pkg.xpkg")
	if err := tarball.WriteToFile(path, name.MustParseReference("example.com/pkg:v1"), img); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPushImagesDefaultTransport swaps defaultPushTransport, so it can't run
// in parallel with other tests that push with no options.
func TestPushImagesDefaultTransport(t *testing.T) { //nolint:paralleltest // Mutates defaultPushTransport.
	type args struct {
		responseHeaderTimeout time.Duration
	}
	type want struct {
		errContains string
		within      time.Duration
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ResponseHeaderTimeout": {
			reason: "pushImages with no options (xpkg batch) should push with defaultPushTransport, so a registry that never sends response headers fails once its header timeout expires.",
			args:   args{responseHeaderTimeout: 100 * time.Millisecond},
			want: want{
				errContains: "timeout awaiting response headers",
				within:      10 * time.Second,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			orig := defaultPushTransport
			defaultPushTransport = clixpkg.NewRegistryTransport(tc.args.responseHeaderTimeout, time.Second)
			t.Cleanup(func() { defaultPushTransport = orig })

			img, err := random.Image(64, 1)
			if err != nil {
				t.Fatal(err)
			}

			start := time.Now()
			err = pushImages(t.Context(), logging.NewNopLogger(), nil, []packageImage{{Image: img}}, stalledRegistry(t)+"/pkg:v1", nil)
			elapsed := time.Since(start)

			if err == nil || !strings.Contains(err.Error(), tc.want.errContains) {
				t.Errorf("\n%s\npushImages(...): want error containing %q, got %v", tc.reason, tc.want.errContains, err)
			}
			if elapsed > tc.want.within {
				t.Errorf("\n%s\npushImages(...): took %s, want within %s", tc.reason, elapsed, tc.want.within)
			}
		})
	}
}
