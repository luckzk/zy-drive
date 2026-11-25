package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	nodes, err := a.Browse(ctx, m.ID, "")
	if err != nil || len(nodes) != 2 {
		t.Fatalf("Browse s3: %v, %+v", err, nodes)
	}
	a2 := reopenApp(t, dbPath)
	if mounts := a2.ListMounts(); len(mounts) != 1 {
		t.Fatalf("s3 mount after restart: %+v", mounts)
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
	// OneDrive has no OAuth provider here (V0.2).
	if _, err := a.BeginOAuth(domain.ProviderOneDrive, "cid"); err == nil {
		t.Fatalf("OneDrive must not offer OAuth in this version")
	}
	start, err := a.BeginOAuth(domain.ProviderDropbox, "cid-1")
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
	// Mock token endpoint: the exchange is a plain form POST.
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("code") != "code-1" || r.Form.Get("redirect_uri") != "http://localhost" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "acc-9", "refresh_token": "ref-9",
		})
	}))
	defer tokenSrv.Close()
	tokens, err := a.ExchangeOAuthCode(domain.ProviderDropbox, "cid-1", "csec-1", "code-1", start.State, tokenSrv.URL)
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
