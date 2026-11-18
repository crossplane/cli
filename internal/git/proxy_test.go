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
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"golang.org/x/net/proxy"
)

func TestHTTPConnectDialer(t *testing.T) {
	t.Parallel()

	// Start a mock HTTP CONNECT proxy server
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen(...): %v", err)
	}
	defer listener.Close()

	proxyAddr := listener.Addr().String()
	targetAddr := "example.com:443"

	// Track if CONNECT was received
	connectReceived := make(chan string, 1)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Read the CONNECT request
		reader := bufio.NewReader(conn)
		req, err := http.ReadRequest(reader)
		if err != nil {
			return
		}

		// Signal that we received the CONNECT request
		connectReceived <- req.URL.String()

		// Send 200 OK response
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 Connection established",
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
		}
		resp.Write(conn)

		// Keep connection open briefly
		time.Sleep(100 * time.Millisecond)
	}()

	// Create dialer using our implementation
	proxyURL, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		t.Fatalf("url.Parse(...): %v", err)
	}

	dialer := &httpConnectDialer{
		proxyURL: proxyURL,
		forward:  proxy.Direct,
	}

	// Dial through proxy; the fixture answers 200, so this must succeed.
	conn, err := dialer.Dial("tcp", targetAddr)
	if err != nil {
		t.Fatalf("Dial(...): %v", err)
	}
	if conn == nil {
		t.Fatal("Dial(...): got nil conn")
	}
	conn.Close()

	// Verify CONNECT request was sent
	select {
	case received := <-connectReceived:
		if !strings.Contains(received, "example.com:443") {
			t.Errorf("expected CONNECT to example.com:443, got: %s", received)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for CONNECT request")
	}
}

func TestHTTPConnectDialerWithAuth(t *testing.T) {
	t.Parallel()

	// Start a mock HTTP CONNECT proxy server that requires auth
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen(...): %v", err)
	}
	defer listener.Close()

	proxyAddr := listener.Addr().String()
	targetAddr := "example.com:22"

	// Track auth header
	authReceived := make(chan string, 1)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Read the CONNECT request
		reader := bufio.NewReader(conn)
		req, err := http.ReadRequest(reader)
		if err != nil {
			return
		}

		// Extract auth header
		authReceived <- req.Header.Get("Proxy-Authorization")

		// Send 200 OK response
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 Connection established",
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
		}
		resp.Write(conn)

		time.Sleep(100 * time.Millisecond)
	}()

	// Create dialer with authentication
	proxyURL, err := url.Parse(fmt.Sprintf("http://testuser:testpass@%s", proxyAddr))
	if err != nil {
		t.Fatalf("url.Parse(...): %v", err)
	}

	dialer := &httpConnectDialer{
		proxyURL: proxyURL,
		forward:  proxy.Direct,
	}

	// Dial through proxy; the fixture answers 200, so this must succeed.
	conn, err := dialer.Dial("tcp", targetAddr)
	if err != nil {
		t.Fatalf("Dial(...): %v", err)
	}
	conn.Close()

	// Verify auth header was sent
	select {
	case auth := <-authReceived:
		// Basic dGVzdHVzZXI6dGVzdHBhc3M= is base64 of "testuser:testpass".
		if diff := cmp.Diff("Basic dGVzdHVzZXI6dGVzdHBhc3M=", auth); diff != "" {
			t.Errorf("Proxy-Authorization header: -want, +got:\n%s", diff)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for auth header")
	}
}

func TestHTTPConnectDialerNon200(t *testing.T) {
	t.Parallel()

	// Start a mock HTTP CONNECT proxy server that returns 403 Forbidden
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen(...): %v", err)
	}
	defer listener.Close()

	proxyAddr := listener.Addr().String()
	targetAddr := "example.com:22"

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Read the CONNECT request
		reader := bufio.NewReader(conn)
		_, err = http.ReadRequest(reader)
		if err != nil {
			return
		}

		// Send 403 Forbidden response
		resp := &http.Response{
			StatusCode: http.StatusForbidden,
			Status:     "403 Forbidden",
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
		}
		resp.Write(conn)
	}()

	proxyURL, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		t.Fatalf("url.Parse(...): %v", err)
	}

	dialer := &httpConnectDialer{
		proxyURL: proxyURL,
		forward:  proxy.Direct,
	}

	conn, err := dialer.Dial("tcp", targetAddr)
	if err == nil {
		t.Fatal("expected error for non-200 proxy response, got nil")
	}
	if conn != nil {
		t.Error("expected nil connection for non-200 proxy response")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("expected error to mention 403 status, got: %s", err.Error())
	}
}

func TestHTTPConnectDialerHTTPS(t *testing.T) {
	t.Parallel()

	// Start a plain TCP listener. The dialer should attempt a TLS
	// handshake because the proxy URL uses the https scheme. Since the
	// listener is plain TCP, the TLS handshake will not complete and the
	// context deadline will fire, proving TLS was attempted.
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen(...): %v", err)
	}
	defer listener.Close()

	// Read the first byte before closing: a TLS ClientHello starts with the
	// handshake record type 0x16, proving the dialer attempted TLS rather
	// than sending a plaintext CONNECT.
	firstByte := make(chan byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 1)
		if _, err := conn.Read(buf); err == nil {
			firstByte <- buf[0]
		}
	}()

	proxyAddr := listener.Addr().String()
	targetAddr := "example.com:22"

	proxyURL, err := url.Parse("https://" + proxyAddr)
	if err != nil {
		t.Fatalf("url.Parse(...): %v", err)
	}

	dialer := &httpConnectDialer{
		proxyURL: proxyURL,
		forward:  proxy.Direct,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := dialer.DialContext(ctx, "tcp", targetAddr)
	if conn != nil {
		conn.Close()
	}
	// We expect an error because the plain TCP listener cannot complete a TLS handshake.
	if err == nil {
		t.Error("expected error when TLS handshake fails against plain TCP proxy, got nil")
	}

	select {
	case b := <-firstByte:
		if b != 0x16 {
			t.Errorf("expected a TLS handshake record (0x16) as the first byte, got 0x%02x", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for the proxy to receive any bytes")
	}
}

// TestHTTPConnectDialerCancellation verifies that DialContext returns promptly
// when the context is cancelled while the proxy accepts a socket but never
// responds (exercising the watchdog goroutine).
func TestHTTPConnectDialerCancellation(t *testing.T) {
	t.Parallel()

	// Start a listener that accepts connections but never writes a response.
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen(...): %v", err)
	}
	defer listener.Close()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		// Hold the connection open; never respond.
		defer conn.Close()
		<-t.Context().Done()
	}()

	proxyURL, _ := url.Parse("http://" + listener.Addr().String())
	dialer := &httpConnectDialer{proxyURL: proxyURL, forward: proxy.Direct}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	conn, err := dialer.DialContext(ctx, "tcp", "example.com:443")
	if err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}
	if conn != nil {
		t.Error("expected nil conn on cancelled context")
	}
}

// TestRegisterHTTPConnectProxy verifies that the init()-driven dialer
// registration works. It must NOT use t.Parallel() because it validates
// package-global proxy dialer registration and must not run in parallel
// with other proxy tests that rely on that global state.
func TestRegisterHTTPConnectProxy(t *testing.T) {
	registerProxyOnce()
	// Verify that HTTP and HTTPS schemes are registered
	// by checking if we can create dialers for these schemes
	httpURL, _ := url.Parse("http://proxy.example.com:8080")
	httpsURL, _ := url.Parse("https://proxy.example.com:8443")

	// These should not panic after registerHTTPConnectProxy is called (via init)
	httpDialer, err := proxy.FromURL(httpURL, proxy.Direct)
	if err != nil {
		t.Fatalf("proxy.FromURL(http): %v", err)
	}
	if httpDialer == nil {
		t.Error("expected a dialer for the http scheme, got nil")
	}

	httpsDialer, err := proxy.FromURL(httpsURL, proxy.Direct)
	if err != nil {
		t.Fatalf("proxy.FromURL(https): %v", err)
	}
	if httpsDialer == nil {
		t.Error("expected a dialer for the https scheme, got nil")
	}
}

// TestHTTPConnectDialerSurvivesDialContextCancel verifies the dialing
// context does not govern the returned connection's lifetime: go-git cancels
// the dial context right after a successful dial, and the tunnel must keep
// working.
func TestHTTPConnectDialerSurvivesDialContextCancel(t *testing.T) {
	t.Parallel()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen(...): %v", err)
	}
	defer listener.Close()

	echoed := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		if _, err := http.ReadRequest(reader); err != nil {
			return
		}
		resp := &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 Connection established",
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
		}
		resp.Write(conn)

		// Echo one line of tunneled data back.
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		echoed <- line
		fmt.Fprint(conn, line)
	}()

	proxyURL, err := url.Parse("http://" + listener.Addr().String())
	if err != nil {
		t.Fatalf("url.Parse(...): %v", err)
	}
	dialer := &httpConnectDialer{proxyURL: proxyURL, forward: proxy.Direct}

	ctx, cancel := context.WithCancel(context.Background())
	conn, err := dialer.DialContext(ctx, "tcp", "example.com:22")
	if err != nil {
		t.Fatalf("DialContext(...): %v", err)
	}
	defer conn.Close()

	// Cancel the dialing context, as go-git does after a successful dial,
	// then exchange data through the tunnel.
	cancel()

	if _, err := fmt.Fprint(conn, "hello\n"); err != nil {
		t.Fatalf("writing through the tunnel after cancel: %v", err)
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("reading through the tunnel after cancel: %v", err)
	}
	if diff := cmp.Diff("hello\n", reply); diff != "" {
		t.Errorf("tunneled echo: -want, +got:\n%s", diff)
	}
	select {
	case <-echoed:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for the proxy to receive tunneled data")
	}
}

// TestProxyAddress verifies default ports are applied when the proxy URL
// omits one.
func TestProxyAddress(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		reason string
		in     string
		want   string
	}{
		"HTTPDefaultPort": {
			reason: "An http proxy URL without a port dials port 80.",
			in:     "http://proxy.example.com",
			want:   "proxy.example.com:80",
		},
		"HTTPSDefaultPort": {
			reason: "An https proxy URL without a port dials port 443.",
			in:     "https://proxy.example.com",
			want:   "proxy.example.com:443",
		},
		"ExplicitPort": {
			reason: "An explicit port is preserved.",
			in:     "http://proxy.example.com:8080",
			want:   "proxy.example.com:8080",
		},
		"IPv6DefaultPort": {
			reason: "An IPv6 host is bracketed with the default port.",
			in:     "http://[::1]",
			want:   "[::1]:80",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			u, err := url.Parse(tc.in)
			if err != nil {
				t.Fatalf("url.Parse(%q): %v", tc.in, err)
			}
			if diff := cmp.Diff(tc.want, proxyAddress(u)); diff != "" {
				t.Errorf("\n%s\nproxyAddress(%q): -want, +got:\n%s", tc.reason, tc.in, diff)
			}
		})
	}
}
