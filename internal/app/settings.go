package app

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/luckzk/zy-drive/internal/domain"
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
// says so instead of pretending: SyncConcurrency /
// SyncSpeedLimitKBps are saved now, but the V0.1 transfer worker is
// fixed at 2-way concurrency with no rate limiter, so they take
// effect only when the engine supports them. SyncConflict has a
// single implemented value ("keep-both", the frozen V0.1 rule in
// internal/sync/conflict.go: newer keeps the name, the other copy is
// kept as a conflict copy); anything else is rejected here.
type Settings struct {
	Theme              string             `json:"theme"`              // light | dark
	Language           string             `json:"language"`           // zh | en
	DownloadDir        string             `json:"downloadDir"`        // default download folder, "" = unset
	SyncCompare        domain.CompareMode `json:"syncCompare"`        // size+time | size | checksum
	SyncConcurrency    int                `json:"syncConcurrency"`    // 1..16, stored; engine not yet wired
	SyncSpeedLimitKBps int                `json:"syncSpeedLimitKBps"` // 0 = unlimited, stored; engine not yet wired
	SyncConflict       string             `json:"syncConflict"`       // keep-both (only implemented value)
}

// DefaultSettings is what a fresh install (or a missing key) gets.
func DefaultSettings() Settings {
	return Settings{
		Theme:              "light",
		Language:           "zh",
		DownloadDir:        "",
		SyncCompare:        domain.CompareSizeTime,
		SyncConcurrency:    4,
		SyncSpeedLimitKBps: 0,
		SyncConflict:       "keep-both",
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
	}
	for k, v := range kv {
		if err := a.DB.SetSetting(k, v); err != nil {
			return err
		}
	}
	return nil
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
