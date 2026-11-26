// SPDX-License-Identifier: MPL-2.0
package drime

import (
	"bytes"
	"io"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/luckzk/zy-drive/internal/cache"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/storage/upload"
)

// newResumableProvider builds a Drime provider whose resume
// sessions persist in a real SQLite database (a temp file), the
// same wiring the desktop app uses: MountID + ResumeStore.
func newResumableProvider(t *testing.T, mock *mockDrime) (*Provider, *httptest.Server, *cache.DB) {
	t.Helper()
	srv := httptest.NewServer(mock)
	db, err := cache.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p, err := New(storage.Config{
		Token:     "test-token",
		MountID:   "mnt-test",
		ResumeStore: db,
		Extra:     map[string]string{"api_base": srv.URL + "/api/v1"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p.(*Provider), srv, db
}

// bigContent builds a deterministic file of the given size (3
// parts at 5 MiB each when size is 12 MiB + tail): every byte
// depends on its offset, so a mixed old/new part fails the
// byte comparison.
func bigContent(size int64) []byte {
	c := make([]byte, size)
	for i := range c {
		c[i] = byte((i * 7) % 251)
	}
	return c
}

// readBack downloads a file through the driver and returns its bytes.
func readBack(t *testing.T, p *Provider, ref string) []byte {
	t.Helper()
	rc, err := p.OpenRead(t.Context(), ref)
	if err != nil {
		t.Fatalf("OpenRead %s: %v", ref, err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll %s: %v", ref, err)
	}
	return got
}

func TestResumeAfterInterrupt(t *testing.T) {
	mock := newMockDrime()
	p, srv, db := newResumableProvider(t, mock)
	defer srv.Close()
	size := int64(3*partSize - 1_000_000) // 3 parts: 5 + 5 + ~4 MiB
	content := bigContent(size)

	// Attempt 1: allow 2 part PUTs, then fail every further PUT
	// (part 3 fails 3x via putPart's re-sign retries).
	mock.failPartPUTsAfter = 2
	_, err := p.UploadResumable(t.Context(), "big.bin", bytes.NewReader(content), size)
	if err == nil {
		t.Fatalf("UploadResumable attempt 1: expected an error (interrupted), got success")
	}
	// The session survived in SQLite: 2 parts done, 10 MiB.
	s, err := db.LoadSession("mnt-test", "big.bin", size)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if s == nil {
		t.Fatalf("LoadSession: no session persisted after the interrupt")
	}
	if s.RemoteID == "" || s.RemoteKey == "" {
		t.Fatalf("LoadSession: session has no server uploadId/key (%q, %q)", s.RemoteID, s.RemoteKey)
	}
	if got := s.DoneBytes(); got != 2*partSize {
		t.Fatalf("DoneBytes: got %d, want %d (2 parts)", got, 2*partSize)
	}
	if got, err := p.ResumeState(t.Context(), "big.bin", size); err != nil || got != 2*partSize {
		t.Fatalf("ResumeState: got (%d, %v), want (%d, nil)", got, err, 2*partSize)
	}

	// Attempt 2: no more failures. Only part 3 may be PUT.
	mock.failPartPUTsAfter = 0
	before := mock.partPUTCount()
	node, err := p.UploadResumable(t.Context(), "big.bin", bytes.NewReader(content), size)
	if err != nil {
		t.Fatalf("UploadResumable attempt 2: %v", err)
	}
	if node.Size != size {
		t.Fatalf("node.Size: got %d, want %d", node.Size, size)
	}
	if got := mock.partPUTCount() - before; got != 1 {
		t.Fatalf("attempt 2 sent %d part PUTs, want 1 (only the missing part)", got)
	}
	// The session row is gone once the upload completes.
	if s, err := db.LoadSession("mnt-test", "big.bin", size); err != nil {
		t.Fatalf("LoadSession after success: %v", err)
	} else if s != nil {
		t.Fatalf("LoadSession after success: session row still present")
	}
	if got := readBack(t, p, "big.bin"); !bytes.Equal(got, content) {
		t.Fatalf("round-trip content mismatch (%d bytes)", len(got))
	}
}

func TestResumeAfterRestart(t *testing.T) {
	mock := newMockDrime()
	p, srv, db := newResumableProvider(t, mock)
	defer srv.Close()
	size := int64(2*partSize + 100) // 3 parts
	content := bigContent(size)

	// Attempt 1 on the first provider instance: 1 part, then fail.
	mock.failPartPUTsAfter = 1
	if _, err := p.UploadResumable(t.Context(), "big.bin", bytes.NewReader(content), size); err == nil {
		t.Fatalf("attempt 1: expected an error, got success")
	}
	// "Restart": a brand-new provider on the SAME store (same
	// mount id). The uploadId lives in SQLite, not in memory.
	mock.failPartPUTsAfter = 0
	p2, err := New(storage.Config{
		Token: "test-token", MountID: "mnt-test", ResumeStore: db,
		Extra: map[string]string{"api_base": srv.URL + "/api/v1"},
	})
	if err != nil {
		t.Fatalf("New (restart): %v", err)
	}
	before := mock.partPUTCount()
	node, err := p2.(*Provider).UploadResumable(t.Context(), "big.bin", bytes.NewReader(content), size)
	if err != nil {
		t.Fatalf("attempt 2 (after restart): %v", err)
	}
	if node.Size != size {
		t.Fatalf("node.Size: got %d, want %d", node.Size, size)
	}
	if got := mock.partPUTCount() - before; got != 2 {
		t.Fatalf("attempt 2 sent %d part PUTs, want 2 (parts 2 and 3)", got)
	}
	if got := readBack(t, p2.(*Provider), "big.bin"); !bytes.Equal(got, content) {
		t.Fatalf("round-trip content mismatch (%d bytes)", len(got))
	}
}

func TestResumeExpiredSession(t *testing.T) {
	mock := newMockDrime()
	p, srv, db := newResumableProvider(t, mock)
	defer srv.Close()
	size := int64(2*partSize + 100)
	content := bigContent(size)

	mock.failPartPUTsAfter = 1
	if _, err := p.UploadResumable(t.Context(), "big.bin", bytes.NewReader(content), size); err == nil {
		t.Fatalf("attempt 1: expected an error, got success")
	}
	s, err := db.LoadSession("mnt-test", "big.bin", size)
	if err != nil || s == nil {
		t.Fatalf("LoadSession: %v, session=%v", err, s)
	}
	// The server forgot the session (expired uploadId): delete it
	// from the mock, exactly as a 404 from get-uploaded-parts.
	mock.expireUpload(s.RemoteID)

	// Attempt 2 must notice the session is gone, create a FRESH
	// one, and re-upload ALL parts in the new session.
	mock.failPartPUTsAfter = 0
	before := mock.partPUTCount()
	node, err := p.UploadResumable(t.Context(), "big.bin", bytes.NewReader(content), size)
	if err != nil {
		t.Fatalf("attempt 2 (expired session): %v", err)
	}
	if node.Size != size {
		t.Fatalf("node.Size: got %d, want %d", node.Size, size)
	}
	if got := mock.partPUTCount() - before; got != 3 {
		t.Fatalf("attempt 2 sent %d part PUTs, want 3 (fresh session, all parts)", got)
	}
	if got := readBack(t, p, "big.bin"); !bytes.Equal(got, content) {
		t.Fatalf("round-trip content mismatch (%d bytes)", len(got))
	}
}

func TestResumeChangedFile(t *testing.T) {
	mock := newMockDrime()
	p, srv, _ := newResumableProvider(t, mock)
	defer srv.Close()
	size := int64(2*partSize + 100)
	content := bigContent(size)

	// Attempt 1: part 1 uploaded, then the attempt fails.
	mock.failPartPUTsAfter = 1
	if _, err := p.UploadResumable(t.Context(), "big.bin", bytes.NewReader(content), size); err == nil {
		t.Fatalf("attempt 1: expected an error, got success")
	}
	// The user edits the file in place between attempts: part 1's
	// bytes change, the size stays exactly the same. A naive
	// resume (trust the server's part list) would mix the OLD
	// part 1 with the new parts into a corrupt file.
	changed := bigContent(size)
	for i := int64(0); i < partSize; i++ {
		changed[i] = ^changed[i]
	}

	// Attempt 2: the MD5 check must catch part 1 and re-upload
	// it, so the result is exactly the CHANGED file.
	mock.failPartPUTsAfter = 0
	before := mock.partPUTCount()
	node, err := p.UploadResumable(t.Context(), "big.bin", bytes.NewReader(changed), size)
	if err != nil {
		t.Fatalf("attempt 2 (changed file): %v", err)
	}
	if node.Size != size {
		t.Fatalf("node.Size: got %d, want %d", node.Size, size)
	}
	if got := mock.partPUTCount() - before; got != 3 {
		t.Fatalf("attempt 2 sent %d part PUTs, want 3 (changed part 1 re-uploaded + parts 2, 3)", got)
	}
	if got := readBack(t, p, "big.bin"); !bytes.Equal(got, changed) {
		t.Fatalf("round-trip content mismatch: got the old part 1 mixed in (%d bytes)", len(got))
	}
}

func TestResumeState(t *testing.T) {
	mock := newMockDrime()
	p, srv, _ := newResumableProvider(t, mock)
	defer srv.Close()
	if got, err := p.ResumeState(t.Context(), "nope.bin", 12345); err != nil || got != 0 {
		t.Fatalf("ResumeState (unknown): got (%d, %v), want (0, nil)", got, err)
	}
	size := int64(2*partSize + 100)
	content := bigContent(size)
	mock.failPartPUTsAfter = 1
	_, _ = p.UploadResumable(t.Context(), "big.bin", bytes.NewReader(content), size)
	if got, err := p.ResumeState(t.Context(), "big.bin", size); err != nil || got != partSize {
		t.Fatalf("ResumeState (1 part): got (%d, %v), want (%d, nil)", got, err, partSize)
	}
}

func TestUploadResumableSmallFile(t *testing.T) {
	mock := newMockDrime()
	p, srv, db := newResumableProvider(t, mock)
	defer srv.Close()
	content := []byte("small file, no multipart")
	node, err := p.UploadResumable(t.Context(), "small.txt", bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatalf("UploadResumable (small): %v", err)
	}
	if node.Size != int64(len(content)) {
		t.Fatalf("node.Size: got %d, want %d", node.Size, len(content))
	}
	// No session was persisted: small files never enter the
	// multipart path.
	if s, err := db.LoadSession("mnt-test", "small.txt", int64(len(content))); err != nil {
		t.Fatalf("LoadSession: %v", err)
	} else if s != nil {
		t.Fatalf("LoadSession: a session row exists for a small file")
	}
	if got := readBack(t, p, "small.txt"); !bytes.Equal(got, content) {
		t.Fatalf("round-trip content mismatch")
	}
}

func TestUploadResumableNoStore(t *testing.T) {
	mock := newMockDrime()
	p, srv := newTestProvider(t, mock) // no MountID, no ResumeStore
	defer srv.Close()
	// Without a store the resumable path falls back to plain
	// Upload: the same file, interrupted, fails with no session
	// kept anywhere (and the plain path aborts server-side).
	size := int64(2*partSize + 100)
	content := bigContent(size)
	mock.failPartPUTsAfter = 1
	if _, err := p.UploadResumable(t.Context(), "big.bin", bytes.NewReader(content), size); err == nil {
		t.Fatalf("UploadResumable (no store): expected an error, got success")
	}
	// And with no failure it still completes through plain Upload.
	mock.failPartPUTsAfter = 0
	node, err := p.UploadResumable(t.Context(), "big.bin", bytes.NewReader(content), size)
	if err != nil {
		t.Fatalf("UploadResumable (no store, clean): %v", err)
	}
	if node.Size != size {
		t.Fatalf("node.Size: got %d, want %d", node.Size, size)
	}
}

// TestSessionStoreRoundTrip pins the upload.Store contract the
// Drime driver relies on: parts + remote id/key survive a save
// and load, and DeleteSession really removes the row.
func TestSessionStoreRoundTrip(t *testing.T) {
	db, err := cache.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer db.Close()
	s, err := upload.NewSession("s1", "a/b.bin", 12_000_000, 5_000_000)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	s.RemoteID = "up-1"
	s.RemoteKey = "uploads/x/x"
	s.MarkUploaded(1, "\"etag-1\"", "0123456789abcdef0123456789abcdef")
	if err := db.SaveSession("mnt-1", s); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	got, err := db.LoadSession("mnt-1", "a/b.bin", 12_000_000)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if got == nil {
		t.Fatalf("LoadSession: nil, want the saved session")
	}
	if got.RemoteID != "up-1" || got.RemoteKey != "uploads/x/x" {
		t.Fatalf("LoadSession: remote (%q, %q), want (up-1, uploads/x/x)", got.RemoteID, got.RemoteKey)
	}
	if got.DoneBytes() != 5_000_000 {
		t.Fatalf("DoneBytes: got %d, want 5000000", got.DoneBytes())
	}
	if got.Parts[0].MD5 != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("Parts[0].MD5: got %q", got.Parts[0].MD5)
	}
	if err := db.DeleteSession("mnt-1", "a/b.bin", 12_000_000); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if got, err := db.LoadSession("mnt-1", "a/b.bin", 12_000_000); err != nil {
		t.Fatalf("LoadSession after delete: %v", err)
	} else if got != nil {
		t.Fatalf("LoadSession after delete: got a session, want nil")
	}
}
