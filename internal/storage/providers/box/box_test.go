// SPDX-License-Identifier: MPL-2.0
package box

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/storage/contracttest"
)

// mockBox is an in-memory Box API v2 for tests. API calls live
// under /2.0, uploads under /upload/2.0, the token endpoint at
// /oauth2/token — the same split as production (api.box.com,
// upload.box.com), so the driver cannot cheat on hosts.
type mockEntry struct {
	id         string
	name       string
	typ        string // "file" or "folder"
	parentID   string
	size       int64
	content    []byte
	modifiedAt string
	sha1       string
}

type mockSession struct {
	id       string
	folderID string
	fileName string
	fileSize int64
	partSize int64
	parts    map[int][]byte // by part number (1-based)
}

type mockBox struct {
	mu       sync.Mutex
	entries  map[string]*mockEntry
	nextID   int
	sessions map[string]*mockSession
	validTok string
	used     int64
	total    int64
}

func newMockBox() *mockBox {
	return &mockBox{
		entries:  map[string]*mockEntry{},
		sessions: map[string]*mockSession{},
		validTok: "test-token",
		used:     1_234_567_890,
		total:    10_737_418_240,
	}
}

func (m *mockBox) addEntry(name, typ, parentID string, content []byte) *mockEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	e := &mockEntry{
		id: fmt.Sprintf("%d", m.nextID), name: name, typ: typ, parentID: parentID,
		size: int64(len(content)), content: content,
		modifiedAt: "2026-11-25T00:00:00Z",
	}
	if typ == "file" {
		sum := sha1.Sum(content)
		e.sha1 = fmt.Sprintf("%x", sum)
	}
	m.entries[e.id] = e
	return e
}

func (m *mockBox) itemJSON(e *mockEntry) map[string]any {
	return map[string]any{
		"id": e.id, "type": e.typ, "name": e.name, "size": e.size,
		"modified_at": e.modifiedAt, "sha1": e.sha1,
	}
}

func (m *mockBox) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (m *mockBox) writeErr(w http.ResponseWriter, status int, code, msg string) {
	m.writeJSON(w, status, map[string]any{
		"type": "error", "status": status, "code": code, "message": msg,
	})
}

func (m *mockBox) checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != "Bearer "+m.validTok {
		m.writeErr(w, http.StatusUnauthorized, "unauthorized", "Unauthorized")
		return false
	}
	return true
}

func (m *mockBox) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Token endpoint: no Bearer check, it mints one.
	if r.URL.Path == "/oauth2/token" && r.Method == http.MethodPost {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") == "refresh_token" && r.Form.Get("refresh_token") == "rt1" {
			m.writeJSON(w, http.StatusOK, map[string]any{
				"access_token": "new-token", "refresh_token": "rt2",
				"expires_in": 3600, "token_type": "bearer",
			})
			return
		}
		m.writeErr(w, http.StatusBadRequest, "invalid_request", "unsupported grant")
		return
	}
	if !m.checkAuth(w, r) {
		return
	}
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/upload/2.0/files/content") && r.Method == http.MethodPost:
		m.handleDirectUpload(w, r)
	case strings.HasPrefix(p, "/upload/2.0/files/upload_sessions") && r.Method == http.MethodPost && strings.HasSuffix(p, "/commit"):
		m.handleCommit(w, r)
	case strings.HasPrefix(p, "/upload/2.0/files/upload_sessions") && r.Method == http.MethodPost:
		m.handleCreateSession(w, r)
	case strings.HasPrefix(p, "/upload/2.0/files/upload_sessions") && r.Method == http.MethodPut:
		m.handlePutPart(w, r)
	case strings.HasPrefix(p, "/upload/2.0/files/upload_sessions") && r.Method == http.MethodDelete:
		m.handleAbortSession(w, r)
	case strings.HasPrefix(p, "/2.0/folders/") && strings.HasSuffix(p, "/items") && r.Method == http.MethodGet:
		m.handleList(w, r)
	case p == "/2.0/folders" && r.Method == http.MethodPost:
		m.handleMkdir(w, r)
	case strings.HasPrefix(p, "/2.0/files/") && strings.HasSuffix(p, "/content") && r.Method == http.MethodGet:
		m.handleDownload(w, r)
	case strings.HasPrefix(p, "/2.0/files/") && strings.HasSuffix(p, "/shared_links") && r.Method == http.MethodPost:
		m.handleShare(w, r)
	case strings.HasPrefix(p, "/2.0/files/") && r.Method == http.MethodGet:
		m.handleGetFile(w, r)
	case strings.HasPrefix(p, "/2.0/files/") && r.Method == http.MethodPut:
		m.handleRenameFile(w, r)
	case strings.HasPrefix(p, "/2.0/files/") && r.Method == http.MethodDelete:
		m.handleDeleteFile(w, r)
	case strings.HasPrefix(p, "/2.0/folders/") && r.Method == http.MethodGet:
		m.handleGetFolder(w, r)
	case strings.HasPrefix(p, "/2.0/folders/") && r.Method == http.MethodPut:
		m.handleRenameFolder(w, r)
	case strings.HasPrefix(p, "/2.0/folders/") && r.Method == http.MethodDelete:
		m.handleDeleteFolder(w, r)
	case p == "/2.0/search" && r.Method == http.MethodGet:
		m.handleSearch(w, r)
	case p == "/2.0/users/me" && r.Method == http.MethodGet:
		m.writeJSON(w, http.StatusOK, map[string]any{
			"id": "1", "type": "user", "login": "test@box.test",
			"space_used": m.used, "space_amount": m.total,
		})
	default:
		m.writeErr(w, http.StatusNotFound, "not_found", "mock: no such route "+p)
	}
}

func (m *mockBox) children(parentID string) []*mockEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*mockEntry
	for _, e := range m.entries {
		if e.parentID == parentID {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func (m *mockBox) entryByID(id string) *mockEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.entries[id]
}

func (m *mockBox) handleList(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/items"), "/2.0/folders/")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 1000
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	all := m.children(id)
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	if offset > len(all) {
		offset = len(all)
	}
	var entries []map[string]any
	for _, e := range all[offset:end] {
		entries = append(entries, m.itemJSON(e))
	}
	m.writeJSON(w, http.StatusOK, map[string]any{
		"total_count": len(all), "entries": entries,
	})
}

func (m *mockBox) handleMkdir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string `json:"name"`
		Parent struct {
			ID string `json:"id"`
		} `json:"parent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.writeErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	for _, e := range m.children(body.Parent.ID) {
		if e.name == body.Name && e.typ == "folder" {
			m.writeErr(w, http.StatusConflict, "item_name_in_use", "folder already exists")
			return
		}
	}
	e := m.addEntry(body.Name, "folder", body.Parent.ID, nil)
	m.writeJSON(w, http.StatusCreated, m.itemJSON(e))
}

func (m *mockBox) handleGetFile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/2.0/files/")
	if e := m.entryByID(id); e != nil && e.typ == "file" {
		m.writeJSON(w, http.StatusOK, m.itemJSON(e))
		return
	}
	m.writeErr(w, http.StatusNotFound, "not_found", "no such file")
}

func (m *mockBox) handleGetFolder(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/2.0/folders/")
	if id == "0" {
		m.writeJSON(w, http.StatusOK, map[string]any{
			"id": "0", "type": "folder", "name": "All Files", "size": 0,
		})
		return
	}
	if e := m.entryByID(id); e != nil && e.typ == "folder" {
		m.writeJSON(w, http.StatusOK, m.itemJSON(e))
		return
	}
	m.writeErr(w, http.StatusNotFound, "not_found", "no such folder")
}

func (m *mockBox) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/2.0/files/"), "/content")
	if e := m.entryByID(id); e != nil && e.typ == "file" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(e.content)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(e.content)
		return
	}
	m.writeErr(w, http.StatusNotFound, "not_found", "no such file")
}

func (m *mockBox) handleDirectUpload(w http.ResponseWriter, r *http.Request) {
	mr, err := r.MultipartReader()
	if err != nil {
		m.writeErr(w, http.StatusBadRequest, "bad_request", "not multipart")
		return
	}
	var attrs struct {
		Name   string `json:"name"`
		Parent struct {
			ID string `json:"id"`
		} `json:"parent"`
	}
	var name string
	var content []byte
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			m.writeErr(w, http.StatusBadRequest, "bad_request", "bad part")
			return
		}
		switch part.FormName() {
		case "attributes":
			data, _ := io.ReadAll(part)
			_ = json.Unmarshal(data, &attrs)
		case "file":
			name = part.FileName()
			content, _ = io.ReadAll(part)
		}
	}
	if attrs.Name == "" {
		attrs.Name = name
	}
	e := m.addEntry(attrs.Name, "file", attrs.Parent.ID, content)
	m.writeJSON(w, http.StatusCreated, map[string]any{
		"total_count": 1, "entries": []map[string]any{m.itemJSON(e)},
	})
}

func (m *mockBox) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FolderID string `json:"folder_id"`
		FileSize int64  `json:"file_size"`
		FileName string `json:"file_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.writeErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	id := fmt.Sprintf("sess-%d", m.nextID)
	// Fixed 8 MiB part size, like production.
	m.sessions[id] = &mockSession{
		id: id, folderID: body.FolderID, fileName: body.FileName,
		fileSize: body.FileSize, partSize: 8 << 20, parts: map[int][]byte{},
	}
	totalParts := int((body.FileSize + (8 << 20) - 1) / (8 << 20))
	m.writeJSON(w, http.StatusCreated, map[string]any{
		"id": id, "part_size": 8 << 20, "total_parts": totalParts,
		"session_expires_at": time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
	})
}

func (m *mockBox) handlePutPart(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/upload/2.0/files/upload_sessions/")
	m.mu.Lock()
	s, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		m.writeErr(w, http.StatusNotFound, "not_found", "unknown session")
		return
	}
	data, _ := io.ReadAll(r.Body)
	// The Digest header must be the SHA-1 of exactly these bytes:
	// this is what proves the driver computes digests correctly.
	want := "sha=" + base64.StdEncoding.EncodeToString(func() []byte {
		sum := sha1.Sum(data)
		return sum[:]
	}())
	if got := r.Header.Get("Digest"); got != want {
		m.writeErr(w, http.StatusBadRequest, "bad_digest",
			fmt.Sprintf("Digest mismatch: got %q want %q", got, want))
		return
	}
	// Content-Range: bytes start-end/total.
	var start, end, total int64
	if _, err := fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-%d/%d", &start, &end, &total); err != nil {
		m.writeErr(w, http.StatusBadRequest, "bad_request", "bad Content-Range")
		return
	}
	if total != s.fileSize || end-start+1 != int64(len(data)) {
		m.writeErr(w, http.StatusBadRequest, "bad_request", "range/size mismatch")
		return
	}
	partNum := int(start/s.partSize) + 1
	m.mu.Lock()
	s.parts[partNum] = data
	m.mu.Unlock()
	m.writeJSON(w, http.StatusOK, map[string]any{"part": map[string]any{
		"part_id": fmt.Sprintf("part-%d", partNum),
		"offset":  start, "size": len(data),
		"sha1":    fmt.Sprintf("%x", sha1.Sum(data)),
	}})
}

func (m *mockBox) handleCommit(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/upload/2.0/files/upload_sessions/"), "/commit")
	var body struct {
		Parts []struct {
			PartID string `json:"part_id"`
			Offset int64  `json:"offset"`
			Size   int64  `json:"size"`
			SHA1   string `json:"sha1"`
		} `json:"parts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.writeErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	m.mu.Lock()
	s, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		m.writeErr(w, http.StatusNotFound, "not_found", "unknown session")
		return
	}
	var buf bytes.Buffer
	for _, p := range body.Parts {
		partNum := int(p.Offset/s.partSize) + 1
		chunk, ok := s.parts[partNum]
		if !ok {
			m.mu.Unlock()
			m.writeErr(w, http.StatusBadRequest, "bad_request", "missing part")
			return
		}
		buf.Write(chunk)
	}
	delete(m.sessions, id)
	m.mu.Unlock()
	if int64(buf.Len()) != s.fileSize {
		m.writeErr(w, http.StatusBadRequest, "bad_request", "size mismatch")
		return
	}
	e := m.addEntry(s.fileName, "file", s.folderID, buf.Bytes())
	m.writeJSON(w, http.StatusCreated, m.itemJSON(e))
}

func (m *mockBox) handleAbortSession(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/upload/2.0/files/upload_sessions/")
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (m *mockBox) handleRenameFile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/2.0/files/")
	var body struct {
		Name   string `json:"name"`
		Parent *struct {
			ID string `json:"id"`
		} `json:"parent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.writeErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok || e.typ != "file" {
		m.writeErr(w, http.StatusNotFound, "not_found", "no such file")
		return
	}
	if body.Name != "" {
		e.name = body.Name
	}
	if body.Parent != nil {
		e.parentID = body.Parent.ID
	}
	m.writeJSON(w, http.StatusOK, m.itemJSON(e))
}

func (m *mockBox) handleRenameFolder(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/2.0/folders/")
	var body struct {
		Name   string `json:"name"`
		Parent *struct {
			ID string `json:"id"`
		} `json:"parent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.writeErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok || e.typ != "folder" {
		m.writeErr(w, http.StatusNotFound, "not_found", "no such folder")
		return
	}
	if body.Name != "" {
		e.name = body.Name
	}
	if body.Parent != nil {
		e.parentID = body.Parent.ID
	}
	m.writeJSON(w, http.StatusOK, m.itemJSON(e))
}

func (m *mockBox) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/2.0/files/")
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[id]; !ok {
		m.writeErr(w, http.StatusNotFound, "not_found", "no such file")
		return
	}
	delete(m.entries, id)
	w.WriteHeader(http.StatusNoContent)
}

func (m *mockBox) handleDeleteFolder(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/2.0/folders/")
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[id]; !ok {
		m.writeErr(w, http.StatusNotFound, "not_found", "no such folder")
		return
	}
	// Recursive delete (Box's recursive=true).
	var del func(pid string)
	del = func(pid string) {
		for _, e := range m.entries {
			if e.parentID == pid {
				del(e.id)
				delete(m.entries, e.id)
			}
		}
	}
	del(id)
	delete(m.entries, id)
	w.WriteHeader(http.StatusNoContent)
}

func (m *mockBox) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(r.URL.Query().Get("query"))
	m.mu.Lock()
	var entries []map[string]any
	for _, e := range m.entries {
		if e.typ != "file" {
			continue
		}
		if !strings.Contains(strings.ToLower(e.name), q) {
			continue
		}
		// path_collection: the ancestors' names, root first.
		var pathNames []string
		cur := e
		for cur.parentID != "0" {
			if parent, ok := m.entries[cur.parentID]; ok {
				pathNames = append([]string{parent.name}, pathNames...)
				cur = parent
			} else {
				break
			}
		}
		var pathEntries []map[string]any
		for _, n := range pathNames {
			pathEntries = append(pathEntries, map[string]any{"name": n})
		}
		item := m.itemJSON(e)
		item["path_collection"] = map[string]any{"entries": pathEntries}
		entries = append(entries, item)
	}
	m.mu.Unlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i]["name"].(string) < entries[j]["name"].(string) })
	m.writeJSON(w, http.StatusOK, map[string]any{
		"total_count": len(entries), "entries": entries,
	})
}

func (m *mockBox) handleShare(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/2.0/files/"), "/shared_links")
	if e := m.entryByID(id); e == nil || e.typ != "file" {
		m.writeErr(w, http.StatusNotFound, "not_found", "no such file")
		return
	}
	m.writeJSON(w, http.StatusOK, map[string]any{
		"shared_link": map[string]any{"url": "https://box.test/s/" + id},
	})
}

// --- tests ---

func newTestProvider(t *testing.T, mock *mockBox) (*Provider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(mock)
	p, err := New(storage.Config{
		Token: "test-token",
		Extra: map[string]string{
			"api_base":    srv.URL + "/2.0",
			"upload_base": srv.URL + "/upload/2.0",
			"token_url":   srv.URL + "/oauth2/token",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p.(*Provider), srv
}

func TestContract(t *testing.T) {
	mock := newMockBox()
	p, srv := newTestProvider(t, mock)
	defer srv.Close()
	contracttest.Run(t, func() (storage.Provider, error) { return p, nil })
}

func TestBoxChunkedUpload(t *testing.T) {
	mock := newMockBox()
	p, srv := newTestProvider(t, mock)
	defer srv.Close()
	// 25 MiB: above the 20 MiB session threshold, 4 parts at the
	// mock's 8 MiB part size (8+8+8+1).
	size := int64(25 << 20)
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i % 251)
	}
	node, err := p.Upload(t.Context(), "big.bin", bytes.NewReader(content), size)
	if err != nil {
		t.Fatalf("Upload chunked: %v", err)
	}
	if node.Size != size {
		t.Fatalf("node.Size: got %d, want %d", node.Size, size)
	}
	rc, err := p.OpenRead(t.Context(), "big.bin")
	if err != nil {
		t.Fatalf("OpenRead: %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("chunked round-trip content mismatch (%d bytes)", len(got))
	}
}

func TestBoxQuota(t *testing.T) {
	mock := newMockBox()
	p, srv := newTestProvider(t, mock)
	defer srv.Close()
	used, total, err := p.Quota(t.Context())
	if err != nil {
		t.Fatalf("Quota: %v", err)
	}
	if used != 1_234_567_890 || total != 10_737_418_240 {
		t.Fatalf("Quota: got (%d, %d), want (1234567890, 10737418240)", used, total)
	}
}

func TestBoxSearch(t *testing.T) {
	mock := newMockBox()
	mock.addEntry("report.pdf", "file", "0", []byte("pdf"))
	mock.addEntry("notes.txt", "file", "0", []byte("txt"))
	p, srv := newTestProvider(t, mock)
	defer srv.Close()
	nodes, err := p.Search(t.Context(), "report")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Name != "report.pdf" {
		t.Fatalf("Search: got %v, want [report.pdf]", nodes)
	}
}

func TestBoxRefresh(t *testing.T) {
	mock := newMockBox()
	mock.validTok = "new-token" // the old token is already dead
	srv := httptest.NewServer(mock)
	defer srv.Close()
	p, err := New(storage.Config{
		Token: "test-token",
		Extra: map[string]string{
			"api_base":      srv.URL + "/2.0",
			"upload_base":   srv.URL + "/upload/2.0",
			"token_url":     srv.URL + "/oauth2/token",
			"refresh_token": "rt1",
			"client_id":     "box-client",
			"client_secret": "box-secret",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// The first List 401s, the client refreshes (rt1 -> new-token)
	// and replays: the retry must carry the NEW token (the
	// authTransport attaches it per attempt).
	nodes, err := p.(*Provider).List(t.Context(), "")
	if err != nil {
		t.Fatalf("List after refresh: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("List: got %d nodes, want 0", len(nodes))
	}
}

func TestBoxShare(t *testing.T) {
	mock := newMockBox()
	mock.addEntry("shared.pdf", "file", "0", []byte("pdf"))
	p, srv := newTestProvider(t, mock)
	defer srv.Close()
	link, err := p.ShareLink(t.Context(), "shared.pdf")
	if err != nil {
		t.Fatalf("ShareLink: %v", err)
	}
	if link != "https://box.test/s/1" {
		t.Fatalf("ShareLink: got %q, want https://box.test/s/1", link)
	}
}
