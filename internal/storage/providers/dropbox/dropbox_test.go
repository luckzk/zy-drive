package dropbox

import (
	"bytes"
	"encoding/json"
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
type mockFS struct {
	dirs  map[string]bool   // "/a/b" -> dir exists ("" is root)
	files map[string][]byte // "/a/b.txt" -> content
}

func newMockFS() *mockFS {
	return &mockFS{dirs: map[string]bool{"": true}, files: map[string][]byte{}}
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
	if h := r.Header.Get("Dropbox-API-Arg"); h != "" {
		_ = json.Unmarshal([]byte(h), &arg)
	} else if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&arg)
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
	case "/2/files/upload":
		data, _ := io.ReadAll(r.Body)
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
