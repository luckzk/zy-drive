// SPDX-License-Identifier: MPL-2.0
package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/luckzk/zy-drive/internal/cache"
	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
)

// memProvider is an in-memory file store for preview tests: only
// Stat/OpenRead/List matter here; the rest refuse honestly.
type memProvider struct {
	files map[string][]byte
	mtime map[string]time.Time
	opens int
}

func newMemProvider() *memProvider {
	return &memProvider{files: map[string][]byte{}, mtime: map[string]time.Time{}}
}

func (m *memProvider) put(ref string, data []byte, mod time.Time) {
	m.files[ref] = data
	m.mtime[ref] = mod
}

func (m *memProvider) List(ctx context.Context, ref string) ([]domain.Node, error) {
	return nil, fmt.Errorf("mem: List not implemented")
}

func (m *memProvider) Stat(ctx context.Context, ref string) (domain.Node, error) {
	data, ok := m.files[ref]
	if !ok {
		return domain.Node{}, fmt.Errorf("mem: no such file %q", ref)
	}
	return domain.Node{Ref: ref, Path: "/" + ref, Name: ref, Size: int64(len(data)), ModTime: m.mtime[ref]}, nil
}

func (m *memProvider) OpenRead(ctx context.Context, ref string) (io.ReadCloser, error) {
	data, ok := m.files[ref]
	if !ok {
		return nil, fmt.Errorf("mem: no such file %q", ref)
	}
	m.opens++
	return io.NopCloser(strings.NewReader(string(data))), nil
}

func (m *memProvider) Upload(ctx context.Context, ref string, r io.Reader, size int64) (domain.Node, error) {
	return domain.Node{}, fmt.Errorf("mem: Upload not implemented")
}

func (m *memProvider) Mkdir(ctx context.Context, ref string) error {
	return fmt.Errorf("mem: Mkdir not implemented")
}

func (m *memProvider) Rename(ctx context.Context, src, dst string) error {
	return fmt.Errorf("mem: Rename not implemented")
}

func (m *memProvider) Delete(ctx context.Context, ref string) error {
	return fmt.Errorf("mem: Delete not implemented")
}

func (m *memProvider) Caps() domain.Caps { return domain.Caps{} }

var _ storage.Provider = (*memProvider)(nil)

func TestPreviewProxyFullAndRange(t *testing.T) {
	mp := newMemProvider()
	mp.put("a.bin", []byte("0123456789"), time.Now())
	px := newPreviewProxy()
	tok, err := px.mint(mp, nil, "a.bin", 10, "application/octet-stream")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	get := func(rangeHdr string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/preview/"+tok, nil)
		if rangeHdr != "" {
			req.Header.Set("Range", rangeHdr)
		}
		rec := httptest.NewRecorder()
		px.ServeHTTP(rec, req)
		return rec
	}
	// Full download.
	rec := get("")
	if rec.Code != http.StatusOK {
		t.Fatalf("full GET: status %d, want 200", rec.Code)
	}
	if rec.Body.String() != "0123456789" {
		t.Fatalf("full GET: body %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("full GET: Content-Type %q", ct)
	}
	if cl := rec.Header().Get("Content-Length"); cl != "10" {
		t.Fatalf("full GET: Content-Length %q, want 10", cl)
	}
	if ar := rec.Header().Get("Accept-Ranges"); ar != "bytes" {
		t.Fatalf("full GET: Accept-Ranges %q, want bytes", ar)
	}
	// Open-ended range.
	rec = get("bytes=4-")
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("range GET: status %d, want 206", rec.Code)
	}
	if rec.Body.String() != "456789" {
		t.Fatalf("range GET bytes=4-: body %q, want 456789", rec.Body.String())
	}
	if cr := rec.Header().Get("Content-Range"); cr != "bytes 4-9/10" {
		t.Fatalf("range GET: Content-Range %q, want bytes 4-9/10", cr)
	}
	if cl := rec.Header().Get("Content-Length"); cl != "6" {
		t.Fatalf("range GET: Content-Length %q, want 6", cl)
	}
	// Closed range.
	rec = get("bytes=2-5")
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "2345" {
		t.Fatalf("range GET bytes=2-5: status %d body %q", rec.Code, rec.Body.String())
	}
	// Out-of-range start.
	rec = get("bytes=10-")
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("range GET bytes=10-: status %d, want 416", rec.Code)
	}
	// Unknown token.
	req := httptest.NewRequest(http.MethodGet, "/preview/deadbeef", nil)
	rec = httptest.NewRecorder()
	px.ServeHTTP(rec, req)
	if rec.Code != http.StatusGone {
		t.Fatalf("unknown token: status %d, want 410", rec.Code)
	}
	// HEAD: headers, no body.
	req = httptest.NewRequest(http.MethodHead, "/preview/"+tok, nil)
	rec = httptest.NewRecorder()
	px.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD: status %d body len %d", rec.Code, rec.Body.Len())
	}
}

func TestPreviewProxyExpiredToken(t *testing.T) {
	mp := newMemProvider()
	mp.put("a.bin", []byte("x"), time.Now())
	px := newPreviewProxy()
	tok, err := px.mint(mp, nil, "a.bin", 1, "application/octet-stream")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	px.mu.Lock()
	px.tokens[tok].expires = time.Now().Add(-time.Minute)
	px.mu.Unlock()
	req := httptest.NewRequest(http.MethodGet, "/preview/"+tok, nil)
	rec := httptest.NewRecorder()
	px.ServeHTTP(rec, req)
	if rec.Code != http.StatusGone {
		t.Fatalf("expired token: status %d, want 410", rec.Code)
	}
}

func TestParseRange(t *testing.T) {
	cases := []struct {
		h     string
		size  int64
		start int64
		end   int64
		rng   bool
	}{
		{"", 10, 0, 9, false},
		{"bytes=0-", 10, 0, 9, true},
		{"bytes=4-", 10, 4, 9, true},
		{"bytes=2-5", 10, 2, 5, true},
		{"bytes=0-99", 10, 0, 9, true},     // clamped to the file end
		{"bytes=-5", 10, 0, 9, false},      // suffix ranges are not served
		{"bytes=5-2", 10, 0, 9, false},     // inverted
		{"bytes=0-1,4-5", 10, 0, 9, false}, // multi-range not served
		{"bytes=abc-", 10, 0, 9, false},
	}
	for _, c := range cases {
		s, e, r := parseRange(c.h, c.size)
		if s != c.start || e != c.end || r != c.rng {
			t.Fatalf("parseRange(%q, %d) = (%d, %d, %v), want (%d, %d, %v)", c.h, c.size, s, e, r, c.start, c.end, c.rng)
		}
	}
}

func TestPreviewMime(t *testing.T) {
	cases := map[string]string{
		"a.png": "image/png", "a.jpg": "image/jpeg", "a.jpeg": "image/jpeg",
		"a.gif": "image/gif", "a.webp": "image/webp", "a.svg": "image/svg+xml",
		"a.mp4": "video/mp4", "a.webm": "video/webm", "a.mov": "video/quicktime",
		"a.mp3": "audio/mpeg", "a.m4a": "audio/mp4", "a.flac": "audio/flac",
		"a.pdf": "application/pdf", "a.md": "text/markdown", "a.txt": "text/plain",
		"a.go": "text/plain", "a.py": "text/plain", "a.json": "text/plain",
		"a.zip": "application/octet-stream", "a.docx": "application/octet-stream",
		"a": "application/octet-stream",
	}
	for name, want := range cases {
		if got := previewMime(name); got != want {
			t.Fatalf("previewMime(%q) = %q, want %q", name, got, want)
		}
	}
}

// addLocalPreviewMount builds a temp dir with test files and adds
// it as a local mount, returning the app + mount.
func addLocalPreviewMount(t *testing.T, files map[string][]byte) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	a := newTestApp(t)
	ctx := context.Background()
	m, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "本地预览", Provider: domain.ProviderLocal, LocalPath: dir,
	})
	if err != nil {
		t.Fatalf("AddMountWithCredentials(local): %v", err)
	}
	return a, m.ID
}

func TestPreviewURLAndTextViaLocalMount(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01}
	big := make([]byte, previewTextLimit+100)
	for i := range big {
		big[i] = 'a'
	}
	a, mountID := addLocalPreviewMount(t, map[string][]byte{
		"photo.png":  png,
		"notes.txt":  []byte("hello\nworld\n"),
		"big.txt":    big,
		"binary.dat": {0x00, 0x01, 0x02, 'x'},
	})
	ctx := context.Background()

	// Local has no DirectURLer: PreviewURL mints a proxy token.
	res, err := a.PreviewURL(ctx, mountID, "photo.png")
	if err != nil {
		t.Fatalf("PreviewURL: %v", err)
	}
	if res.Kind != "proxy" {
		t.Fatalf("PreviewURL kind = %q, want proxy (local has no direct URL)", res.Kind)
	}
	if !strings.HasPrefix(res.URL, previewSchemeBase) {
		t.Fatalf("PreviewURL = %q, want prefix %q", res.URL, previewSchemeBase)
	}
	if res.Mime != "image/png" || res.Size != int64(len(png)) {
		t.Fatalf("PreviewURL = %+v, want mime image/png size %d", res, len(png))
	}
	// The minted URL actually serves the bytes through the App's
	// preview handler.
	tok := strings.TrimPrefix(res.URL, previewSchemeBase)
	req := httptest.NewRequest(http.MethodGet, "/preview/"+tok, nil)
	rec := httptest.NewRecorder()
	a.PreviewHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("proxy GET minted URL: status %d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), png) {
		t.Fatalf("proxy GET: body %v, want the PNG bytes", rec.Body.Bytes())
	}
	// Folders cannot be previewed.
	if _, err := a.PreviewURL(ctx, mountID, ""); err == nil {
		t.Fatal("PreviewURL(folder): want an error")
	}
	// Text: exact content, not truncated.
	txt, err := a.PreviewText(ctx, mountID, "notes.txt")
	if err != nil {
		t.Fatalf("PreviewText: %v", err)
	}
	if txt.Text != "hello\nworld\n" || txt.Truncated {
		t.Fatalf("PreviewText = %+v", txt)
	}
	// Big text: truncated at exactly the limit.
	bigTxt, err := a.PreviewText(ctx, mountID, "big.txt")
	if err != nil {
		t.Fatalf("PreviewText(big): %v", err)
	}
	if !bigTxt.Truncated || len(bigTxt.Text) != previewTextLimit {
		t.Fatalf("PreviewText(big): truncated=%v len=%d, want true/%d", bigTxt.Truncated, len(bigTxt.Text), previewTextLimit)
	}
	if bigTxt.Size != int64(len(big)) {
		t.Fatalf("PreviewText(big): Size %d, want %d", bigTxt.Size, len(big))
	}
	// Binary is not text.
	if _, err := a.PreviewText(ctx, mountID, "binary.dat"); err == nil {
		t.Fatal("PreviewText(binary): want an error, not garbage")
	}
	// Unknown mount.
	if _, err := a.PreviewURL(ctx, "no-such-mount", "photo.png"); err == nil {
		t.Fatal("PreviewURL(unknown mount): want an error")
	}
}

func TestPreviewCacheEnsureReuseAndInvalidation(t *testing.T) {
	a := newTestApp(t)
	pc := &previewCache{dir: t.TempDir(), db: a.DB}
	ctx := context.Background()
	mod := time.Now().UTC().Truncate(time.Second)
	mp := newMemProvider()
	mp.put("doc.pdf", []byte("pdf-bytes"), mod)

	p1, reused, err := pc.ensure(ctx, mp, nil, "m1", "doc.pdf", 9, mod)
	if err != nil {
		t.Fatalf("ensure(miss): %v", err)
	}
	if reused {
		t.Fatal("ensure(miss): reused must be false")
	}
	if mp.opens != 1 {
		t.Fatalf("ensure(miss): provider opens = %d, want 1", mp.opens)
	}
	fi, err := os.Stat(p1)
	if err != nil {
		t.Fatalf("cached file missing: %v", err)
	}
	if fi.Mode().Perm() != 0o444 {
		t.Fatalf("cached file mode = %o, want 0444 (read-only)", fi.Mode().Perm())
	}
	if e, err := a.DB.LoadPreviewEntry(previewCacheKey("m1", "doc.pdf")); err != nil || e == nil {
		t.Fatalf("manifest row missing: e=%v err=%v", e, err)
	}
	// Second call: cache hit, no second download.
	p2, reused, err := pc.ensure(ctx, mp, nil, "m1", "doc.pdf", 9, mod)
	if err != nil {
		t.Fatalf("ensure(hit): %v", err)
	}
	if !reused || p1 != p2 {
		t.Fatalf("ensure(hit): reused=%v p1=%q p2=%q", reused, p1, p2)
	}
	if mp.opens != 1 {
		t.Fatalf("ensure(hit): provider opens = %d, want still 1", mp.opens)
	}
	// Remote changed (same size, newer mtime): re-download.
	mod2 := mod.Add(time.Hour)
	mp.put("doc.pdf", []byte("pdf-bytes2"), mod2)
	p3, reused, err := pc.ensure(ctx, mp, nil, "m1", "doc.pdf", 10, mod2)
	if err != nil {
		t.Fatalf("ensure(changed): %v", err)
	}
	if reused {
		t.Fatal("ensure(changed): a changed remote file must re-download")
	}
	if p3 != p1 {
		t.Fatalf("ensure(changed): path %q, want the same cache file %q", p3, p1)
	}
	data, _ := os.ReadFile(p3)
	if string(data) != "pdf-bytes2" {
		t.Fatalf("ensure(changed): cached content %q", string(data))
	}
}

func TestPreviewCacheTrimLRU(t *testing.T) {
	a := newTestApp(t)
	pc := &previewCache{dir: t.TempDir(), db: a.DB}
	ctx := context.Background()
	now := time.Now().UTC()
	mp := newMemProvider()
	mp.put("old.bin", make([]byte, 600*1024), now.Add(-time.Hour))
	mp.put("new.bin", make([]byte, 600*1024), now)
	if _, _, err := pc.ensure(ctx, mp, nil, "m1", "old.bin", 600*1024, now.Add(-time.Hour)); err != nil {
		t.Fatalf("ensure old: %v", err)
	}
	if _, _, err := pc.ensure(ctx, mp, nil, "m1", "new.bin", 600*1024, now); err != nil {
		t.Fatalf("ensure new: %v", err)
	}
	// Age the old entry past the 5-minute guard so it is
	// evictable; the new one stays protected.
	e, err := a.DB.LoadPreviewEntry(previewCacheKey("m1", "old.bin"))
	if err != nil || e == nil {
		t.Fatalf("load old entry: %v %v", e, err)
	}
	e.LastAccess = now.Add(-10 * time.Minute)
	if err := a.DB.SavePreviewEntry(e); err != nil {
		t.Fatalf("age old entry: %v", err)
	}
	// Total is ~1.2 MB; a 1 MB cap must evict the old file only.
	pc.mu.Lock()
	err = pc.trimLocked(1)
	pc.mu.Unlock()
	if err != nil {
		t.Fatalf("trim: %v", err)
	}
	if _, err := a.DB.LoadPreviewEntry(previewCacheKey("m1", "old.bin")); err != nil {
		t.Fatalf("trim: load old: %v", err)
	} else {
		// The row must be gone.
		if e2, _ := a.DB.LoadPreviewEntry(previewCacheKey("m1", "old.bin")); e2 != nil {
			t.Fatal("trim: the old entry must be evicted under a 1 MB cap")
		}
	}
	if e2, _ := a.DB.LoadPreviewEntry(previewCacheKey("m1", "new.bin")); e2 == nil {
		t.Fatal("trim: the new entry (accessed just now) must survive")
	}
	if _, err := os.Stat(filepath.Join(pc.dir, previewCacheFilename(previewCacheKey("m1", "old.bin"), "old.bin"))); !os.IsNotExist(err) {
		t.Fatal("trim: the old cached file must be deleted from disk")
	}
}

func TestPreviewCacheReconcileAndClear(t *testing.T) {
	a := newTestApp(t)
	pc := &previewCache{dir: t.TempDir(), db: a.DB}
	ctx := context.Background()
	mod := time.Now().UTC()
	mp := newMemProvider()
	mp.put("a.txt", []byte("aaa"), mod)
	if _, _, err := pc.ensure(ctx, mp, nil, "m1", "a.txt", 3, mod); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	// A manifest row whose file was deleted externally.
	if err := a.DB.SavePreviewEntry(&cache.PreviewEntry{
		Key: previewCacheKey("m1", "gone.txt"), MountID: "m1", Ref: "gone.txt",
		Filename: previewCacheFilename(previewCacheKey("m1", "gone.txt"), "gone.txt"),
		Size:     3, RemoteMtime: mod, LastAccess: mod,
	}); err != nil {
		t.Fatalf("seed gone row: %v", err)
	}
	// A file with no row, and an orphan .part temp.
	orphan := filepath.Join(pc.dir, "orphan.bin")
	if err := os.WriteFile(orphan, []byte("orphan"), 0o644); err != nil {
		t.Fatalf("write orphan: %v", err)
	}
	part := filepath.Join(pc.dir, "x.part-abc")
	if err := os.WriteFile(part, []byte("half"), 0o644); err != nil {
		t.Fatalf("write part: %v", err)
	}
	pc.mu.Lock()
	err := pc.reconcileLocked()
	pc.mu.Unlock()
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if e, _ := a.DB.LoadPreviewEntry(previewCacheKey("m1", "gone.txt")); e != nil {
		t.Fatal("reconcile: the row without a file must be dropped")
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("reconcile: the file without a row must be deleted")
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatal("reconcile: the orphan .part temp must be deleted")
	}
	if _, err := os.Stat(filepath.Join(pc.dir, previewCacheFilename(previewCacheKey("m1", "a.txt"), "a.txt"))); err != nil {
		t.Fatalf("reconcile: the good cached file must survive: %v", err)
	}
	// Info + clear.
	info, err := a.PreviewCacheInfo()
	if err != nil {
		t.Fatalf("PreviewCacheInfo: %v", err)
	}
	_ = info // the App-level Info uses the real cache dir; the manager-level check is below.
	pc.mu.Lock()
	size, count, err := pc.infoLocked()
	pc.mu.Unlock()
	if err != nil || size != 3 || count != 1 {
		t.Fatalf("infoLocked = (%d, %d, %v), want (3, 1, nil)", size, count, err)
	}
	pc.mu.Lock()
	freed, n, err := pc.clearLocked()
	pc.mu.Unlock()
	if err != nil || freed != 3 || n != 1 {
		t.Fatalf("clearLocked = (%d, %d, %v), want (3, 1, nil)", freed, n, err)
	}
	if entries, _ := a.DB.ListPreviewEntries(); len(entries) != 0 {
		t.Fatalf("clear: manifest must be empty, got %d rows", len(entries))
	}
	if files, _ := os.ReadDir(pc.dir); len(files) != 0 {
		t.Fatalf("clear: cache dir must be empty, got %d files", len(files))
	}
}

func TestPreviewCacheKeyAndFilename(t *testing.T) {
	k1 := previewCacheKey("m1", "/a/b.txt")
	k2 := previewCacheKey("m1", "/a/b.txt")
	k3 := previewCacheKey("m2", "/a/b.txt")
	if k1 != k2 || k1 == k3 {
		t.Fatalf("keys: same mount+ref must match, different mounts must not: %q %q %q", k1, k2, k3)
	}
	if len(k1) != 64 {
		t.Fatalf("key len = %d, want 64 hex", len(k1))
	}
	fn := previewCacheFilename(k1, "my file?.docx")
	if !strings.HasPrefix(fn, k1+"_") || !strings.HasSuffix(fn, ".docx") {
		t.Fatalf("filename %q must keep the key prefix and the extension", fn)
	}
	if strings.Contains(fn, " ") || strings.Contains(fn, "?") {
		t.Fatalf("filename %q must sanitize unsafe characters", fn)
	}
	fn2 := previewCacheFilename(k1, "../../etc/passwd")
	if strings.Contains(fn2, "/") && !strings.HasPrefix(fn2, k1+"_") {
		t.Fatalf("filename %q must not escape the cache dir", fn2)
	}
}

func TestPreviewSettingsRoundTrip(t *testing.T) {
	a := newTestApp(t)
	// Point the cache manager at a temp dir so SaveSettings'
	// trim hook never touches the real user cache dir.
	a.pcache = &previewCache{dir: t.TempDir(), db: a.DB}
	s, err := a.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if s.PreviewCacheLimitMB != previewCacheLimitDefaultMB || s.PreviewCacheClearOnExit {
		t.Fatalf("defaults = (%d, %v), want (2048, false)", s.PreviewCacheLimitMB, s.PreviewCacheClearOnExit)
	}
	s.PreviewCacheLimitMB = 1024
	s.PreviewCacheClearOnExit = true
	if err := a.SaveSettings(s); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	s2, err := a.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings after save: %v", err)
	}
	if s2.PreviewCacheLimitMB != 1024 || !s2.PreviewCacheClearOnExit {
		t.Fatalf("round-trip = (%d, %v), want (1024, true)", s2.PreviewCacheLimitMB, s2.PreviewCacheClearOnExit)
	}
	s.PreviewCacheLimitMB = 100
	if err := a.SaveSettings(s); err == nil {
		t.Fatal("SaveSettings(limit 100): want a validation error")
	}
}
