package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckzk/zy-drive/internal/cache"
	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/transfer"
)

// --- mock HTTP backends (no real accounts, no real network) ---

// mockDropboxServer speaks just enough Dropbox API v2 for the probe:
// list_folder on the root, plus the OAuth token endpoint used when
// only a refresh token is given. failList makes the probe return 401.
func mockDropboxServer(failList bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth2/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "mock-access", "token_type": "bearer", "expires_in": 14400,
			})
		case "/2/files/list_folder":
			if failList {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"error_summary": "invalid_access_token/"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"entries": []map[string]any{
					{".tag": "folder", "name": "照片", "path_display": "/照片", "path_lower": "/照片", "id": "id:photos"},
					{".tag": "file", "name": "a.txt", "path_display": "/a.txt", "path_lower": "/a.txt", "id": "id:a", "size": 3, "server_modified": "2026-11-24T00:00:00Z"},
				},
				"cursor": "cur-1", "has_more": false,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// mockDriveServer speaks just enough Drive v3 for the probe: the root
// list query "'root' in parents". Any other files query is empty.
func mockDriveServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/drive/v3/files" && r.Method == http.MethodGet {
			q := r.URL.Query().Get("q")
			files := []map[string]any{}
			if strings.Contains(q, "'root' in parents") {
				files = []map[string]any{
					{"id": "id-1", "name": "报告.txt", "mimeType": "text/plain", "size": "5", "modifiedTime": "2026-11-24T00:00:00Z"},
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"files": files})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

// mockDrimeServer speaks just enough Drime API v1 for the probe:
// GET /drive/file-entries on the root. failList answers 401.
func mockDrimeServer(failList bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/drive/file-entries" && r.Method == http.MethodGet {
			if failList || r.Header.Get("Authorization") != "Bearer test-token" {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"message": "Unauthenticated."})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": 1001, "name": "照片", "type": "folder", "hash": "hash-1001", "file_size": 0, "parent_id": nil, "workspace_id": 0, "created_at": "2026-11-25T00:00:00.000000Z", "updated_at": "2026-11-25T00:00:00.000000Z"},
					{"id": 1002, "name": "a.txt", "type": "file", "hash": "hash-1002", "file_size": 3, "mime": "text/plain", "parent_id": nil, "workspace_id": 0, "created_at": "2026-11-25T00:00:00.000000Z", "updated_at": "2026-11-25T00:00:00.000000Z"},
				},
				"current_page": 1, "last_page": 1, "total": 2,
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

func TestAddDrimeMountMockRoundTrip(t *testing.T) {
	srv := mockDrimeServer(false)
	defer srv.Close()
	dbPath := filepath.Join(t.TempDir(), "app.db")
	a := newTestAppWithDB(t, dbPath)
	ctx := context.Background()
	// Personal access token (the recommended Drime auth): no OAuth,
	// no refresh, never expires.
	m, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "我的 Drime", Provider: domain.ProviderDrime,
		Group: "个人", AccessToken: "test-token", APIBase: srv.URL + "/api/v1",
	})
	if err != nil {
		t.Fatalf("AddMountWithCredentials(drime): %v", err)
	}
	if m.RootRef != "" {
		t.Fatalf("drime root: RootRef=%q, want \"\"", m.RootRef)
	}
	nodes, err := a.Browse(ctx, m.ID, "")
	if err != nil || len(nodes) != 2 {
		t.Fatalf("Browse drime: %v, %d nodes", err, len(nodes))
	}
	a2 := reopenApp(t, dbPath)
	mounts := a2.ListMounts()
	if len(mounts) != 1 || mounts[0].Group != "个人" {
		t.Fatalf("drime mount after restart: %+v", mounts)
	}
	nodes, err = a2.Browse(ctx, mounts[0].ID, "")
	if err != nil || len(nodes) != 2 {
		t.Fatalf("Browse drime after restart: %v, %d nodes", err, len(nodes))
	}
	// A bad token must fail the probe and save nothing.
	a3 := newTestApp(t)
	if _, err := a3.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "坏 Drime", Provider: domain.ProviderDrime,
		AccessToken: "wrong-token", APIBase: srv.URL + "/api/v1",
	}); err == nil {
		t.Fatalf("AddMountWithCredentials(drime, bad token): want an error")
	}
	if mounts := a3.ListMounts(); len(mounts) != 0 {
		t.Fatalf("failed drime probe saved %d mount(s)", len(mounts))
	}
}

// mockGraphServer speaks just enough Microsoft Graph v1.0 for the
// probe: GET /me/drive/items/root/children on the root. failList
// answers 401 (bad token).
func mockGraphServer(failList bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/me/drive/items/root/children" && r.Method == http.MethodGet {
			if failList || r.Header.Get("Authorization") != "Bearer test-token" {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "InvalidAuthenticationToken", "message": "Access token is empty or invalid."}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"value": []map[string]any{
					{"id": "id-photos", "name": "照片", "folder": map[string]any{"childCount": 0}, "parentReference": map[string]any{"id": "root", "path": "/drive/root:"}, "lastModifiedDateTime": "2026-11-25T00:00:00Z"},
					{"id": "id-a", "name": "a.txt", "size": 3, "file": map[string]any{"mimeType": "text/plain", "hashes": map[string]any{"sha1Hash": "a9993e364706816aba3e25717850c26c9cd0d89d"}}, "parentReference": map[string]any{"id": "root", "path": "/drive/root:"}, "lastModifiedDateTime": "2026-11-25T00:00:00Z"},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
}

func TestAddOneDriveMountMockRoundTrip(t *testing.T) {
	srv := mockGraphServer(false)
	defer srv.Close()
	dbPath := filepath.Join(t.TempDir(), "app.db")
	a := newTestAppWithDB(t, dbPath)
	ctx := context.Background()
	// Access token pasted directly (or exchanged from an OAuth code):
	// no refresh token here, the probe just lists the root.
	m, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "我的 OneDrive", Provider: domain.ProviderOneDrive,
		Group: "个人", AccessToken: "test-token", APIBase: srv.URL,
	})
	if err != nil {
		t.Fatalf("AddMountWithCredentials(onedrive): %v", err)
	}
	if m.RootRef != "" {
		t.Fatalf("onedrive root: RootRef=%q, want \"\"", m.RootRef)
	}
	nodes, err := a.Browse(ctx, m.ID, "")
	if err != nil || len(nodes) != 2 {
		t.Fatalf("Browse onedrive: %v, %d nodes", err, len(nodes))
	}
	a2 := reopenApp(t, dbPath)
	mounts := a2.ListMounts()
	if len(mounts) != 1 || mounts[0].Group != "个人" {
		t.Fatalf("onedrive mount after restart: %+v", mounts)
	}
	nodes, err = a2.Browse(ctx, mounts[0].ID, "")
	if err != nil || len(nodes) != 2 {
		t.Fatalf("Browse onedrive after restart: %v, %d nodes", err, len(nodes))
	}
	// A bad token must fail the probe and save nothing.
	a3 := newTestApp(t)
	if _, err := a3.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "坏 OneDrive", Provider: domain.ProviderOneDrive,
		AccessToken: "wrong-token", APIBase: srv.URL,
	}); err == nil {
		t.Fatalf("AddMountWithCredentials(onedrive, bad token): want an error")
	}
	if mounts := a3.ListMounts(); len(mounts) != 0 {
		t.Fatalf("failed onedrive probe saved %d mount(s)", len(mounts))
	}
}

// mockS3Server speaks just enough S3 ListObjectsV2 for the probe.
// failList answers 403 InvalidAccessKeyId instead.
func mockS3Server(failList bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("list-type") != "2" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if failList {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>InvalidAccessKeyId</Code><Message>The AWS Access Key Id you provided does not exist in our records.</Message></Error>`))
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>test-bucket</Name><Prefix></Prefix><KeyCount>2</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated><Contents><Key>hello.txt</Key><LastModified>2026-11-24T00:00:00Z</LastModified><ETag>"abc"</ETag><Size>5</Size><StorageClass>STANDARD</StorageClass></Contents><CommonPrefixes><Prefix>photos/</Prefix></CommonPrefixes></ListBucketResult>`))
	}))
}

// reopenApp builds a fresh App over the same DB file, with an equal
// (re-derived) secrets store — the closest a test gets to a restart.
func reopenApp(t *testing.T, dbPath string) *App {
	t.Helper()
	db, err := cache.Open(dbPath)
	if err != nil {
		t.Fatalf("cache.Open (reopen): %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	a := New(db, storage.Default(), transfer.NewQueue(db))
	a.SetSecrets(testSecretsStore(t))
	if err := a.LoadMounts(); err != nil {
		t.Fatalf("LoadMounts: %v", err)
	}
	return a
}

func TestAddLocalMountRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "照片"), 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "app.db")
	a := newTestAppWithDB(t, dbPath)
	ctx := context.Background()
	m, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "本地照片", Provider: domain.ProviderLocal,
		Group: "工作", LocalPath: dir,
	})
	if err != nil {
		t.Fatalf("AddMountWithCredentials(local): %v", err)
	}
	if m.RootRef != dir || m.Group != "工作" {
		t.Fatalf("mount fields: %+v", m)
	}
	// Real end-to-end browse through the app layer.
	nodes, err := a.Browse(ctx, m.ID, "")
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("Browse root: got %d nodes, want 2 (%v)", len(nodes), nodes)
	}
	// "Restart": a fresh App over the same DB still sees the mount,
	// its group, and its real files — no re-adding.
	a2 := reopenApp(t, dbPath)
	mounts := a2.ListMounts()
	if len(mounts) != 1 || mounts[0].DisplayName != "本地照片" || mounts[0].Group != "工作" {
		t.Fatalf("mounts after restart: %+v", mounts)
	}
	nodes, err = a2.Browse(ctx, mounts[0].ID, "")
	if err != nil || len(nodes) != 2 {
		t.Fatalf("Browse after restart: %v, %d nodes", err, len(nodes))
	}
}

func TestAddMountProbeFailureNotSaved(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	// Local: a path that does not exist.
	if _, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "不存在", Provider: domain.ProviderLocal,
		LocalPath: filepath.Join(t.TempDir(), "no-such-dir"),
	}); err == nil {
		t.Fatalf("nonexistent local path must fail the probe")
	}
	// Dropbox: 401 from the mock API.
	srv := mockDropboxServer(true)
	defer srv.Close()
	if _, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "坏 Dropbox", Provider: domain.ProviderDropbox,
		AccessToken: "bad-token", APIBase: srv.URL,
	}); err == nil || !strings.Contains(err.Error(), "授权失败") {
		t.Fatalf("bad Dropbox token must fail with a plain-language auth error, got %v", err)
	}
	// S3: 403 InvalidAccessKeyId from the mock.
	s3srv := mockS3Server(true)
	defer s3srv.Close()
	if _, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "坏 S3", Provider: domain.ProviderS3,
		Endpoint: s3srv.URL, Region: "us-east-1", Bucket: "test-bucket",
		AccessKey: "bad", SecretKey: "bad", PathStyle: true,
	}); err == nil || !strings.Contains(err.Error(), "S3 拒绝访问") {
		t.Fatalf("bad S3 keys must fail with a plain-language error, got %v", err)
	}
	// Nothing was persisted by any failed probe.
	if got := a.ListMounts(); len(got) != 0 {
		t.Fatalf("failed probes must not save mounts, got %+v", got)
	}
	if persisted, err := a.DB.ListMounts(); err != nil || len(persisted) != 0 {
		t.Fatalf("DB must hold no mounts after failed probes: %v %+v", err, persisted)
	}
}

// TestAddMountSkipProbeSavesUnverified (C, one-page form): saving
// without a probe stores the mount with Verified=false, and a
// later TestMount flips it to true. The probe-gated path still
// refuses a bad config — SkipProbe only skips the probe, never
// the validation.
func TestAddMountSkipProbeSavesUnverified(t *testing.T) {
	dir := t.TempDir()
	a := newTestApp(t)
	ctx := context.Background()
	m, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "先存后测", Provider: domain.ProviderLocal,
		LocalPath: dir, SkipProbe: true,
	})
	if err != nil {
		t.Fatalf("AddMountWithCredentials(skipProbe): %v", err)
	}
	if m.Verified {
		t.Fatalf("skip-probe mount must be Verified=false, got %+v", m)
	}
	// Persisted as unverified too (a restart must not "heal" it).
	persisted, err := a.DB.ListMounts()
	if err != nil || len(persisted) != 1 || persisted[0].Mount.Verified {
		t.Fatalf("DB must hold one unverified mount: %v %+v", err, persisted)
	}
	// Re-test from Settings: the path exists, so it passes and the
	// mount becomes verified.
	updated, err := a.TestMount(ctx, m.ID)
	if err != nil {
		t.Fatalf("TestMount: %v", err)
	}
	if !updated.Verified {
		t.Fatalf("TestMount must mark the mount verified, got %+v", updated)
	}
	// A failed re-test changes nothing: the mount stays verified
	// from the pass above (TestMount never un-verifies on failure;
	// it only reports the error).
	if _, err := a.TestMount(ctx, "mnt-does-not-exist"); err == nil {
		t.Fatalf("TestMount on an unknown mount must fail")
	}
}

// TestTestMountConfigProbesWithoutSaving (C): the one-page form's
// "测试连接" validates + probes and persists nothing, good or bad.
func TestTestMountConfigProbesWithoutSaving(t *testing.T) {
	dir := t.TempDir()
	a := newTestApp(t)
	ctx := context.Background()
	if err := a.TestMountConfig(ctx, AddMountInput{
		DisplayName: "本地", Provider: domain.ProviderLocal, LocalPath: dir,
	}); err != nil {
		t.Fatalf("TestMountConfig(existing dir): %v", err)
	}
	if err := a.TestMountConfig(ctx, AddMountInput{
		DisplayName: "不存在", Provider: domain.ProviderLocal,
		LocalPath: filepath.Join(t.TempDir(), "no-such-dir"),
	}); err == nil {
		t.Fatalf("TestMountConfig(missing dir) must fail")
	}
	if got := a.ListMounts(); len(got) != 0 {
		t.Fatalf("TestMountConfig must not save anything, got %+v", got)
	}
}

func TestAddDropboxMountMockRoundTrip(t *testing.T) {
	srv := mockDropboxServer(false)
	defer srv.Close()
	dbPath := filepath.Join(t.TempDir(), "app.db")
	a := newTestAppWithDB(t, dbPath)
	ctx := context.Background()
	// Refresh-token-only: the probe must refresh against the mock
	// token endpoint before listing.
	m, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "我的 Dropbox", Provider: domain.ProviderDropbox,
		Group: "个人", RefreshToken: "rt-1", ClientID: "cid-1",
		ClientSecret: "csec-1", TokenURL: srv.URL + "/oauth2/token",
		APIBase: srv.URL,
	})
	if err != nil {
		t.Fatalf("AddMountWithCredentials(dropbox): %v", err)
	}
	nodes, err := a.Browse(ctx, m.ID, "")
	if err != nil || len(nodes) != 2 {
		t.Fatalf("Browse dropbox: %v, %d nodes", err, len(nodes))
	}
	a2 := reopenApp(t, dbPath)
	mounts := a2.ListMounts()
	if len(mounts) != 1 || mounts[0].Group != "个人" {
		t.Fatalf("dropbox mount after restart: %+v", mounts)
	}
	nodes, err = a2.Browse(ctx, mounts[0].ID, "")
	if err != nil || len(nodes) != 2 {
		t.Fatalf("Browse dropbox after restart: %v, %d nodes", err, len(nodes))
	}
}

func TestAddDriveMountMockRoundTrip(t *testing.T) {
	srv := mockDriveServer()
	defer srv.Close()
	dbPath := filepath.Join(t.TempDir(), "app.db")
	a := newTestAppWithDB(t, dbPath)
	ctx := context.Background()
	m, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "工作 Drive", Provider: domain.ProviderDrive,
		AccessToken: "test-token", APIBase: srv.URL,
	})
	if err != nil {
		t.Fatalf("AddMountWithCredentials(drive): %v", err)
	}
	_ = srv
	nodes, err := a.Browse(ctx, m.ID, "")
	if err != nil || len(nodes) != 1 || nodes[0].Name != "报告.txt" {
		t.Fatalf("Browse drive: %v, %+v", err, nodes)
	}
	a2 := reopenApp(t, dbPath)
	if mounts := a2.ListMounts(); len(mounts) != 1 {
		t.Fatalf("drive mount after restart: %+v", mounts)
	}
}

func TestAddS3MountMockRoundTrip(t *testing.T) {
	srv := mockS3Server(false)
	defer srv.Close()
	dbPath := filepath.Join(t.TempDir(), "app.db")
	a := newTestAppWithDB(t, dbPath)
	ctx := context.Background()
	m, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "R2 备份", Provider: domain.ProviderS3,
		Endpoint: srv.URL, Region: "us-east-1", Bucket: "test-bucket",
		AccessKey: "ak-1", SecretKey: "sk-1", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("AddMountWithCredentials(s3): %v", err)
	}
	// S3 root is the provider root (""): the bucket lives in the
	// driver config, never in RootRef — RootRef "test-bucket" used
	// to make Browse list a "test-bucket" folder inside the bucket.
	if m.RootRef != "" || m.RootLabel != "test-bucket" {
		t.Fatalf("s3 root: RootRef=%q RootLabel=%q, want \"\" / \"test-bucket\"", m.RootRef, m.RootLabel)
	}
	nodes, err := a.Browse(ctx, m.ID, "")
	if err != nil || len(nodes) != 2 {
		t.Fatalf("Browse s3: %v, %+v", err, nodes)
	}
	a2 := reopenApp(t, dbPath)
	if mounts := a2.ListMounts(); len(mounts) != 1 {
		t.Fatalf("s3 mount after restart: %+v", mounts)
	}
}

// TestS3RootRefWithPrefix keeps the prefix out of RootRef too: the
// driver already scopes every key by its own prefix.
func TestS3RootRefWithPrefix(t *testing.T) {
	srv := mockS3Server(false)
	defer srv.Close()
	a := newTestApp(t)
	m, err := a.AddMountWithCredentials(context.Background(), AddMountInput{
		DisplayName: "R2 照片", Provider: domain.ProviderS3,
		Endpoint: srv.URL, Region: "us-east-1", Bucket: "test-bucket",
		Prefix: "photos/2024", AccessKey: "ak-1", SecretKey: "sk-1", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("AddMountWithCredentials(s3 prefix): %v", err)
	}
	if m.RootRef != "" || m.RootLabel != "test-bucket/photos/2024" {
		t.Fatalf("s3 prefix root: RootRef=%q RootLabel=%q, want \"\" / \"test-bucket/photos/2024\"", m.RootRef, m.RootLabel)
	}
}

func TestMountConfigRejectsBadS3BucketPrefix(t *testing.T) {
	a := newTestApp(t)
	base := AddMountInput{
		DisplayName: "R2", Provider: domain.ProviderS3,
		Endpoint: "https://example.invalid", Region: "us-east-1",
		Bucket: "test-bucket", AccessKey: "ak-1", SecretKey: "sk-1",
	}
	cases := []struct {
		name   string
		bucket string
		prefix string
		want   string
	}{
		{"bucket with slash", "a/b", "", "不能有 /"},
		{"prefix equals bucket", "test-bucket", "test-bucket", "不能就是桶名本身"},
		{"prefix repeats bucket", "test-bucket", "test-bucket/photos", "不要把桶名再填一遍"},
	}
	for _, c := range cases {
		in := base
		in.Bucket, in.Prefix = c.bucket, c.prefix
		if _, _, err := a.mountConfig(in); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: got %v, want error containing %q", c.name, err, c.want)
		}
	}
	in := base
	in.Prefix = "photos/2024"
	if _, _, err := a.mountConfig(in); err != nil {
		t.Fatalf("valid bucket/prefix must pass: %v", err)
	}
}

// TestLoadMountsHealsLegacyS3RootRef simulates the user's existing R2
// mount as saved before the fix (RootRef "test-bucket"): reopening
// the app must rewrite it to "" with the right RootLabel, from the
// stored bucket config, without deleting and re-adding the mount.
func TestLoadMountsHealsLegacyS3RootRef(t *testing.T) {
	srv := mockS3Server(false)
	defer srv.Close()
	dbPath := filepath.Join(t.TempDir(), "app.db")
	a := newTestAppWithDB(t, dbPath)
	ctx := context.Background()
	m, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "R2 备份", Provider: domain.ProviderS3,
		Endpoint: srv.URL, Region: "us-east-1", Bucket: "test-bucket",
		AccessKey: "ak-1", SecretKey: "sk-1", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("AddMountWithCredentials(s3): %v", err)
	}
	// Corrupt the row the way the pre-fix code saved it.
	persisted, err := a.DB.ListMounts()
	if err != nil || len(persisted) != 1 {
		t.Fatalf("ListMounts: %v %+v", err, persisted)
	}
	legacy := persisted[0].Mount
	legacy.RootRef = "test-bucket"
	legacy.RootLabel = ""
	if err := a.DB.SaveMount(legacy, persisted[0].Config); err != nil {
		t.Fatalf("SaveMount legacy row: %v", err)
	}
	a2 := reopenApp(t, dbPath)
	mounts := a2.ListMounts()
	if len(mounts) != 1 || mounts[0].ID != m.ID {
		t.Fatalf("mounts after restart: %+v", mounts)
	}
	if mounts[0].RootRef != "" || mounts[0].RootLabel != "test-bucket" {
		t.Fatalf("healed root: RootRef=%q RootLabel=%q, want \"\" / \"test-bucket\"", mounts[0].RootRef, mounts[0].RootLabel)
	}
	// The heal wrote back: the DB row itself is fixed, not just the
	// in-memory copy.
	persisted, err = a2.DB.ListMounts()
	if err != nil || len(persisted) != 1 || persisted[0].Mount.RootRef != "" || persisted[0].Mount.RootLabel != "test-bucket" {
		t.Fatalf("DB row after heal: %v %+v", err, persisted)
	}
	// And the healed mount still browses its real root.
	nodes, err := a2.Browse(ctx, mounts[0].ID, "")
	if err != nil || len(nodes) != 2 {
		t.Fatalf("Browse healed s3: %v, %+v", err, nodes)
	}
}

// TestMountEndpointHost: EndpointHost is the display-only host of the
// S3 endpoint (the sidebar picks the R2 vs AWS S3 brand mark from
// it). It is set at add time and recomputed from the stored config
// on every LoadMounts — it is never a column in the mounts table,
// so older rows gain it on restart with no migration.
func TestMountEndpointHost(t *testing.T) {
	if got := endpointHost("https://abc123.r2.cloudflarestorage.com"); got != "abc123.r2.cloudflarestorage.com" {
		t.Fatalf("endpointHost(r2) = %q", got)
	}
	if got := endpointHost("https://s3.ap-northeast-1.amazonaws.com"); got != "s3.ap-northeast-1.amazonaws.com" {
		t.Fatalf("endpointHost(aws) = %q", got)
	}
	if got := endpointHost(""); got != "" {
		t.Fatalf("endpointHost(\"\") = %q", got)
	}
	if got := endpointHost("://bad url"); got != "" {
		t.Fatalf("endpointHost(garbage) = %q", got)
	}
	srv := mockS3Server(false)
	defer srv.Close()
	dbPath := filepath.Join(t.TempDir(), "app.db")
	a := newTestAppWithDB(t, dbPath)
	ctx := context.Background()
	m, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "R2 备份", Provider: domain.ProviderS3,
		Endpoint: srv.URL, Region: "us-east-1", Bucket: "test-bucket",
		AccessKey: "ak-1", SecretKey: "sk-1", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("AddMountWithCredentials(s3): %v", err)
	}
	// The mock server runs on 127.0.0.1:<port>: the host is parsed,
	// the scheme is dropped, and no credential leaks into the field.
	if m.EndpointHost == "" || !strings.HasPrefix(m.EndpointHost, "127.0.0.1:") {
		t.Fatalf("EndpointHost after add = %q, want 127.0.0.1:<port>", m.EndpointHost)
	}
	a2 := reopenApp(t, dbPath)
	mounts := a2.ListMounts()
	if len(mounts) != 1 || mounts[0].EndpointHost != m.EndpointHost {
		t.Fatalf("EndpointHost after restart = %q, want %q", mounts[0].EndpointHost, m.EndpointHost)
	}
}

func TestUpdateMountRoot(t *testing.T) {
	srv := mockS3Server(false)
	defer srv.Close()
	dbPath := filepath.Join(t.TempDir(), "app.db")
	a := newTestAppWithDB(t, dbPath)
	ctx := context.Background()
	m, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "R2 备份", Provider: domain.ProviderS3,
		Endpoint: srv.URL, Region: "us-east-1", Bucket: "test-bucket",
		AccessKey: "ak-1", SecretKey: "sk-1", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("AddMountWithCredentials(s3): %v", err)
	}
	// Narrow the mount to a prefix: probe must pass before saving.
	updated, err := a.UpdateMountRoot(ctx, m.ID, "test-bucket", "photos")
	if err != nil {
		t.Fatalf("UpdateMountRoot: %v", err)
	}
	if updated.RootRef != "" || updated.RootLabel != "test-bucket/photos" {
		t.Fatalf("updated root: RootRef=%q RootLabel=%q", updated.RootRef, updated.RootLabel)
	}
	// Persisted across a restart.
	a2 := reopenApp(t, dbPath)
	mounts := a2.ListMounts()
	if len(mounts) != 1 || mounts[0].RootLabel != "test-bucket/photos" {
		t.Fatalf("root after restart: %+v", mounts)
	}
	// A rejected prefix changes nothing.
	if _, err := a2.UpdateMountRoot(ctx, m.ID, "test-bucket", "test-bucket"); err == nil {
		t.Fatalf("prefix == bucket must be rejected")
	}
	if got := a2.ListMounts(); got[0].RootLabel != "test-bucket/photos" {
		t.Fatalf("failed update must not change the mount: %+v", got)
	}
	// Non-S3 mounts cannot use this entry.
	dir := t.TempDir()
	lm, err := a2.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "本地", Provider: domain.ProviderLocal, LocalPath: dir,
	})
	if err != nil {
		t.Fatalf("AddMount(local): %v", err)
	}
	if _, err := a2.UpdateMountRoot(ctx, lm.ID, "b", ""); err == nil {
		t.Fatalf("UpdateMountRoot on a local mount must be rejected")
	}
}

func TestRemoveMountDeletesLocalRecordsOnly(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "app.db")
	a := newTestAppWithDB(t, dbPath)
	m, err := a.AddMountWithCredentials(context.Background(), AddMountInput{
		DisplayName: "临时", Provider: domain.ProviderLocal, LocalPath: dir,
	})
	if err != nil {
		t.Fatalf("AddMount: %v", err)
	}
	if err := a.RemoveMount(m.ID); err != nil {
		t.Fatalf("RemoveMount: %v", err)
	}
	if got := a.ListMounts(); len(got) != 0 {
		t.Fatalf("mount still listed after remove: %+v", got)
	}
	if persisted, _ := a.DB.ListMounts(); len(persisted) != 0 {
		t.Fatalf("mount row still in DB after remove")
	}
	if err := a.RemoveMount(m.ID); err == nil {
		t.Fatalf("removing an unknown mount must error")
	}
	// The local folder itself is untouched.
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("local folder must survive RemoveMount: %v", err)
	}
}

func TestUpdateMountGroupPersists(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "app.db")
	a := newTestAppWithDB(t, dbPath)
	m, err := a.AddMountWithCredentials(context.Background(), AddMountInput{
		DisplayName: "本地盘", Provider: domain.ProviderLocal,
		Group: "工作", LocalPath: dir,
	})
	if err != nil {
		t.Fatalf("AddMount: %v", err)
	}
	if err := a.UpdateMountGroup(m.ID, "生活"); err != nil {
		t.Fatalf("UpdateMountGroup: %v", err)
	}
	a2 := reopenApp(t, dbPath)
	mounts := a2.ListMounts()
	if len(mounts) != 1 || mounts[0].Group != "生活" {
		t.Fatalf("group after restart: %+v", mounts)
	}
}

func TestBeginExchangeOAuth(t *testing.T) {
	a := newTestApp(t)
	// OneDrive offers OAuth now (V0.6.0): Microsoft identity
	// platform, "common" tenant, PKCE S256, no secret for public
	// clients. The authorize URL must carry the PKCE challenge,
	// the Files.ReadWrite.All + offline_access scopes, and no
	// client secret anywhere.
	start, err := a.BeginOAuth(domain.ProviderOneDrive, "cid-od")
	if err != nil {
		t.Fatalf("BeginOAuth(onedrive): %v", err)
	}
	for _, want := range []string{
		"login.microsoftonline.com/common/oauth2/v2.0/authorize",
		"client_id=cid-od", "response_type=code", "state=",
		"code_challenge=", "code_challenge_method=S256",
		"Files.ReadWrite.All", "offline_access",
	} {
		if !strings.Contains(start.AuthURL, want) {
			t.Fatalf("onedrive auth URL %q missing %q", start.AuthURL, want)
		}
	}
	if strings.Contains(start.AuthURL, "client_secret") {
		t.Fatalf("onedrive auth URL must not carry a client secret: %q", start.AuthURL)
	}
	// Mock token endpoint: the exchange is a plain form POST and
	// must present the PKCE verifier, not a secret — and the
	// redirect_uri must be the per-flow loopback URI the authorize
	// URL carried, not the old fixed "http://localhost".
	authURL, _ := url.Parse(start.AuthURL)
	wantRedirect := authURL.Query().Get("redirect_uri")
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code") != "code-od" || r.Form.Get("redirect_uri") != wantRedirect {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Form.Get("code_verifier") == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_request","error_description":"missing code_verifier"}`))
			return
		}
		if r.Form.Get("client_secret") != "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_request","error_description":"public client must not send a secret"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "acc-od", "refresh_token": "ref-od",
		})
	}))
	defer tokenSrv.Close()
	tokens, err := a.ExchangeOAuthCode(domain.ProviderOneDrive, "cid-od", "", "code-od", start.State, tokenSrv.URL)
	if err != nil {
		t.Fatalf("ExchangeOAuthCode(onedrive): %v", err)
	}
	if tokens.AccessToken != "acc-od" || tokens.RefreshToken != "ref-od" {
		t.Fatalf("onedrive tokens: %+v", tokens)
	}
	// State is single-use: replaying it must fail.
	if _, err := a.ExchangeOAuthCode(domain.ProviderOneDrive, "cid-od", "", "code-od", start.State, tokenSrv.URL); err == nil {
		t.Fatalf("replayed onedrive state must be rejected")
	}
	start, err = a.BeginOAuth(domain.ProviderDropbox, "cid-1")
	if err != nil {
		t.Fatalf("BeginOAuth: %v", err)
	}
	for _, want := range []string{
		"dropbox.com/oauth2/authorize", "client_id=cid-1",
		"response_type=code", "token_access_type=offline", "state=",
	} {
		if !strings.Contains(start.AuthURL, want) {
			t.Fatalf("auth URL %q missing %q", start.AuthURL, want)
		}
	}
	if start.State == "" {
		t.Fatalf("empty state")
	}
	// Mock token endpoint: the exchange is a plain form POST. The
	// redirect_uri must be the per-flow loopback URI the authorize
	// URL carried.
	authURL, _ = url.Parse(start.AuthURL)
	wantRedirect = authURL.Query().Get("redirect_uri")
	tokenSrv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code") != "code-1" || r.Form.Get("redirect_uri") != wantRedirect {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "acc-9", "refresh_token": "ref-9",
		})
	}))
	defer tokenSrv.Close()
	tokens, err = a.ExchangeOAuthCode(domain.ProviderDropbox, "cid-1", "csec-1", "code-1", start.State, tokenSrv.URL)
	if err != nil {
		t.Fatalf("ExchangeOAuthCode: %v", err)
	}
	if tokens.AccessToken != "acc-9" || tokens.RefreshToken != "ref-9" {
		t.Fatalf("tokens: %+v", tokens)
	}
	// State is single-use: replaying it must fail.
	if _, err := a.ExchangeOAuthCode(domain.ProviderDropbox, "cid-1", "csec-1", "code-1", start.State, tokenSrv.URL); err == nil {
		t.Fatalf("replayed state must be rejected")
	}
	// Unknown state must fail.
	if _, err := a.ExchangeOAuthCode(domain.ProviderDropbox, "cid-1", "csec-1", "code-1", "nope", tokenSrv.URL); err == nil {
		t.Fatalf("unknown state must be rejected")
	}
}

// TestMountConfigWithoutName (2026-11-25): the wizard's
// in-component test runs right after the token exchange, possibly
// before the user typed a name — a connectivity probe must not
// fail on the missing display name. The name is still required
// at save time (AddMountWithCredentials) and by the wizard's
// Next gating.
func TestMountConfigWithoutName(t *testing.T) {
	srv := mockS3Server(false)
	defer srv.Close()
	a := newTestApp(t)
	ctx := context.Background()
	if err := a.TestMountConfig(ctx, AddMountInput{
		Provider: domain.ProviderS3,
		Endpoint: srv.URL, Region: "us-east-1", Bucket: "test-bucket",
		AccessKey: "ak-1", SecretKey: "sk-1", PathStyle: true,
	}); err != nil {
		t.Fatalf("TestMountConfig without a name must probe, not complain about the name: %v", err)
	}
}

// TestAddMountSkipProbeUsesRecentTest (2026-11-25): the wizard
// tests in-component (step 2) and saves in step 3 without probing
// again. A SkipProbe save of the exact config tested minutes ago
// is stored Verified=true; the same save with a changed credential
// is stored Verified=false — the memo keys on the probe inputs,
// never on the display name (renaming after a passed test must
// not void it).
func TestAddMountSkipProbeUsesRecentTest(t *testing.T) {
	srv := mockS3Server(false)
	defer srv.Close()
	a := newTestApp(t)
	ctx := context.Background()
	base := AddMountInput{
		Provider: domain.ProviderS3,
		Endpoint: srv.URL, Region: "us-east-1", Bucket: "test-bucket",
		AccessKey: "ak-1", SecretKey: "sk-1", PathStyle: true,
	}
	if err := a.TestMountConfig(ctx, base); err != nil {
		t.Fatalf("TestMountConfig: %v", err)
	}
	m, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "R2 备份", Provider: base.Provider,
		Endpoint: base.Endpoint, Region: base.Region, Bucket: base.Bucket,
		AccessKey: base.AccessKey, SecretKey: base.SecretKey, PathStyle: true,
		SkipProbe: true,
	})
	if err != nil {
		t.Fatalf("AddMountWithCredentials(skipProbe after test): %v", err)
	}
	if !m.Verified {
		t.Fatal("a mount saved right after a passed test must be Verified, without a second probe")
	}
	// A different credential was never tested: unverified.
	m2, err := a.AddMountWithCredentials(ctx, AddMountInput{
		DisplayName: "R2 备份 2", Provider: base.Provider,
		Endpoint: base.Endpoint, Region: base.Region, Bucket: base.Bucket,
		AccessKey: "ak-OTHER", SecretKey: base.SecretKey, PathStyle: true,
		SkipProbe: true,
	})
	if err != nil {
		t.Fatalf("AddMountWithCredentials(untested skipProbe): %v", err)
	}
	if m2.Verified {
		t.Fatal("a mount saved without any passed test must stay unverified")
	}
}
