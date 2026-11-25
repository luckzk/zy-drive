// SPDX-License-Identifier: MPL-2.0
package netproxy

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var pacProxyRe = regexp.MustCompile(`(?i)(PROXY|SOCKS5?)\s+([A-Za-z0-9._-]+:\d+)`)

// FetchPAC downloads a PAC file and extracts its proxy. PAC files are
// JavaScript with per-URL rules (FindProxyForURL); this package does
// not run JS, so it takes the first PROXY entry (or SOCKS5/SOCKS when
// there is no PROXY) and uses it for every host. That covers the
// common PAC shape in China (one fixed proxy for all URLs, e.g.
// `return "PROXY 127.0.0.1:7890"`); a PAC that returns DIRECT for
// some hosts and PROXY for others is NOT honoured per-URL — the
// Settings page says so, and the user can switch to manual proxy
// with the same host:port instead.
func FetchPAC(rawurl string) (string, error) {
	if rawurl == "" {
		return "", fmt.Errorf("PAC 地址为空")
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil},
		Timeout:   5 * time.Second,
	}
	resp, err := client.Get(rawurl)
	if err != nil {
		return "", fmt.Errorf("PAC 文件下载失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("PAC 文件下载失败: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("PAC 文件读取失败: %v", err)
	}
	var socks string
	for _, m := range pacProxyRe.FindAllStringSubmatch(string(body), -1) {
		kind, addr := strings.ToUpper(m[1]), m[2]
		if kind == "PROXY" {
			return "http://" + addr, nil
		}
		if socks == "" {
			socks = "socks5://" + addr
		}
	}
	if socks != "" {
		return socks, nil
	}
	return "", fmt.Errorf("PAC 文件里没有找到 PROXY / SOCKS 地址（只支持固定代理的 PAC，按 URL 分流的 PAC 不支持）")
}
