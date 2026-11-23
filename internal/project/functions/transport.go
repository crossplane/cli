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
	"fmt"
	"net/http"

	clixpkg "github.com/crossplane/cli/v2/internal/xpkg"
)

// registryTransport is the transport function builders pull runtime base
// images with. http.DefaultTransport has no response header timeout and no
// HTTP/2 health check (its 90s idle timeout only reaps unused connections), so
// a registry connection that goes silent would block a build forever.
var registryTransport http.RoundTripper = clixpkg.NewRegistryTransport(clixpkg.RegistryResponseHeaderTimeout, clixpkg.RegistrySendPingTimeout) //nolint:gochecknoglobals // One connection pool shared by every builder, like http.DefaultTransport.

// fetchContext returns a context for a registry fetch whose results include
// lazy layers. Until detach is called it is cancelled with ctx, so a cancelled
// build stops its eager fetches (ping, manifest, config). After detach it is
// never cancelled. ggcr binds this context into the lazy layers, which are read
// after Build returns, when callers have often cancelled ctx already (for
// example an errgroup's context once Wait returns). Those later reads are
// bounded by registryTransport's timeouts instead.
//
// fetchCtx is cancelled with ctx's cause, so a caller's deadline still reads
// as context.DeadlineExceeded; see fetchError.
func fetchContext(ctx context.Context) (fetchCtx context.Context, detach func()) {
	fetchCtx, cancel := context.WithCancelCause(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, func() { cancel(context.Cause(ctx)) })
	return fetchCtx, func() { stop() }
}

// fetchError adds fetchCtx's cancellation cause to err when err does not
// already carry it. Some transport paths (HTTP/2) report only ctx.Err(), which
// for a fetchContext is context.Canceled even when the caller's deadline
// expired.
func fetchError(fetchCtx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if cause := context.Cause(fetchCtx); cause != nil && !errors.Is(err, cause) {
		return fmt.Errorf("%w: %w", err, cause)
	}
	return err
}
