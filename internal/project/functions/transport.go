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
	"net/http"
	"time"
)

const (
	// registryResponseHeaderTimeout bounds each registry request's wait for
	// response headers (ping, manifest, config, blob). ggcr retries the
	// timeout on HTTP/2, where it is a temporary error. On HTTP/1.1 it also
	// matches context.DeadlineExceeded, which ggcr's retry predicate rejects,
	// so the request fails without a retry.
	registryResponseHeaderTimeout = 30 * time.Second

	// registrySendPingTimeout is how long an HTTP/2 connection may go without
	// frames before it is pinged. An unanswered ping (PingTimeout, 15s by
	// default) closes the connection and fails every stream on it, including
	// layer bodies mid-read, so later requests dial fresh instead of queueing
	// on a dead connection.
	registrySendPingTimeout = 30 * time.Second
)

// registryTransport is the transport function builders pull runtime base
// images with. http.DefaultTransport has no response header timeout and no
// HTTP/2 health check (its 90s idle timeout only reaps unused connections), so
// a registry connection that goes silent would block a build forever.
var registryTransport http.RoundTripper = newRegistryTransport(registryResponseHeaderTimeout, registrySendPingTimeout) //nolint:gochecknoglobals // One connection pool shared by every builder, like http.DefaultTransport.

// newRegistryTransport returns a clone of http.DefaultTransport with the given
// response header timeout and HTTP/2 ping health check.
//
// ponytail: an HTTP/1.1 connection that stalls mid-body (after headers) is
// still unbounded; only HTTP/2 gets the ping check. If a registry that only
// speaks HTTP/1.1 shows the problem, wrap DialContext's conn with an idle read
// deadline (reset on every Read), since the lazy layer reads have no ctx to
// bound them.
func newRegistryTransport(responseHeaderTimeout, sendPingTimeout time.Duration) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone() //nolint:forcetypeassert // http.DefaultTransport is always *http.Transport.
	t.ResponseHeaderTimeout = responseHeaderTimeout
	t.HTTP2 = &http.HTTP2Config{SendPingTimeout: sendPingTimeout}
	return t
}
