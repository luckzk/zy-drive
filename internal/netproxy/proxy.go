// SPDX-License-Identifier: MPL-2.0
// Package netproxy owns every outbound HTTP proxy decision in ZyDrive.
//
// Why it exists (2026-11-25, Dropbox one-click): the browser authorizes
// fine (it uses the macOS system proxy), but the Go backend's token
// exchange failed with a network error. The first guess was "Go does
// not read the macOS system proxy" — Go's ProxyFromEnvironment only
// reads HTTP_PROXY/HTTPS_PROXY/NO_PROXY env vars, never the system
// settings. But the user then ran with those env vars set and it still
// failed, and curl proved why: `curl -x http://127.0.0.1:7890` got
// "Failed to connect to 127.0.0.1 port 7890" — there was no proxy
// listening on 7890 at all. The app DID use the proxy; the proxy
// itself was down. So this package does three things, in order:
//  1. Route every outbound request through one shared transport
//     (env vars are honoured — nothing in the codebase overrides
//     http.DefaultTransport, so DefaultClient already did that, but
//     now it is explicit and testable).
//  2. Read the macOS system proxy (scutil --proxy, incl. PAC) so the
//     backend follows the same proxy the browser uses.
//  3. Diagnose failures precisely instead of a generic "network
//     problem": proxy itself unreachable, proxy reached but the
//     target failed, or direct connection blocked — see ClassifyError.
//
// The shared transport reads the current Config on every request, so
// Configure takes effect immediately — no restart, no client rebuild.
//
// One trap this package deliberately avoids: net/http's
// ProxyFromEnvironment caches the env config on first use, so later
// env changes are silently ignored. The env fallback below reads
// through golang.org/x/net/http/httpproxy.FromEnvironment() on every
// call instead, which re-reads os.Getenv each time.
package netproxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http/httpproxy"
	"golang.org/x/net/proxy"
)

// Mode selects where the proxy comes from.
type Mode string

const (
	// ModeSystem (default): use the macOS system proxy when one is
	// detected, otherwise fall back to the HTTP(S)_PROXY env vars.
	ModeSystem Mode = "system"
	// ModeManual: use the HTTP / SOCKS5 address from Config.
	ModeManual Mode = "manual"
	// ModeDirect: no proxy at all, even if env vars are set.
	ModeDirect Mode = "direct"
)

// Config is the whole proxy setting. HTTP is an HTTP proxy address
// ("host:port" or "http://host:port"); SOCKS5 is a SOCKS5 address
// ("host:port" or "socks5://host:port"). Either may be empty.
type Config struct {
	Mode   Mode
	HTTP   string
	SOCKS5 string
}

// DefaultConfig is what a fresh install gets: follow the system.
func DefaultConfig() Config { return Config{Mode: ModeSystem} }

// NormalizeAddr validates a manual proxy address and returns it with
// an explicit scheme ("http://host:port" or "socks5://host:port").
// Empty input returns "" with no error (means "not set").
func NormalizeAddr(raw, scheme string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !strings.Contains(raw, "://") {
		raw = scheme + "://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("netproxy: 代理地址 %q 解析失败: %v", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" {
		return "", fmt.Errorf("netproxy: 代理地址 %q 的协议不受支持（只支持 http/https/socks5）", raw)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || host == "" || port == "" {
		return "", fmt.Errorf("netproxy: 代理地址 %q 要写成 主机:端口（例如 127.0.0.1:7890）", raw)
	}
	if n, err := net.LookupPort("tcp", port); err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("netproxy: 代理地址 %q 的端口无效", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

// Validate checks a Config the same way SaveSettings does.
func (c Config) Validate() error {
	switch c.Mode {
	case ModeSystem, ModeManual, ModeDirect:
	default:
		return fmt.Errorf("netproxy: 未知的代理模式 %q", c.Mode)
	}
	if c.Mode == ModeManual {
		httpAddr, err := NormalizeAddr(c.HTTP, "http")
		if err != nil {
			return err
		}
		socksAddr, err := NormalizeAddr(c.SOCKS5, "socks5")
		if err != nil {
			return err
		}
		if httpAddr == "" && socksAddr == "" {
			return fmt.Errorf("netproxy: 手动代理请至少填一个地址（HTTP 或 SOCKS5）")
		}
	}
	return nil
}

// SystemProxy is what the OS reports (darwin: scutil --proxy).
// Detected is false when no proxy is configured at all.
type SystemProxy struct {
	Detected bool
	HTTP     string // "http://host:port" or ""
	HTTPS    string // "http://host:port" or ""
	SOCKS5   string // "socks5://host:port" or ""
	PACURL   string // the PAC URL when ProxyAutoConfig is on
	PACProxy string // proxy extracted from the PAC file, "" when none/unparseable
	PACError string // why the PAC could not be used, "" when n/a
	Error    string // detection itself failed, "" when ok
}

// resolved is the effective proxy for one request: the proxy URL to
// use (nil = direct) and where it came from, for logs and errors.
type resolved struct {
	proxy *url.URL
	via   string // "manual" | "system" | "env" | "none"
}

var (
	mu      sync.RWMutex
	current = DefaultConfig()

	// sysMu guards the scutil cache (a subprocess call; re-detecting
	// on every request would fork on every request).
	sysMu    sync.Mutex
	sysCache *SystemProxy

	// sharedTransport is built once; its Proxy func and DialContext
	// read Current() on every request, so Configure applies at once.
	// Per-config transports (TransportFor) are built by the same
	// buildTransport with a fixed config instead.
	sharedTransport = buildTransport(Current)
)

// Configure replaces the proxy config; it takes effect immediately
// for all clients built on Transport() — no restart. An invalid
// config keeps the previous one and returns the error, so a typo
// never kills networking.
func Configure(cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if cfg.Mode == ModeManual {
		httpAddr, _ := NormalizeAddr(cfg.HTTP, "http")
		socksAddr, _ := NormalizeAddr(cfg.SOCKS5, "socks5")
		cfg.HTTP, cfg.SOCKS5 = httpAddr, socksAddr
	}
	mu.Lock()
	current = cfg
	mu.Unlock()
	return nil
}

// Current returns the active config.
func Current() Config {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Transport returns the shared RoundTripper every outbound client
// must use.
func Transport() http.RoundTripper { return sharedTransport }

// TransportFor returns a fresh transport bound to one proxy config
// (v0.18.0, per-mount proxy): unlike Transport() it does NOT follow
// later Configure calls — the config is fixed at build time, so a
// mount built under one proxy keeps using it until it is rebuilt.
// Built from the same code as the shared transport, never a
// second implementation: buildTransport is parameterized by where
// the config comes from.
func TransportFor(cfg Config) *http.Transport {
	return buildTransport(func() Config { return cfg })
}

// NewClient returns an *http.Client on the shared transport.
func NewClient(timeoutSeconds int) *http.Client {
	return &http.Client{Transport: Transport(), Timeout: time.Duration(timeoutSeconds) * time.Second}
}

// NewClientFor returns an *http.Client on a transport bound to one
// proxy config (see TransportFor). A zero config means "not set":
// it falls back to the shared live transport, so drivers built
// without a per-mount proxy keep following Configure immediately,
// exactly as before v0.18.0.
func NewClientFor(cfg Config, timeoutSeconds int) *http.Client {
	if cfg == (Config{}) {
		return NewClient(timeoutSeconds)
	}
	return &http.Client{Transport: TransportFor(cfg), Timeout: time.Duration(timeoutSeconds) * time.Second}
}

// TransportOrShared returns TransportFor(cfg) when cfg is set, else
// the shared live transport (same zero-config rule as
// NewClientFor). Direct-Transport call sites (MEGA, the
// authTransport wrappers) use this so a mount without its own proxy
// keeps the old live-global behaviour.
func TransportOrShared(cfg Config) http.RoundTripper {
	if cfg == (Config{}) {
		return Transport()
	}
	return TransportFor(cfg)
}

// ProxyForURL reports which proxy (if any) a request to target would
// use, and where it came from. Safe to log: a proxy URL with userinfo
// is stripped to host only by resolve.
func ProxyForURL(target string) (proxyAddr, via string) {
	return ProxyForURLWith(Current(), target)
}

// ProxyForURLWith is ProxyForURL under one given config instead of
// the current global one (v0.18.0: proxy tests and per-mount
// diagnostics report the proxy the mount actually uses).
func ProxyForURLWith(cfg Config, target string) (proxyAddr, via string) {
	u, err := url.Parse(target)
	if err != nil {
		return "", "none"
	}
	r := resolve(cfg, u)
	if r.proxy == nil {
		return "", "none"
	}
	return r.proxy.Host, r.via
}

// resolve picks the proxy for one URL under cfg.
func resolve(cfg Config, target *url.URL) resolved {
	if isLoopback(target.Hostname()) {
		return resolved{via: "none"}
	}
	switch cfg.Mode {
	case ModeDirect:
		return resolved{via: "none"}
	case ModeManual:
		if cfg.HTTP != "" && (target.Scheme == "http" || target.Scheme == "https") {
			if u, err := url.Parse(cfg.HTTP); err == nil {
				return resolved{proxy: u, via: "manual"}
			}
		}
		// SOCKS5 alone is handled by the transport's DialContext,
		// not by the Proxy func — but resolve still reports it so
		// diagnostics can say "via SOCKS5".
		if cfg.SOCKS5 != "" {
			if u, err := url.Parse(cfg.SOCKS5); err == nil {
				return resolved{proxy: u, via: "manual"}
			}
		}
		return resolved{via: "none"}
	default: // ModeSystem
		sys := detectSystem()
		if sys.Detected {
			if target.Scheme == "http" && sys.HTTP != "" {
				if u, err := url.Parse(sys.HTTP); err == nil {
					return resolved{proxy: u, via: "system"}
				}
			}
			if target.Scheme == "https" && sys.HTTPS != "" {
				if u, err := url.Parse(sys.HTTPS); err == nil {
					return resolved{proxy: u, via: "system"}
				}
			}
			if sys.SOCKS5 != "" {
				if u, err := url.Parse(sys.SOCKS5); err == nil {
					return resolved{proxy: u, via: "system"}
				}
			}
			// A system proxy is configured but has no entry for
			// this scheme: fall through to the env vars rather
			// than going direct silently.
		}
		if p, err := httpproxy.FromEnvironment().ProxyFunc()(target); err == nil && p != nil {
			return resolved{proxy: p, via: "env"}
		}
		return resolved{via: "none"}
	}
}

// proxyFuncFor is the http.Transport Proxy hook under one config.
// The shared transport passes Current() on every request, so
// Configure applies immediately; a TransportFor transport passes
// its fixed config instead.
func proxyFuncFor(cfg Config, req *http.Request) (*url.URL, error) {
	if cfg.Mode == ModeManual && cfg.SOCKS5 != "" && cfg.HTTP == "" {
		// SOCKS5-only: the DialContext routes everything, so the
		// Proxy func must stay nil (a socks5:// URL here would make
		// the transport try an HTTP proxy).
		return nil, nil
	}
	r := resolve(cfg, req.URL)
	if r.proxy == nil {
		return nil, nil
	}
	if r.via == "env" {
		// ProxyFromEnvironment already applies NO_PROXY.
		return r.proxy, nil
	}
	if noProxyMatch(req.URL.Hostname()) {
		return nil, nil
	}
	return r.proxy, nil
}

// buildTransport clones the default transport and wires the proxy.
// getCfg supplies the config per request: Current for the shared
// transport, a fixed config for TransportFor.
func buildTransport(getCfg func() Config) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = func(req *http.Request) (*url.URL, error) { return proxyFuncFor(getCfg(), req) }
	t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return socksDialContextFor(getCfg(), ctx, network, address)
	}
	return t
}

// socksDialContextFor routes dials through SOCKS5 when the given
// config asks for it — and only then: when an HTTP(S) proxy is set,
// the HTTP proxy handles http/https (macOS semantics) and the dial
// to the proxy host itself must be direct. Loopback targets are
// always dialled direct (OAuth listener, Agent API).
func socksDialContextFor(cfg Config, ctx context.Context, network, address string) (net.Conn, error) {
	var d net.Dialer
	if host, _, err := net.SplitHostPort(address); err == nil && isLoopback(host) {
		return d.DialContext(ctx, network, address)
	}
	addr := ""
	switch cfg.Mode {
	case ModeManual:
		if cfg.HTTP == "" {
			addr = cfg.SOCKS5
		}
	case ModeSystem:
		if sys := detectSystem(); sys.Detected && sys.HTTP == "" && sys.HTTPS == "" {
			addr = sys.SOCKS5
		}
	}
	if addr == "" {
		return d.DialContext(ctx, network, address)
	}
	u, err := url.Parse(addr)
	if err != nil {
		return d.DialContext(ctx, network, address)
	}
	dialer, err := proxy.SOCKS5("tcp", u.Host, nil, proxy.Direct)
	if err != nil {
		return d.DialContext(ctx, network, address)
	}
	if cd, ok := dialer.(proxy.ContextDialer); ok {
		return cd.DialContext(ctx, network, address)
	}
	return dialer.Dial(network, address)
}

// detectSystem returns the cached OS proxy detection.
func detectSystem() *SystemProxy {
	sysMu.Lock()
	defer sysMu.Unlock()
	if sysCache == nil {
		sysCache = detectSystemOS()
	}
	return sysCache
}

// RefreshSystem clears the detection cache so the next request
// re-runs scutil (used after the user changes the system proxy).
func RefreshSystem() {
	sysMu.Lock()
	sysCache = nil
	sysMu.Unlock()
}

// DetectSystem exposes the OS detection for the Settings page.
func DetectSystem() *SystemProxy { return detectSystem() }

// isLoopback reports whether host is localhost / loopback. Loopback
// is never proxied — the OAuth loopback listener and the Agent API
// both live there.
func isLoopback(host string) bool {
	if host == "localhost" || host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// noProxyMatch applies NO_PROXY / no_proxy to a hostname (exact,
// suffix ".example.com", "*" and host:port entries). Only consulted
// for explicit (manual/system) proxies; ProxyFromEnvironment does
// its own NO_PROXY handling for the env path.
func noProxyMatch(host string) bool {
	for _, raw := range []string{os.Getenv("NO_PROXY"), os.Getenv("no_proxy")} {
		for _, entry := range strings.Split(raw, ",") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			if entry == "*" {
				return true
			}
			if h, _, err := net.SplitHostPort(entry); err == nil {
				entry = h
			}
			if strings.EqualFold(entry, host) || strings.HasSuffix(strings.ToLower(host), "."+strings.ToLower(entry)) {
				return true
			}
		}
	}
	return false
}
