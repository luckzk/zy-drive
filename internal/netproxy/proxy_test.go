// SPDX-License-Identifier: MPL-2.0
package netproxy

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// restoreConfig resets the global proxy config after a test.
func restoreConfig(t *testing.T) {
	t.Helper()
	old := Current()
	t.Cleanup(func() { _ = Configure(old) })
}

// setProxyEnv sets both cases of the proxy env vars: x/net's
// httpproxy prioritizes the LOWERCASE ones, so setting only the
// uppercase would silently lose to the sandbox's http_proxy.
func setProxyEnv(t *testing.T, httpProxy, httpsProxy, noProxy string) {
	t.Helper()
	t.Setenv("HTTP_PROXY", httpProxy)
	t.Setenv("http_proxy", httpProxy)
	t.Setenv("HTTPS_PROXY", httpsProxy)
	t.Setenv("https_proxy", httpsProxy)
	t.Setenv("NO_PROXY", noProxy)
	t.Setenv("no_proxy", noProxy)
}

func TestNormalizeAddr(t *testing.T) {
	cases := []struct {
		raw, scheme, want string
		wantErr           bool
	}{
		{"", "http", "", false},
		{"127.0.0.1:7890", "http", "http://127.0.0.1:7890", false},
		{"http://127.0.0.1:7890", "http", "http://127.0.0.1:7890", false},
		{"127.0.0.1:1080", "socks5", "socks5://127.0.0.1:1080", false},
		{"socks5://127.0.0.1:1080", "socks5", "socks5://127.0.0.1:1080", false},
		{"127.0.0.1", "http", "", true},       // no port
		{"127.0.0.1:99999", "http", "", true}, // port out of range
		{"ftp://127.0.0.1:21", "http", "", true},
	}
	for _, c := range cases {
		got, err := NormalizeAddr(c.raw, c.scheme)
		if c.wantErr && err == nil {
			t.Errorf("NormalizeAddr(%q): expected error, got %q", c.raw, got)
		}
		if !c.wantErr && (err != nil || got != c.want) {
			t.Errorf("NormalizeAddr(%q): got (%q, %v), want (%q, nil)", c.raw, got, err, c.want)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	if err := (Config{Mode: ModeSystem}).Validate(); err != nil {
		t.Errorf("system should validate: %v", err)
	}
	if err := (Config{Mode: ModeManual}).Validate(); err == nil {
		t.Error("manual with no address should not validate")
	}
	if err := (Config{Mode: ModeManual, HTTP: "127.0.0.1:7890"}).Validate(); err != nil {
		t.Errorf("manual with HTTP should validate: %v", err)
	}
	if err := (Config{Mode: "bogus"}).Validate(); err == nil {
		t.Error("bogus mode should not validate")
	}
}

func TestResolveEnvPriority(t *testing.T) {
	restoreConfig(t)
	setProxyEnv(t, "http://127.0.0.1:18080", "http://127.0.0.1:18081", "")
	_ = Configure(Config{Mode: ModeSystem})

	// HTTP_PROXY and HTTPS_PROXY must not fight: each scheme takes
	// its own variable (Go's ProxyFromEnvironment rule).
	if addr, via := ProxyForURL("http://example.com/"); addr != "127.0.0.1:18080" || via != "env" {
		t.Errorf("http URL: got proxy %q via %q, want 127.0.0.1:18080 via env", addr, via)
	}
	if addr, via := ProxyForURL("https://api.dropboxapi.com/"); addr != "127.0.0.1:18081" || via != "env" {
		t.Errorf("https URL: got proxy %q via %q, want 127.0.0.1:18081 via env", addr, via)
	}
	// Loopback is never proxied (OAuth listener, Agent API).
	if addr, via := ProxyForURL("http://127.0.0.1:18080/x"); addr != "" || via != "none" {
		t.Errorf("loopback: got proxy %q via %q, want none", addr, via)
	}
	if addr, via := ProxyForURL("http://localhost:18080/x"); addr != "" || via != "none" {
		t.Errorf("localhost: got proxy %q via %q, want none", addr, via)
	}
	// NO_PROXY bypasses the env proxy.
	setProxyEnv(t, "http://127.0.0.1:18080", "http://127.0.0.1:18081", "api.dropboxapi.com")
	if addr, via := ProxyForURL("https://api.dropboxapi.com/"); addr != "" || via != "none" {
		t.Errorf("NO_PROXY: got proxy %q via %q, want none", addr, via)
	}
	// Direct mode ignores env vars entirely.
	_ = Configure(Config{Mode: ModeDirect})
	if addr, via := ProxyForURL("https://api.dropboxapi.com/"); addr != "" || via != "none" {
		t.Errorf("direct: got proxy %q via %q, want none", addr, via)
	}
}

func TestResolveManual(t *testing.T) {
	restoreConfig(t)
	setProxyEnv(t, "", "", "")
	_ = Configure(Config{Mode: ModeManual, HTTP: "127.0.0.1:18080"})

	if addr, via := ProxyForURL("https://api.dropboxapi.com/"); addr != "127.0.0.1:18080" || via != "manual" {
		t.Errorf("manual HTTP: got %q via %q", addr, via)
	}
	_ = Configure(Config{Mode: ModeManual, SOCKS5: "127.0.0.1:11080"})
	if addr, via := ProxyForURL("https://api.dropboxapi.com/"); addr != "127.0.0.1:11080" || via != "manual" {
		t.Errorf("manual SOCKS5: got %q via %q", addr, via)
	}
}

// recordingProxy is a minimal HTTP proxy for tests: it records every
// request it receives and answers with canned JSON, so a test can
// prove a request really went through the proxy.
type recordingProxy struct {
	mu   sync.Mutex
	seen []string
	srv  *httptest.Server
}

func newRecordingProxy(t *testing.T) *recordingProxy {
	t.Helper()
	p := &recordingProxy{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.seen = append(p.seen, r.Method+" "+r.URL.String())
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"tok-via-proxy","token_type":"bearer"}`)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *recordingProxy) url() string { return p.srv.URL }

func (p *recordingProxy) requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

// nonLoopbackTarget is a TEST-NET-1 address: unroutable, so a
// request to it can only succeed through a proxy — the proof that
// the proxy path is really taken. (The sandbox intercepts traffic
// to its own 198.19.0.2 gateway address, so a "real" non-loopback
// local IP cannot be used here.)
const nonLoopbackTarget = "http://192.0.2.10:18080"

// TestRequestGoesThroughProxy is the proof the 2026-11-25 Dropbox
// debugging needed: with HTTP_PROXY set, a request to a mock token
// endpoint must arrive at the proxy (recorded), not go direct.
func TestRequestGoesThroughProxy(t *testing.T) {
	restoreConfig(t)
	proxy := newRecordingProxy(t)
	// The token endpoint itself, on loopback (direct hits counted
	// separately): loopback targets are never proxied, by design.
	directHits := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directHits++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"tok-direct","token_type":"bearer"}`)
	}))
	defer endpoint.Close()

	setProxyEnv(t, proxy.url(), "", "")
	_ = Configure(Config{Mode: ModeSystem})

	client := NewClient(10)
	// The target is unroutable directly: only the proxy can answer.
	resp, err := client.Post(nonLoopbackTarget+"/token", "application/x-www-form-urlencoded", strings.NewReader("grant_type=authorization_code"))
	if err != nil {
		t.Fatalf("request through proxy: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "tok-via-proxy") {
		t.Fatalf("response came from %q, want the proxy's canned token (request did not go through the proxy)", body)
	}
	if got := proxy.requests(); len(got) != 1 {
		t.Fatalf("proxy saw %d requests, want 1: %v", len(got), got)
	}
	if directHits != 0 {
		t.Fatalf("endpoint was hit directly %d times, want 0", directHits)
	}

	// NO_PROXY for the target host: the same URL must now bypass
	// the proxy (ProxyForURL says none), and a loopback request
	// with the proxy still configured must go direct.
	setProxyEnv(t, proxy.url(), "", "192.0.2.10")
	if addr, via := ProxyForURL(nonLoopbackTarget + "/token"); addr != "" || via != "none" {
		t.Fatalf("NO_PROXY: ProxyForURL got %q via %q, want none", addr, via)
	}
	resp2, err := client.Post(endpoint.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader("grant_type=authorization_code"))
	if err != nil {
		t.Fatalf("direct request: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if !strings.Contains(string(body2), "tok-direct") {
		t.Fatalf("loopback response came from %q, want direct", body2)
	}
	if got := proxy.requests(); len(got) != 1 {
		t.Fatalf("proxy saw %d requests after NO_PROXY, want still 1: %v", len(got), got)
	}
}

// socks5Stub is a minimal SOCKS5 server for tests: it answers the
// handshake, records the CONNECT target, then forwards bytes to the
// target so the request actually completes. forwardAddr, when set,
// overrides the dial target (test-only: the requested target is a
// TEST-NET address that cannot be dialled, so the stub forwards to
// the real local endpoint instead — the recorded CONNECT target is
// still asserted).
type socks5Stub struct {
	mu          sync.Mutex
	targets     []string
	ln          net.Listener
	forwardAddr string
}

func newSocks5Stub(t *testing.T) *socks5Stub {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen socks stub: %v", err)
	}
	s := &socks5Stub{ln: ln}
	t.Cleanup(func() { ln.Close() })
	go s.serve()
	return s
}

func (s *socks5Stub) addr() string { return s.ln.Addr().String() }

func (s *socks5Stub) connectTargets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.targets...)
}

func (s *socks5Stub) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *socks5Stub) handle(c net.Conn) {
	defer c.Close()
	buf := make([]byte, 512)
	// Greeting: VER, NMETHODS, METHODS...
	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		return
	}
	n := int(buf[1])
	if _, err := io.ReadFull(c, buf[:n]); err != nil {
		return
	}
	if _, err := c.Write([]byte{0x05, 0x00}); err != nil { // no auth
		return
	}
	// Request: VER, CMD, RSV, ATYP, DST.ADDR, DST.PORT
	if _, err := io.ReadFull(c, buf[:4]); err != nil {
		return
	}
	if buf[1] != 0x01 { // only CONNECT
		return
	}
	var host string
	switch buf[3] {
	case 0x01: // IPv4
		if _, err := io.ReadFull(c, buf[:4]); err != nil {
			return
		}
		host = net.IP(buf[:4]).String()
	case 0x03: // domain
		if _, err := io.ReadFull(c, buf[:1]); err != nil {
			return
		}
		l := int(buf[0])
		if _, err := io.ReadFull(c, buf[:l]); err != nil {
			return
		}
		host = string(buf[:l])
	case 0x04: // IPv6
		if _, err := io.ReadFull(c, buf[:16]); err != nil {
			return
		}
		host = net.IP(buf[:16]).String()
	default:
		return
	}
	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		return
	}
	port := int(buf[0])<<8 | int(buf[1])
	target := fmt.Sprintf("%s:%d", host, port)
	s.mu.Lock()
	s.targets = append(s.targets, target)
	forward := s.forwardAddr
	s.mu.Unlock()
	if forward == "" {
		forward = target
	}
	up, err := net.Dial("tcp", forward)
	if err != nil {
		c.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(up, c) }()
	go func() { defer wg.Done(); io.Copy(c, up) }()
	wg.Wait()
}

func TestRequestGoesThroughSocks5(t *testing.T) {
	restoreConfig(t)
	stub := newSocks5Stub(t)
	// The endpoint listens on loopback; the stub forwards to it
	// while the client asks for the unroutable TEST-NET target, so
	// the request can only complete via the SOCKS handshake.
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "socks-ok")
	}))
	defer endpoint.Close()
	epURL, _ := url.Parse(endpoint.URL)
	stub.forwardAddr = epURL.Host

	setProxyEnv(t, "", "", "")
	_ = Configure(Config{Mode: ModeManual, SOCKS5: stub.addr()})

	client := NewClient(10)
	resp, err := client.Get(nonLoopbackTarget + "/")
	if err != nil {
		t.Fatalf("request through SOCKS5: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "socks-ok" {
		t.Fatalf("unexpected body %q", body)
	}
	targets := stub.connectTargets()
	if len(targets) != 1 || targets[0] != "192.0.2.10:18080" {
		t.Fatalf("SOCKS CONNECT targets %v, want [192.0.2.10:18080]", targets)
	}
}

func TestClassifyError(t *testing.T) {
	restoreConfig(t)
	setProxyEnv(t, "http://127.0.0.1:17890", "http://127.0.0.1:17890", "")
	_ = Configure(Config{Mode: ModeSystem})

	// The 2026-11-25 Dropbox case, verbatim shape: the proxy host
	// refuses the connection.
	kind, addr := ClassifyError("https://api.dropboxapi.com/oauth2/token",
		fmt.Errorf(`Post "https://api.dropboxapi.com/oauth2/token": proxyconnect tcp: dial tcp 127.0.0.1:17890: connect: connection refused`))
	if kind != KindProxyDown || addr != "127.0.0.1:17890" {
		t.Errorf("proxy refused: got (%q, %q), want (proxy-down, 127.0.0.1:17890)", kind, addr)
	}
	// Proxy reached, tunnel failed.
	kind, addr = ClassifyError("https://api.dropboxapi.com/oauth2/token",
		fmt.Errorf(`Post "https://api.dropboxapi.com/oauth2/token": proxyconnect tcp: dial tcp 93.184.216.34:443: i/o timeout`))
	if kind != KindProxyTarget || addr != "127.0.0.1:17890" {
		t.Errorf("proxy target: got (%q, %q), want (proxy-target, 127.0.0.1:17890)", kind, addr)
	}
	// No proxy at all: direct blocked.
	_ = Configure(Config{Mode: ModeDirect})
	kind, addr = ClassifyError("https://api.dropboxapi.com/oauth2/token",
		fmt.Errorf(`Post "https://api.dropboxapi.com/oauth2/token": dial tcp 93.184.216.34:443: connect: connection refused`))
	if kind != KindDirect || addr != "" {
		t.Errorf("direct: got (%q, %q), want (direct, \"\")", kind, addr)
	}
}

func TestFetchPAC(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fixed.pac":
			fmt.Fprint(w, `function FindProxyForURL(url, host) { return "PROXY 127.0.0.1:7890"; }`)
		case "/socks.pac":
			fmt.Fprint(w, `function FindProxyForURL(url, host) { return "SOCKS5 127.0.0.1:1080"; }`)
		case "/direct.pac":
			fmt.Fprint(w, `function FindProxyForURL(url, host) { return "DIRECT"; }`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	if got, err := FetchPAC(srv.URL + "/fixed.pac"); err != nil || got != "http://127.0.0.1:7890" {
		t.Errorf("fixed PAC: got (%q, %v)", got, err)
	}
	if got, err := FetchPAC(srv.URL + "/socks.pac"); err != nil || got != "socks5://127.0.0.1:1080" {
		t.Errorf("socks PAC: got (%q, %v)", got, err)
	}
	if _, err := FetchPAC(srv.URL + "/direct.pac"); err == nil {
		t.Error("DIRECT-only PAC should error (nothing to use)")
	}
	if _, err := FetchPAC(srv.URL + "/missing.pac"); err == nil {
		t.Error("missing PAC should error")
	}
}
