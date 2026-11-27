// SPDX-License-Identifier: MPL-2.0
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/netproxy"
)

// proxies.go — the per-mount proxy list (v0.18.0).
//
// Before this, ZyDrive had exactly one proxy setting (the global
// Settings → Network: system / manual / direct) and every driver
// shared it. A mount that needs a different egress — a MEGA account
// that only works through one node, a Dropbox that must go direct —
// had no way to say so, and a dead global proxy took every mount
// down with it. Now the user keeps a named list of proxies and each
// mount picks one: "" follows the global setting, "direct" means no
// proxy, an id means that list entry. The choice is resolved at
// provider-build time (resolveMountProxyConfig) into a
// netproxy.Config that travels in storage.Config.Proxy.
//
// Storage: the list lives in the settings table under "proxy.list"
// as a JSON array — no migration, a missing key is an empty list.
// A mount's choice lives in its config map as "proxy_ref" — no new
// mounts-table column. Deleting a proxy that mounts still reference
// is allowed: those mounts fall back to the global setting (the
// resolution logs the miss) instead of failing to build.

// ProxyEntry is one saved proxy in the proxy list.
type ProxyEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // "http" | "socks5"
	Addr string `json:"addr"` // normalized "scheme://host:port"
}

// proxyListKey is the settings-table key for the proxy list.
const proxyListKey = "proxy.list"

// ListProxies returns the saved proxy list (empty, never nil, when
// none was ever saved).
func (a *App) ListProxies() []ProxyEntry {
	entries, _ := a.loadProxies()
	if entries == nil {
		return []ProxyEntry{}
	}
	return entries
}

// loadProxies reads the proxy list from the settings table. A
// missing key is an empty list; a corrupt value is logged and also
// reads as an empty list — a bad row must never wedge Settings.
func (a *App) loadProxies() ([]ProxyEntry, error) {
	raw, ok := a.getSetting(proxyListKey)
	if !ok || strings.TrimSpace(raw) == "" {
		return []ProxyEntry{}, nil
	}
	var entries []ProxyEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		log.Printf("proxies: proxy.list is corrupt (%v); treating as empty", err)
		return []ProxyEntry{}, err
	}
	if entries == nil {
		entries = []ProxyEntry{}
	}
	return entries, nil
}

func (a *App) saveProxies(entries []ProxyEntry) error {
	if a.DB == nil {
		return fmt.Errorf("app: 本地数据库还没初始化，保存不了代理")
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("app: 保存代理列表：%w", err)
	}
	return a.DB.SetSetting(proxyListKey, string(raw))
}

// SaveProxy creates or updates one proxy-list entry. An empty ID
// mints a new one ("proxy-" + 16 random hex chars, unique in the
// list); an existing ID updates that entry in place. Name is
// required, Type must be "http" or "socks5", and Addr is
// normalized to "scheme://host:port" (http entries use "http",
// socks5 entries use "socks5").
func (a *App) SaveProxy(in ProxyEntry) (ProxyEntry, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return ProxyEntry{}, fmt.Errorf("app: 请给代理起个名字")
	}
	switch in.Type {
	case "http", "socks5":
	default:
		return ProxyEntry{}, fmt.Errorf("app: 代理类型只能是 http 或 socks5")
	}
	if strings.TrimSpace(in.Addr) == "" {
		return ProxyEntry{}, fmt.Errorf("app: 请填代理地址（主机:端口，例如 127.0.0.1:7890）")
	}
	addr, err := netproxy.NormalizeAddr(in.Addr, in.Type)
	if err != nil {
		return ProxyEntry{}, fmt.Errorf("app: %w", err)
	}
	in.Addr = addr
	entries, _ := a.loadProxies()
	if strings.TrimSpace(in.ID) == "" {
		// Mint a unique id.
		for {
			var b [8]byte
			if _, err := rand.Read(b[:]); err != nil {
				return ProxyEntry{}, fmt.Errorf("app: 生成代理 ID：%w", err)
			}
			in.ID = "proxy-" + hex.EncodeToString(b[:])
			dup := false
			for _, e := range entries {
				if e.ID == in.ID {
					dup = true
					break
				}
			}
			if !dup {
				break
			}
		}
		entries = append(entries, in)
	} else {
		found := false
		for i, e := range entries {
			if e.ID == in.ID {
				entries[i] = in
				found = true
				break
			}
		}
		if !found {
			entries = append(entries, in)
		}
	}
	if err := a.saveProxies(entries); err != nil {
		return ProxyEntry{}, err
	}
	return in, nil
}

// DeleteProxy removes one proxy-list entry. A mount that referenced
// it is NOT rejected and NOT changed: the next resolution finds no
// entry for the id and falls back to the global setting (see
// resolveMountProxyConfig), so the mount keeps working on the
// global proxy instead of failing to build.
func (a *App) DeleteProxy(id string) error {
	entries, _ := a.loadProxies()
	kept := entries[:0]
	found := false
	for _, e := range entries {
		if e.ID == id {
			found = true
			continue
		}
		kept = append(kept, e)
	}
	if !found {
		return fmt.Errorf("app: 找不到代理 %q", id)
	}
	return a.saveProxies(kept)
}

// resolveMountProxyConfig turns a mount's ProxyRef into the
// netproxy.Config its driver must use:
//   - ""       → the current global proxy setting;
//   - "direct" → no proxy at all, even if env vars are set;
//   - an id    → that proxy-list entry (http → manual HTTP,
//     socks5 → manual SOCKS5);
//   - unknown id → the global setting + a log line. A mount must
//     never fail to build because its proxy entry went missing
//     (e.g. the entry was deleted, or the list is corrupt).
func (a *App) resolveMountProxyConfig(proxyRef string) netproxy.Config {
	proxyRef = strings.TrimSpace(proxyRef)
	switch proxyRef {
	case "":
		return netproxy.Current()
	case "direct":
		return netproxy.Config{Mode: netproxy.ModeDirect}
	}
	entries, _ := a.loadProxies()
	for _, e := range entries {
		if e.ID != proxyRef {
			continue
		}
		switch e.Type {
		case "http":
			return netproxy.Config{Mode: netproxy.ModeManual, HTTP: e.Addr}
		case "socks5":
			return netproxy.Config{Mode: netproxy.ModeManual, SOCKS5: e.Addr}
		}
	}
	log.Printf("proxies: mount proxy %q not found in the proxy list; falling back to the global proxy setting", proxyRef)
	return netproxy.Current()
}

// UpdateMountProxy changes one mount's proxy choice, rebuilds its
// provider through the new proxy and probes the root — reusing the
// TestMount rebuild path. proxyRef must be "", "direct" or an
// existing proxy-list id. On success the new choice is persisted
// (config map "proxy_ref") and the live provider is swapped; on
// failure the old provider stays exactly as it was and the caller
// gets the error. A successful rebuild also clears LoadError.
func (a *App) UpdateMountProxy(id, proxyRef string) (*domain.Mount, error) {
	h, ok := a.Mount(id)
	if !ok {
		return nil, fmt.Errorf("app: unknown mount %q", id)
	}
	proxyRef = strings.TrimSpace(proxyRef)
	switch proxyRef {
	case "", "direct":
	default:
		entries, _ := a.loadProxies()
		found := false
		for _, e := range entries {
			if e.ID == proxyRef {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("app: 找不到代理 %q，请先在设置里添加它", proxyRef)
		}
	}
	if a.DB == nil {
		return nil, fmt.Errorf("app: 本地数据库还没初始化，改不了代理")
	}
	cfgMap, err := a.persistedConfig(id)
	if err != nil {
		return nil, err
	}
	newCfg := map[string]string{}
	for k, v := range cfgMap {
		newCfg[k] = v
	}
	if proxyRef == "" {
		delete(newCfg, "proxy_ref")
	} else {
		newCfg["proxy_ref"] = proxyRef
	}
	// Rebuild + probe through the NEW proxy before touching the
	// live handle: a proxy that cannot reach the provider must
	// not strand the mount on a dead driver.
	probeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := a.buildSavedMountProvider(probeCtx, id, newCfg)
	if err != nil {
		return nil, err
	}
	// Copy-on-write, like TestMount.
	m := *h.Mount
	m.ProxyRef = proxyRef
	m.Caps = p.Caps()
	m.Status = domain.MountOK
	m.LoadError = ""
	m.Verified = true
	m.UpdatedAt = time.Now().UTC()
	a.setHandle(id, MountHandle{Mount: &m, Provider: p})
	if err := a.DB.SaveMount(&m, newCfg); err != nil {
		return nil, err
	}
	return &m, nil
}
