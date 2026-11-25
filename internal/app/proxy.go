// SPDX-License-Identifier: MPL-2.0
package app

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/luckzk/zy-drive/internal/netproxy"
)

// proxy.go — the Settings -> Network support surface: what the OS
// reports, and a one-shot proxy test. The actual routing lives in
// internal/netproxy; this file only exposes it to the frontend.
//
// TestProxy hits a fixed, tiny endpoint (the Dropbox token host —
// any HTTP response, even a 400/404, proves the proxy path works)
// through the CURRENT transport, so the result reflects the saved
// proxy mode, not the env vars the process started with. It never
// sends credentials, codes or tokens.

// SystemProxyInfo is what GetSystemProxy returns for display.
type SystemProxyInfo struct {
	Detected bool   `json:"detected"`
	HTTP     string `json:"http,omitempty"`
	HTTPS    string `json:"https,omitempty"`
	SOCKS5   string `json:"socks5,omitempty"`
	PAC      bool   `json:"pac,omitempty"` // a PAC URL was configured
	// PACNote explains the PAC limitation in plain language when
	// the system proxy came from a PAC file.
	PACNote string `json:"pacNote,omitempty"`
	// Via is the one-line summary the Settings page shows, e.g.
	// "系统代理：http://127.0.0.1:7890" or "未检测到系统代理".
	Via string `json:"via"`
}

// GetSystemProxy reports the proxy macOS (or the OS) currently has
// configured, for the Settings page's "跟随系统代理" line.
func (a *App) GetSystemProxy() SystemProxyInfo {
	sys := netproxy.DetectSystem()
	info := SystemProxyInfo{
		Detected: sys.Detected,
		HTTP:     sys.HTTP,
		HTTPS:    sys.HTTPS,
		SOCKS5:   sys.SOCKS5,
		PAC:      sys.PACURL != "",
	}
	if !sys.Detected {
		info.Via = "未检测到系统代理（将走环境变量里的代理设置，或直连）"
		return info
	}
	addr := sys.HTTP
	if addr == "" {
		addr = sys.HTTPS
	}
	if addr == "" {
		addr = sys.SOCKS5
	}
	if sys.PACURL != "" {
		info.Via = "系统代理来自 PAC（自动配置），当前解析到：" + addr
		info.PACNote = "PAC 文件只解析出第一个代理地址；按网址分流的规则（DIRECT/PROXY 逐条）不支持——如果某个站走了 DIRECT 而 ZyDrive 仍用代理，请改用手动设置。"
	} else {
		info.Via = "系统代理：" + addr
	}
	return info
}

// ProxyTestResult is what TestProxy returns.
type ProxyTestResult struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// TestProxy sends one request through the current transport to the
// Dropbox token host and reports pass/fail with the reason — so the
// user no longer has to guess whether their proxy works. A failure
// distinguishes "cannot reach the proxy itself" (the 2026-11-25
// 7890 case: nothing was listening) from "the proxy cannot reach
// the target" and "direct is blocked".
func (a *App) TestProxy() ProxyTestResult {
	target := "https://api.dropboxapi.com/oauth2/token"
	client := netproxy.NewClient(15)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return ProxyTestResult{OK: false, Detail: fmt.Sprintf("测试请求建不起来：%v", err)}
	}
	resp, err := client.Do(req)
	if err != nil {
		kind, proxyAddr := netproxy.ClassifyError(target, err)
		switch kind {
		case netproxy.KindProxyDown:
			if proxyAddr != "" {
				return ProxyTestResult{OK: false, Detail: fmt.Sprintf("连不上代理 %s：请检查代理软件是否在运行、端口是否正确", proxyAddr)}
			}
			return ProxyTestResult{OK: false, Detail: "连不上代理：请检查代理软件是否在运行、端口是否正确"}
		case netproxy.KindProxyTarget:
			return ProxyTestResult{OK: false, Detail: "代理已连上，但代理连不上 api.dropboxapi.com：请检查代理节点或规则"}
		case netproxy.KindDirect:
			return ProxyTestResult{OK: false, Detail: "直连 api.dropboxapi.com 被挡：请打开代理（跟随系统代理，或手动填代理地址）再试"}
		default:
			return ProxyTestResult{OK: false, Detail: fmt.Sprintf("代理测试失败：%v", err)}
		}
	}
	defer resp.Body.Close()
	addr, via := netproxy.ProxyForURL(target)
	if addr == "" {
		return ProxyTestResult{OK: true, Detail: fmt.Sprintf("直连通（%s），不需要代理", resp.Status)}
	}
	return ProxyTestResult{OK: true, Detail: fmt.Sprintf("经代理 %s 连通（%s，%s）", addr, resp.Status, via)}
}
