// SPDX-License-Identifier: MPL-2.0
package app

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/luckzk/zy-drive/internal/auth"
	"github.com/luckzk/zy-drive/internal/cache"
	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/storage/providers/local"
	"github.com/luckzk/zy-drive/internal/storage/providers/s3"
	"github.com/luckzk/zy-drive/internal/transfer"
)

// fakeS3 is an in-memory S3-compatible server for bundle tests:
// ListObjectsV2 (with delimiter), Head/Get/Put/DeleteObject.
// No auth checks — the point is the bundle + runner flow, not the
// S3 driver's signing (already proven against real R2 in v0.4.1).
type fakeS3 struct {
	mu      sync.Mutex
	buckets map[string]map[string][]byte
}

func newFakeS3() *fakeS3 { return &fakeS3{buckets: map[string]map[string][]byte{}} }

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	bucket := ""
	key := ""
	if len(parts) >= 1 {
		bucket = parts[0]
	}
	if len(parts) >= 2 {
		key = strings.Join(parts[1:], "/")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	bkt, ok := f.buckets[bucket]
	if !ok {
		bkt = map[string][]byte{}
		f.buckets[bucket] = bkt
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
		prefix := r.URL.Query().Get("prefix")
		delim := r.URL.Query().Get("delimiter")
		type contents struct {
			Key          string
			LastModified string
			ETag         string
			Size         int64
			StorageClass string
		}
		type listResult struct {
			XMLName        xml.Name `xml:"ListBucketResult"`
			Xmlns          string   `xml:"xmlns,attr"`
			Name           string
			Prefix         string
			KeyCount       int
			MaxKeys        int
			IsTruncated    bool
			Contents       []contents                `xml:"Contents"`
			CommonPrefixes []struct{ Prefix string } `xml:"CommonPrefixes"`
		}
		res := listResult{Xmlns: "http://s3.amazonaws.com/doc/2006-03-01/", Name: bucket, Prefix: prefix, MaxKeys: 1000}
		seenPrefix := map[string]bool{}
		keys := make([]string, 0, len(bkt))
		for k := range bkt {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !strings.HasPrefix(k, prefix) {
				continue
			}
			rest := strings.TrimPrefix(k, prefix)
			if delim != "" {
				if i := strings.Index(rest, delim); i >= 0 {
					cp := prefix + rest[:i+1]
					if !seenPrefix[cp] {
						seenPrefix[cp] = true
						res.CommonPrefixes = append(res.CommonPrefixes, struct{ Prefix string }{cp})
					}
					continue
				}
			}
			res.Contents = append(res.Contents, contents{
				Key: k, LastModified: "2026-11-25T00:00:00Z",
				ETag: `"etag"`, Size: int64(len(bkt[k])), StorageClass: "STANDARD",
			})
		}
		res.KeyCount = len(res.Contents)
		w.Header().Set("Content-Type", "application/xml")
		_ = xml.NewEncoder(w).Encode(res)
	case r.Method == http.MethodPut:
		data, _ := io.ReadAll(r.Body)
		bkt[key] = data
		w.Header().Set("ETag", `"etag"`)
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet:
		data, ok := bkt[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.Header().Set("ETag", `"etag"`)
		_, _ = w.Write(data)
	case r.Method == http.MethodHead:
		data, ok := bkt[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.Header().Set("ETag", `"etag"`)
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodDelete:
		delete(bkt, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// newAgentSideApp builds an App with a DIFFERENT master key than
// the desktop test app — the import must re-encrypt under the
// Agent's own key, not the desktop's.
func newAgentSideApp(t *testing.T, dbPath string) *App {
	t.Helper()
	db, err := cache.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := storage.Default()
	register := func(kind domain.ProviderKind, f storage.Factory) {
		defer func() { _ = recover() }()
		reg.Register(kind, f)
	}
	register(domain.ProviderLocal, local.New)
	register(domain.ProviderS3, s3.New)
	a := New(db, reg, transfer.NewQueue(db))
	secrets, err := auth.NewSecretsStore("agent-side-master-password", []byte("agent-salt-16byte"))
	if err != nil {
		t.Fatal(err)
	}
	a.SetSecrets(secrets)
	return a
}

// TestExportImportBundleRoundTrip: desktop exports an encrypted
// bundle (S3 -> S3 job), the file holds no plaintext secret, and an
// Agent-side App with a different master key imports it and can
// actually run the job — the file appears in the destination bucket.
func TestExportImportBundleRoundTrip(t *testing.T) {
	s3srv := httptest.NewServer(newFakeS3())
	defer s3srv.Close()
	ctx := context.Background()
	desktop := newTestApp(t)
	src, err := desktop.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "源桶", Provider: domain.ProviderS3,
		Endpoint: s3srv.URL, Region: "us-east-1", Bucket: "bkt-src",
		AccessKey: "ak-src", SecretKey: "sk-src-secret", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("AddMount(src): %v", err)
	}
	dst, err := desktop.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "目标桶", Provider: domain.ProviderS3,
		Endpoint: s3srv.URL, Region: "us-east-1", Bucket: "bkt-dst",
		AccessKey: "ak-dst", SecretKey: "sk-dst-secret", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("AddMount(dst): %v", err)
	}
	if err := desktop.AddAgent(&domain.Agent{ID: "ag-1", Name: "测试服务器", Address: "127.0.0.1:18080"}); err != nil {
		t.Fatalf("AddAgent: %v", err)
	}
	job, err := desktop.SaveSyncJob(&domain.SyncJob{
		Name: "桶同步", SrcMount: src.ID, SrcRef: "", DstMount: dst.ID, DstRef: "",
		Direction: domain.DirectionCopy, RunOn: domain.RunOnAgent, AgentID: "ag-1",
		Schedule: "daily 02:00", Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveSyncJob: %v", err)
	}
	// A job with a local side is skipped openly, not exported.
	localDir := t.TempDir()
	lm, err := desktop.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "本地", Provider: domain.ProviderLocal, LocalPath: localDir,
	})
	if err != nil {
		t.Fatalf("AddMount(local): %v", err)
	}
	if _, err := desktop.SaveSyncJob(&domain.SyncJob{
		Name: "本地同步", SrcMount: src.ID, SrcRef: "", DstMount: lm.ID, DstRef: "",
		Direction: domain.DirectionCopy, RunOn: domain.RunOnAgent, AgentID: "ag-1",
	}); err != nil {
		t.Fatalf("SaveSyncJob(local job): %v", err)
	}
	res, err := desktop.ExportAgentBundle(ctx, "ag-1", "correct-horse-battery")
	if err != nil {
		t.Fatalf("ExportAgentBundle: %v", err)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "本地同步") {
		t.Fatalf("export skipped = %v, want the local-side job listed", res.Skipped)
	}
	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sk-src-secret") || strings.Contains(string(data), "sk-dst-secret") {
		t.Fatal("bundle file contains a plaintext secret")
	}
	// Wrong passphrase fails before anything is imported.
	agent := newAgentSideApp(t, filepath.Join(t.TempDir(), "agent.db"))
	if _, err := agent.ImportBundle(data, "wrong-passphrase"); err == nil {
		t.Fatal("import with the wrong passphrase must fail")
	}
	if mounts := agent.ListMounts(); len(mounts) != 0 {
		t.Fatalf("failed import left %d mounts behind", len(mounts))
	}
	bundle, err := agent.ImportBundle(data, "correct-horse-battery")
	if err != nil {
		t.Fatalf("ImportBundle: %v", err)
	}
	if len(bundle.Mounts) != 2 || len(bundle.Jobs) != 1 {
		t.Fatalf("bundle = %d mounts %d jobs, want 2/1", len(bundle.Mounts), len(bundle.Jobs))
	}
	if mounts := agent.ListMounts(); len(mounts) != 2 {
		t.Fatalf("agent mounts = %d, want 2", len(mounts))
	}
	jobs := agent.ListSyncJobs()
	if len(jobs) != 1 || jobs[0].ID != job.ID || jobs[0].Schedule != "daily 02:00" {
		t.Fatalf("agent jobs = %+v, want the exported job", jobs)
	}
	// The imported credentials actually work: put a file in the
	// source bucket and run the job on the agent side.
	srcProv, err := agent.ProviderFor(src.ID)
	if err != nil {
		t.Fatalf("agent ProviderFor(src): %v", err)
	}
	if _, err := srcProv.Upload(ctx, "hello.txt", strings.NewReader("hello-agent"), 11); err != nil {
		t.Fatalf("upload to src bucket: %v", err)
	}
	plan, err := agent.PreviewJob(ctx, jobs[0])
	if err != nil {
		t.Fatalf("agent PreviewJob: %v", err)
	}
	if plan.Summary().ToUpload != 1 {
		t.Fatalf("plan = %+v, want 1 file to upload", plan.Summary())
	}
	run, err := agent.RunJobAsAgent(ctx, jobs[0], plan)
	if err != nil {
		t.Fatalf("agent RunJobAsAgent: %v", err)
	}
	if run.Status != domain.RunCompleted {
		t.Fatalf("run status = %v, want completed", run.Status)
	}
	dstProv, err := agent.ProviderFor(dst.ID)
	if err != nil {
		t.Fatalf("agent ProviderFor(dst): %v", err)
	}
	rc, err := dstProv.OpenRead(ctx, "hello.txt")
	if err != nil {
		t.Fatalf("read dst file: %v (job did not copy it)", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "hello-agent" {
		t.Fatalf("dst file = %q, want hello-agent", got)
	}
	// (Run records are written by the agentrun Runner, not by
	// RunJobAsAgent itself — see agentrun/runner_test.go.)
}
