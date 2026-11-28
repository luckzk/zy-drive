// SPDX-License-Identifier: MPL-2.0
// Package mount implements the FUSE volume ~/ZyDrive (v0.19.0, P0):
// one virtual volume, one top-level folder per enabled mount,
// on-demand download into a write-back cache, async upload on
// close. Only Drive, Dropbox and OneDrive are mountable in P0
// (Caps.SupportsMount); the rest never get a switch.
//
// The service never touches the frontend: it only exposes state
// and methods, and internal/app binds them. It runs inside the
// desktop process (a goroutine like LoadMountsAsync and the
// transfer worker), never as a separate binary; the Agent does
// not mount.
//
// macOS notes: the FUSE driver (FUSE-T / macFUSE) can only be
// verified on the user's Mac — see driver.go. Linux builds
// compile and can mount against /dev/fuse, but the macOS paths
// are untested here and labelled as such wherever they matter.
package mount

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/luckzk/zy-drive/internal/cache"
	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/transfer"
)

// Volume / per-mount states. The frontend polls GetMountStatus
// and renders from these — a switch tap must always land the UI
// in a visible state, never a silent no-op.
const (
	// StateUnmounted is "未挂载" (switch off / volume down).
	StateUnmounted = "unmounted"
	// StateMounting is "挂载中" (in progress: spinner, switch disabled).
	StateMounting = "mounting"
	// StateMounted is "已挂载".
	StateMounted = "mounted"
	// StateFailed is "挂载失败" — Error carries the human reason.
	StateFailed = "failed"
	// StateUnmounting is "卸载中" (in progress).
	StateUnmounting = "unmounting"
)

// MountPointName is the volume name shown in Finder and the
// folder name under the user's home directory. The design doc
// (§0–§7) wrote a /Volumes path; the user overruled it on
// 2026-11-27: the mount point is ~/ZyDrive. No /Volumes path
// remains anywhere in this package.
const MountPointName = "ZyDrive"

// DefaultCacheLimitGB is the mount-cache default (user decision,
// 2026-11-27): 5 GB,档位 2 / 5 / 10 / 20 GB, stored in the
// generic settings KV as "mount.cache_gb".
const DefaultCacheLimitGB = 5

// DirListTTL is the per-directory listing cache TTL (user
// decision, 2026-11-27): 30 seconds, lazy per directory —
// visiting a directory refreshes only that directory's listing,
// never a full-volume refresh.
const DirListTTL = 30 * time.Second

// recentGuard is the LRU protection window shared with the
// preview cache: files accessed within this long are never
// evicted.
const recentGuard = 5 * time.Minute

// flushTimeout is the exit flush budget (user decision,
// 2026-11-27): 30 seconds of best-effort upload, then quit —
// whatever is still dirty stays in the cache for next start.
const flushTimeout = 30 * time.Second

// MountPoint returns the volume mount point: ~/ZyDrive.
func MountPoint() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("mount: 找不到用户主目录：%w", err)
	}
	return filepath.Join(home, MountPointName), nil
}

// DriverStatus is the FUSE driver state for Settings / status.
type DriverStatus struct {
	Kind      string `json:"kind"` // "fuse-t" | "macfuse" | "fuse" | "missing"
	Installed bool   `json:"installed"`
	// Detail is the human guidance shown when the driver is
	// missing (install instructions). Empty when installed.
	Detail string `json:"detail,omitempty"`
}

// MountEntryStatus is one mount's state inside GetMountStatus.
type MountEntryStatus struct {
	MountID     string `json:"mountId"`
	DisplayName string `json:"displayName"`
	// FolderName is the top-level folder name under ~/ZyDrive
	// (DisplayName cleaned, deduped with -2/-3). "" while the
	// mount is disabled.
	FolderName string `json:"folderName,omitempty"`
	// Enabled is the switch position (the persisted
	// mount_enabled value), not the live state: a mount can be
	// enabled-but-failed (switch stays on, card red).
	Enabled bool `json:"enabled"`
	// State is one of the State* constants.
	State string `json:"state"`
	// Error is the human reason when State is "failed", in
	// Chinese, never a raw stack.
	Error string `json:"error,omitempty"`
	// Notice is a non-fatal note, e.g. the latest conflict-copy
	// message ("另一台电脑也改了…，你的版本已另存为…").
	Notice string `json:"notice,omitempty"`
	// OpenFiles is how many files of this mount are open right
	// now (handles keep working after the switch is turned
	// off, until closed).
	OpenFiles int `json:"openFiles"`
	// FailedUploads lists the files whose write-back upload
	// failed after all retries (names only, for the card).
	FailedUploads []string `json:"failedUploads,omitempty"`
}

// VolumeStatus is the single return value of GetMountStatus:
// volume state + driver + every mountable mount.
type VolumeStatus struct {
	State      string `json:"state"`
	MountPoint string `json:"mountPoint"`
	// Error is the human reason when State is "failed".
	Error  string             `json:"error,omitempty"`
	Driver DriverStatus       `json:"driver"`
	Mounts []MountEntryStatus `json:"mounts"`
	// OpenFiles is the total open-file count across mounts
	// (what the exit dialog needs).
	OpenFiles int `json:"openFiles"`
}

// CacheInfo is the single return value of GetMountCacheInfo.
type CacheInfo struct {
	SizeBytes  int64  `json:"sizeBytes"`
	FileCount  int    `json:"fileCount"`
	LimitGB    int    `json:"limitGB"`
	DirtyCount int    `json:"dirtyCount"`
	DirtyBytes int64  `json:"dirtyBytes"`
	Dir        string `json:"dir"`
}

// CacheCleared is the single return value of ClearMountCache.
type CacheCleared struct {
	FreedBytes int64 `json:"freedBytes"`
	FileCount  int   `json:"fileCount"`
}

// ExitState is the single return value of GetMountExitState
// (what the exit dialog shows before quitting).
type ExitState struct {
	DirtyCount int   `json:"dirtyCount"`
	DirtyBytes int64 `json:"dirtyBytes"`
	OpenFiles  int   `json:"openFiles"`
}

// FlushResult is the single return value of FlushMountDirty.
type FlushResult struct {
	Flushed   int  `json:"flushed"`
	Remaining int  `json:"remaining"`
	TimedOut  bool `json:"timedOut"`
}

// Deps are the service's inputs from the app layer. The service
// never imports internal/app (that would cycle): the app hands
// it closures instead.
type Deps struct {
	DB *cache.DB
	// ProviderFor returns the live provider for a mount (or a
	// human error when the mount is unknown / not loaded /
	// auth-expired — the app's ProviderFor already phrases
	// those in Chinese).
	ProviderFor func(mountID string) (storage.Provider, error)
	// ListMounts returns the live mounts (copy-on-write
	// structs, safe to read).
	ListMounts func() []*domain.Mount
	// SaveEnabled persists the switch (config map key
	// "mount_enabled") and updates the in-memory Mount.
	SaveEnabled func(mountID string, enabled bool) error
	// Limiter is the shared transfer rate limiter.
	Limiter func() *transfer.Limiter
	// CacheLimitGB reads the "mount.cache_gb" setting (2/5/10/20).
	CacheLimitGB func() int
	// Backend performs the actual FUSE mount. Nil means the
	// default go-fuse backend. Tests inject a stub so the
	// state machine can be exercised without /dev/fuse.
	Backend volumeBackend
	// DetectDriver reports the FUSE driver state. Nil means
	// the real DetectDriver. Tests inject a stub (the VM has
	// no /dev/fuse, so the real check reports "missing").
	DetectDriver func() DriverStatus
}

// Service owns the volume lifecycle, the per-mount switches and
// the state machine. All methods are safe for concurrent use.
type Service struct {
	db           *cache.DB
	providerFor  func(string) (storage.Provider, error)
	listMounts   func() []*domain.Mount
	saveEnabled  func(string, bool) error
	limiter      func() *transfer.Limiter
	cacheLimitGB func() int
	backend      volumeBackend
	detectDriver func() DriverStatus

	mu          sync.Mutex
	volumeState string
	volumeErr   string
	server      volumeServer
	root        *rootNode
	enabled     map[string]bool
	mstate      map[string]string
	merr        map[string]string
	folder      map[string]string
	notices     map[string]string
	listings    map[string]*dirListing
	// pending overlays files created in the mount but not yet
	// uploaded (key mountID+"\x00"+dirRef): they show in
	// listings immediately, although the cloud has no copy yet.
	pending  map[string][]domain.Node
	watchers map[string]*watcher
	stopped  bool
	// mountOp serializes volume mount/unmount operations so two
	// switch taps cannot race a mount against an unmount.
	mountOp sync.Mutex

	cache    *mountCache
	uploader *uploader
}

// dirListing is one cached Provider.List result with its age.
type dirListing struct {
	entries []domain.Node
	at      time.Time
}

// New builds a Service. It does not mount anything and does not
// start any goroutine: Start does that (after the DB is open).
func New(d Deps) *Service {
	if d.Backend == nil {
		d.Backend = goFuseBackend{}
	}
	if d.DetectDriver == nil {
		d.DetectDriver = DetectDriver
	}
	if d.CacheLimitGB == nil {
		d.CacheLimitGB = func() int { return DefaultCacheLimitGB }
	}
	s := &Service{
		db:           d.DB,
		providerFor:  d.ProviderFor,
		listMounts:   d.ListMounts,
		saveEnabled:  d.SaveEnabled,
		limiter:      d.Limiter,
		cacheLimitGB: d.CacheLimitGB,
		backend:      d.Backend,
		detectDriver: d.DetectDriver,
		volumeState:  StateUnmounted,
		enabled:      map[string]bool{},
		mstate:       map[string]string{},
		merr:         map[string]string{},
		folder:       map[string]string{},
		notices:      map[string]string{},
		listings:     map[string]*dirListing{},
		pending:      map[string][]domain.Node{},
		watchers:     map[string]*watcher{},
	}
	s.cache = newMountCache(d.DB, s.cacheLimitGB)
	s.uploader = newUploader(s)
	return s
}

// Start reconciles the cache manifest with disk and starts the
// upload worker. Called once at desktop startup; a failure is
// logged by the caller, never fatal — a stale cache must not
// block the app.
func (s *Service) Start() error {
	if err := s.cache.reconcile(); err != nil {
		return err
	}
	if err := s.cache.trimToLimit(); err != nil {
		return err
	}
	s.uploader.start()
	return nil
}

// Stop halts the watchers and the upload worker. The volume is
// left alone — Shutdown owns the unmount.
func (s *Service) Stop() {
	s.mu.Lock()
	s.stopped = true
	for _, w := range s.watchers {
		w.stop()
	}
	s.watchers = map[string]*watcher{}
	s.mu.Unlock()
	s.uploader.stop()
}

// Status snapshots the volume state, the driver state and every
// mountable mount. It never blocks on the network.
func (s *Service) Status() VolumeStatus {
	drv := s.detectDriver()
	mp, _ := MountPoint()
	s.mu.Lock()
	st := VolumeStatus{
		State:      s.volumeState,
		MountPoint: mp,
		Error:      s.volumeErr,
		Driver:     drv,
	}
	s.mu.Unlock()

	mounts := s.listMounts()
	openByMount := s.cache.openCountByMount()
	failedByMount := s.uploader.failedByMount()
	entries := make([]MountEntryStatus, 0, len(mounts))
	for _, m := range mounts {
		if !m.Caps.SupportsMount {
			continue
		}
		s.mu.Lock()
		state := s.mstate[m.ID]
		errStr := s.merr[m.ID]
		folder := s.folder[m.ID]
		notice := s.notices[m.ID]
		s.mu.Unlock()
		if state == "" {
			// A mount that was enabled in the DB but has not
			// been restored yet (startup still loading) shows
			// as mounting, not as silently unmounted: the
			// switch is on, so the UI must not claim it is off.
			if m.MountEnabled {
				state = StateMounting
			} else {
				state = StateUnmounted
			}
		}
		e := MountEntryStatus{
			MountID:     m.ID,
			DisplayName: m.DisplayName,
			FolderName:  folder,
			// The switch position is the persisted value
			// (m.MountEnabled from the DB): the service's
			// enabled set is the live volume membership and
			// can lag by a mount op.
			Enabled:       m.MountEnabled,
			State:         state,
			Error:         errStr,
			Notice:        notice,
			OpenFiles:     openByMount[m.ID],
			FailedUploads: failedByMount[m.ID],
		}
		entries = append(entries, e)
		st.OpenFiles += e.OpenFiles
	}
	st.Mounts = entries
	return st
}

// SetEnabled flips one mount's switch. Enabling validates first
// (mountable, loaded, authorized) and persists only after the
// checks pass, so a refused tap leaves the switch exactly where
// it was. Disabling removes the mount from the volume at once;
// already-open handles keep working until closed.
func (s *Service) SetEnabled(ctx context.Context, mountID string, enabled bool) error {
	return s.setEnabled(ctx, mountID, enabled, false)
}

// setEnabled is SetEnabled plus the restore flag: during
// RestoreEnabled a mount whose load failed keeps its switch on
// and is recorded failed (card red, human reason), because the
// user already asked for it on — a restore must never silently
// switch a mount off.
func (s *Service) setEnabled(ctx context.Context, mountID string, enabled bool, fromRestore bool) error {
	var m *domain.Mount
	for _, cand := range s.listMounts() {
		if cand.ID == mountID {
			m = cand
			break
		}
	}
	if m == nil {
		return fmt.Errorf("mount: 找不到这个网盘")
	}
	if !enabled {
		return s.disable(mountID)
	}
	if !m.Caps.SupportsMount {
		return fmt.Errorf("mount: 这个网盘暂不支持挂载，P0 只支持 Drive、Dropbox、OneDrive")
	}
	// The mount must be loaded before its folder can appear:
	// an unloaded mount has no provider to list from.
	prov, err := s.providerFor(mountID)
	if err != nil {
		if fromRestore {
			s.failMount(mountID, err.Error())
		}
		return err
	}
	_ = prov
	if m.Status == domain.MountAuthExpired {
		err := fmt.Errorf("mount: 这个网盘的授权已过期，请先在设置 → 网盘里重新授权")
		if fromRestore {
			s.failMount(mountID, err.Error())
		}
		return err
	}
	if m.Status != domain.MountOK {
		reason := m.LoadError
		if reason == "" {
			reason = "还没加载好，请稍后再试"
		}
		err := fmt.Errorf("mount: 这个网盘加载失败：%s", reason)
		if fromRestore {
			s.failMount(mountID, err.Error())
		}
		return err
	}
	if s.saveEnabled != nil {
		if err := s.saveEnabled(mountID, true); err != nil {
			return err
		}
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return fmt.Errorf("mount: 挂载服务已停止")
	}
	s.enabled[mountID] = true
	s.mstate[mountID] = StateMounting
	s.merr[mountID] = ""
	s.folder[mountID] = s.folderNameLocked(m)
	s.mu.Unlock()
	s.startWatcher(mountID)
	s.ensureVolume()
	return nil
}

// failMount records a per-mount failure (switch stays on).
func (s *Service) failMount(mountID, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mstate[mountID] = StateFailed
	s.merr[mountID] = reason
}

// setNotice records a non-fatal per-mount note (e.g. the latest
// conflict-copy message) for the status card.
func (s *Service) setNotice(mountID, notice string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notices[mountID] = notice
}

// disable removes one mount from the volume.
func (s *Service) disable(mountID string) error {
	if s.saveEnabled != nil {
		if err := s.saveEnabled(mountID, false); err != nil {
			return err
		}
	}
	s.mu.Lock()
	folder := s.folder[mountID]
	delete(s.enabled, mountID)
	delete(s.folder, mountID)
	s.mstate[mountID] = StateUnmounted
	s.merr[mountID] = ""
	root := s.root
	empty := len(s.enabled) == 0
	s.mu.Unlock()
	s.stopWatcher(mountID)
	if root != nil && folder != "" {
		// Drop the cached inode so the folder stops resolving;
		// open handles hold their own inode reference and keep
		// working until closed.
		root.EmbeddedInode().RmChild(folder)
	}
	if empty {
		s.maybeUnmount()
	}
	return nil
}

// Retry is "关→开": re-enable a mount (used after a failure).
// It is literally disable + enable, so it re-runs the same
// validation and mount path as a switch tap.
func (s *Service) Retry(ctx context.Context, mountID string) error {
	if err := s.disable(mountID); err != nil {
		return err
	}
	return s.setEnabled(ctx, mountID, true, false)
}

// RestoreEnabled re-applies the persisted switches after the
// startup mount load finishes: every mount with mount_enabled=1
// is enabled again. A failure only fails that mount (switch
// stays on, card red) — it never blocks the others or the
// volume. Call after LoadMountsAsync completes.
func (s *Service) RestoreEnabled(ctx context.Context) {
	for _, m := range s.listMounts() {
		if !m.MountEnabled || !m.Caps.SupportsMount {
			continue
		}
		_ = s.setEnabled(ctx, m.ID, true, true)
	}
}

// Shutdown flushes dirty files (30s best-effort) and unmounts
// the volume. Called after the window loop returns. When files
// are still open the unmount is refused (data in open handles
// must not be cut off) and the error names the count — the
// process exit then leaves a stale volume, which the next start
// force-unmounts before re-mounting.
func (s *Service) Shutdown(ctx context.Context) error {
	s.Stop()
	_ = s.FlushDirty(flushTimeout)
	s.mu.Lock()
	open := s.cache.openTotal()
	server := s.server
	volumeMounted := s.volumeState == StateMounted
	s.mu.Unlock()
	if volumeMounted && open > 0 {
		return fmt.Errorf("mount: 仍有 %d 个文件正打开，挂载卷这次卸不掉（未上传的改动已留在本地缓存，下次启动续传）", open)
	}
	if !volumeMounted || server == nil {
		return nil
	}
	s.mu.Lock()
	s.volumeState = StateUnmounting
	s.mu.Unlock()
	if err := server.Unmount(); err != nil {
		s.mu.Lock()
		s.volumeState = StateFailed
		s.volumeErr = "卸载失败：" + firstLine(err)
		s.mu.Unlock()
		return fmt.Errorf("mount: 卸载失败：%s", firstLine(err))
	}
	s.mu.Lock()
	s.server = nil
	s.root = nil
	s.volumeState = StateUnmounted
	s.volumeErr = ""
	for id := range s.enabled {
		s.mstate[id] = StateUnmounted
	}
	s.mu.Unlock()
	return nil
}

// ExitState reports the dirty + open counts for the exit dialog.
func (s *Service) ExitState() ExitState {
	dirtyCount, dirtyBytes := s.cache.dirtyTotals()
	return ExitState{
		DirtyCount: dirtyCount,
		DirtyBytes: dirtyBytes,
		OpenFiles:  s.cache.openTotal(),
	}
}

// FlushDirty uploads dirty files within timeout (30s at exit).
func (s *Service) FlushDirty(timeout time.Duration) FlushResult {
	return s.uploader.flush(timeout)
}

// CacheInfo reports the mount cache as it stands on disk.
func (s *Service) CacheInfo() (CacheInfo, error) {
	return s.cache.info()
}

// ClearCache deletes every cached file that is neither dirty
// nor pinned (dirty is un-uploaded user data — it stays).
func (s *Service) ClearCache() (CacheCleared, error) {
	return s.cache.clear()
}

// TrimToLimit trims the cache to the current cap (best-effort
// hook for Settings saves).
func (s *Service) TrimToLimit() {
	_ = s.cache.trimToLimit()
}

// ensureVolume mounts the volume when at least one mount is
// enabled. The mount itself runs in a goroutine: the switch tap
// returns at once with state "mounting", and the frontend polls
// Status for the outcome.
func (s *Service) ensureVolume() {
	s.mountOp.Lock()
	s.mu.Lock()
	if s.volumeState == StateMounted {
		// A mount enabled while the volume is already up is
		// mounted at once — there is no mount op to wait for.
		for id := range s.enabled {
			if s.mstate[id] == StateMounting {
				s.mstate[id] = StateMounted
				s.merr[id] = ""
			}
		}
		s.mu.Unlock()
		s.mountOp.Unlock()
		return
	}
	if s.volumeState == StateMounting {
		s.mu.Unlock()
		s.mountOp.Unlock()
		return
	}
	if len(s.enabled) == 0 {
		s.mu.Unlock()
		s.mountOp.Unlock()
		return
	}
	s.volumeState = StateMounting
	s.volumeErr = ""
	s.mu.Unlock()
	s.mountOp.Unlock()
	go s.doMount()
}

// doMount is the mount worker: driver pre-check, stale-volume
// cleanup, then the real FUSE mount.
func (s *Service) doMount() {
	s.mountOp.Lock()
	defer s.mountOp.Unlock()
	s.mu.Lock()
	if len(s.enabled) == 0 || s.volumeState != StateMounting {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	drv := s.detectDriver()
	if !drv.Installed {
		s.mu.Lock()
		s.volumeState = StateFailed
		s.volumeErr = drv.Detail
		for id := range s.enabled {
			s.mstate[id] = StateFailed
			s.merr[id] = drv.Detail
		}
		s.mu.Unlock()
		return
	}
	dir, err := MountPoint()
	if err != nil {
		s.failVolume(err.Error())
		return
	}
	// A previous crash leaves a stale volume at the mount
	// point: force-unmount it first (best effort — if it
	// fails, the mount below fails with a human reason).
	s.forceUnmountStale(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.failVolume(fmt.Sprintf("建挂载点文件夹失败：%s", firstLine(err)))
		return
	}
	root := newRootNode(s)
	server, err := s.backend.mount(dir, root, mountOptions())
	if err != nil {
		human, _ := ClassifyMountError(err)
		s.failVolume(human)
		return
	}
	s.mu.Lock()
	s.server = server
	s.root = root
	s.volumeState = StateMounted
	s.volumeErr = ""
	for id := range s.enabled {
		s.mstate[id] = StateMounted
		s.merr[id] = ""
	}
	s.mu.Unlock()
}

// failVolume records a volume-level mount failure.
func (s *Service) failVolume(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.volumeState = StateFailed
	s.volumeErr = reason
	for id := range s.enabled {
		s.mstate[id] = StateFailed
		s.merr[id] = reason
	}
}

// maybeUnmount unmounts the volume when the last mount was
// disabled — unless files are still open, in which case the
// volume stays up until the last handle closes (see
// afterClose), so an open editor is never cut off mid-save.
func (s *Service) maybeUnmount() {
	s.mu.Lock()
	if s.volumeState != StateMounted || s.server == nil {
		s.mu.Unlock()
		return
	}
	if s.cache.openTotal() > 0 {
		s.mu.Unlock()
		return
	}
	s.volumeState = StateUnmounting
	server := s.server
	s.mu.Unlock()
	go func() {
		if err := server.Unmount(); err != nil {
			s.mu.Lock()
			// The volume is still there: report it instead
			// of pretending it went away.
			s.volumeState = StateMounted
			s.volumeErr = "卸载失败：" + firstLine(err)
			s.mu.Unlock()
			return
		}
		s.mu.Lock()
		s.server = nil
		s.root = nil
		s.volumeState = StateUnmounted
		s.volumeErr = ""
		s.mu.Unlock()
	}()
}

// afterClose runs when a file handle closes: with no mounts
// enabled and no files open, the volume can finally go away.
func (s *Service) afterClose() {
	s.mu.Lock()
	empty := len(s.enabled) == 0
	s.mu.Unlock()
	if empty {
		s.maybeUnmount()
	}
}

// forceUnmountStale tries to unmount whatever a previous crash
// left at dir (best effort, 5s). A stale volume must never wedge
// the next start: if this fails, the mount below reports it.
func (s *Service) forceUnmountStale(dir string) {
	if _, err := os.Stat(dir); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		// macOS has no fusermount; plain umount handles both
		// FUSE-T and macFUSE volumes.
		cmd = exec.CommandContext(ctx, "umount", dir)
	} else {
		cmd = exec.CommandContext(ctx, "fusermount", "-u", dir)
	}
	_ = cmd.Run()
	// Fallback: plain umount on Linux too (a stale mount is
	// not always a FUSE one from this process's view).
	if runtime.GOOS != "darwin" {
		cmd = exec.CommandContext(ctx, "umount", dir)
		_ = cmd.Run()
	}
}

// folderNameLocked computes the top-level folder name for a
// mount (caller holds s.mu): DisplayName cleaned, deduped with
// -2/-3 against the other enabled mounts. The mapping lives
// only in memory — a renamed mount just gets a new folder on
// the next mount.
func (s *Service) folderNameLocked(m *domain.Mount) string {
	base := cleanFolderName(m.DisplayName)
	taken := map[string]bool{}
	for id, f := range s.folder {
		if id != m.ID {
			taken[f] = true
		}
	}
	name := base
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

// cleanFolderName turns a DisplayName into a safe folder name:
// illegal characters (/, :, leading/trailing spaces) become _.
func cleanFolderName(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		switch r {
		case '/', ':', '\x00':
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." {
		out = "网盘"
	}
	return out
}

// limiterValue returns the shared transfer limiter (nil when
// the app never configured one — the cache treats nil as
// unlimited, like the preview cache).
func (s *Service) limiterValue() *transfer.Limiter {
	if s.limiter == nil {
		return nil
	}
	return s.limiter()
}

// mountRootRef returns a mount's root ref ("" for the P0 three:
// their List("") is the account root).
func (s *Service) mountRootRef(mountID string) string {
	for _, m := range s.listMounts() {
		if m.ID == mountID {
			return m.RootRef
		}
	}
	return ""
}

// --- pending overlay (created-but-not-uploaded files) ---

// addPending records a locally created file in its parent's
// listing overlay.
func (s *Service) addPending(mountID, dirRef string, node domain.Node) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := mountID + "\x00" + dirRef
	for _, n := range s.pending[key] {
		if n.Name == node.Name {
			return
		}
	}
	s.pending[key] = append(s.pending[key], node)
}

// removePending drops a pending entry (uploaded, deleted, or
// renamed away).
func (s *Service) removePending(mountID, dirRef, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := mountID + "\x00" + dirRef
	out := s.pending[key][:0]
	for _, n := range s.pending[key] {
		if n.Name != name {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		delete(s.pending, key)
	} else {
		s.pending[key] = out
	}
}

// isPending reports whether a ref is a locally created file
// that has not been uploaded yet.
func (s *Service) isPending(mountID, ref string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.pending[mountID+"\x00"+parentRef(ref)] {
		if n.Ref == ref {
			return true
		}
	}
	return false
}

// movePending moves a pending entry across a rename.
func (s *Service) movePending(mountID, oldDir, oldName, newDir, newName, newRef string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	oldKey, newKey := mountID+"\x00"+oldDir, mountID+"\x00"+newDir
	var moved *domain.Node
	out := s.pending[oldKey][:0]
	for _, n := range s.pending[oldKey] {
		if n.Name == oldName {
			c := n
			c.Ref, c.Path, c.Name = newRef, newRef, newName
			moved = &c
			continue
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		delete(s.pending, oldKey)
	} else {
		s.pending[oldKey] = out
	}
	if moved != nil {
		s.pending[newKey] = append(s.pending[newKey], *moved)
	}
}

// renamePendingPrefix moves pending entries under a renamed
// directory (both the entries' refs and their bucket keys).
func (s *Service) renamePendingPrefix(mountID, oldDir, newDir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := strings.TrimSuffix(oldDir, "/") + "/"
	moved := map[string][]domain.Node{}
	for key, nodes := range s.pending {
		if !strings.HasPrefix(key, mountID+"\x00") {
			continue
		}
		dir := strings.TrimPrefix(key, mountID+"\x00")
		newKey := key
		if dir == oldDir || strings.HasPrefix(dir, prefix) {
			newKey = mountID + "\x00" + newDir + strings.TrimPrefix(dir, oldDir)
		}
		out := make([]domain.Node, 0, len(nodes))
		for _, n := range nodes {
			if n.Ref == oldDir || strings.HasPrefix(n.Ref, prefix) {
				n.Ref = newDir + strings.TrimPrefix(n.Ref, oldDir)
				n.Path = n.Ref
			}
			out = append(out, n)
		}
		moved[newKey] = append(moved[newKey], out...)
		if newKey != key {
			delete(s.pending, key)
		} else {
			s.pending[key] = out
		}
	}
	for k, v := range moved {
		s.pending[k] = v
	}
}

// listDir returns one directory's children, using the 30s
// per-directory listing cache (lazy: only the visited directory
// is refreshed, never the whole volume).
func (s *Service) listDir(mountID, ref string) ([]domain.Node, error) {
	key := mountID + "\x00" + ref
	s.mu.Lock()
	l, cached := s.listings[key]
	pend := append([]domain.Node(nil), s.pending[key]...)
	s.mu.Unlock()
	var entries []domain.Node
	if cached && time.Since(l.at) < DirListTTL {
		entries = append([]domain.Node(nil), l.entries...)
	} else {
		prov, err := s.providerFor(mountID)
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		remote, err := prov.List(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("mount: 列目录失败：%s", firstLine(err))
		}
		s.mu.Lock()
		s.listings[key] = &dirListing{entries: append([]domain.Node(nil), remote...), at: time.Now()}
		// The pending overlay may have grown while listing.
		pend = append([]domain.Node(nil), s.pending[key]...)
		s.mu.Unlock()
		entries = remote
	}
	// Merge the pending overlay: locally created files show
	// at once, although the cloud has no copy yet. A pending
	// name that the remote also lists (upload finished but
	// the overlay not yet cleared) loses to the remote entry.
	if len(pend) > 0 {
		seen := map[string]bool{}
		for _, n := range entries {
			seen[n.Name] = true
		}
		for _, n := range pend {
			if !seen[n.Name] {
				entries = append(entries, n)
			}
		}
	}
	return entries, nil
}

// invalidateDir drops one directory's listing cache (called on
// local mutation and on watcher-detected remote change).
func (s *Service) invalidateDir(mountID, ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.listings, mountID+"\x00"+ref)
}

// statNode stats one node, preferring the parent directory's
// listing (remote + pending overlay) and falling back to
// Provider.Stat.
func (s *Service) statNode(mountID, ref string) (domain.Node, error) {
	if ref == "" || ref == "/" {
		return domain.Node{Ref: ref, Path: "/", IsDir: true, Size: -1}, nil
	}
	parent := parentRef(ref)
	if entries, err := s.listDir(mountID, parent); err == nil {
		for _, n := range entries {
			if n.Ref == ref || n.Name == baseName(ref) {
				return n, nil
			}
		}
	}
	prov, err := s.providerFor(mountID)
	if err != nil {
		return domain.Node{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, err := prov.Stat(ctx, ref)
	if err != nil {
		return domain.Node{}, fmt.Errorf("mount: 找不到这个文件：%s", firstLine(err))
	}
	return n, nil
}

// parentRef returns the parent dir ref of a path-like ref.
// The mount root's ref is "" (not "/"): a top-level file's
// parent is "", so the listing cache key matches the one
// listDir uses for the root.
func parentRef(ref string) string {
	ref = strings.TrimSuffix(ref, "/")
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		if i == 0 {
			return ""
		}
		return ref[:i]
	}
	return ""
}

// baseName returns the last segment of a path-like ref.
func baseName(ref string) string {
	ref = strings.TrimSuffix(ref, "/")
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[i+1:]
	}
	return ref
}

// joinRef joins a dir ref and a child name into a path-like ref.
func joinRef(dir, name string) string {
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" || dir == "/" {
		return "/" + name
	}
	return dir + "/" + name
}

// firstLine returns the first line of an error, trimmed — human
// error text never carries a stack.
func firstLine(err error) string {
	if err == nil {
		return ""
	}
	s := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// startWatcher / stopWatcher manage one mount's change watcher.
func (s *Service) startWatcher(mountID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	if _, ok := s.watchers[mountID]; ok {
		return
	}
	w := newWatcher(s, mountID)
	s.watchers[mountID] = w
	go w.run()
}

func (s *Service) stopWatcher(mountID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w, ok := s.watchers[mountID]; ok {
		w.stop()
		delete(s.watchers, mountID)
	}
}
