// SPDX-License-Identifier: MPL-2.0
package netproxy

import (
	"net/http"
	"testing"
)

// TestTransportForIsolation checks that transports bound to different
// proxy configs actually route differently (v0.18.0, per-mount
// proxy): a direct transport never uses a proxy, a manual transport
// reports the manual proxy for the same target, and loopback targets
// are never proxied under any config.
func TestTransportForIsolation(t *testing.T) {
	direct := Config{Mode: ModeDirect}
	manual := Config{Mode: ModeManual, HTTP: "http://127.0.0.1:18080"}
	socks := Config{Mode: ModeManual, SOCKS5: "socks5://127.0.0.1:1080"}
	target := "https://example.com/"

	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	if addr, via := ProxyForURLWith(direct, target); addr != "" || via != "none" {
		t.Fatalf("ProxyForURLWith(direct): (%q, %q), want (\"\", \"none\")", addr, via)
	}
	if u, err := TransportFor(direct).Proxy(req); err != nil || u != nil {
		t.Fatalf("TransportFor(direct).Proxy: (%v, %v), want (nil, nil)", u, err)
	}

	if addr, via := ProxyForURLWith(manual, target); addr != "127.0.0.1:18080" || via != "manual" {
		t.Fatalf("ProxyForURLWith(manual): (%q, %q), want (\"127.0.0.1:18080\", \"manual\")", addr, via)
	}
	u, err := TransportFor(manual).Proxy(req)
	if err != nil || u == nil || u.Host != "127.0.0.1:18080" {
		t.Fatalf("TransportFor(manual).Proxy: (%v, %v), want a proxy URL for 127.0.0.1:18080", u, err)
	}

	// SOCKS5-only: resolve still reports it, but the transport's
	// Proxy func must stay nil — the DialContext routes it.
	if addr, via := ProxyForURLWith(socks, target); addr != "127.0.0.1:1080" || via != "manual" {
		t.Fatalf("ProxyForURLWith(socks5): (%q, %q), want (\"127.0.0.1:1080\", \"manual\")", addr, via)
	}
	if u, err := TransportFor(socks).Proxy(req); err != nil || u != nil {
		t.Fatalf("TransportFor(socks5).Proxy: (%v, %v), want (nil, nil)", u, err)
	}

	// Loopback targets are never proxied, even under a manual config.
	if addr, _ := ProxyForURLWith(manual, "http://127.0.0.1:8080/x"); addr != "" {
		t.Fatalf("ProxyForURLWith(manual, loopback): %q, want \"\"", addr)
	}

	// The two transports are distinct objects with different routing.
	if TransportFor(direct) == TransportFor(manual) {
		t.Fatalf("TransportFor(direct) and TransportFor(manual) must be different transports")
	}

	// A zero config means "not set": it falls back to the shared
	// live transport, so old callers keep following Configure.
	if NewClientFor(Config{}, 5).Transport != Transport() {
		t.Fatalf("NewClientFor(zero config) must use the shared transport")
	}
	if TransportOrShared(Config{}) != Transport() {
		t.Fatalf("TransportOrShared(zero config) must return the shared transport")
	}
	if TransportOrShared(manual) == Transport() {
		t.Fatalf("TransportOrShared(manual) must not return the shared transport")
	}

	// The global helpers are unchanged: Transport() is still the
	// single shared transport.
	if Transport() != Transport() {
		t.Fatalf("Transport() must be stable")
	}
}
