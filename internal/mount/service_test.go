// SPDX-License-Identifier: MPL-2.0
package mount

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/luckzk/zy-drive/internal/cache"
	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/transfer"
)

// --- fake provider (in-memory, no network) ---

type fakeFile struct {
	data    []byte
	modTime time.Time
	isDir   bool
}

func normRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "/"
	}
	if !strings.HasPrefix(ref, "/") {
		ref = "/" + ref
	}
	if len(ref) > 1 {
		ref = strings.TrimSuffix(ref, "/")
	}
	return ref
}

func parentOf(ref string) string {
	ref = normRef(ref)
	if ref == "/" {
		return "/"
	}
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		if i == 0 {
			return "/"
		}
		return ref[:i]
	}
	return "/"
}

type fakeProvider struct {
	mu            sync.Mutex
	files         map[string]*fakeFile
	uploadErr     error
	openReadErr   error
	listCalls     int
	listDirs      []string // every ref List was called with, in order
	openReadCalls int      // every OpenRead call (zero-download assertions)
	deltaChanges  []domain.Node
	// sizeOverride lets a test claim a remote size different
	// from the stored bytes (e.g. a 3 GB file).
	sizeOverride map[string]int64
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{files: map[string]*fakeFile{"/": {isDir: true, modTime: time.Now()}}, sizeOverride: map[string]int64{}}
}

func (p *fakeProvider) putFile(ref, content string, modTime time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.files[normRef(ref)] = &fakeFile{data: []byte(content), modTime: modTime}
}

func (p *fakeProvider) node(ref string, f *fakeFile) domain.Node {
	name := strings.TrimPrefix(ref, "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	n := domain.Node{
		Ref: ref, Path: ref, Name: name, IsDir: f.isDir,
		Size: -1, ModTime: f.modTime,
	}
	if !f.isDir {
		n.Size = int64(len(f.data))
		if override, ok := p.sizeOverride[ref]; ok {
			n.Size = override
		}
		sum := sha256.Sum256(f.data)
		n.Hash = hex.EncodeToString(sum[:])
		n.HashKind = "sha256"
	}
	return n
}

func (p *fakeProvider) List(ctx context.Context, ref string) ([]domain.Node, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.listCalls++
	p.listDirs = append(p.listDirs, ref)
	dir := normRef(ref)
	var out []domain.Node
	for r, f := range p.files {
		if r == dir || parentOf(r) != dir {
			continue
		}
		out = append(out, p.node(r, f))
	}
	return out, nil
}

func (p *fakeProvider) Stat(ctx context.Context, ref string) (domain.Node, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f, ok := p.files[normRef(ref)]
	if !ok {
		return domain.Node{}, fmt.Errorf("fake: no such file %q", ref)
	}
	return p.node(normRef(ref), f), nil
}

func (p *fakeProvider) OpenRead(ctx context.Context, ref string) (io.ReadCloser, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.openReadCalls++
	if p.openReadErr != nil {
		return nil, p.openReadErr
	}
	f, ok := p.files[normRef(ref)]
	if !ok || f.isDir {
		return nil, fmt.Errorf("fake: no such file %q", ref)
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), f.data...))), nil
}

func (p *fakeProvider) Upload(ctx context.Context, ref string, r io.Reader, size int64) (domain.Node, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.uploadErr != nil {
		return domain.Node{}, p.uploadErr
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return domain.Node{}, err
	}
	key := normRef(ref)
	p.files[key] = &fakeFile{data: data, modTime: time.Now()}
	return p.node(key, p.files[key]), nil
}

func (p *fakeProvider) Mkdir(ctx context.Context, ref string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.files[normRef(ref)] = &fakeFile{isDir: true, modTime: time.Now()}
	return nil
}

func (p *fakeProvider) Rename(ctx context.Context, src, dst string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	src, dst = normRef(src), normRef(dst)
	f, ok := p.files[src]
	if !ok {
		return fmt.Errorf("fake: no such file %q", src)
	}
	delete(p.files, src)
	p.files[dst] = f
	prefix := src + "/"
	for r, child := range p.files {
		if strings.HasPrefix(r, prefix) {
			delete(p.files, r)
			p.files[dst+strings.TrimPrefix(r, src)] = child
		}
	}
	return nil
}

func (p *fakeProvider) Delete(ctx context.Context, ref string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := normRef(ref)
	delete(p.files, key)
	prefix := key + "/"
	for r := range p.files {
		if strings.HasPrefix(r, prefix) {
			delete(p.files, r)
		}
	}
	return nil
}

func (p *fakeProvider) Caps() domain.Caps {
	return domain.Caps{
		CanMkdir: true, CanRename: true, CanDelete: true,
		SupportsMount: true,
	}
}

// Delta implements storage.DeltaSource for the watcher test.
func (p *fakeProvider) Delta(ctx context.Context, cursor string) ([]domain.Node, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]domain.Node(nil), p.deltaChanges...), "cursor-1", nil
}

// --- stub FUSE backend (no /dev/fuse needed) ---

type stubServer struct{ b *stubBackend }

func (s stubServer) Unmount() error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	s.b.unmountCalls++
	s.b.mounted = false
	return s.b.unmountErr
}

type stubBackend struct {
	mu           sync.Mutex
	mounted      bool
	mountCalls   int
	unmountCalls int
	mountErr     error
	unmountErr   error
}

func (b *stubBackend) mount(dir string, root fs.InodeEmbedder, opts *fs.Options) (volumeServer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.mountErr != nil {
		return nil, b.mountErr
	}
	b.mounted = true
	b.mountCalls++
	return stubServer{b: b}, nil
}

// --- test env ---

type testEnv struct {
	t         *testing.T
	db        *cache.DB
	svc       *Service
	backend   *stubBackend
	providers map[string]*fakeProvider
	mounts    []*domain.Mount
	saved     map[string]bool
	cacheGB   int
}

func newTestEnv(t *testing.T, cacheGB int) *testEnv {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmp, "cache"))
	db, err := cache.Open(filepath.Join(tmp, "app.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	env := &testEnv{
		t: t, db: db,
		backend:   &stubBackend{},
		providers: map[string]*fakeProvider{},
		saved:     map[string]bool{},
		cacheGB:   cacheGB,
	}
	mkMount := func(id, name string, kind domain.ProviderKind) *domain.Mount {
		p := newFakeProvider()
		env.providers[id] = p
		m := &domain.Mount{
			ID: id, DisplayName: name, Provider: kind,
			Status: domain.MountOK,
			Caps:   domain.Caps{SupportsMount: true, CanMkdir: true, CanRename: true, CanDelete: true},
		}
		env.mounts = append(env.mounts, m)
		return m
	}
	mkMount("drive-1", "工作 Drive", domain.ProviderDrive)
	mkMount("dropbox-1", "个人 Dropbox", domain.ProviderDropbox)
	env.svc = New(Deps{
		DB: db,
		ProviderFor: func(mountID string) (storage.Provider, error) {
			if p, ok := env.providers[mountID]; ok {
				return p, nil
			}
			return nil, fmt.Errorf("mount: 找不到这个网盘")
		},
		ListMounts: func() []*domain.Mount { return env.mounts },
		SaveEnabled: func(mountID string, enabled bool) error {
			env.saved[mountID] = enabled
			for _, m := range env.mounts {
				if m.ID == mountID {
					m.MountEnabled = enabled
				}
			}
			return nil
		},
		Limiter:      func() *transfer.Limiter { return nil },
		CacheLimitGB: func() int { return env.cacheGB },
		Backend:      env.backend,
		DetectDriver: func() DriverStatus { return DriverStatus{Kind: "fuse", Installed: true} },
	})
	if err := env.svc.Start(); err != nil {
		t.Fatalf("svc.Start: %v", err)
	}
	t.Cleanup(func() { env.svc.Stop() })
	return env
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (e *testEnv) statusOf(mountID string) MountEntryStatus {
	for _, m := range e.svc.Status().Mounts {
		if m.MountID == mountID {
			return m
		}
	}
	e.t.Fatalf("no status entry for %s", mountID)
	return MountEntryStatus{}
}

// TestSwitchEnableMountsVolume checks the happy path: enabling
// a mount mounts the volume (stub backend), enabling a second
// does not re-mount, disabling the last one unmounts it.
func TestSwitchEnableMountsVolume(t *testing.T) {
	env := newTestEnv(t, 5)
	ctx := context.Background()
	if err := env.svc.SetEnabled(ctx, "drive-1", true); err != nil {
		t.Fatalf("enable drive: %v", err)
	}
	waitFor(t, "volume mounted", func() bool { return env.svc.Status().State == StateMounted })
	if got := env.backend.mountCalls; got != 1 {
		t.Fatalf("mountCalls = %d, want 1", got)
	}
	if got := env.statusOf("drive-1"); got.State != StateMounted || got.FolderName != "工作 Drive" {
		t.Fatalf("drive status = %+v, want mounted/工作 Drive", got)
	}
	if err := env.svc.SetEnabled(ctx, "dropbox-1", true); err != nil {
		t.Fatalf("enable dropbox: %v", err)
	}
	waitFor(t, "dropbox mounted", func() bool { return env.statusOf("dropbox-1").State == StateMounted })
	if got := env.backend.mountCalls; got != 1 {
		t.Fatalf("second enable re-mounted the volume: mountCalls = %d", got)
	}
	if err := env.svc.SetEnabled(ctx, "drive-1", false); err != nil {
		t.Fatalf("disable drive: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if env.svc.Status().State != StateMounted {
		t.Fatalf("volume unmounted while dropbox still enabled: %s", env.svc.Status().State)
	}
	if err := env.svc.SetEnabled(ctx, "dropbox-1", false); err != nil {
		t.Fatalf("disable dropbox: %v", err)
	}
	waitFor(t, "volume unmounted", func() bool { return env.svc.Status().State == StateUnmounted })
	if got := env.backend.unmountCalls; got != 1 {
		t.Fatalf("unmountCalls = %d, want 1", got)
	}
	// The switch was persisted through the save closure.
	if env.saved["drive-1"] {
		t.Fatalf("saved drive-1 should be false after disable")
	}
}

// TestSingleMountFailureIsolation: one mount failing (refused
// enable + failed restore) never affects the other mount.
func TestSingleMountFailureIsolation(t *testing.T) {
	env := newTestEnv(t, 5)
	ctx := context.Background()
	// drive-1 loads fine; dropbox-1 failed to load.
	for _, m := range env.mounts {
		if m.ID == "dropbox-1" {
			m.Status = domain.MountError
			m.LoadError = "连不上网盘，检查网络或代理"
		}
	}
	// The provider for a failed mount is gone.
	delete(env.providers, "dropbox-1")
	if err := env.svc.SetEnabled(ctx, "drive-1", true); err != nil {
		t.Fatalf("enable drive: %v", err)
	}
	waitFor(t, "volume mounted", func() bool { return env.svc.Status().State == StateMounted })
	// Enabling the failed mount is refused with a human reason,
	// and drive-1 stays mounted.
	if err := env.svc.SetEnabled(ctx, "dropbox-1", true); err == nil {
		t.Fatalf("enabling a failed mount should be refused")
	} else if !strings.Contains(err.Error(), "连不上网盘") && !strings.Contains(err.Error(), "找不到") {
		t.Fatalf("refusal reason should be human, got: %v", err)
	}
	if got := env.statusOf("drive-1"); got.State != StateMounted {
		t.Fatalf("drive-1 should stay mounted, got %+v", got)
	}
	// Restore path: a switch that was on before a restart
	// records the mount failed (switch stays on) while the
	// healthy mount still mounts.
	env2 := newTestEnv(t, 5)
	for _, m := range env2.mounts {
		if m.ID == "dropbox-1" {
			m.Status = domain.MountError
			m.LoadError = "连不上网盘，检查网络或代理"
			m.MountEnabled = true
		}
		if m.ID == "drive-1" {
			m.MountEnabled = true
		}
	}
	delete(env2.providers, "dropbox-1")
	env2.svc.RestoreEnabled(ctx)
	waitFor(t, "volume mounted after restore", func() bool { return env2.svc.Status().State == StateMounted })
	if got := env2.statusOf("drive-1"); got.State != StateMounted {
		t.Fatalf("drive-1 should mount on restore, got %+v", got)
	}
	if got := env2.statusOf("dropbox-1"); got.State != StateFailed {
		t.Fatalf("dropbox-1 should be failed on restore, got %+v", got)
	} else if got.Enabled != true {
		t.Fatalf("dropbox-1 switch should stay on, got enabled=%v", got.Enabled)
	}
}

// TestDriverMissingFailsVolume: with no FUSE driver the volume
// fails with install guidance (not a raw error) and the mounts
// are recorded failed.
func TestDriverMissingFailsVolume(t *testing.T) {
	env := newTestEnv(t, 5)
	env.svc.detectDriver = func() DriverStatus {
		return DriverStatus{Kind: "missing", Detail: driverInstallGuidance}
	}
	if err := env.svc.SetEnabled(context.Background(), "drive-1", true); err != nil {
		t.Fatalf("enable drive: %v", err)
	}
	waitFor(t, "volume failed", func() bool { return env.svc.Status().State == StateFailed })
	if got := env.svc.Status().Error; !strings.Contains(got, "FUSE-T") {
		t.Fatalf("volume error should guide to FUSE-T, got %q", got)
	}
	if got := env.statusOf("drive-1"); got.State != StateFailed {
		t.Fatalf("drive-1 should be failed, got %+v", got)
	}
}

// TestFolderNameCleaning: illegal chars become _, duplicate
// display names get -2/-3 suffixes.
func TestFolderNameCleaning(t *testing.T) {
	env := newTestEnv(t, 5)
	env.mounts = append(env.mounts,
		&domain.Mount{ID: "m3", DisplayName: "My/Drive:2026", Provider: domain.ProviderDrive, Status: domain.MountOK, Caps: domain.Caps{SupportsMount: true}},
		&domain.Mount{ID: "m4", DisplayName: "工作 Drive", Provider: domain.ProviderDropbox, Status: domain.MountOK, Caps: domain.Caps{SupportsMount: true}},
	)
	env.providers["m3"] = newFakeProvider()
	env.providers["m4"] = newFakeProvider()
	ctx := context.Background()
	for _, id := range []string{"drive-1", "m3", "m4"} {
		if err := env.svc.SetEnabled(ctx, id, true); err != nil {
			t.Fatalf("enable %s: %v", id, err)
		}
	}
	waitFor(t, "volume mounted", func() bool { return env.svc.Status().State == StateMounted })
	if got := env.statusOf("m3").FolderName; got != "My_Drive_2026" {
		t.Fatalf("m3 folder = %q, want My_Drive_2026", got)
	}
	if got := env.statusOf("m4").FolderName; got != "工作 Drive-2" {
		t.Fatalf("m4 folder = %q, want 工作 Drive-2", got)
	}
}

// TestOpenFileBlocksUnmount: disabling the last mount while a
// file is open leaves the volume up; closing the last handle
// unmounts it.
func TestOpenFileBlocksUnmount(t *testing.T) {
	env := newTestEnv(t, 5)
	ctx := context.Background()
	if err := env.svc.SetEnabled(ctx, "drive-1", true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	waitFor(t, "volume mounted", func() bool { return env.svc.Status().State == StateMounted })
	env.svc.cache.AddOpen("drive-1", "/a.txt")
	if err := env.svc.SetEnabled(ctx, "drive-1", false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := env.svc.Status().State; got != StateMounted {
		t.Fatalf("volume should stay mounted with an open file, got %s", got)
	}
	if got := env.svc.Status().OpenFiles; got != 1 {
		t.Fatalf("open files = %d, want 1", got)
	}
	env.svc.cache.RemoveOpen("drive-1", "/a.txt")
	env.svc.afterClose()
	waitFor(t, "volume unmounted after last close", func() bool { return env.svc.Status().State == StateUnmounted })
}

// TestNonMountableMountHasNoSwitch: a mount without
// SupportsMount is refused with a human reason.
func TestNonMountableMountHasNoSwitch(t *testing.T) {
	env := newTestEnv(t, 5)
	env.mounts = append(env.mounts,
		&domain.Mount{ID: "s3-1", DisplayName: "R2", Provider: domain.ProviderS3, Status: domain.MountOK, Caps: domain.Caps{}},
	)
	env.providers["s3-1"] = newFakeProvider()
	if err := env.svc.SetEnabled(context.Background(), "s3-1", true); err == nil {
		t.Fatalf("S3 mount should be refused in P0")
	} else if !strings.Contains(err.Error(), "暂不支持挂载") {
		t.Fatalf("refusal should name the P0 scope, got: %v", err)
	}
}

// TestListDirLazyPerDirectory: the 30s listing cache is per
// directory and lazy — visiting /sub lists only /sub, re-visits
// inside the TTL do not re-list, invalidating the root re-lists
// only the root, and a listing older than DirListTTL re-lists
// on the next visit. Nothing here ever does a full-volume refresh.
func TestListDirLazyPerDirectory(t *testing.T) {
	env := newTestEnv(t, 5)
	p := env.providers["drive-1"]
	mtime := time.Now().Add(-time.Hour).Truncate(time.Second)
	p.putFile("/a.txt", "aaa", mtime)
	p.mu.Lock()
	p.files["/sub"] = &fakeFile{isDir: true, modTime: mtime}
	p.mu.Unlock()
	p.putFile("/sub/b.txt", "bbb", mtime)

	list := func(ref string) {
		t.Helper()
		if _, err := env.svc.listDir("drive-1", ref); err != nil {
			t.Fatalf("listDir(%q): %v", ref, err)
		}
	}
	calls := func(want int, what string) {
		t.Helper()
		if p.listCalls != want {
			t.Fatalf("%s: listCalls = %d, want %d (listed dirs so far: %q)", what, p.listCalls, want, p.listDirs)
		}
	}

	// Visit root, then /sub: exactly one List each.
	list("")
	list("/sub")
	calls(2, "first visit of root + /sub")
	if got := p.listDirs; len(got) != 2 || got[0] != "" || got[1] != "/sub" {
		t.Fatalf("listed dirs = %q, want [\"\" \"/sub\"]", got)
	}
	// Re-visits inside the TTL must not re-list either dir.
	list("")
	list("/sub")
	calls(2, "re-visit inside TTL")
	// Invalidate only the root: the next root visit re-lists,
	// but /sub stays cached (no second List for it).
	env.svc.invalidateDir("drive-1", "")
	list("")
	calls(3, "root visit after invalidating only the root")
	list("/sub")
	calls(3, "/sub visit after invalidating only the root")
	// TTL expiry: backdate the root entry past DirListTTL and
	// the next root visit re-lists.
	env.svc.mu.Lock()
	env.svc.listings["drive-1\x00"].at = time.Now().Add(-DirListTTL - time.Second)
	env.svc.mu.Unlock()
	list("")
	calls(4, "root visit after TTL expiry")
	if got := p.listDirs; len(got) != 4 || got[2] != "" || got[3] != "" {
		t.Fatalf("listed dirs = %q, want [\"\" \"/sub\" \"\" \"\"]", got)
	}
}
