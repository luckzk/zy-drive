// SPDX-License-Identifier: MPL-2.0
package http

import (
	"net/http"
	"testing"

	"github.com/luckzk/zy-drive/internal/netproxy"
)

// TestNewWithProxyWiring proves the driver-level wiring (v0.18.0,
// per-mount proxy): a client built with a manual proxy config routes
// a public target through that proxy, and one built with a direct
// config uses no proxy at all — without sending any real request.
func TestNewWithProxyWiring(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.com/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	proxyOf := func(c *Client) *http.Transport {
		t.Helper()
		tr, ok := c.HTTP.Transport.(*http.Transport)
		if !ok {
			t.Fatalf("client transport is %T, want *http.Transport", c.HTTP.Transport)
		}
		return tr
	}

	manual := netproxy.Config{Mode: netproxy.ModeManual, HTTP: "http://127.0.0.1:18080"}
	u, err := proxyOf(NewWithProxy(nil, manual)).Proxy(req)
	if err != nil || u == nil || u.Host != "127.0.0.1:18080" {
		t.Fatalf("NewWithProxy(manual).Proxy: (%v, %v), want a proxy URL for 127.0.0.1:18080", u, err)
	}

	direct := netproxy.Config{Mode: netproxy.ModeDirect}
	u, err = proxyOf(NewWithProxy(nil, direct)).Proxy(req)
	if err != nil || u != nil {
		t.Fatalf("NewWithProxy(direct).Proxy: (%v, %v), want (nil, nil)", u, err)
	}

	// A zero config keeps the old behaviour: the shared transport.
	if NewWithProxy(nil, netproxy.Config{}).HTTP.Transport != netproxy.Transport() {
		t.Fatalf("NewWithProxy(zero config) must use the shared transport")
	}
}
