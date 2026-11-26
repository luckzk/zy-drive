// SPDX-License-Identifier: MPL-2.0
package app

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/netproxy"
	"github.com/luckzk/zy-drive/internal/transfer"
	"github.com/luckzk/zy-drive/internal/version"
)

// Settings is the whole user-facing settings surface, persisted as
// key/value rows in the pre-existing `settings` table (see
// internal/cache/settings.go — no new table was needed). The frontend
// loads it once at startup and saves it from the Settings page; a
// restart reloads the same rows, so theme/language/download folder
// and the sync defaults all survive.
//
// Fields the engine cannot honour yet are still stored, and the UI
// says so instead of pretending: SyncConflict has a single
// implemented value ("keep-both", the frozen V0.1 rule in
// internal/sync/conflict.go: newer keeps the name, the other copy is
// kept as a conflict copy); anything else is rejected here.
// SyncConcurrency / SyncSpeedLimitKBps are wired since V0.9.0:
// ApplyTransferSettings pushes them into the transfer worker and
// the sync runner live (see settings.go).
type Settings struct {
	Theme              string             `json:"theme"`              // light | dark
	Language           string             `json:"language"`           // zh | en
	DownloadDir        string             `json:"downloadDir"`        // default download folder, "" = unset
	SyncCompare        domain.CompareMode `json:"syncCompare"`        // size+time | size | checksum
	SyncConcurrency    int                `json:"syncConcurrency"`    // 1..16, wired into the sync engine + transfer worker (V0.9.0)
	SyncSpeedLimitKBps int                `json:"syncSpeedLimitKBps"` // 0 = unlimited; global cap across all transfers (V0.9.0)
	SyncConflict       string             `json:"syncConflict"`       // keep-both (only implemented value)
	// Proxy (Settings -> Network, 2026-11-25): where outbound
	// traffic goes. system (default) = macOS system proxy when
	// detected, else HTTP(S)_PROXY env vars; manual = the two
	// addresses below; direct = no proxy at all. Saved as
	// proxy.mode / proxy.http / proxy.socks5. Applies
	// immediately on save — the shared netproxy transport reads
	// the current config per request, no restart.
	ProxyMode   string `json:"proxyMode"`   // system | manual | direct
	ProxyHTTP   string `json:"proxyHttp"`   // manual HTTP proxy, "host:port" or full URL
	ProxySOCKS5 string `json:"proxySocks5"` // manual SOCKS5 proxy, "host:port" or full URL
	// Preview cache (Settings → 缓存， v0.9.1): the cap for
	// downloaded "open with system app" copies, in MB — one of
	// 512 / 1024 / 2048 / 5120, default 2048. ClearOnExit wipes
	// the cache when the app quits (mygo has no OnExit hook, so
	// main runs it after App.Run returns); default off.
	PreviewCacheLimitMB     int  `json:"previewCacheLimitMB"`
	PreviewCacheClearOnExit bool `json:"previewCacheClearOnExit"`
}

// DefaultSettings is what a fresh install (or a missing key) gets.
func DefaultSettings() Settings {
	return Settings{
		Theme:                   "light",
		Language:                "zh",
		DownloadDir:             "",
		SyncCompare:             domain.CompareSizeTime,
		SyncConcurrency:         4,
		SyncSpeedLimitKBps:      0,
		SyncConflict:            "keep-both",
		ProxyMode:               string(netproxy.ModeSystem),
		ProxyHTTP:               "",
		ProxySOCKS5:             "",
		PreviewCacheLimitMB:     previewCacheLimitDefaultMB,
		PreviewCacheClearOnExit: false,
	}
}

func (a *App) getSetting(key string) (string, bool) {
	if a.DB == nil {
		return "", false
	}
	v, ok, err := a.DB.GetSetting(key)
	if err != nil {
		return "", false
	}
	return v, ok
}

// GetSettings loads settings, applying defaults for keys that were
// never saved or hold an unknown value. It never errors on a missing
// key: an unset setting is a default, not a failure.
func (a *App) GetSettings() (Settings, error) {
	s := DefaultSettings()
	if a.DB == nil {
		return s, nil
	}
	if v, ok := a.getSetting("theme"); ok && (v == "light" || v == "dark") {
		s.Theme = v
	}
	if v, ok := a.getSetting("language"); ok && (v == "zh" || v == "en") {
		s.Language = v
	}
	if v, ok := a.getSetting("download_dir"); ok {
		s.DownloadDir = v
	}
	if v, ok := a.getSetting("sync.compare"); ok {
		switch domain.CompareMode(v) {
		case domain.CompareSizeTime, domain.CompareSize, domain.CompareChecksum:
			s.SyncCompare = domain.CompareMode(v)
		}
	}
	if v, ok := a.getSetting("sync.concurrency"); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 16 {
			s.SyncConcurrency = n
		}
	}
	if v, ok := a.getSetting("sync.speed_limit_kbps"); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			s.SyncSpeedLimitKBps = n
		}
	}
	if v, ok := a.getSetting("sync.conflict"); ok && v == "keep-both" {
		s.SyncConflict = v
	}
	if v, ok := a.getSetting("proxy.mode"); ok {
		switch v {
		case string(netproxy.ModeSystem), string(netproxy.ModeManual), string(netproxy.ModeDirect):
			s.ProxyMode = v
		}
	}
	if v, ok := a.getSetting("proxy.http"); ok {
		s.ProxyHTTP = v
	}
	if v, ok := a.getSetting("proxy.socks5"); ok {
		s.ProxySOCKS5 = v
	}
	if v, ok := a.getSetting("preview.cache_limit_mb"); ok {
		if n, err := strconv.Atoi(v); err == nil {
			switch n {
			case 512, 1024, 2048, 5120:
				s.PreviewCacheLimitMB = n
			}
		}
	}
	if v, ok := a.getSetting("preview.cache_clear_on_exit"); ok {
		s.PreviewCacheClearOnExit = v == "1"
	}
	return s, nil
}

// SaveSettings validates and persists the whole settings struct.
// The download folder is checked against the real filesystem here:
// it must exist and be a directory, so a typo never gets saved.
func (a *App) SaveSettings(s Settings) error {
	if s.Theme != "light" && s.Theme != "dark" {
		return fmt.Errorf("app: 未知的主题 %q", s.Theme)
	}
	if s.Language != "zh" && s.Language != "en" {
		return fmt.Errorf("app: 未知的语言 %q", s.Language)
	}
	switch s.SyncCompare {
	case domain.CompareSizeTime, domain.CompareSize, domain.CompareChecksum:
	default:
		return fmt.Errorf("app: 未知的比较方式 %q", s.SyncCompare)
	}
	if s.SyncConcurrency < 1 || s.SyncConcurrency > 16 {
		return fmt.Errorf("app: 并发数要在 1–16 之间")
	}
	if s.SyncSpeedLimitKBps < 0 {
		return fmt.Errorf("app: 限速不能是负数")
	}
	if s.SyncConflict != "keep-both" {
		return fmt.Errorf("app: 冲突处理只有“保留双方”已实现，其他策略在后续版本提供")
	}
	switch s.PreviewCacheLimitMB {
	case 0:
		// An empty value (old clients, hand-built structs) is the
		// default, not an error.
		s.PreviewCacheLimitMB = previewCacheLimitDefaultMB
	case 512, 1024, 2048, 5120:
	default:
		return fmt.Errorf("app: 预览缓存上限只能是 500 MB、1 GB、2 GB 或 5 GB")
	}
	// An empty mode (old clients, hand-built structs) is the
	// system default, not an error.
	if s.ProxyMode == "" {
		s.ProxyMode = string(netproxy.ModeSystem)
	}
	proxyCfg := netproxy.Config{Mode: netproxy.Mode(s.ProxyMode), HTTP: s.ProxyHTTP, SOCKS5: s.ProxySOCKS5}
	if err := proxyCfg.Validate(); err != nil {
		return err
	}
	// Normalize manual addresses before persisting, so the
	// stored value is always the canonical "scheme://host:port".
	if s.ProxyMode == string(netproxy.ModeManual) {
		if addr, err := netproxy.NormalizeAddr(s.ProxyHTTP, "http"); err == nil {
			s.ProxyHTTP = addr
			proxyCfg.HTTP = addr
		}
		if addr, err := netproxy.NormalizeAddr(s.ProxySOCKS5, "socks5"); err == nil {
			s.ProxySOCKS5 = addr
			proxyCfg.SOCKS5 = addr
		}
	}
	if dir := strings.TrimSpace(s.DownloadDir); dir != "" {
		if err := checkDownloadDir(dir); err != nil {
			return err
		}
		s.DownloadDir = dir
	} else {
		s.DownloadDir = ""
	}
	if a.DB == nil {
		return fmt.Errorf("app: 数据库还没初始化，设置保存不了")
	}
	kv := map[string]string{
		"theme":                  s.Theme,
		"language":               s.Language,
		"download_dir":           s.DownloadDir,
		"sync.compare":           string(s.SyncCompare),
		"sync.concurrency":       strconv.Itoa(s.SyncConcurrency),
		"sync.speed_limit_kbps":  strconv.Itoa(s.SyncSpeedLimitKBps),
		"sync.conflict":          s.SyncConflict,
		"proxy.mode":             s.ProxyMode,
		"proxy.http":             s.ProxyHTTP,
		"proxy.socks5":           s.ProxySOCKS5,
		"preview.cache_limit_mb": strconv.Itoa(s.PreviewCacheLimitMB),
		"preview.cache_clear_on_exit": func() string {
			if s.PreviewCacheClearOnExit {
				return "1"
			}
			return "0"
		}(),
	}
	for k, v := range kv {
		if err := a.DB.SetSetting(k, v); err != nil {
			return err
		}
	}
	// Apply immediately: the next outbound request already uses
	// the new proxy (Validate passed above, so this cannot fail
	// for a config reason).
	if err := netproxy.Configure(proxyCfg); err != nil {
		return err
	}
	// And the transfer engine: concurrency + speed limit take
	// effect for the next transfer / sync run, no restart.
	a.ApplyTransferSettings()
	// And the preview cache: shrinking the limit trims to the new
	// cap right away (a cache failure here is best-effort — the
	// settings themselves are already saved, and the next cache
	// use trims again).
	if pc, err := a.previewCacheManager(); err == nil {
		pc.mu.Lock()
		_ = pc.trimLocked(s.PreviewCacheLimitMB)
		pc.mu.Unlock()
	}
	return nil
}

// ApplyTransferSettings pushes the stored sync concurrency and
// speed limit into the transfer worker and the shared rate limiter
// (V0.9.0 — these used to be saved but ignored). It is safe to
// call before the worker exists (tests, Agent before startup):
// the limiter is still created, and RunJob reads the same values
// from Settings each run. The limit is a global cap shared by all
// concurrent transfers — one setting, one ceiling — so a 1 MB/s
// limit with 4-way concurrency still totals 1 MB/s, not 4.
//
// Drime caution: Drime 429s can cost a ~15-hour penalty ban, so
// the default concurrency stays 4 (the Settings default) and the
// UI warns before raising it; the engine itself never retries a
// 429 in a loop (see the Drime driver).
func (a *App) ApplyTransferSettings() {
	s, err := a.GetSettings()
	if err != nil {
		return
	}
	conc := s.SyncConcurrency
	if conc < 1 {
		conc = 1
	}
	if conc > 16 {
		conc = 16
	}
	if a.Worker != nil {
		a.Worker.SetConcurrency(conc)
	}
	if a.transferLimiter == nil {
		a.transferLimiter = transfer.NewLimiter(int64(s.SyncSpeedLimitKBps) * 1024)
	} else {
		a.transferLimiter.SetRate(int64(s.SyncSpeedLimitKBps) * 1024)
	}
	if a.Worker != nil {
		a.Worker.SetLimiter(a.transferLimiter)
	}
}

// transferEngineSettings returns the concurrency and limiter the
// sync runner should use for the next run (live Settings values,
// defaults when nothing was ever saved).
func (a *App) transferEngineSettings() (int, *transfer.Limiter) {
	s, err := a.GetSettings()
	if err != nil {
		return 4, nil
	}
	conc := s.SyncConcurrency
	if conc < 1 {
		conc = 1
	}
	if conc > 16 {
		conc = 16
	}
	if a.transferLimiter == nil {
		a.transferLimiter = transfer.NewLimiter(int64(s.SyncSpeedLimitKBps) * 1024)
	}
	return conc, a.transferLimiter
}

// ApplyProxySettings reads the stored proxy settings and applies
// them to the shared transport. Called once at startup (after the
// DB opens, before LoadMounts) so a saved manual proxy is live
// from the first request; SaveSettings applies the same way.
func (a *App) ApplyProxySettings() error {
	s, err := a.GetSettings()
	if err != nil {
		return err
	}
	return netproxy.Configure(netproxy.Config{
		Mode:   netproxy.Mode(s.ProxyMode),
		HTTP:   s.ProxyHTTP,
		SOCKS5: s.ProxySOCKS5,
	})
}

// CheckDownloadDir reports whether a path exists and is a directory.
// The Settings page calls it for instant feedback before saving.
func (a *App) CheckDownloadDir(path string) error {
	return checkDownloadDir(strings.TrimSpace(path))
}

func checkDownloadDir(path string) error {
	if path == "" {
		return fmt.Errorf("app: 请先填一个文件夹路径")
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("app: 请填绝对路径，以 / 开头")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("app: 这个文件夹不存在：%s", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("app: 这不是一个文件夹：%s", path)
	}
	return nil
}

// VersionInfo is the About section: backend + protocol versions
// from the Go build. The frontend version is a frontend constant
// (frontend/src/version.ts); the two are displayed side by side.
type VersionInfo struct {
	Backend  string `json:"backend"`
	Protocol string `json:"protocol"`
	Engine   string `json:"engine"`
}

// GetVersions returns the backend's own versions.
func (a *App) GetVersions() VersionInfo {
	return VersionInfo{
		Backend:  version.AppVersion,
		Protocol: version.ProtocolVersion,
		Engine:   version.EngineVersion,
	}
}

// --- File picker (mygo) ---
//
// Same hook shape as the open-at-login hooks above: internal/app
// must not import mygo, so cmd/desktop installs a hook that calls
// mygo.Dialog.Open (native NSOpenPanel sheet on macOS). Without
// the hook — dev shell, tests, plain browser — the picker reports
// itself unavailable instead of the UI showing a dead button.
func (a *App) SetPickFilesHook(pick func(ctx context.Context) ([]string, error)) {
	a.pickFiles = pick
}

// PickFiles opens the system's file picker and returns the chosen
// absolute paths, or nil when the user cancels.
func (a *App) PickFiles(ctx context.Context) ([]string, error) {
	if a.pickFiles == nil {
		return nil, fmt.Errorf("app: 文件选择器在当前运行方式下不可用（需要在 ZyDrive 桌面窗口里打开）")
	}
	return a.pickFiles(ctx)
}

// --- Open at login (mygo) ---
//
// internal/app must not import mygo (the app layer is UI-shell
// agnostic), so cmd/desktop installs two hooks at startup that call
// mygo.App.OpenAtLogin / SetOpenAtLogin on the main thread. Without
// hooks — dev shell, tests, or any non-mygo run — the setting
// honestly reports itself unavailable instead of pretending to work.
// mygo v0.10.0 (docs/app.md "Start at login"): macOS 13+ needs the
// packaged app bundle (login item); macOS 12 uses a launch agent;
// Linux writes an XDG autostart entry; Windows writes a Run entry.
func (a *App) SetLoginHooks(get func() (bool, error), set func(bool) error) {
	a.loginGet = get
	a.loginSet = set
}

// GetOpenAtLogin reports whether the app starts at login.
func (a *App) GetOpenAtLogin() (bool, error) {
	if a.loginGet == nil {
		return false, fmt.Errorf("app: 开机启动在当前运行方式下不可用（需要打包后的桌面应用）")
	}
	return a.loginGet()
}

// SetOpenAtLogin turns start-at-login on or off.
func (a *App) SetOpenAtLogin(on bool) error {
	if a.loginSet == nil {
		return fmt.Errorf("app: 开机启动在当前运行方式下不可用（需要打包后的桌面应用）")
	}
	return a.loginSet(on)
}
