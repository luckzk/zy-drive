// SPDX-License-Identifier: MPL-2.0
//go:build darwin

package netproxy

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// detectSystemOS reads the macOS system proxy via `scutil --proxy`.
// This is what the browser uses and what Go's ProxyFromEnvironment
// never sees: System Preferences → Network → Proxies (global proxy
// and PAC both live here, not in env vars).
//
// Sample output (keys vary by configuration):
//
//	<dictionary> {
//	  HTTPEnable : 1
//	  HTTPPort : 7890
//	  HTTPProxy : 127.0.0.1
//	  HTTPSEnable : 1
//	  HTTPSPort : 7890
//	  HTTPSProxy : 127.0.0.1
//	  SOCKSEnable : 0
//	  ProxyAutoConfigEnable : 1
//	  ProxyAutoConfigURLString : http://example.com/proxy.pac
//	}
func detectSystemOS() *SystemProxy {
	out, err := runScutil()
	if err != nil {
		return &SystemProxy{Error: fmt.Sprintf("scutil --proxy 失败: %v", err)}
	}
	kv := parseScutil(out)
	sp := &SystemProxy{}
	if kv["ProxyAutoConfigEnable"] == "1" {
		sp.PACURL = kv["ProxyAutoConfigURLString"]
	}
	// PAC first: when a PAC is on, the HTTP/HTTPS/SOCKS keys in the
	// same dictionary are usually empty — the PAC decides. Fetch it
	// and take its proxy (see FetchPAC: per-URL rules are ignored,
	// only the first PROXY/SOCKS entry is used).
	if sp.PACURL != "" {
		if p, perr := FetchPAC(sp.PACURL); perr != nil {
			sp.PACError = perr.Error()
		} else if p != "" {
			sp.PACProxy = p
			sp.Detected = true
			sp.HTTP, sp.HTTPS = p, p
			if strings.HasPrefix(p, "socks5://") {
				sp.HTTP, sp.HTTPS, sp.SOCKS5 = "", "", p
			}
		}
	}
	if kv["HTTPEnable"] == "1" && kv["HTTPProxy"] != "" {
		sp.HTTP = "http://" + kv["HTTPProxy"] + ":" + portOr(kv["HTTPPort"], "80")
		sp.Detected = true
	}
	if kv["HTTPSEnable"] == "1" && kv["HTTPSProxy"] != "" {
		sp.HTTPS = "http://" + kv["HTTPSProxy"] + ":" + portOr(kv["HTTPSPort"], "443")
		sp.Detected = true
	}
	if kv["SOCKSEnable"] == "1" && kv["SOCKSProxy"] != "" {
		sp.SOCKS5 = "socks5://" + kv["SOCKSProxy"] + ":" + portOr(kv["SOCKSPort"], "1080")
		sp.Detected = true
	}
	return sp
}

func runScutil() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "scutil", "--proxy")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func portOr(port, def string) string {
	if strings.TrimSpace(port) == "" {
		return def
	}
	return strings.TrimSpace(port)
}

// parseScutil turns `scutil --proxy` output into a key/value map.
func parseScutil(out []byte) map[string]string {
	kv := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		k := strings.TrimSpace(parts[0])
		v := strings.TrimSpace(parts[1])
		if k != "" {
			kv[k] = v
		}
	}
	return kv
}
