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
	"errors"
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
		errIs       error
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
				transport: clixpkg.NewRegistryTransport(100*time.Millisecond, time.Second),
			},
			want: want{
				errContains: "timeout awaiting response headers",
				// httptest serves HTTP/1.1, where ggcr does not retry the
				// timeout; allow for its 1s and 3s backoff anyway.
				within: 10 * time.Second,
			},
		},
		"ContextDeadline": {
			reason: "A deadline on Build's context should end a stalled fetch even when the transport would wait forever.",
			args: args{
				transport: http.DefaultTransport,
				timeout:   200 * time.Millisecond,
			},
			want: want{
				errIs:  context.DeadlineExceeded,
				within: 2 * time.Second,
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
			if tc.want.errIs != nil && !errors.Is(err, tc.want.errIs) {
				t.Errorf("\n%s\nBuild(...): want errors.Is(err, %v), got %v", tc.reason, tc.want.errIs, err)
			}
			if elapsed > tc.want.within {
				t.Errorf("\n%s\nBuild(...): returned after %s, want within %s", tc.reason, elapsed, tc.want.within)
			}
		})
	}
}

func TestFetchContext(t *testing.T) {
	t.Parallel()

	type args struct {
		cancelBeforeFetch  bool
		detachBeforeCancel bool
	}
	type want struct {
		cancelled bool
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"AlreadyCancelled": {
			reason: "A build whose context is already cancelled should not start its fetch.",
			args:   args{cancelBeforeFetch: true},
			want:   want{cancelled: true},
		},
		"CancelledBeforeDetach": {
			reason: "Cancelling the build's context during the eager fetch should cancel the fetch.",
			args:   args{},
			want:   want{cancelled: true},
		},
		"CancelledAfterDetach": {
			reason: "Cancelling the build's context after the fetch returns should not cancel the lazy layer reads bound to it.",
			args:   args{detachBeforeCancel: true},
			want:   want{cancelled: false},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(t.Context())
			if tc.args.cancelBeforeFetch {
				cancel()
			}
			fetchCtx, detach := fetchContext(ctx)
			if tc.args.detachBeforeCancel {
				detach()
			}
			cancel()

			if !tc.want.cancelled {
				// detach returned, so AfterFunc can no longer cancel fetchCtx.
				if err := fetchCtx.Err(); err != nil {
					t.Errorf("\n%s\nfetchCtx.Err(): want nil, got %v", tc.reason, err)
				}
				return
			}
			select {
			case <-fetchCtx.Done():
			case <-time.After(10 * time.Second):
				t.Errorf("\n%s\nfetchCtx not cancelled within 10s", tc.reason)
			}
		})
	}
}

func TestFetchError(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")

	type args struct {
		deadline bool // Whether the build's context ends by deadline rather than cancel.
		err      error
	}
	type want struct {
		is []error
	}
	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"Nil": {
			reason: "A nil error should stay nil.",
			args:   args{deadline: true, err: nil},
			want:   want{},
		},
		"HTTP2CancelOnDeadline": {
			reason: "The HTTP/2 transport reports only ctx.Err(), context.Canceled; a caller deadline should still match DeadlineExceeded.",
			args:   args{deadline: true, err: context.Canceled},
			want:   want{is: []error{context.Canceled, context.DeadlineExceeded}},
		},
		"Cancel": {
			reason: "A caller cancellation should read as context.Canceled.",
			args:   args{err: errBoom},
			want:   want{is: []error{errBoom, context.Canceled}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(t.Context())
			if tc.args.deadline {
				ctx, cancel = context.WithDeadline(t.Context(), time.Now())
			}
			fetchCtx, detach := fetchContext(ctx)
			defer detach()
			cancel()
			<-fetchCtx.Done()

			err := fetchError(fetchCtx, tc.args.err)
			if tc.want.is == nil && err != nil {
				t.Errorf("\n%s\nfetchError(...): want nil, got %v", tc.reason, err)
			}
			for _, target := range tc.want.is {
				if !errors.Is(err, target) {
					t.Errorf("\n%s\nfetchError(...): want errors.Is(err, %v), got %v", tc.reason, target, err)
				}
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
