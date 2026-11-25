package netproxy

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"syscall"
)

// ErrorKind separates the three network failure shapes the user can
// actually act on. A generic "network problem" hides which one it
// is — and the 2026-11-25 Dropbox case was exactly the first one:
// the user believed a proxy was running on 127.0.0.1:7890, but
// nothing was listening there (curl proved it), so the exchange
// died connecting to the proxy itself, not to Dropbox.
type ErrorKind string

const (
	// KindProxyDown: a proxy was configured, but the proxy itself
	// could not be reached (connection refused / timeout / DNS on
	// the proxy host). Fix: start the proxy software, fix the port.
	KindProxyDown ErrorKind = "proxy-down"
	// KindProxyTarget: the proxy answered, but reaching the target
	// through it failed (CONNECT rejected, TLS failure, proxy 502).
	// Fix: check the proxy node / rules.
	KindProxyTarget ErrorKind = "proxy-target"
	// KindDirect: no proxy was in the path and the direct connection
	// to the target failed (blocked in China). Fix: turn a proxy on.
	KindDirect ErrorKind = "direct"
	// KindOther: anything else (context cancelled, unknown).
	KindOther ErrorKind = "other"
)

// ClassifyError decides which of the three shapes err is, for a
// request to target. proxyAddr is the proxy host:port when one was
// in the path ("" when direct). It never logs or returns secrets.
func ClassifyError(target string, err error) (kind ErrorKind, proxyAddr string) {
	if _, uerr := url.Parse(target); uerr != nil {
		return KindOther, ""
	}
	proxyHost, via := ProxyForURL(target)
	if via == "none" {
		return KindDirect, ""
	}
	// The request went (or tried to go) through proxyHost. Was the
	// failure talking to the proxy, or past it?
	msg := err.Error()
	if dialAddr, ok := proxyConnectDialAddr(err); ok {
		if dialAddr == proxyHost {
			return KindProxyDown, proxyHost
		}
		return KindProxyTarget, proxyHost
	}
	if isConnRefused(err) {
		// A refused dial with a proxy configured: the dial target
		// is the proxy (the transport dials the proxy host, never
		// the target, when a proxy is set) — unless the proxy is
		// SOCKS5, where the dial target is also the proxy.
		return KindProxyDown, proxyHost
	}
	if strings.Contains(msg, "proxyconnect tcp") {
		// CONNECT failed for a reason that is not a refused dial
		// to the proxy itself (e.g. EOF, reset): the proxy was
		// reached, the tunnel was not established.
		return KindProxyTarget, proxyHost
	}
	if strings.Contains(msg, "TLS handshake") || strings.Contains(msg, "tls:") ||
		strings.Contains(msg, "unexpected EOF") || strings.Contains(msg, "connection reset") {
		return KindProxyTarget, proxyHost
	}
	if strings.Contains(msg, "Client.Timeout") || strings.Contains(msg, "i/o timeout") {
		// A timeout through a proxy is ambiguous (slow proxy vs
		// slow target); report it as the target side — the proxy
		// answered the TCP connect or we would have seen refused.
		return KindProxyTarget, proxyHost
	}
	return KindProxyTarget, proxyHost
}

// DescribeError is the one-line log form: provider-agnostic, no
// secrets, no code/token. Example:
// "GET https://api.dropboxapi.com via proxy 127.0.0.1:7890 (env): proxy unreachable (connection refused)".
func DescribeError(target string, err error) string {
	kind, proxyAddr := ClassifyError(target, err)
	_, via := ProxyForURL(target)
	if proxyAddr == "" {
		return fmt.Sprintf("%s direct: %s (kind=%s)", target, shortErr(err), kind)
	}
	return fmt.Sprintf("%s via proxy %s (%s): %s (kind=%s)", target, proxyAddr, via, shortErr(err), kind)
}

func shortErr(err error) string {
	msg := err.Error()
	// Strip the url.Error wrapper ("Post \"https://...\": ") to keep
	// the log line short; the target is already in DescribeError.
	if ue, ok := err.(*url.Error); ok {
		msg = ue.Err.Error()
	}
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return msg
}

// proxyConnectDialAddr extracts the "dial tcp <addr>" address from a
// failed CONNECT error, e.g.:
// "proxyconnect tcp: dial tcp 127.0.0.1:7890: connect: connection refused".
func proxyConnectDialAddr(err error) (string, bool) {
	msg := err.Error()
	idx := strings.Index(msg, "proxyconnect tcp: dial tcp ")
	if idx < 0 {
		return "", false
	}
	rest := msg[idx+len("proxyconnect tcp: dial tcp "):]
	// rest is "<addr>: <reason...>" — the address ends at the first
	// ": " after the host:port.
	if i := strings.Index(rest, ": "); i > 0 {
		return rest[:i], true
	}
	return rest, true
}

func isConnRefused(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.ECONNREFUSED
	}
	return strings.Contains(err.Error(), "connection refused")
}
