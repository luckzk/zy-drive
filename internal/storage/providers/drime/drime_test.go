// SPDX-License-Identifier: MPL-2.0
package drime

import (
	"bytes"
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

// mockDrime is an in-memory Drime API v1 for tests. It speaks just
// enough of the API to exercise the driver: no real account, no real
// network to Drime. Presigned part URLs point back at this same
// server (/r2part), so multipart uploads run end to end in memory.
type mockEntry struct {
	id        int64
	name      string
	typ       string // "folder" or a file type
	parentID  *int64
	size      int64
	content   []byte
	mime      string
	thumbURL  string
	createdAt string
	updatedAt string
}

type mockUpload struct {
	key     string
	uuid    string
	content []byte // assembled on complete
	parts   map[int][]byte
}

type mockDrime struct {
	mu       sync.Mutex
	entries  map[int64]*mockEntry
	nextID   int64
	uploads  map[string]*mockUpload // by uploadId
	links    map[int64]string       // entry id -> share hash
	fail429  int                    // next N API calls fail with 429
	validTok string                 // accepted Bearer token
	// spaceUsageSeen records every workspaceId the quota endpoint
	// was asked for, in order — the quota is workspace-scoped, so
	// a test can prove the driver asked for the mount's workspace
	// instead of the account default.
	spaceUsageSeen []int
	// partPUTs counts every presigned part PUT the mock received
	// (including retries); failPartPUTsAfter, when > 0, makes
	// every part PUT after that count fail with 500 — the
	// interrupt injection for resume tests (v0.9.1).
	partPUTs          int
	failPartPUTsAfter int
}

// partPUTCount returns how many presigned part PUTs the mock has
// received so far (including retries).
func (m *mockDrime) partPUTCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.partPUTs
}

// expireUpload deletes a server-side multipart session, so the
// next get-uploaded-parts for it answers 404 — the "session
// expired" case for resume tests.
func (m *mockDrime) expireUpload(uploadID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.uploads, uploadID)
}

func newMockDrime() *mockDrime {
	return &mockDrime{
		entries:  map[int64]*mockEntry{},
		nextID:   1000,
		uploads:  map[string]*mockUpload{},
		links:    map[int64]string{},
		validTok: "test-token",
	}
}

func (m *mockDrime) entryJSON(e *mockEntry) map[string]any {
	parent := any(nil)
	if e.parentID != nil {
		parent = *e.parentID
	}
	return map[string]any{
		"id": e.id, "name": e.name, "type": e.typ,
		"hash":                 fmt.Sprintf("hash-%d", e.id),
		"file_hash":            "",
		"file_size":            e.size,
		"mime":                 e.mime,
		"extension":            "",
		"parent_id":            parent,
		"workspace_id":         0,
		"url":                  "",
		"thumbnail_url":        e.thumbURL,
		"created_at":           e.createdAt,
		"updated_at":           e.updatedAt,
		"client_last_modified": nil,
	}
}

func (m *mockDrime) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (m *mockDrime) writeErr(w http.ResponseWriter, status int, msg string) {
	m.writeJSON(w, status, map[string]any{"message": msg})
}

func (m *mockDrime) checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != "Bearer "+m.validTok {
		m.writeErr(w, http.StatusUnauthorized, "Unauthenticated.")
		return false
	}
	return true
}

func (m *mockDrime) maybe429(w http.ResponseWriter) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail429 > 0 {
		m.fail429--
		m.writeErr(w, http.StatusTooManyRequests, "Too many requests.")
		return true
	}
	return false
}

func (m *mockDrime) addEntry(name, typ string, parentID *int64, content []byte) *mockEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	e := &mockEntry{
		id: m.nextID, name: name, typ: typ, parentID: parentID,
		size: int64(len(content)), content: content,
		createdAt: "2026-11-25T00:00:00.000000Z",
		updatedAt: "2026-11-25T00:00:00.000000Z",
	}
	if typ == "folder" {
		e.size = 0
	}
	m.entries[e.id] = e
	return e
}

func (m *mockDrime) children(parentID *int64) []*mockEntry {
	var out []*mockEntry
	for _, e := range m.entries {
		if (e.parentID == nil && parentID == nil) ||
			(e.parentID != nil && parentID != nil && *e.parentID == *parentID) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func (m *mockDrime) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Presigned part PUTs carry no credential.
	if r.URL.Path == "/r2part" && r.Method == http.MethodPut {
		data, _ := io.ReadAll(r.Body)
		up := r.URL.Query().Get("up")
		n, _ := strconv.Atoi(r.URL.Query().Get("part"))
		m.mu.Lock()
		m.partPUTs++
		fail := m.failPartPUTsAfter > 0 && m.partPUTs > m.failPartPUTsAfter
		if !fail {
			if u, ok := m.uploads[up]; ok {
				u.parts[n] = data
			}
		}
		m.mu.Unlock()
		if fail {
			// The part is NOT stored: the server never saw it.
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("ETag", fmt.Sprintf("\"etag-%d-%d\"", n, len(data)))
		w.WriteHeader(http.StatusOK)
		return
	}
	// OAuth token endpoint (no Bearer check: it mints one).
	if r.URL.Path == "/oauth/token" && r.Method == http.MethodPost {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") == "refresh_token" && r.Form.Get("refresh_token") != "" {
			m.writeJSON(w, http.StatusOK, map[string]any{
				"access_token": "new-token", "refresh_token": "rt2",
				"expires_in": 3600, "token_type": "Bearer",
			})
			return
		}
		m.writeErr(w, http.StatusBadRequest, "unsupported grant")
		return
	}
	if !m.checkAuth(w, r) {
		return
	}
	if m.maybe429(w) {
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/api/v1")
	switch {
	case p == "/drive/file-entries" && r.Method == http.MethodGet:
		m.handleList(w, r)
	case strings.HasPrefix(p, "/file-entries/") && strings.HasSuffix(p, "/model") && r.Method == http.MethodGet:
		m.handleModel(w, r)
	case strings.HasPrefix(p, "/file-entries/") && strings.HasSuffix(p, "/shareable-link"):
		m.handleShareLink(w, r)
	case p == "/file-entries/move" && r.Method == http.MethodPost:
		m.handleMove(w, r)
	case p == "/file-entries/delete" && r.Method == http.MethodPost:
		m.handleDelete(w, r)
	case strings.HasPrefix(p, "/file-entries/") && r.Method == http.MethodPut:
		m.handleRename(w, r)
	case strings.HasPrefix(p, "/file-entries/") && r.Method == http.MethodGet:
		m.handleDownload(w, r)
	case p == "/uploads" && r.Method == http.MethodPost:
		m.handleSimpleUpload(w, r)
	case p == "/folders" && r.Method == http.MethodPost:
		m.handleMkdir(w, r)
	case p == "/s3/multipart/create" && r.Method == http.MethodPost:
		m.handleMultipartCreate(w, r)
	case p == "/s3/multipart/batch-sign-part-urls" && r.Method == http.MethodPost:
		m.handleSign(w, r)
	case p == "/s3/multipart/complete" && r.Method == http.MethodPost:
		m.handleComplete(w, r)
	case p == "/s3/multipart/get-uploaded-parts" && r.Method == http.MethodPost:
		m.handleUploadedParts(w, r)
	case p == "/s3/multipart/abort" && r.Method == http.MethodPost:
		m.writeJSON(w, http.StatusOK, map[string]any{"status": "success"})
	case p == "/s3/entries" && r.Method == http.MethodPost:
		m.handleRegister(w, r)
	case p == "/me/workspaces" && r.Method == http.MethodGet:
		// The real shape: {"workspaces": [{"id": N, "name": "...",
		// "currentUser": {"role_name": "..."}}]}. The personal
		// workspace (0) is never in the list — ListWorkspaces
		// prepends it client-side.
		m.writeJSON(w, http.StatusOK, map[string]any{"workspaces": []map[string]any{
			{"id": 8762, "name": "sese", "currentUser": map[string]any{"role_name": "Workspace Owner"}},
			{"id": 1234, "name": "设计组", "currentUser": map[string]any{"role_name": "Member"}},
		}})
	case p == "/user/space-usage" && r.Method == http.MethodGet:
		// Workspace-scoped, like the real endpoint: each workspace
		// reports its own numbers, so a test that asks for 8762
		// and gets the personal numbers back fails loudly.
		ws, _ := strconv.Atoi(r.URL.Query().Get("workspaceId"))
		m.mu.Lock()
		m.spaceUsageSeen = append(m.spaceUsageSeen, ws)
		m.mu.Unlock()
		used, avail := int64(100_000_000_000), int64(412_000_000_000) // personal (0)
		if ws == 8762 {
			used, avail = 312345678901, 237165364723
		}
		m.writeJSON(w, http.StatusOK, map[string]any{"used": used, "available": avail})
	default:
		m.writeErr(w, http.StatusNotFound, "mock: no such route "+p)
	}
}

func (m *mockDrime) handleList(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := r.URL.Query()
	var parentID *int64
	if v := q.Get("parentIds[]"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			m.writeErr(w, http.StatusBadRequest, "bad parentIds")
			return
		}
		parentID = &n
	}
	query := strings.ToLower(q.Get("query"))
	var all []*mockEntry
	if query != "" && q.Get("parentIds[]") == "" {
		// A query searches the whole workspace, not just one folder.
		for _, e := range m.entries {
			if strings.Contains(strings.ToLower(e.name), query) {
				all = append(all, e)
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].name < all[j].name })
	} else {
		for _, e := range m.children(parentID) {
			if query != "" && !strings.Contains(strings.ToLower(e.name), query) {
				continue
			}
			all = append(all, e)
		}
	}
	perPage := 50
	if v := q.Get("perPage"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			perPage = n
		}
	}
	page := 1
	if v := q.Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			page = n
		}
	}
	lastPage := (len(all) + perPage - 1) / perPage
	if lastPage == 0 {
		lastPage = 1
	}
	start := (page - 1) * perPage
	if start > len(all) {
		start = len(all)
	}
	end := start + perPage
	if end > len(all) {
		end = len(all)
	}
	data := []map[string]any{}
	for _, e := range all[start:end] {
		data = append(data, m.entryJSON(e))
	}
	m.writeJSON(w, http.StatusOK, map[string]any{
		"data": data, "current_page": page, "last_page": lastPage,
		"per_page": perPage, "total": len(all),
	})
}

func (m *mockDrime) entryByID(w http.ResponseWriter, r *http.Request) (*mockEntry, bool) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/file-entries/"), "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		m.writeErr(w, http.StatusBadRequest, "bad entry id")
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		m.writeErr(w, http.StatusNotFound, "entry not found")
		return nil, false
	}
	return e, true
}

func (m *mockDrime) handleModel(w http.ResponseWriter, r *http.Request) {
	e, ok := m.entryByID(w, r)
	if !ok {
		return
	}
	m.writeJSON(w, http.StatusOK, map[string]any{"fileEntry": m.entryJSON(e), "status": "success"})
}

func (m *mockDrime) handleDownload(w http.ResponseWriter, r *http.Request) {
	e, ok := m.entryByID(w, r)
	if !ok {
		return
	}
	if e.typ == "folder" {
		m.writeErr(w, http.StatusBadRequest, "cannot download a folder here")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", e.name))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(e.content)
}

func (m *mockDrime) handleSimpleUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		m.writeErr(w, http.StatusBadRequest, "bad multipart form")
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		m.writeErr(w, http.StatusUnprocessableEntity, "The file field is required.")
		return
	}
	defer f.Close()
	data, _ := io.ReadAll(f)
	var parentID *int64
	if v := r.FormValue("parentId"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			m.writeErr(w, http.StatusBadRequest, "bad parentId")
			return
		}
		parentID = &n
	}
	e := m.addEntry(hdr.Filename, "file", parentID, data)
	e.mime = mimeOf(hdr.Filename)
	m.writeJSON(w, http.StatusCreated, map[string]any{"fileEntry": m.entryJSON(e), "status": "success"})
}

func (m *mockDrime) handleMkdir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		ParentID *int64 `json:"parentId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		m.writeErr(w, http.StatusUnprocessableEntity, "The name field is required.")
		return
	}
	for _, e := range m.children(body.ParentID) {
		if e.typ == "folder" && e.name == body.Name {
			m.writeErr(w, http.StatusUnprocessableEntity, "The name has already been taken.")
			return
		}
	}
	e := m.addEntry(body.Name, "folder", body.ParentID, nil)
	m.writeJSON(w, http.StatusOK, map[string]any{"folder": m.entryJSON(e), "status": "success"})
}

func (m *mockDrime) handleRename(w http.ResponseWriter, r *http.Request) {
	e, ok := m.entryByID(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		m.writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	m.mu.Lock()
	e.name = body.Name
	e.updatedAt = "2026-11-25T01:00:00.000000Z"
	m.mu.Unlock()
	m.writeJSON(w, http.StatusOK, map[string]any{"fileEntry": m.entryJSON(e), "status": "success"})
}

func (m *mockDrime) handleMove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EntryIDs      []int64 `json:"entryIds"`
		DestinationID *int64  `json:"destinationId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.writeErr(w, http.StatusBadRequest, "bad body")
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var moved []map[string]any
	for _, id := range body.EntryIDs {
		if e, ok := m.entries[id]; ok {
			e.parentID = body.DestinationID
			moved = append(moved, m.entryJSON(e))
		}
	}
	m.writeJSON(w, http.StatusOK, map[string]any{"entries": moved, "status": "success"})
}

func (m *mockDrime) handleDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EntryIDs []int64 `json:"entryIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.writeErr(w, http.StatusBadRequest, "bad body")
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range body.EntryIDs {
		delete(m.entries, id)
	}
	m.writeJSON(w, http.StatusOK, map[string]any{"status": "success"})
}

func (m *mockDrime) handleMultipartCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.writeErr(w, http.StatusBadRequest, "bad body")
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	uuid := fmt.Sprintf("uuid-%d", m.nextID)
	m.nextID++
	upID := fmt.Sprintf("up-%d", m.nextID)
	m.uploads[upID] = &mockUpload{key: "uploads/" + uuid + "/" + uuid, uuid: uuid, parts: map[int][]byte{}}
	m.writeJSON(w, http.StatusOK, map[string]any{
		"key": m.uploads[upID].key, "uploadId": upID,
		"acl": "private", "status": "success",
	})
}

func (m *mockDrime) handleSign(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key         string `json:"key"`
		UploadID    string `json:"uploadId"`
		PartNumbers []int  `json:"partNumbers"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.writeErr(w, http.StatusBadRequest, "bad body")
		return
	}
	m.mu.Lock()
	_, ok := m.uploads[body.UploadID]
	m.mu.Unlock()
	if !ok {
		m.writeErr(w, http.StatusNotFound, "unknown uploadId")
		return
	}
	var urls []map[string]any
	for _, n := range body.PartNumbers {
		urls = append(urls, map[string]any{
			"partNumber": n,
			"url":        fmt.Sprintf("http://%s/r2part?up=%s&part=%d", r.Host, body.UploadID, n),
		})
	}
	m.writeJSON(w, http.StatusOK, map[string]any{"urls": urls, "status": "success"})
}

func (m *mockDrime) handleComplete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key      string `json:"key"`
		UploadID string `json:"uploadId"`
		Parts    []struct {
			PartNumber int    `json:"PartNumber"`
			ETag       string `json:"ETag"`
		} `json:"parts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.writeErr(w, http.StatusBadRequest, "bad body")
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.uploads[body.UploadID]
	if !ok {
		m.writeErr(w, http.StatusNotFound, "unknown uploadId")
		return
	}
	var buf bytes.Buffer
	for _, p := range body.Parts {
		chunk, ok := u.parts[p.PartNumber]
		if !ok {
			m.writeErr(w, http.StatusBadRequest, "missing part")
			return
		}
		buf.Write(chunk)
	}
	u.content = buf.Bytes()
	m.writeJSON(w, http.StatusOK, map[string]any{"location": "https://mock/r2/" + u.key, "status": "success"})
}

func (m *mockDrime) handleUploadedParts(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key      string `json:"key"`
		UploadID string `json:"uploadId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.writeErr(w, http.StatusBadRequest, "bad body")
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.uploads[body.UploadID]
	if !ok {
		// The real endpoint answers 4xx for an unknown upload
		// id; the driver treats that as "session expired".
		m.writeErr(w, http.StatusNotFound, "upload not found")
		return
	}
	var parts []map[string]any
	for n, data := range u.parts {
		parts = append(parts, map[string]any{
			"PartNumber": n,
			// Opaque on purpose (like a non-MD5 ETag): the
			// driver verifies parts with the MD5 it recorded
			// locally, never with this value.
			"ETag": fmt.Sprintf("\"etag-%d-%d\"", n, len(data)),
			"Size": len(data),
		})
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i]["PartNumber"].(int) < parts[j]["PartNumber"].(int) })
	m.writeJSON(w, http.StatusOK, map[string]any{"parts": parts, "status": "success"})
}

func (m *mockDrime) handleRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Filename   string `json:"filename"`
		Size       int64  `json:"size"`
		ClientName string `json:"clientName"`
		ParentID   *int64 `json:"parentId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		m.writeErr(w, http.StatusBadRequest, "bad body")
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var content []byte
	for _, u := range m.uploads {
		if u.uuid == body.Filename {
			content = u.content
			break
		}
	}
	if content == nil {
		m.writeErr(w, http.StatusNotFound, "no completed upload for filename")
		return
	}
	if int64(len(content)) != body.Size {
		m.writeErr(w, http.StatusBadRequest, "size mismatch")
		return
	}
	e := &mockEntry{
		name: body.ClientName, typ: "file", parentID: body.ParentID,
		size: body.Size, content: content,
		createdAt: "2026-11-25T00:00:00.000000Z",
		updatedAt: "2026-11-25T00:00:00.000000Z",
	}
	m.nextID++
	e.id = m.nextID
	e.mime = mimeOf(e.name)
	m.entries[e.id] = e
	m.writeJSON(w, http.StatusOK, map[string]any{"fileEntry": m.entryJSON(e), "status": "success"})
}

func (m *mockDrime) handleShareLink(w http.ResponseWriter, r *http.Request) {
	e, ok := m.entryByID(w, r)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.Method == http.MethodGet {
		hash, ok := m.links[e.id]
		if !ok {
			m.writeErr(w, http.StatusNotFound, "Shareable link not found")
			return
		}
		m.writeJSON(w, http.StatusOK, map[string]any{
			"link": map[string]any{"id": 789, "hash": hash, "entry_id": e.id}, "status": "success",
		})
		return
	}
	hash := fmt.Sprintf("hash-%d", e.id)
	m.links[e.id] = hash
	m.writeJSON(w, http.StatusOK, map[string]any{
		"link": map[string]any{"id": 789, "hash": hash, "entry_id": e.id}, "status": "success",
	})
}

// --- tests ---

func newTestProvider(t *testing.T, mock *mockDrime) (*Provider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(mock)
	p, err := New(storage.Config{
		Token: "test-token",
		Extra: map[string]string{"api_base": srv.URL + "/api/v1"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p.(*Provider), srv
}

func TestContract(t *testing.T) {
	mock := newMockDrime()
	p, srv := newTestProvider(t, mock)
	defer srv.Close()
	contracttest.Run(t, func() (storage.Provider, error) { return p, nil })
}

func TestListPagination(t *testing.T) {
	mock := newMockDrime()
	for i := 0; i < 250; i++ {
		mock.addEntry(fmt.Sprintf("file-%03d.txt", i), "file", nil, []byte("x"))
	}
	p, srv := newTestProvider(t, mock)
	defer srv.Close()
	nodes, err := p.List(t.Context(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(nodes) != 250 {
		t.Fatalf("List: got %d nodes, want 250 (pagination)", len(nodes))
	}
}

func TestMultipartUpload(t *testing.T) {
	mock := newMockDrime()
	p, srv := newTestProvider(t, mock)
	defer srv.Close()
	size := int64(singleUploadLimit) + 123
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i % 251)
	}
	node, err := p.Upload(t.Context(), "big.bin", bytes.NewReader(content), size)
	if err != nil {
		t.Fatalf("Upload multipart: %v", err)
	}
	if node.Size != size {
		t.Fatalf("Upload size: got %d want %d", node.Size, size)
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
		t.Fatalf("multipart round-trip content mismatch (%d bytes)", len(got))
	}
}

func TestUnknownSizeUpload(t *testing.T) {
	mock := newMockDrime()
	p, srv := newTestProvider(t, mock)
	defer srv.Close()
	// Just over the simple limit with unknown size: exercises the
	// temp-file spool path.
	size := int64(singleUploadLimit) + 10
	content := bytes.Repeat([]byte("z"), int(size))
	node, err := p.Upload(t.Context(), "spooled.bin", bytes.NewReader(content), -1)
	if err != nil {
		t.Fatalf("Upload unknown size: %v", err)
	}
	if node.Size != size {
		t.Fatalf("Upload size: got %d want %d", node.Size, size)
	}
}

func TestAuthErrors(t *testing.T) {
	mock := newMockDrime()
	srv := httptest.NewServer(mock)
	defer srv.Close()
	p, err := New(storage.Config{
		Token: "wrong-token",
		Extra: map[string]string{"api_base": srv.URL + "/api/v1"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.List(t.Context(), ""); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("List with bad token: got %v, want a 401 error", err)
	}
}

func TestRateLimitGivesUp(t *testing.T) {
	mock := newMockDrime()
	p, srv := newTestProvider(t, mock)
	defer srv.Close()
	// Shorten the 429 wait for the test (production waits 60s and
	// retries once — the count, not the duration, is what this
	// test proves).
	p.client.Policy.Backoff429 = time.Millisecond
	mock.mu.Lock()
	mock.fail429 = 2 // first call + the one retry both hit 429
	mock.mu.Unlock()
	if _, err := p.List(t.Context(), ""); err == nil || !strings.Contains(err.Error(), "rate limited (429)") {
		t.Fatalf("List under 429: got %v, want a rate-limited give-up", err)
	}
}

func TestRateLimitOneRetrySucceeds(t *testing.T) {
	mock := newMockDrime()
	p, srv := newTestProvider(t, mock)
	defer srv.Close()
	p.client.Policy.Backoff429 = time.Millisecond
	mock.mu.Lock()
	mock.fail429 = 1 // first call 429s, the single retry succeeds
	mock.mu.Unlock()
	if _, err := p.List(t.Context(), ""); err != nil {
		t.Fatalf("List after one 429: %v", err)
	}
}

func TestOAuthRefresh(t *testing.T) {
	mock := newMockDrime()
	// The stale token 401s; the token endpoint mints "new-token",
	// which the mock then accepts, so the replayed request succeeds.
	mock.validTok = "new-token"
	srv := httptest.NewServer(mock)
	defer srv.Close()
	p, err := New(storage.Config{
		Token: "old-token",
		Extra: map[string]string{
			"api_base":      srv.URL + "/api/v1",
			"refresh_token": "rt",
			"client_id":     "cid",
			"token_url":     srv.URL + "/oauth/token",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.List(t.Context(), ""); err != nil {
		t.Fatalf("List after refresh: %v", err)
	}
}

func TestShareAndSearch(t *testing.T) {
	mock := newMockDrime()
	p, srv := newTestProvider(t, mock)
	defer srv.Close()
	ctx := t.Context()
	if err := p.Mkdir(ctx, "photos"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if _, err := p.Upload(ctx, "photos/sunset.jpg", strings.NewReader("jpeg-bytes"), 10); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	url, err := p.Share(ctx, "photos/sunset.jpg")
	if err != nil {
		t.Fatalf("Share: %v", err)
	}
	if !strings.HasPrefix(url, "https://app.drime.cloud/drive/shares/") {
		t.Fatalf("Share URL: %q", url)
	}
	// Sharing again reuses the same link.
	url2, err := p.Share(ctx, "photos/sunset.jpg")
	if err != nil || url2 != url {
		t.Fatalf("Share twice: %q vs %q (%v)", url, url2, err)
	}
	nodes, err := p.Search(ctx, "sunset")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("Search: got %d nodes, want 1", len(nodes))
	}
	if nodes[0].Ref != "photos/sunset.jpg" {
		t.Fatalf("Search ref: %q, want photos/sunset.jpg", nodes[0].Ref)
	}
	// The search ref must resolve: Stat on it works.
	if _, err := p.Stat(ctx, nodes[0].Ref); err != nil {
		t.Fatalf("Stat(search ref): %v", err)
	}
	thumb, err := p.Thumbnail(ctx, "photos/sunset.jpg")
	if err == nil {
		t.Fatalf("Thumbnail without one: got %q, want an error", thumb)
	}
}

func TestMkdirIdempotentAndMoveRename(t *testing.T) {
	mock := newMockDrime()
	p, srv := newTestProvider(t, mock)
	defer srv.Close()
	ctx := t.Context()
	if err := p.Mkdir(ctx, "a"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := p.Mkdir(ctx, "a"); err != nil {
		t.Fatalf("Mkdir twice: %v (must be idempotent)", err)
	}
	if _, err := p.Upload(ctx, "a/f.txt", strings.NewReader("hi"), 2); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	// Same-folder rename.
	if err := p.Rename(ctx, "a/f.txt", "a/g.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	// Cross-folder move (creates the target folder first).
	if err := p.Mkdir(ctx, "b"); err != nil {
		t.Fatalf("Mkdir b: %v", err)
	}
	if err := p.Rename(ctx, "a/g.txt", "b/g.txt"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	nodes, err := p.List(ctx, "b")
	if err != nil {
		t.Fatalf("List b: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Name != "g.txt" {
		t.Fatalf("List b: %+v", nodes)
	}
	// Old path is gone.
	if _, err := p.Stat(ctx, "a/g.txt"); err == nil {
		t.Fatalf("old path still resolves after move")
	}
}

func TestNewRequiresToken(t *testing.T) {
	if _, err := New(storage.Config{}); err == nil {
		t.Fatalf("New without token: want an error")
	}
	if _, err := New(storage.Config{Extra: map[string]string{"workspace_id": "abc"}}); err == nil {
		t.Fatalf("New with bad workspace_id: want an error")
	}
}

func TestCaps(t *testing.T) {
	mock := newMockDrime()
	p, srv := newTestProvider(t, mock)
	defer srv.Close()
	caps := p.Caps()
	if !caps.IDBased || !caps.SupportsSearch || !caps.SupportsShare || !caps.SupportsThumbnail {
		t.Fatalf("Caps missing Drime features: %+v", caps)
	}
	if caps.SupportsVersions || caps.SupportsDelta || caps.SupportsServerSideCopy {
		t.Fatalf("Caps claims unsupported features: %+v", caps)
	}
	if caps.MinPartSize != singleUploadLimit {
		t.Fatalf("MinPartSize: got %d want %d", caps.MinPartSize, singleUploadLimit)
	}
}

// TestDrimeQuotaIsWorkspaceScoped: the quota is the MOUNT's
// workspace usage, not the account total. Two providers against
// the same mock — one bound to team workspace 8762, one to the
// personal workspace (no workspace_id) — must ask for their own
// workspaceId and get back different numbers. (The old mock
// returned one fixed pair whatever the query said, so a driver
// that always asked for workspace 0 would still have passed.)
func TestDrimeQuotaIsWorkspaceScoped(t *testing.T) {
	mock := newMockDrime()
	srv := httptest.NewServer(mock)
	defer srv.Close()
	newProv := func(workspace string) *Provider {
		t.Helper()
		extra := map[string]string{"api_base": srv.URL + "/api/v1"}
		if workspace != "" {
			extra["workspace_id"] = workspace
		}
		p, err := New(storage.Config{Token: "test-token", Extra: extra})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return p.(*Provider)
	}
	team := newProv("8762")
	used, total, err := team.Quota(t.Context())
	if err != nil {
		t.Fatalf("team Quota: %v", err)
	}
	// Mock team workspace 8762: used=312345678901,
	// available=237165364723, total is their sum.
	if used != 312345678901 || total != 549511043624 {
		t.Fatalf("team Quota: got used=%d total=%d, want 312345678901 / 549511043624", used, total)
	}
	personal := newProv("")
	used, total, err = personal.Quota(t.Context())
	if err != nil {
		t.Fatalf("personal Quota: %v", err)
	}
	if used != 100_000_000_000 || total != 512_000_000_000 {
		t.Fatalf("personal Quota: got used=%d total=%d, want 100000000000 / 512000000000", used, total)
	}
	mock.mu.Lock()
	seen := append([]int(nil), mock.spaceUsageSeen...)
	mock.mu.Unlock()
	if len(seen) != 2 || seen[0] != 8762 || seen[1] != 0 {
		t.Fatalf("space-usage workspaceIds: got %v, want [8762 0]", seen)
	}
}

// TestListWorkspaces: GET /me/workspaces returns the team
// workspaces; the personal workspace (id 0, "个人空间") is
// prepended client-side because the server never lists it. The
// shape mirrors the real API (verified 2026-11-19: 8762 "sese",
// holder is Workspace Owner).
func TestListWorkspaces(t *testing.T) {
	mock := newMockDrime()
	srv := httptest.NewServer(mock)
	defer srv.Close()
	ws, err := ListWorkspaces(t.Context(), srv.URL+"/api/v1", "test-token")
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	want := []Workspace{
		{ID: 0, Name: "个人空间"},
		{ID: 8762, Name: "sese", Role: "Workspace Owner"},
		{ID: 1234, Name: "设计组", Role: "Member"},
	}
	if len(ws) != len(want) {
		t.Fatalf("workspaces = %+v, want %+v", ws, want)
	}
	for i, w := range want {
		if ws[i] != w {
			t.Fatalf("workspaces[%d] = %+v, want %+v", i, ws[i], w)
		}
	}
}

// TestListWorkspacesBadToken: a wrong token is a plain-language
// 401, not a raw dump — and the wizard shows it next to the token
// field.
func TestListWorkspacesBadToken(t *testing.T) {
	mock := newMockDrime()
	srv := httptest.NewServer(mock)
	defer srv.Close()
	if _, err := ListWorkspaces(t.Context(), srv.URL+"/api/v1", "wrong-token"); err == nil {
		t.Fatal("wrong token must fail")
	} else if !strings.Contains(err.Error(), "令牌") {
		t.Fatalf("bad-token error = %v, want it to mention the token", err)
	}
}

// TestListWorkspacesEmptyToken: no token, no request.
func TestListWorkspacesEmptyToken(t *testing.T) {
	if _, err := ListWorkspaces(t.Context(), "", ""); err == nil {
		t.Fatal("empty token must fail before any request")
	}
}
