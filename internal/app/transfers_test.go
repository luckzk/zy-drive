package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/storage/providers/local"
	"github.com/luckzk/zy-drive/internal/transfer"
)

// startTestWorker runs the transfer worker with the app's executor
// (the same code path cmd/desktop wires up) until the test ends.
func startTestWorker(t *testing.T, a *App) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := transfer.NewWorker(a.Queue, a.ExecuteTransfer, 2, nil)
	go w.Start(ctx)
}

// waitTransfersTerminal polls Transfers() until every listed ID is
// completed / failed / cancelled, or fails the test on timeout.
func waitTransfersTerminal(t *testing.T, a *App, ids []string, timeout time.Duration) map[string]*domain.Transfer {
	t.Helper()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		got := map[string]*domain.Transfer{}
		allDone := true
		for _, tr := range a.Transfers() {
			if !want[tr.ID] {
				continue
			}
			got[tr.ID] = tr
			switch tr.Status {
			case domain.TransferCompleted, domain.TransferFailed, domain.TransferCancelled:
			default:
				allDone = false
			}
		}
		if len(got) == len(want) && allDone {
			return got
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("transfers did not reach a terminal state in %s: %+v", timeout, a.Transfers())
	return nil
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func transferIDs(ts []*domain.Transfer) []string {
	ids := make([]string, 0, len(ts))
	for _, tr := range ts {
		ids = append(ids, tr.ID)
	}
	return ids
}

func TestUploadFilesLocalEndToEnd(t *testing.T) {
	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, "a.txt"), "hello")
	writeTestFile(t, filepath.Join(src, "sub", "b.txt"), "nested")
	writeTestFile(t, filepath.Join(src, "sub", "deep", "c.txt"), "deep")

	dst := t.TempDir()
	a := newTestApp(t)
	startTestWorker(t, a)
	ctx := context.Background()
	m, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "本地目标", Provider: domain.ProviderLocal, LocalPath: dst,
	})
	if err != nil {
		t.Fatalf("AddMountWithCredentials(local): %v", err)
	}

	res, err := a.UploadFiles(ctx, m.ID, "", []string{
		filepath.Join(src, "a.txt"),
		filepath.Join(src, "sub"),
	})
	if err != nil {
		t.Fatalf("UploadFiles: %v", err)
	}
	if len(res.Transfers) != 3 {
		t.Fatalf("want 3 transfers, got %d (%+v, skipped %+v)", len(res.Transfers), res.Transfers, res.Skipped)
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("want no skipped files, got %+v", res.Skipped)
	}
	// The queue shows the uploads from enqueue time (before the
	// worker finishes them): Transfers() is the live view.
	if got := a.Transfers(); len(got) < 3 {
		t.Fatalf("Transfers() right after enqueue: got %d, want >= 3", len(got))
	}

	done := waitTransfersTerminal(t, a, transferIDs(res.Transfers), 10*time.Second)
	for _, tr := range done {
		if tr.Status != domain.TransferCompleted {
			t.Fatalf("transfer %+v: status %q, error %q", tr, tr.Status, tr.Error)
		}
		if tr.Kind != domain.TransferUpload || tr.Done != tr.Size {
			t.Fatalf("transfer %+v: kind/done mismatch", tr)
		}
	}
	// Directory recursion kept relative paths; contents match.
	for rel, want := range map[string]string{
		"a.txt":          "hello",
		"sub/b.txt":      "nested",
		"sub/deep/c.txt": "deep",
	} {
		got, err := os.ReadFile(filepath.Join(dst, rel))
		if err != nil {
			t.Fatalf("read dst %s: %v", rel, err)
		}
		if string(got) != want {
			t.Fatalf("dst %s: got %q, want %q", rel, got, want)
		}
	}
}

func TestUploadFilesSkipsOversizeAndMissing(t *testing.T) {
	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, "small.txt"), "12345")        // 5 B
	writeTestFile(t, filepath.Join(src, "big.txt"), "12345678901234567890") // 20 B

	dst := t.TempDir()
	a := newTestApp(t)
	startTestWorker(t, a)
	// Hand-inserted mount with a 10-byte cap: the real local driver
	// for storage, a tiny Caps.MaxFileSize for the cap check.
	lp, err := local.New(storage.Config{Extra: map[string]string{"root": dst}})
	if err != nil {
		t.Fatalf("local.New: %v", err)
	}
	a.mounts["m-small"] = MountHandle{
		Mount: &domain.Mount{
			ID: "m-small", DisplayName: "小盘", Provider: domain.ProviderLocal,
			RootRef: dst, Caps: domain.Caps{MaxFileSize: 10}, Status: domain.MountOK,
		},
		Provider: lp,
	}

	ctx := context.Background()
	res, err := a.UploadFiles(ctx, "m-small", "", []string{
		filepath.Join(src, "small.txt"),
		filepath.Join(src, "big.txt"),
		filepath.Join(src, "missing.txt"),
	})
	if err != nil {
		t.Fatalf("UploadFiles: %v", err)
	}
	if len(res.Transfers) != 1 {
		t.Fatalf("want 1 transfer, got %d (%+v)", len(res.Transfers), res.Transfers)
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("want 2 skipped, got %+v", res.Skipped)
	}
	var bigReason, missingReason string
	for _, s := range res.Skipped {
		switch {
		case strings.HasSuffix(s.Path, "big.txt"):
			bigReason = s.Reason
		case strings.HasSuffix(s.Path, "missing.txt"):
			missingReason = s.Reason
		}
	}
	if !strings.Contains(bigReason, "太大") || !strings.Contains(bigReason, "10 B") {
		t.Fatalf("oversize reason should name the limit, got %q", bigReason)
	}
	if missingReason == "" {
		t.Fatalf("missing file must be skipped with a reason")
	}

	done := waitTransfersTerminal(t, a, transferIDs(res.Transfers), 10*time.Second)
	for _, tr := range done {
		if tr.Status != domain.TransferCompleted {
			t.Fatalf("small transfer: status %q, error %q", tr.Status, tr.Error)
		}
	}
	got, err := os.ReadFile(filepath.Join(dst, "small.txt"))
	if err != nil || string(got) != "12345" {
		t.Fatalf("dst small.txt: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "big.txt")); !os.IsNotExist(err) {
		t.Fatalf("oversize file must never land on the target")
	}
}

func TestUploadFilesUnknownMount(t *testing.T) {
	a := newTestApp(t)
	if _, err := a.UploadFiles(context.Background(), "nope", "", []string{"/tmp/x"}); err == nil {
		t.Fatalf("unknown mount must fail the whole call")
	}
}

// failUploadProvider is a stub driver whose Upload always fails, so a
// queued upload can be observed reaching "failed" with its error.
type failUploadProvider struct{ caps domain.Caps }

func (failUploadProvider) List(ctx context.Context, ref string) ([]domain.Node, error) {
	return nil, fmt.Errorf("failUpload: list not implemented")
}
func (failUploadProvider) Stat(ctx context.Context, ref string) (domain.Node, error) {
	return domain.Node{}, fmt.Errorf("failUpload: stat not implemented")
}
func (failUploadProvider) OpenRead(ctx context.Context, ref string) (io.ReadCloser, error) {
	return nil, fmt.Errorf("failUpload: open not implemented")
}
func (failUploadProvider) Upload(ctx context.Context, ref string, r io.Reader, size int64) (domain.Node, error) {
	return domain.Node{}, fmt.Errorf("failUpload: 目标拒绝写入")
}
func (failUploadProvider) Mkdir(ctx context.Context, ref string) error { return nil }
func (failUploadProvider) Rename(ctx context.Context, src, dst string) error {
	return fmt.Errorf("failUpload: rename not implemented")
}
func (failUploadProvider) Delete(ctx context.Context, ref string) error {
	return fmt.Errorf("failUpload: delete not implemented")
}
func (f failUploadProvider) Caps() domain.Caps { return f.caps }

func TestUploadFilesFailedTransferKeepsError(t *testing.T) {
	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, "a.txt"), "hello")
	a := newTestApp(t)
	startTestWorker(t, a)
	a.mounts["m-fail"] = MountHandle{
		Mount: &domain.Mount{
			ID: "m-fail", DisplayName: "坏盘", Provider: domain.ProviderLocal,
			RootRef: src, Status: domain.MountOK,
		},
		Provider: failUploadProvider{},
	}
	res, err := a.UploadFiles(context.Background(), "m-fail", "", []string{filepath.Join(src, "a.txt")})
	if err != nil {
		t.Fatalf("UploadFiles: %v", err)
	}
	if len(res.Transfers) != 1 {
		t.Fatalf("want 1 transfer, got %d", len(res.Transfers))
	}
	done := waitTransfersTerminal(t, a, transferIDs(res.Transfers), 10*time.Second)
	for _, tr := range done {
		if tr.Status != domain.TransferFailed {
			t.Fatalf("transfer status: got %q, want failed", tr.Status)
		}
		if !strings.Contains(tr.Error, "拒绝写入") {
			t.Fatalf("failed transfer must keep its error, got %q", tr.Error)
		}
	}
}

func TestJoinRef(t *testing.T) {
	for k, want := range map[[2]string]string{
		{"", "a.txt"}:              "a.txt",
		{"/", "a.txt"}:             "a.txt",
		{"/photos", "a.txt"}:       "/photos/a.txt",
		{"/photos/", "sub/b.txt"}:  "/photos/sub/b.txt",
		{"bucket/prefix", "a.txt"}: "bucket/prefix/a.txt",
	} {
		if got := joinRef(k[0], k[1]); got != want {
			t.Fatalf("joinRef(%q, %q) = %q, want %q", k[0], k[1], got, want)
		}
	}
}

// mockS3UploadServer is a minimal in-memory S3: PUT stores the object,
// HEAD answers Stat, ListBucket answers the probe/Browse. It is a
// contract mock, not a real R2 account.
type mockS3UploadServer struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func (s *mockS3UploadServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/test-bucket/")
	key = strings.TrimPrefix(key, "/")
	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.objs[key] = body
		s.mu.Unlock()
		w.Header().Set("ETag", `"mock-etag"`)
		w.WriteHeader(http.StatusOK)
	case http.MethodHead:
		s.mu.Lock()
		body, ok := s.objs[key]
		s.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Header().Set("ETag", `"mock-etag"`)
		w.Header().Set("Last-Modified", "Wed, 25 Nov 2026 00:00:00 GMT")
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		if r.URL.Query().Get("list-type") == "2" {
			prefix := r.URL.Query().Get("prefix")
			s.mu.Lock()
			var contents strings.Builder
			count := 0
			for k, body := range s.objs {
				if !strings.HasPrefix(k, prefix) {
					continue
				}
				rest := strings.TrimPrefix(k, prefix)
				if idx := strings.Index(rest, "/"); idx >= 0 {
					// Directory grouping is not needed for this test's
					// flat keys; skip nested keys at this level.
					_ = idx
					continue
				}
				count++
				fmt.Fprintf(&contents, `<Contents><Key>%s</Key><LastModified>2026-11-25T00:00:00Z</LastModified><ETag>"mock-etag"</ETag><Size>%d</Size><StorageClass>STANDARD</StorageClass></Contents>`, k, len(body))
			}
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>test-bucket</Name><Prefix>%s</Prefix><KeyCount>%d</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>%s</ListBucketResult>`, prefix, count, contents.String())
			return
		}
		s.mu.Lock()
		body, ok := s.objs[key]
		s.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestUploadFilesS3MockEndToEnd(t *testing.T) {
	store := &mockS3UploadServer{objs: map[string][]byte{}}
	srv := httptest.NewServer(store)
	defer srv.Close()

	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, "up.txt"), "s3-content")
	a := newTestApp(t)
	startTestWorker(t, a)
	ctx := context.Background()
	m, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "Mock S3", Provider: domain.ProviderS3,
		Endpoint: srv.URL, Region: "us-east-1", Bucket: "test-bucket",
		AccessKey: "ak-1", SecretKey: "sk-1", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("AddMountWithCredentials(s3 mock): %v", err)
	}
	res, err := a.UploadFiles(ctx, m.ID, "", []string{filepath.Join(src, "up.txt")})
	if err != nil {
		t.Fatalf("UploadFiles: %v", err)
	}
	if len(res.Transfers) != 1 || len(res.Skipped) != 0 {
		t.Fatalf("want 1 transfer, 0 skipped, got %+v %+v", res.Transfers, res.Skipped)
	}
	done := waitTransfersTerminal(t, a, transferIDs(res.Transfers), 10*time.Second)
	for _, tr := range done {
		if tr.Status != domain.TransferCompleted {
			t.Fatalf("s3 transfer: status %q, error %q", tr.Status, tr.Error)
		}
	}
	store.mu.Lock()
	got := store.objs["up.txt"]
	store.mu.Unlock()
	if string(got) != "s3-content" {
		t.Fatalf("mock S3 stored %q, want %q", got, "s3-content")
	}
	nodes, err := a.Browse(ctx, m.ID, "")
	if err != nil || len(nodes) != 1 || nodes[0].Name != "up.txt" {
		t.Fatalf("Browse s3 after upload: %v, %+v", err, nodes)
	}
}
