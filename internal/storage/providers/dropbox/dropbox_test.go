// SPDX-License-Identifier: MPL-2.0
package dropbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/storage/contracttest"
)

// mockFS is an in-memory Dropbox-shaped backend for tests. It speaks
// just enough of API v2 to exercise the driver: no real account, no
// real network to Dropbox.
type mockRev struct {
	rev      string
	data     []byte
	modified string
}

type mockFS struct {
	dirs  map[string]bool   // "/a/b" -> dir exists ("" is root)
	files map[string][]byte // "/a/b.txt" -> content
	// revisions: path -> older contents, oldest first. An upload
	// that overwrites a file pushes its old bytes here first.
	revisions map[string][]mockRev
	revSeq    int
	// Quota controls: quotaResp overrides the get_space_usage
	// body (default: individual 2 GiB), quotaStatus/quotaErr
	// make it fail, quotaBody records the raw request body of
	// the last Quota call (to assert the `null` Void form).
	quotaResp   map[string]any
	quotaStatus int
	quotaErr    string
	quotaBody   string
}

func newMockFS() *mockFS {
	return &mockFS{dirs: map[string]bool{"": true}, files: map[string][]byte{}, revisions: map[string][]mockRev{}}
}

// pushRevision saves p's current bytes as a new revision before an
// overwrite replaces them (no-op for a file with no content yet).
func (m *mockFS) pushRevision(p string) {
	data, ok := m.files[p]
	if !ok {
		return
	}
	m.revSeq++
	m.revisions[p] = append(m.revisions[p], mockRev{
		rev: fmt.Sprintf("rev-%d", m.revSeq), data: data, modified: "2026-11-24T00:00:00Z",
	})
}

func (m *mockFS) revisionJSON(p string, r mockRev) map[string]any {
	name := p[strings.LastIndex(p, "/")+1:]
	return map[string]any{".tag": "file", "name": name, "path_display": p, "path_lower": strings.ToLower(p),
		"id": "id:" + p, "rev": r.rev, "size": len(r.data), "server_modified": r.modified}
}

func (m *mockFS) parent(p string) string {
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return ""
	}
	return p[:i]
}

func (m *mockFS) entryJSON(p string) map[string]any {
	if m.dirs[p] {
		name := p[strings.LastIndex(p, "/")+1:]
		return map[string]any{".tag": "folder", "name": name, "path_display": p, "path_lower": strings.ToLower(p), "id": "id:" + p}
	}
	data := m.files[p]
	name := p[strings.LastIndex(p, "/")+1:]
	return map[string]any{".tag": "file", "name": name, "path_display": p, "path_lower": strings.ToLower(p), "id": "id:" + p,
		"size": len(data), "content_hash": "hash-" + p, "server_modified": "2026-11-24T00:00:00Z"}
}

func (m *mockFS) list(p string) []map[string]any {
	var out []map[string]any
	for d := range m.dirs {
		if d != "" && m.parent(d) == p {
			out = append(out, m.entryJSON(d))
		}
	}
	for f := range m.files {
		if m.parent(f) == p {
			out = append(out, m.entryJSON(f))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, summary string) {
	w.WriteHeader(status)
	writeJSON(w, map[string]any{"error_summary": summary})
}

// handler serves both the RPC and content endpoints on one server.
func (m *mockFS) handler(w http.ResponseWriter, r *http.Request) {
	var arg map[string]any
	rawBody := ""
	if h := r.Header.Get("Dropbox-API-Arg"); h != "" {
		_ = json.Unmarshal([]byte(h), &arg)
	} else if r.Body != nil {
		data, _ := io.ReadAll(r.Body)
		rawBody = string(data)
		_ = json.Unmarshal(data, &arg)
	}
	pth := ""
	if v, ok := arg["path"].(string); ok {
		pth = v
	}
	switch r.URL.Path {
	case "/2/files/list_folder":
		if !m.dirs[pth] {
			writeErr(w, http.StatusConflict, "path/not_found/")
			return
		}
		writeJSON(w, map[string]any{"entries": m.list(pth), "cursor": "cur-" + pth, "has_more": false})
	case "/2/files/list_folder/continue":
		writeJSON(w, map[string]any{"entries": []any{}, "cursor": arg["cursor"], "has_more": false})
	case "/2/files/get_metadata":
		if m.dirs[pth] || m.files[pth] != nil {
			writeJSON(w, m.entryJSON(pth))
			return
		}
		writeErr(w, http.StatusConflict, "path/not_found/")
	case "/2/files/create_folder_v2":
		if m.dirs[pth] {
			writeErr(w, http.StatusConflict, "path/conflict/folder/")
			return
		}
		m.dirs[pth] = true
		writeJSON(w, map[string]any{"metadata": m.entryJSON(pth)})
	case "/2/files/move_v2":
		from, _ := arg["from_path"].(string)
		to, _ := arg["to_path"].(string)
		if data, ok := m.files[from]; ok {
			delete(m.files, from)
			m.files[to] = data
			writeJSON(w, map[string]any{"metadata": m.entryJSON(to)})
			return
		}
		if m.dirs[from] {
			delete(m.dirs, from)
			m.dirs[to] = true
			writeJSON(w, map[string]any{"metadata": m.entryJSON(to)})
			return
		}
		writeErr(w, http.StatusConflict, "path/not_found/")
	case "/2/files/delete_v2":
		if m.files[pth] != nil {
			delete(m.files, pth)
			writeJSON(w, map[string]any{"metadata": m.entryJSON(pth)})
			return
		}
		if m.dirs[pth] {
			delete(m.dirs, pth)
			writeJSON(w, map[string]any{"metadata": m.entryJSON(pth)})
			return
		}
		writeErr(w, http.StatusConflict, "path/not_found/")
	case "/2/files/download":
		data, ok := m.files[pth]
		if !ok {
			writeErr(w, http.StatusConflict, "path/not_found/")
			return
		}
		w.Header().Set("Dropbox-API-Result", `{"name":"x"}`)
		_, _ = w.Write(data)
	case "/2/files/list_revisions":
		var entries []map[string]any
		for _, r := range m.revisions[pth] {
			entries = append(entries, m.revisionJSON(pth, r))
		}
		if entries == nil {
			entries = []map[string]any{}
		}
		writeJSON(w, map[string]any{"entries": entries, "is_deleted": false})
	case "/2/files/restore":
		rev, _ := arg["rev"].(string)
		for _, r := range m.revisions[pth] {
			if r.rev == rev {
				// Restoring creates a new head version: the old
				// head becomes a revision first.
				m.pushRevision(pth)
				m.files[pth] = r.data
				writeJSON(w, m.entryJSON(pth))
				return
			}
		}
		writeErr(w, http.StatusConflict, "path/not_found/")
	case "/2/files/copy_v2":
		from, _ := arg["from_path"].(string)
		to, _ := arg["to_path"].(string)
		if data, ok := m.files[from]; ok {
			m.files[to] = data
			m.dirs[m.parent(to)] = true
			writeJSON(w, map[string]any{"metadata": m.entryJSON(to)})
			return
		}
		if m.dirs[from] {
			m.dirs[to] = true
			for d := range m.dirs {
				if strings.HasPrefix(d, from+"/") {
					m.dirs[to+strings.TrimPrefix(d, from)] = true
				}
			}
			for f, data := range m.files {
				if strings.HasPrefix(f, from+"/") {
					m.files[to+strings.TrimPrefix(f, from)] = data
				}
			}
			writeJSON(w, map[string]any{"metadata": m.entryJSON(to)})
			return
		}
		writeErr(w, http.StatusConflict, "path/not_found/")
	case "/2/files/upload":
		data, _ := io.ReadAll(r.Body)
		m.pushRevision(pth)
		m.files[pth] = data
		m.dirs[m.parent(pth)] = true
		writeJSON(w, m.entryJSON(pth))
	case "/2/files/upload_session/start":
		writeJSON(w, map[string]any{"session_id": "sess-1"})
	case "/2/files/upload_session/append_v2":
		// The test session path is not exercised by the contract suite
		// (small files use the single call); accept and discard.
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	case "/2/files/upload_session/finish":
		data, _ := io.ReadAll(r.Body)
		commit, _ := arg["commit"].(map[string]any)
		cp, _ := commit["path"].(string)
		m.pushRevision(cp)
		m.files[cp] = data
		writeJSON(w, m.entryJSON(cp))
	case "/2/files/search_v2":
		q, _ := arg["query"].(string)
		var matches []map[string]any
		for f := range m.files {
			if strings.Contains(f, q) {
				matches = append(matches, map[string]any{"metadata": m.entryJSON(f)})
			}
		}
		writeJSON(w, map[string]any{"matches": matches, "has_more": false})
	case "/2/sharing/create_shared_link_with_settings":
		writeJSON(w, map[string]any{"url": "https://dl.dropboxusercontent.com/s/x"})
	case "/2/files/get_temporary_link":
		writeJSON(w, map[string]any{"link": "https://dl.dropboxusercontent.com/s/tmp"})
	case "/2/users/get_space_usage":
		m.quotaBody = rawBody
		if m.quotaStatus != 0 {
			writeErr(w, m.quotaStatus, m.quotaErr)
			return
		}
		if m.quotaResp != nil {
			writeJSON(w, m.quotaResp)
			return
		}
		writeJSON(w, map[string]any{"used": 123456789, "allocation": map[string]any{"allocated": 2147483648, ".tag": "individual"}})
	case "/oauth2/token":
		writeJSON(w, map[string]any{"access_token": "new-access-token", "token_type": "bearer", "expires_in": 14400})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newTestProvider(t *testing.T) (*Provider, *httptest.Server) {
	t.Helper()
	fs := newMockFS()
	srv := httptest.NewServer(http.HandlerFunc(fs.handler))
	p, err := newWithBases(storage.Config{Token: "test-token"}, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("newWithBases: %v", err)
	}
	return p, srv
}

func TestDropboxContractAgainstMock(t *testing.T) {
	p, srv := newTestProvider(t)
	defer srv.Close()
	contracttest.Run(t, func() (storage.Provider, error) { return p, nil })
}

func TestDropboxSearchShareThumbnail(t *testing.T) {
	p, srv := newTestProvider(t)
	defer srv.Close()
	ctx := t.Context()
	if _, err := p.Upload(ctx, "a.txt", strings.NewReader("hi"), 2); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	found, err := p.Search(ctx, "a.txt")
	if err != nil || len(found) != 1 {
		t.Fatalf("Search: %v, %d results", err, len(found))
	}
	if _, err := p.Share(ctx, "a.txt"); err != nil {
		t.Fatalf("Share: %v", err)
	}
	if _, err := p.Thumbnail(ctx, "a.txt"); err != nil {
		t.Fatalf("Thumbnail: %v", err)
	}
	if _, _, err := p.Delta(ctx, ""); err != nil {
		t.Fatalf("Delta: %v", err)
	}
}

func TestDropboxTokenRefresh(t *testing.T) {
	fs := newMockFS()
	srv := httptest.NewServer(http.HandlerFunc(fs.handler))
	defer srv.Close()
	// No access token, only a refresh token: the driver must refresh
	// against the mock token endpoint before its first call.
	p, err := newWithBases(storage.Config{Extra: map[string]string{
		"refresh_token": "rt-1", "token_url": srv.URL + "/oauth2/token",
	}}, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("newWithBases: %v", err)
	}
	nodes, err := p.List(t.Context(), "")
	if err != nil {
		t.Fatalf("List after refresh: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("expected empty root, got %d", len(nodes))
	}
}

func TestDropboxUploadSessionLarge(t *testing.T) {
	p, srv := newTestProvider(t)
	defer srv.Close()
	// size -1 forces the session path even for a small body.
	data := bytes.Repeat([]byte("x"), 100)
	node, err := p.Upload(t.Context(), "big.bin", bytes.NewReader(data), -1)
	if err != nil {
		t.Fatalf("session Upload: %v", err)
	}
	if node.Size != int64(len(data)) {
		t.Fatalf("session upload size: got %d want %d", node.Size, len(data))
	}
}

func TestDropboxQuota(t *testing.T) {
	p, srv := newTestProvider(t)
	defer srv.Close()
	qs, ok := any(p).(storage.QuotaSource)
	if !ok {
		t.Fatal("dropbox provider must implement storage.QuotaSource")
	}
	used, total, err := qs.Quota(t.Context())
	if err != nil {
		t.Fatalf("Quota: %v", err)
	}
	if used != 123456789 || total != 2147483648 {
		t.Fatalf("Quota: got used=%d total=%d, want 123456789 / 2147483648", used, total)
	}
}

// TestDropboxQuotaTeamAllocation: a team account reports
// allocation {.tag: "team", allocated, used} — the total still
// comes from `allocated`. (The reporting user is personal, so
// this is a regression guard, not the live root cause.)
func TestDropboxQuotaTeamAllocation(t *testing.T) {
	fs := newMockFS()
	fs.quotaResp = map[string]any{"used": 111, "allocation": map[string]any{".tag": "team", "allocated": 1099511627776, "used": 222}}
	srv := httptest.NewServer(http.HandlerFunc(fs.handler))
	defer srv.Close()
	p, err := newWithBases(storage.Config{Token: "test-token"}, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("newWithBases: %v", err)
	}
	qs := any(p).(storage.QuotaSource)
	used, total, err := qs.Quota(t.Context())
	if err != nil {
		t.Fatalf("Quota(team): %v", err)
	}
	if used != 111 || total != 1099511627776 {
		t.Fatalf("Quota(team): got used=%d total=%d, want 111 / 1099511627776", used, total)
	}
}

// TestDropboxQuotaMissingScope (2026-11-25): the live root cause
// of the user's "容量暂不可用" — get_space_usage requires
// account_info.read, which the 0.8.4 token did not have, so
// Dropbox answers 403 missing_scope while file browsing works.
// The driver must surface that error (not swallow it, not turn
// it into a zero quota) so the quota layer can map it to the
// scope + re-authorize guidance.
func TestDropboxQuotaMissingScope(t *testing.T) {
	fs := newMockFS()
	fs.quotaStatus = http.StatusForbidden
	fs.quotaErr = "missing_scope/"
	srv := httptest.NewServer(http.HandlerFunc(fs.handler))
	defer srv.Close()
	// A refresher is configured (as a real mount has one): the
	// shared client refreshes once on the 403 and retries, and
	// the retry still 403s — that second 403 is the error the
	// quota layer sees. Without a refresher the refresh failure
	// would mask the 403 entirely.
	p, err := newWithBases(storage.Config{Token: "test-token", Extra: map[string]string{
		"refresh_token": "test-refresh", "token_url": srv.URL + "/oauth2/token", "client_id": "x",
	}}, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("newWithBases: %v", err)
	}
	qs := any(p).(storage.QuotaSource)
	_, _, err = qs.Quota(t.Context())
	if err == nil {
		t.Fatal("Quota must fail on 403 missing_scope")
	}
	if !strings.Contains(err.Error(), "missing_scope") || !strings.Contains(err.Error(), "403") {
		t.Fatalf("Quota error must carry the 403 missing_scope, got: %v", err)
	}
}

// TestDropboxQuotaSendsNullBody: Void RPC input is sent as a
// literal JSON `null` body (the documented form), not an empty
// body.
func TestDropboxQuotaSendsNullBody(t *testing.T) {
	fs := newMockFS()
	srv := httptest.NewServer(http.HandlerFunc(fs.handler))
	defer srv.Close()
	p, err := newWithBases(storage.Config{Token: "test-token"}, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("newWithBases: %v", err)
	}
	qs := any(p).(storage.QuotaSource)
	if _, _, err := qs.Quota(t.Context()); err != nil {
		t.Fatalf("Quota: %v", err)
	}
	if fs.quotaBody != "null" {
		t.Fatalf("Quota request body: %q, want JSON null", fs.quotaBody)
	}
}

// TestDropboxDirectURL pins get_temporary_link as the preview URL:
// the mock returns the same link Thumbnail uses, and the provider
// advertises storage.DirectURLer.
func TestDropboxDirectURL(t *testing.T) {
	p, srv := newTestProvider(t)
	defer srv.Close()
	du, ok := any(p).(storage.DirectURLer)
	if !ok {
		t.Fatal("dropbox provider must implement storage.DirectURLer")
	}
	u, err := du.DirectURL(context.Background(), "/a.txt")
	if err != nil {
		t.Fatalf("DirectURL: %v", err)
	}
	if u != "https://dl.dropboxusercontent.com/s/tmp" {
		t.Fatalf("DirectURL = %q, want the mock temporary link", u)
	}
}

// TestDropboxVersionsRestoreCopy: two uploads of the same file
// leave one revision behind; restoring it brings the old bytes
// back as the new head (and the v2 head becomes a revision, so a
// restore is undoable); copy_v2 duplicates server-side without
// touching the source.
func TestDropboxVersionsRestoreCopy(t *testing.T) {
	p, srv := newTestProvider(t)
	defer srv.Close()
	ctx := t.Context()
	if _, ok := any(p).(storage.Versioner); !ok {
		t.Fatal("dropbox provider must implement storage.Versioner")
	}
	cp, ok := any(p).(storage.Copier)
	if !ok {
		t.Fatal("dropbox provider must implement storage.Copier")
	}
	if _, err := p.Upload(ctx, "a.txt", strings.NewReader("v1"), 2); err != nil {
		t.Fatalf("Upload v1: %v", err)
	}
	if vs, err := p.Versions(ctx, "a.txt"); err != nil || len(vs) != 0 {
		t.Fatalf("Versions after first upload: %v, %d versions, want 0", err, len(vs))
	}
	if _, err := p.Upload(ctx, "a.txt", strings.NewReader("v2"), 2); err != nil {
		t.Fatalf("Upload v2: %v", err)
	}
	vs, err := p.Versions(ctx, "a.txt")
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(vs) != 1 {
		t.Fatalf("Versions: got %d, want 1", len(vs))
	}
	if vs[0].Extra["versionId"] == "" {
		t.Fatalf("version node must carry Extra[versionId], got %+v", vs[0])
	}
	if vs[0].Name != "a.txt" || vs[0].Size != 2 {
		t.Fatalf("version node: %+v, want name a.txt size 2", vs[0])
	}
	if err := p.Restore(ctx, "a.txt", vs[0].Extra["versionId"]); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	rc, err := p.OpenRead(ctx, "a.txt")
	if err != nil {
		t.Fatalf("OpenRead after restore: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "v1" {
		t.Fatalf("content after restore: %q, want %q", got, "v1")
	}
	vs, err = p.Versions(ctx, "a.txt")
	if err != nil || len(vs) != 2 {
		t.Fatalf("Versions after restore: %v, %d versions, want 2", err, len(vs))
	}
	if err := p.Restore(ctx, "a.txt", "rev-nope"); err == nil {
		t.Fatal("Restore of an unknown revision must fail")
	}

	// copy_v2: the source stays, the destination has the same bytes.
	if err := cp.Copy(ctx, "a.txt", "b.txt"); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	for _, ref := range []string{"a.txt", "b.txt"} {
		rc, err := p.OpenRead(ctx, ref)
		if err != nil {
			t.Fatalf("OpenRead %s: %v", ref, err)
		}
		got, _ := io.ReadAll(rc)
		_ = rc.Close()
		if string(got) != "v1" {
			t.Fatalf("OpenRead %s: %q, want %q", ref, got, "v1")
		}
	}
	if err := cp.Copy(ctx, "missing.txt", "c.txt"); err == nil {
		t.Fatal("Copy of a missing source must fail")
	}
}
