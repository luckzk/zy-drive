// SPDX-License-Identifier: MPL-2.0
package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/luckzk/zy-drive/internal/cache"
	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/netproxy"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/transfer"
)

// Tests for the v0.18.0 startup load (async, per-mount timeout),
// the per-mount proxy list, the custom proxy-test URL and
// UpdateMountProxy. All network is local (httptest) or a fake
// registry — no real accounts, no real network.

// stubProvider is the minimal Provider a blocking factory can hand
// back: List answers empty, everything else is a no-op success.
type stubProvider struct{}

func (stubProvider) List(context.Context, string) ([]domain.Node, error) { return nil, nil }
func (stubProvider) Stat(context.Context, string) (domain.Node, error) {
	return domain.Node{}, nil
}
func (stubProvider) OpenRead(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (stubProvider) Upload(context.Context, string, io.Reader, int64) (domain.Node, error) {
	return domain.Node{}, nil
}
func (stubProvider) Mkdir(context.Context, string) error          { return nil }
func (stubProvider) Rename(context.Context, string, string) error { return nil }
func (stubProvider) Delete(context.Context, string) error         { return nil }
func (stubProvider) Caps() domain.Caps                            { return domain.Caps{} }

// blockingFactory is a Registry factory that waits until release is
// closed (or done, so a timed-out test never leaks the goroutine).
type blockingFactory struct {
	release chan struct{}
	done    chan struct{}
}

func (f *blockingFactory) New(storage.Config) (storage.Provider, error) {
	select {
	case <-f.release:
		return stubProvider{}, nil
	case <-f.done:
		return stubProvider{}, nil
	}
}

// newTestAppWithRegistry builds an App on a caller-owned registry
// (so tests can register a blocking/fake factory for a real kind
// without touching the process-wide default registry).
func newTestAppWithRegistry(t *testing.T, reg *storage.Registry) *App {
	t.Helper()
	db, err := cache.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a := New(db, reg, transfer.NewQueue(db))
	a.SetSecrets(testSecretsStore(t))
	return a
}

func saveTestMountRow(t *testing.T, a *App, id string, provider domain.ProviderKind) {
	t.Helper()
	now := time.Now().UTC()
	if err := a.DB.SaveMount(&domain.Mount{
		ID: id, DisplayName: "盘 " + id, Provider: provider,
		AccountID: secretIDFor(id), Status: domain.MountOK,
		CreatedAt: now, UpdatedAt: now,
	}, map[string]string{}); err != nil {
		t.Fatalf("SaveMount: %v", err)
	}
}

func TestLoadMountsAsyncDoesNotBlock(t *testing.T) {
	factory := &blockingFactory{release: make(chan struct{}), done: make(chan struct{})}
	t.Cleanup(func() { close(factory.done) })
	reg := storage.NewRegistry()
	reg.Register(domain.ProviderDropbox, factory.New)
	a := newTestAppWithRegistry(t, reg)
	saveTestMountRow(t, a, "mnt-hang", domain.ProviderDropbox)

	go a.LoadMountsAsync()
	// The load must be in progress (not finished, not blocking the
	// caller): poll briefly for the flag, then check the list is
	// still empty while the factory is blocked.
	deadline := time.Now().Add(2 * time.Second)
	loading := false
	for time.Now().Before(deadline) {
		if a.MountsLoading() {
			loading = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !loading {
		t.Fatalf("MountsLoading should be true while the factory is blocked")
	}
	if got := a.ListMounts(); len(got) != 0 {
		t.Fatalf("ListMounts during load: %d mounts, want 0 (not loaded yet)", len(got))
	}
	close(factory.release)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !a.MountsLoading() && len(a.ListMounts()) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if a.MountsLoading() {
		t.Fatalf("MountsLoading should be false after the factory unblocked")
	}
	mounts := a.ListMounts()
	if len(mounts) != 1 || mounts[0].Status != domain.MountOK {
		t.Fatalf("mount after async load: %+v", mounts)
	}
}

func TestLoadMountsSingleMountTimeout(t *testing.T) {
	oldPer, oldTotal := mountLoadTimeoutPerMount, mountLoadTimeoutTotal
	mountLoadTimeoutPerMount = 50 * time.Millisecond
	mountLoadTimeoutTotal = 200 * time.Millisecond
	t.Cleanup(func() {
		mountLoadTimeoutPerMount, mountLoadTimeoutTotal = oldPer, oldTotal
	})
	factory := &blockingFactory{release: make(chan struct{}), done: make(chan struct{})}
	t.Cleanup(func() { close(factory.done) })
	reg := storage.NewRegistry()
	reg.Register(domain.ProviderDropbox, factory.New)
	a := newTestAppWithRegistry(t, reg)
	saveTestMountRow(t, a, "mnt-hang", domain.ProviderDropbox)

	start := time.Now()
	if err := a.LoadMounts(); err != nil {
		t.Fatalf("LoadMounts: %v", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("LoadMounts with a hung mount took %s, want it bounded by the timeout", took)
	}
	mounts := a.ListMounts()
	if len(mounts) != 1 {
		t.Fatalf("ListMounts: %d mounts, want 1 failed mount", len(mounts))
	}
	if mounts[0].Status != domain.MountError {
		t.Fatalf("hung mount status: %q, want %q", mounts[0].Status, domain.MountError)
	}
	if !strings.Contains(mounts[0].LoadError, "超时") {
		t.Fatalf("hung mount LoadError: %q, want it to say 超时", mounts[0].LoadError)
	}
}

func TestProxyListCRUDAndResolve(t *testing.T) {
	a := newTestApp(t)
	// The global setting is process-wide: pin it to direct here
	// and restore it, so the "" resolution below is deterministic
	// and other tests keep the default.
	cur := netproxy.Current()
	t.Cleanup(func() { _ = netproxy.Configure(cur) })
	if err := netproxy.Configure(netproxy.Config{Mode: netproxy.ModeDirect}); err != nil {
		t.Fatalf("Configure(direct): %v", err)
	}

	// Validation.
	if _, err := a.SaveProxy(ProxyEntry{Name: "", Type: "http", Addr: "127.0.0.1:7890"}); err == nil {
		t.Fatalf("empty proxy name must be rejected")
	}
	if _, err := a.SaveProxy(ProxyEntry{Name: "x", Type: "https", Addr: "127.0.0.1:7890"}); err == nil {
		t.Fatalf("proxy type https must be rejected")
	}
	if _, err := a.SaveProxy(ProxyEntry{Name: "x", Type: "http", Addr: "not-an-addr"}); err == nil {
		t.Fatalf("proxy address without a port must be rejected")
	}

	httpEntry, err := a.SaveProxy(ProxyEntry{Name: "节点 A", Type: "http", Addr: "127.0.0.1:7890"})
	if err != nil {
		t.Fatalf("SaveProxy(http): %v", err)
	}
	if !strings.HasPrefix(httpEntry.ID, "proxy-") {
		t.Fatalf("proxy id: %q, want a proxy- prefix", httpEntry.ID)
	}
	if httpEntry.Addr != "http://127.0.0.1:7890" {
		t.Fatalf("http addr: %q, want it normalized to http://127.0.0.1:7890", httpEntry.Addr)
	}
	socksEntry, err := a.SaveProxy(ProxyEntry{Name: "节点 B", Type: "socks5", Addr: "127.0.0.1:1080"})
	if err != nil {
		t.Fatalf("SaveProxy(socks5): %v", err)
	}
	if socksEntry.Addr != "socks5://127.0.0.1:1080" {
		t.Fatalf("socks5 addr: %q, want socks5://127.0.0.1:1080", socksEntry.Addr)
	}
	if got := a.ListProxies(); len(got) != 2 {
		t.Fatalf("ListProxies: %d entries, want 2", len(got))
	}

	// Resolution.
	if cfg := a.resolveMountProxyConfig(""); cfg.Mode != netproxy.ModeDirect {
		t.Fatalf(`resolve(""): mode %q, want the global setting (direct)`, cfg.Mode)
	}
	if cfg := a.resolveMountProxyConfig("direct"); cfg.Mode != netproxy.ModeDirect {
		t.Fatalf(`resolve("direct"): mode %q, want direct`, cfg.Mode)
	}
	if cfg := a.resolveMountProxyConfig(httpEntry.ID); cfg.Mode != netproxy.ModeManual || cfg.HTTP != "http://127.0.0.1:7890" {
		t.Fatalf("resolve(http id): %+v, want manual + that HTTP addr", cfg)
	}
	if cfg := a.resolveMountProxyConfig(socksEntry.ID); cfg.Mode != netproxy.ModeManual || cfg.SOCKS5 != "socks5://127.0.0.1:1080" {
		t.Fatalf("resolve(socks5 id): %+v, want manual + that SOCKS5 addr", cfg)
	}
	// An unknown id falls back to the global setting, never an
	// error: the mount must still build.
	if cfg := a.resolveMountProxyConfig("proxy-nope"); cfg.Mode != netproxy.ModeDirect {
		t.Fatalf("resolve(unknown id): mode %q, want the global fallback (direct)", cfg.Mode)
	}

	// Update in place keeps the id.
	updated, err := a.SaveProxy(ProxyEntry{ID: httpEntry.ID, Name: "节点 A2", Type: "http", Addr: "127.0.0.1:7891"})
	if err != nil || updated.ID != httpEntry.ID || updated.Name != "节点 A2" {
		t.Fatalf("SaveProxy(update): %+v, %v", updated, err)
	}
	if got := a.ListProxies(); len(got) != 2 {
		t.Fatalf("ListProxies after update: %d entries, want 2", len(got))
	}

	// Deleting a referenced proxy is allowed: the mount falls
	// back to the global setting.
	if err := a.DeleteProxy(httpEntry.ID); err != nil {
		t.Fatalf("DeleteProxy: %v", err)
	}
	if cfg := a.resolveMountProxyConfig(httpEntry.ID); cfg.Mode != netproxy.ModeDirect {
		t.Fatalf("resolve(deleted id): mode %q, want the global fallback (direct)", cfg.Mode)
	}
	if got := a.ListProxies(); len(got) != 1 {
		t.Fatalf("ListProxies after delete: %d entries, want 1", len(got))
	}
	if err := a.DeleteProxy("proxy-nope"); err == nil {
		t.Fatalf("deleting an unknown proxy must error")
	}
}

func TestTestMemoKeyIncludesProxy(t *testing.T) {
	creds := mountCredentials{}
	direct := storage.Config{Extra: map[string]string{}, Proxy: netproxy.Config{Mode: netproxy.ModeDirect}}
	manual := storage.Config{Extra: map[string]string{}, Proxy: netproxy.Config{Mode: netproxy.ModeManual, HTTP: "http://127.0.0.1:7890"}}
	if testMemoKey(domain.ProviderDropbox, direct, creds) == testMemoKey(domain.ProviderDropbox, manual, creds) {
		t.Fatalf("testMemoKey must differ when only the proxy changes, or a passed probe would be reused after a proxy change")
	}
}

func TestTestProxyCustomURL(t *testing.T) {
	a := newTestApp(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("teapot"))
	}))
	defer srv.Close()

	// Any HTTP response counts — even a 418. The success Detail is
	// plain language ("正常，直连…") and never carries the status.
	if got := a.TestProxy(srv.URL, ""); !got.OK || !strings.Contains(got.Detail, "正常，直连") {
		t.Fatalf("TestProxy(local server): %+v, want OK + 正常，直连", got)
	}
	if got := a.TestProxy(srv.URL, "direct"); !got.OK {
		t.Fatalf("TestProxy(local server, direct): %+v, want OK", got)
	}
	// Invalid URLs are rejected with a plain-language error.
	for _, bad := range []string{
		"ftp://example.com/x",
		"https://",
		"://bad",
		"example.com/no-scheme",
	} {
		if got := a.TestProxy(bad, ""); got.OK || !strings.Contains(got.Detail, "http(s)") {
			t.Fatalf("TestProxy(%q): %+v, want OK=false + an http(s) hint", bad, got)
		}
	}
	// A proxy id is really used: the target is a public host, so
	// the request must go through the (dead) proxy and fail as
	// proxy-down naming the proxy address. No external traffic
	// happens: the dial to the dead proxy fails first.
	dead, err := a.SaveProxy(ProxyEntry{Name: "死的", Type: "http", Addr: "127.0.0.1:1"})
	if err != nil {
		t.Fatalf("SaveProxy(dead): %v", err)
	}
	got := a.TestProxy("https://example.com/", dead.ID)
	if got.OK || !strings.Contains(got.Detail, "连不上代理 127.0.0.1:1") {
		t.Fatalf("TestProxy(dead proxy id): %+v, want OK=false + 连不上代理 127.0.0.1:1", got)
	}
	// An unknown proxy id resolves to the global setting, so the
	// test still runs instead of erroring.
	if got := a.TestProxy(srv.URL, "proxy-nope"); !got.OK {
		t.Fatalf("TestProxy(unknown proxy id): %+v, want the global fallback to run", got)
	}
}

func TestUpdateMountProxy(t *testing.T) {
	srv := mockDropboxServer(false)
	defer srv.Close()
	dbPath := filepath.Join(t.TempDir(), "app.db")
	a := newTestAppWithDB(t, dbPath)
	ctx := context.Background()
	m, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "我的 Dropbox", Provider: domain.ProviderDropbox,
		AccessToken: "test-token", APIBase: srv.URL,
	})
	if err != nil {
		t.Fatalf("AddMountWithCredentials(dropbox): %v", err)
	}

	// "direct": rebuild + probe through direct, persist proxy_ref.
	got, err := a.UpdateMountProxy(m.ID, "direct")
	if err != nil {
		t.Fatalf("UpdateMountProxy(direct): %v", err)
	}
	if got.ProxyRef != "direct" {
		t.Fatalf("ProxyRef: %q, want direct", got.ProxyRef)
	}
	cfgMap, err := a.persistedConfig(m.ID)
	if err != nil || cfgMap["proxy_ref"] != "direct" {
		t.Fatalf("persisted proxy_ref: %q, %v", cfgMap["proxy_ref"], err)
	}
	// The choice survives a restart.
	a2 := reopenApp(t, dbPath)
	if mounts := a2.ListMounts(); len(mounts) != 1 || mounts[0].ProxyRef != "direct" {
		t.Fatalf("mount after restart: %+v, want ProxyRef direct", mounts)
	}
	if _, err := a2.Browse(ctx, m.ID, ""); err != nil {
		t.Fatalf("Browse after proxy change: %v", err)
	}

	// A proxy-list id: rebuild through it (the mock is loopback,
	// so the probe goes direct past the proxy by design — this
	// asserts the rebuild + persist path, not proxy routing; the
	// routing itself is covered by the netproxy / shttp tests).
	entry, err := a.SaveProxy(ProxyEntry{Name: "节点", Type: "http", Addr: "127.0.0.1:7890"})
	if err != nil {
		t.Fatalf("SaveProxy: %v", err)
	}
	got, err = a.UpdateMountProxy(m.ID, entry.ID)
	if err != nil {
		t.Fatalf("UpdateMountProxy(id): %v", err)
	}
	if got.ProxyRef != entry.ID {
		t.Fatalf("ProxyRef: %q, want %q", got.ProxyRef, entry.ID)
	}

	// An unknown id is rejected before anything is rebuilt.
	if _, err := a.UpdateMountProxy(m.ID, "proxy-nope"); err == nil {
		t.Fatalf("UpdateMountProxy(unknown id) must error")
	}

	// Back to "follow global": the key is removed, not stored as "".
	got, err = a.UpdateMountProxy(m.ID, "")
	if err != nil {
		t.Fatalf("UpdateMountProxy(\"\"): %v", err)
	}
	if got.ProxyRef != "" {
		t.Fatalf("ProxyRef: %q, want empty", got.ProxyRef)
	}
	cfgMap, err = a.persistedConfig(m.ID)
	if err != nil {
		t.Fatalf("persistedConfig: %v", err)
	}
	if _, ok := cfgMap["proxy_ref"]; ok {
		t.Fatalf("persisted proxy_ref should be removed for follow-global, got %q", cfgMap["proxy_ref"])
	}

	// A failed rebuild keeps the old provider: delete the secret
	// row so the rebuild cannot even construct the driver, then
	// the mount must still browse with the old provider.
	if err := a.DB.DeleteSecret(secretIDFor(m.ID)); err != nil {
		t.Fatalf("DeleteSecret: %v", err)
	}
	if _, err := a.UpdateMountProxy(m.ID, "direct"); err == nil {
		t.Fatalf("UpdateMountProxy with no stored credentials must error")
	}
	if _, err := a.ProviderFor(m.ID); err != nil {
		t.Fatalf("old provider must survive a failed proxy change: %v", err)
	}
}

func TestTestMountClearsLoadError(t *testing.T) {
	srv := mockDropboxServer(false)
	defer srv.Close()
	a := newTestApp(t)
	ctx := context.Background()
	m, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "我的 Dropbox", Provider: domain.ProviderDropbox,
		AccessToken: "test-token", APIBase: srv.URL,
	})
	if err != nil {
		t.Fatalf("AddMountWithCredentials(dropbox): %v", err)
	}
	// Simulate a failed startup load (LoadError is runtime-only):
	// the next successful TestMount must clear it.
	a.ListMounts()[0].LoadError = "加载超时（30s 内无响应）"
	got, err := a.TestMount(ctx, m.ID)
	if err != nil {
		t.Fatalf("TestMount: %v", err)
	}
	if got.LoadError != "" {
		t.Fatalf("LoadError after a successful TestMount: %q, want empty", got.LoadError)
	}
}

// TestProxyListPersistsAcrossRestart checks the proxy list itself
// survives a restart (settings-table JSON, no migration).
func TestProxyListPersistsAcrossRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "app.db")
	a := newTestAppWithDB(t, dbPath)
	entry, err := a.SaveProxy(ProxyEntry{Name: "节点", Type: "socks5", Addr: "127.0.0.1:1080"})
	if err != nil {
		t.Fatalf("SaveProxy: %v", err)
	}
	raw, ok := a.getSetting(proxyListKey)
	if !ok {
		t.Fatalf("proxy.list was not saved to the settings table")
	}
	var saved []ProxyEntry
	if err := json.Unmarshal([]byte(raw), &saved); err != nil || len(saved) != 1 || saved[0].ID != entry.ID {
		t.Fatalf("proxy.list JSON: %q, %v", raw, err)
	}
	a2 := reopenApp(t, dbPath)
	got := a2.ListProxies()
	if len(got) != 1 || got[0].ID != entry.ID || got[0].Addr != "socks5://127.0.0.1:1080" {
		t.Fatalf("proxy list after restart: %+v", got)
	}
}
