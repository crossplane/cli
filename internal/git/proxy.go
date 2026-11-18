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
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"

	"golang.org/x/net/proxy"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
)

// bufferedConn wraps a net.Conn with a buffered reader so that any data read
// during the CONNECT handshake (which may include bytes past the response
// headers) is not lost. Its Close is idempotent and also stops the
// context-cancellation watchdog started in DialContext.
type bufferedConn struct {
	net.Conn

	r io.Reader

	closeOnce sync.Once
	closeFn   func() error
}

func (b *bufferedConn) Read(p []byte) (int, error) {
	return b.r.Read(p)
}

func (b *bufferedConn) Close() error {
	var err error
	b.closeOnce.Do(func() {
		err = b.closeFn()
	})
	return err
}

// httpConnectDialer implements proxy.ContextDialer for HTTP CONNECT proxies.
// This allows SSH connections to be tunneled through HTTP proxies,
// similar to using ssh -o ProxyCommand='nc -X connect -x proxy:port %h %p'.
type httpConnectDialer struct {
	proxyURL *url.URL
	forward  proxy.Dialer
}

// Dial establishes a connection to the target address through the HTTP CONNECT proxy.
func (d *httpConnectDialer) Dial(network, addr string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, addr)
}

// DialContext establishes a connection to the target address through the HTTP CONNECT proxy
// with context support for cancellation and timeouts.
func (d *httpConnectDialer) DialContext(ctx context.Context, _, addr string) (net.Conn, error) {
	// Try to use DialContext if forward dialer supports it
	var conn net.Conn
	var err error

	proxyAddr := proxyAddress(d.proxyURL)
	if ctxDialer, ok := d.forward.(proxy.ContextDialer); ok {
		conn, err = ctxDialer.DialContext(ctx, "tcp", proxyAddr)
	} else {
		conn, err = d.forward.Dial("tcp", proxyAddr)
	}
	if err != nil {
		return nil, errors.Wrapf(err, "failed to connect to the git proxy at %s; check the --git-proxy URL and that the proxy is reachable", proxyAddr)
	}

	// Wrap with TLS if proxy uses HTTPS
	if d.proxyURL.Scheme == "https" {
		conn = tls.Client(conn, &tls.Config{
			ServerName: d.proxyURL.Hostname(),
			MinVersion: tls.VersionTLS12,
		})
	}

	// closeOnce guards conn.Close() against races between the handshake's
	// error paths, the context-cancellation watchdog, and the caller's Close
	// on the returned bufferedConn.
	var closeOnce sync.Once
	closeConn := func() error {
		var err error
		closeOnce.Do(func() { err = conn.Close() })
		return err
	}

	// The watchdog closes the connection if ctx fires while the CONNECT
	// handshake is in flight. It is disarmed when DialContext returns: the
	// dialing context must not govern the returned connection's lifetime
	// (go-git cancels it right after a successful dial).
	done := make(chan struct{})
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		select {
		case <-ctx.Done():
			_ = closeConn()
		case <-done:
		}
	}()
	defer func() {
		close(done)
		// Wait for the watchdog to exit so a context cancellation racing
		// with a successful handshake cannot close the connection after we
		// have handed it to the caller.
		<-watchdogDone
	}()

	// Build CONNECT request
	connectReq := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: addr},
		Host:   addr,
		Header: make(http.Header),
	}

	// Add proxy authentication if credentials are present in URL
	if d.proxyURL.User != nil {
		password, _ := d.proxyURL.User.Password()
		auth := d.proxyURL.User.Username() + ":" + password
		connectReq.Header.Set("Proxy-Authorization",
			"Basic "+base64.StdEncoding.EncodeToString([]byte(auth)))
	}

	// Send CONNECT request
	if err := connectReq.Write(conn); err != nil {
		_ = closeConn()
		return nil, errors.Wrapf(err, "failed to connect to %s through the git proxy at %s; check the --git-proxy URL and proxy access", addr, proxyAddr)
	}

	// Read response. Create the buffered reader once so that any bytes
	// buffered beyond the HTTP response headers are not lost.
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, connectReq)
	if err != nil {
		_ = closeConn()
		return nil, errors.Wrapf(err, "failed to connect to %s through the git proxy at %s: error reading the proxy response", addr, proxyAddr)
	}
	resp.Body.Close() //nolint:errcheck // Body is typically empty for CONNECT responses.

	if resp.StatusCode != http.StatusOK {
		_ = closeConn()
		return nil, errors.Errorf("the git proxy at %s refused the connection to %s with status %q; check proxy access", proxyAddr, addr, resp.Status)
	}

	return &bufferedConn{
		r:       br,
		Conn:    conn,
		closeFn: closeConn,
	}, nil
}

// proxyAddress returns the host-and-port address to dial for the proxy URL,
// defaulting the port by scheme (80 for http, 443 for https) when the URL
// omits it. TCP dialing requires an explicit port.
func proxyAddress(u *url.URL) string {
	if u.Port() != "" {
		return u.Host
	}
	port := "80"
	if u.Scheme == "https" {
		port = "443"
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// registerHTTPConnectProxy registers HTTP and HTTPS CONNECT proxy support
// with the golang.org/x/net/proxy package. It is called from init() so that
// importing this package is sufficient to enable HTTP CONNECT proxies for
// go-git's ProxyOptions (e.g. "http://proxy:8080" or "https://proxy:8443").
func registerHTTPConnectProxy() {
	newDialer := func(u *url.URL, forward proxy.Dialer) (proxy.Dialer, error) {
		return &httpConnectDialer{proxyURL: u, forward: forward}, nil
	}
	proxy.RegisterDialerType("http", newDialer)
	proxy.RegisterDialerType("https", newDialer)
}

// init automatically registers HTTP CONNECT proxy support when this package is imported.
// registerProxyOnce performs the dialer registration once; it is invoked
// from every code path that may dial through a proxy.
//
//nolint:gochecknoglobals // The x/net/proxy dialer registry is process-global; registration must happen exactly once.
var registerProxyOnce = sync.OnceFunc(registerHTTPConnectProxy)
