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

package functions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestBuildStalledRegistry(t *testing.T) {
	t.Parallel()

	type args struct {
		transport http.RoundTripper
		timeout   time.Duration // Build's ctx deadline; 0 means none.
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
			reason: "A registry that never sends response headers should fail the fetch once the transport's header timeout expires, even with no deadline on the context.",
			args: args{
				transport: newRegistryTransport(100*time.Millisecond, time.Second),
			},
			want: want{
				errContains: "timeout awaiting response headers",
				// httptest serves HTTP/1.1, where ggcr does not retry the
				// timeout; allow for its 1s and 3s backoff anyway.
				within: 10 * time.Second,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			b := &goTemplatingBuilder{
				baseImage:   stalledRegistry(t) + "/fn:v1",
				transport:   tc.args.transport,
				configStore: clixpkg.NewStaticImageConfigStore(nil),
			}

			ctx := t.Context()
			if tc.args.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.args.timeout)
				defer cancel()
			}

			start := time.Now()
			_, err := b.Build(ctx, BuildContext{
				ProjectFS:     nil,
				Architectures: []string{"amd64"},
			})
			elapsed := time.Since(start)

			if err == nil || !strings.Contains(err.Error(), tc.want.errContains) {
				t.Errorf("\n%s\nBuild(...): want error containing %q, got %v", tc.reason, tc.want.errContains, err)
			}
			if elapsed > tc.want.within {
				t.Errorf("\n%s\nBuild(...): returned after %s, want within %s", tc.reason, elapsed, tc.want.within)
			}
		})
	}
}

func TestBuildersUseRegistryTransport(t *testing.T) {
	t.Parallel()

	type args struct {
		transport http.RoundTripper
	}
	cases := map[string]struct {
		reason string
		args   args
	}{
		"GoTemplating": {
			reason: "The go-templating builder should pull base images with registryTransport.",
			args:   args{transport: newGoTemplatingBuilder(nil).transport},
		},
		"KCL": {
			reason: "The KCL builder should pull base images with registryTransport.",
			args:   args{transport: newKCLBuilder(nil).transport},
		},
		"Python": {
			reason: "The Python builder should pull base images with registryTransport.",
			args:   args{transport: newPythonBuilder(nil).transport},
		},
		"Rust": {
			reason: "The Rust builder should pull base images with registryTransport.",
			args:   args{transport: newRustBuilder(nil).transport},
		},
		"Go": {
			reason: "The Go builder should pull base images with registryTransport.",
			args:   args{transport: newGoBuilder(nil).transport},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if tc.args.transport != registryTransport {
				t.Errorf("\n%s\ntransport: want registryTransport, got %T %p", tc.reason, tc.args.transport, tc.args.transport)
			}
		})
	}
}
