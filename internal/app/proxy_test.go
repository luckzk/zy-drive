// SPDX-License-Identifier: MPL-2.0
package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Tests for the v0.18.1 proxy-test wording: every Detail is plain
// language — a success never carries the HTTP status code (a bare
// "404" reads like an error), a timeout says 超时, and no raw Go
// error ever reaches the user. All targets are local (httptest);
// the "direct" proxyRef keeps the test off any proxy the process
// environment might configure.
func TestProxySuccessPlainLanguage(t *testing.T) {
	a := newTestApp(t)
	// The endpoint answers 404 on purpose (like the Dropbox token
	// host on GET): any HTTP response proves the path works.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	res := a.TestProxy(srv.URL+"/oauth2/token", "direct")
	if !res.OK {
		t.Fatalf("TestProxy against a responding server should pass: %q", res.Detail)
	}
	if res.Detail != "正常，直连，不需要代理" {
		t.Fatalf("success Detail should be plain language without a status code, got %q", res.Detail)
	}
	for _, leak := range []string{"404", "200", "Not Found", "OK"} {
		if strings.Contains(res.Detail, leak) {
			t.Fatalf("success Detail leaks a status code/text %q: %q", leak, res.Detail)
		}
	}
}

func TestProxyTimeoutPlainLanguage(t *testing.T) {
	a := newTestApp(t)
	// Accepts the connection and never answers: the 15s client
	// timeout must fire and the Detail must say 超时 in plain
	// language — no "context deadline exceeded".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Second)
	}))
	defer srv.Close()
	start := time.Now()
	res := a.TestProxy(srv.URL, "direct")
	if res.OK {
		t.Fatalf("TestProxy against a never-answering server should fail")
	}
	if time.Since(start) > 30*time.Second {
		t.Fatalf("TestProxy took %v; the 15s timeout should have fired", time.Since(start))
	}
	if !strings.Contains(res.Detail, "超时") {
		t.Fatalf("timeout Detail must say 超时, got %q", res.Detail)
	}
	for _, leak := range []string{"deadline", "context", "Client.Timeout", "i/o timeout"} {
		if strings.Contains(res.Detail, leak) {
			t.Fatalf("timeout Detail leaks a raw Go error %q: %q", leak, res.Detail)
		}
	}
}

func TestProxyInvalidURLPlainLanguage(t *testing.T) {
	a := newTestApp(t)
	res := a.TestProxy("ftp://example.com/x", "direct")
	if res.OK {
		t.Fatalf("TestProxy with a non-http(s) URL should fail")
	}
	if strings.Contains(res.Detail, "ftp") || strings.Contains(res.Detail, "url.Parse") {
		t.Fatalf("invalid-URL Detail should be a plain prompt, got %q", res.Detail)
	}
}

func TestProxyRefusedPlainLanguage(t *testing.T) {
	a := newTestApp(t)
	// A closed loopback port: direct dial refused, fast.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	res := a.TestProxy(url, "direct")
	if res.OK {
		t.Fatalf("TestProxy against a closed port should fail")
	}
	if !strings.Contains(res.Detail, "直连") || !strings.Contains(res.Detail, "被挡") {
		t.Fatalf("refused Detail should say the direct connection is blocked, got %q", res.Detail)
	}
	for _, leak := range []string{"connection refused", "dial tcp", "connect:"} {
		if strings.Contains(res.Detail, leak) {
			t.Fatalf("refused Detail leaks a raw Go error %q: %q", leak, res.Detail)
		}
	}
}
