package onedrive

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/luckzk/zy-drive/internal/storage"
)

// mockGraph is an in-memory Microsoft Graph v1.0 for tests. It speaks
// just enough of the drive API to exercise the driver: no real
// account, no real network to Microsoft. The downloadUrl and the
// upload-session uploadUrl point back at this same server, so
// downloads and chunked uploads run end to end in memory.
type mockItem struct {
	id       string
	name     string
	parentID string
	isDir    bool
	content  []byte
	mime     string
	deleted  bool
}

type mockSession struct {
	parentID string
	name     string
	total    int64
	buf      []byte
}

type mockGraph struct {
	mu       sync.Mutex
	items    map[string]*mockItem
	nextID   int
	sessions map[string]*mockSession
	fail429  int    // next N Graph calls fail with 429 (Retry-After: 0)
	validTok string // accepted Bearer token
	base     string // server URL, set after httptest.NewServer
	// delta fixture: items reported by the token=tok1 delta call.
	deltaChanged []string // item ids
	deltaDeleted []string // item ids reported with the deleted facet
}

func newMockGraph() *mockGraph {
	m := &mockGraph{
		items:    map[string]*mockItem{},
		sessions: map[string]*mockSession{},
		nextID:   100,
		validTok: "test-token",
	}
	m.items["root"] = &mockItem{id: "root", name: "root", isDir: true}
	return m
}

func (m *mockGraph) addItem(name, parentID string, isDir bool, content []byte) *mockItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	it := &mockItem{
		id:       strconv.Itoa(m.nextID),
		name:     name,
		parentID: parentID,
		isDir:    isDir,
		content:  content,
		mime:     mimeOf(name),
	}
	m.items[it.id] = it
	return it
}

func (m *mockGraph) itemJSON(it *mockItem) map[string]any {
	out := map[string]any{
		"id": it.id, "name": it.name, "size": int64(len(it.content)),
		"createdDateTime": "2026-01-02T03:04:05Z", "lastModifiedDateTime": "2026-01-02T03:04:05Z",
		"webUrl": "https://onedrive.live.com/?id=" + it.id,
	}
	if it.isDir {
		out["folder"] = map[string]any{"childCount": 0}
	} else {
		h := sha1.Sum(it.content)
		out["file"] = map[string]any{
			"mimeType": it.mime,
			"hashes":   map[string]any{"sha1Hash": fmt.Sprintf("%x", h)},
		}
		out["@microsoft.graph.downloadUrl"] = m.base + "/dl/" + it.id
	}
	if it.parentID != "" && it.parentID != "root" {
		out["parentReference"] = map[string]any{"id": it.parentID, "path": "/drive/root:" + m.pathOf(it.parentID)}
	} else if it.parentID == "root" {
		out["parentReference"] = map[string]any{"id": "root", "path": "/drive/root:"}
	}
	if it.deleted {
		out["deleted"] = map[string]any{}
	}
	return out
}

// pathOf returns the display path of an item id ("/a/b"), "" for root.
func (m *mockGraph) pathOf(id string) string {
	var parts []string
	for id != "" && id != "root" {
		it := m.items[id]
		if it == nil {
			break
		}
		parts = append([]string{it.name}, parts...)
		id = it.parentID
	}
	if len(parts) == 0 {
		return ""
	}
	return "/" + strings.Join(parts, "/")
}

func (m *mockGraph) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (m *mockGraph) writeErr(w http.ResponseWriter, status int, code, msg string) {
	m.writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": msg}})
}

func (m *mockGraph) checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != "Bearer "+m.validTok {
		m.writeErr(w, http.StatusUnauthorized, "InvalidAuthenticationToken", "Access token is empty or invalid.")
		return false
	}
	return true
}

func (m *mockGraph) maybe429(w http.ResponseWriter) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail429 > 0 {
		m.fail429--
		w.Header().Set("Retry-After", "0")
		m.writeErr(w, http.StatusTooManyRequests, "TooManyRequests", "Too many requests.")
		return true
	}
	return false
}

func (m *mockGraph) childrenOf(parentID string) []*mockItem {
	var out []*mockItem
	for _, it := range m.items {
		if it.parentID == parentID && !it.deleted {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func (m *mockGraph) findByPath(p string) *mockItem {
	p = strings.Trim(p, "/")
	if p == "" {
		return m.items["root"]
	}
	parts := strings.Split(p, "/")
	cur := m.items["root"]
	for _, seg := range parts {
		var next *mockItem
		for _, it := range m.items {
			if it.parentID == cur.id && it.name == seg && !it.deleted {
				next = it
				break
			}
		}
		if next == nil {
			return nil
		}
		cur = next
	}
	return cur
}

func (m *mockGraph) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Token endpoint (refresh flow) — no bearer check here.
	if r.URL.Path == "/token" && r.Method == http.MethodPost {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") == "refresh_token" && r.Form.Get("refresh_token") == "rt-1" {
			m.writeJSON(w, http.StatusOK, map[string]any{
				"access_token": "test-token", "refresh_token": "rt-2",
				"expires_in": 3600, "token_type": "Bearer",
			})
			return
		}
		m.writeErr(w, http.StatusBadRequest, "invalid_grant", "Refresh token is invalid.")
		return
	}
	// Download URL: pre-authenticated, no bearer token.
	if strings.HasPrefix(r.URL.Path, "/dl/") {
		id := strings.TrimPrefix(r.URL.Path, "/dl/")
		it := m.items[id]
		if it == nil || it.isDir {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(it.content)
		return
	}
	// Upload session chunk PUTs: pre-authenticated, no bearer token.
	if strings.HasPrefix(r.URL.Path, "/session/") {
		m.serveSession(w, r)
		return
	}
	if !m.checkAuth(w, r) {
		return
	}
	if m.maybe429(w) {
		return
	}
	m.serveGraph(w, r)
}

func (m *mockGraph) serveSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sid := strings.TrimPrefix(r.URL.Path, "/session/")
	m.mu.Lock()
	s, ok := m.sessions[sid]
	m.mu.Unlock()
	if !ok {
		m.writeErr(w, http.StatusNotFound, "resourceNotFound", "Upload session not found.")
		return
	}
	cr := r.Header.Get("Content-Range") // "bytes start-end/total"
	var start, end, total int64
	if _, err := fmt.Sscanf(cr, "bytes %d-%d/%d", &start, &end, &total); err != nil {
		m.writeErr(w, http.StatusBadRequest, "invalidRequest", "Bad Content-Range.")
		return
	}
	data, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	if s.buf == nil {
		s.buf = make([]byte, total)
		s.total = total
	}
	copy(s.buf[start:end+1], data)
	done := end+1 >= s.total
	var buf []byte
	var parentID, name string
	if done {
		buf = append([]byte(nil), s.buf...)
		parentID, name = s.parentID, s.name
		delete(m.sessions, sid)
	}
	m.mu.Unlock()
	if !done {
		m.writeJSON(w, http.StatusAccepted, map[string]any{
			"nextExpectedRanges": []string{fmt.Sprintf("%d-", end+1)},
		})
		return
	}
	it := m.addItem(name, parentID, false, buf)
	m.writeJSON(w, http.StatusCreated, m.itemJSON(it))
}

func (m *mockGraph) serveGraph(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path

	// Delta: /me/drive/root/delta
	if p == "/me/drive/root/delta" {
		token := r.URL.Query().Get("token")
		if token == "latest" {
			m.writeJSON(w, http.StatusOK, map[string]any{
				"value":          []any{},
				"@odata.deltaLink": m.base + "/me/drive/root/delta?token=tok1",
			})
			return
		}
		if token == "tok1" {
			var value []any
			for _, id := range m.deltaChanged {
				if it := m.items[id]; it != nil {
					value = append(value, m.itemJSON(it))
				}
			}
			for _, id := range m.deltaDeleted {
				if it := m.items[id]; it != nil {
					j := m.itemJSON(it)
					j["deleted"] = map[string]any{}
					value = append(value, j)
				}
			}
			m.writeJSON(w, http.StatusOK, map[string]any{
				"value":          value,
				"@odata.deltaLink": m.base + "/me/drive/root/delta?token=tok2",
			})
			return
		}
		m.writeErr(w, http.StatusBadRequest, "invalidRequest", "Unknown delta token.")
		return
	}

	// Search: /me/drive/root/search(q='...')
	if strings.HasPrefix(p, "/me/drive/root/search(q=") {
		raw := strings.TrimSuffix(strings.TrimPrefix(p, "/me/drive/root/search(q="), ")")
		q, _ := url.QueryUnescape(strings.Trim(raw, "'"))
		q = strings.ToLower(q)
		var value []any
		for _, it := range m.items {
			if it.id == "root" || it.deleted {
				continue
			}
			if strings.Contains(strings.ToLower(it.name), q) {
				value = append(value, m.itemJSON(it))
			}
		}
		m.writeJSON(w, http.StatusOK, map[string]any{"value": value})
		return
	}

	// Path addressing: /me/drive/root:/a/b and /me/drive/root:/a/b:/content
	if strings.HasPrefix(p, "/me/drive/root:/") {
		rest := strings.TrimPrefix(p, "/me/drive/root:/")
		if strings.HasSuffix(rest, ":/content") && r.Method == http.MethodGet {
			rest = strings.TrimSuffix(rest, ":/content")
			it := m.findByPath(rest)
			if it == nil || it.isDir {
				m.writeErr(w, http.StatusNotFound, "itemNotFound", "The resource could not be found.")
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(it.content)
			return
		}
		it := m.findByPath(rest)
		if it == nil {
			m.writeErr(w, http.StatusNotFound, "itemNotFound", "The resource could not be found.")
			return
		}
		m.writeJSON(w, http.StatusOK, m.itemJSON(it))
		return
	}

	// /me/drive/items/{id}:/{name}:/content (PUT) and :/createUploadSession (POST)
	if strings.HasPrefix(p, "/me/drive/items/") && strings.Contains(p, ":/") {
		rest := strings.TrimPrefix(p, "/me/drive/items/")
		// rest = "{parentId}:/{name}:/content" or "...:/createUploadSession"
		parts := strings.SplitN(rest, ":/", 2)
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		parentID, tail := parts[0], parts[1]
		if strings.HasSuffix(tail, ":/content") && r.Method == http.MethodPut {
			name := strings.TrimSuffix(tail, ":/content")
			data, _ := io.ReadAll(r.Body)
			m.mu.Lock()
			var it *mockItem
			for _, c := range m.items {
				if c.parentID == parentID && c.name == name && !c.deleted {
					it = c
					break
				}
			}
			m.mu.Unlock()
			if it == nil {
				it = m.addItem(name, parentID, false, data)
			} else {
				m.mu.Lock()
				it.content = data
				m.mu.Unlock()
			}
			m.writeJSON(w, http.StatusCreated, m.itemJSON(it))
			return
		}
		if strings.HasSuffix(tail, ":/createUploadSession") && r.Method == http.MethodPost {
			name := strings.TrimSuffix(tail, ":/createUploadSession")
			m.mu.Lock()
			sid := fmt.Sprintf("sess-%d", len(m.sessions)+1)
			// Total size is unknown at create time; the session
			// buffer is grown on first chunk (see serveSession).
			// The driver always knows the total when it creates a
			// session, so the mock reads it from the first chunk's
			// Content-Range instead.
			m.sessions[sid] = &mockSession{parentID: parentID, name: name, buf: nil}
			m.mu.Unlock()
			m.writeJSON(w, http.StatusOK, map[string]any{"uploadUrl": m.base + "/session/" + sid})
			return
		}
		http.NotFound(w, r)
		return
	}

	// /me/drive/items/{id}[/*]
	if strings.HasPrefix(p, "/me/drive/items/") {
		rest := strings.TrimPrefix(p, "/me/drive/items/")
		parts := strings.SplitN(rest, "/", 2)
		id, sub := parts[0], ""
		if len(parts) == 2 {
			sub = parts[1]
		}
		switch {
		case sub == "children" && r.Method == http.MethodGet:
			// Paginated at 2 per page via $skiptoken.
			kids := m.childrenOf(id)
			skip := 0
			if v := r.URL.Query().Get("$skiptoken"); v != "" {
				skip, _ = strconv.Atoi(v)
			}
			end := skip + 2
			if end > len(kids) {
				end = len(kids)
			}
			var value []any
			for _, k := range kids[skip:end] {
				value = append(value, m.itemJSON(k))
			}
			resp := map[string]any{"value": value}
			if end < len(kids) {
				resp["@odata.nextLink"] = m.base + "/me/drive/items/" + id + "/children?$skiptoken=" + strconv.Itoa(end)
			}
			m.writeJSON(w, http.StatusOK, resp)
			return
		case sub == "children" && r.Method == http.MethodPost:
			var body struct {
				Name   string `json:"name"`
				Folder *struct{} `json:"folder"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, k := range m.childrenOf(id) {
				if k.name == body.Name {
					m.writeErr(w, http.StatusConflict, "nameAlreadyExists", "A file or folder with this name already exists.")
					return
				}
			}
			it := m.addItem(body.Name, id, true, nil)
			m.writeJSON(w, http.StatusCreated, m.itemJSON(it))
			return
		case sub == "content" && r.Method == http.MethodGet:
			it := m.items[id]
			if it == nil || it.isDir {
				m.writeErr(w, http.StatusNotFound, "itemNotFound", "The resource could not be found.")
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(it.content)
			return
		case sub == "createLink" && r.Method == http.MethodPost:
			if m.items[id] == nil {
				m.writeErr(w, http.StatusNotFound, "itemNotFound", "The resource could not be found.")
				return
			}
			m.writeJSON(w, http.StatusOK, map[string]any{
				"link": map[string]any{"webUrl": "https://1drv.ms/u/s!share-" + id},
			})
			return
		case sub == "versions" && r.Method == http.MethodGet:
			m.writeJSON(w, http.StatusOK, map[string]any{"value": []any{
				map[string]any{"id": "v1", "lastModifiedDateTime": "2026-01-01T00:00:00Z", "size": 3},
				map[string]any{"id": "v2", "lastModifiedDateTime": "2026-01-02T00:00:00Z", "size": 5},
			}})
			return
		case strings.HasPrefix(sub, "versions/") && strings.HasSuffix(sub, "/restoreVersion") && r.Method == http.MethodPost:
			m.writeJSON(w, http.StatusNoContent, map[string]any{})
			return
		case sub == "thumbnails" && r.Method == http.MethodGet:
			m.writeJSON(w, http.StatusOK, map[string]any{"value": []any{
				map[string]any{
					"large":  map[string]any{"url": m.base + "/thumb/large-" + id},
					"medium": map[string]any{"url": m.base + "/thumb/med-" + id},
					"small":  map[string]any{"url": m.base + "/thumb/small-" + id},
				},
			}})
			return
		case sub == "" && r.Method == http.MethodGet:
			it := m.items[id]
			if it == nil || it.deleted {
				m.writeErr(w, http.StatusNotFound, "itemNotFound", "The resource could not be found.")
				return
			}
			m.writeJSON(w, http.StatusOK, m.itemJSON(it))
			return
		case sub == "" && r.Method == http.MethodPatch:
			it := m.items[id]
			if it == nil {
				m.writeErr(w, http.StatusNotFound, "itemNotFound", "The resource could not be found.")
				return
			}
			var body struct {
				Name            string `json:"name"`
				ParentReference *struct {
					ID string `json:"id"`
				} `json:"parentReference"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			m.mu.Lock()
			if body.Name != "" {
				it.name = body.Name
			}
			if body.ParentReference != nil && body.ParentReference.ID != "" {
				it.parentID = body.ParentReference.ID
			}
			m.mu.Unlock()
			m.writeJSON(w, http.StatusOK, m.itemJSON(it))
			return
		case sub == "" && r.Method == http.MethodDelete:
			it := m.items[id]
			if it == nil {
				m.writeErr(w, http.StatusNotFound, "itemNotFound", "The resource could not be found.")
				return
			}
			m.mu.Lock()
			it.deleted = true
			m.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
		return
	}

	http.NotFound(w, r)
}

// newTestProvider builds a driver against a fresh mock Graph.
func newTestProvider(t *testing.T, seed func(m *mockGraph)) (*Provider, *mockGraph) {
	t.Helper()
	m := newMockGraph()
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	m.base = srv.URL
	if seed != nil {
		seed(m)
	}
	p, err := New(storage.Config{
		Token: "test-token",
		Extra: map[string]string{"api_base": srv.URL},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p.(*Provider), m
}

func TestCaps(t *testing.T) {
	p, _ := newTestProvider(t, nil)
	c := p.Caps()
	if !c.IDBased || !c.SupportsDelta || !c.SupportsVersions || !c.SupportsShare || !c.SupportsSearch || !c.SupportsThumbnail {
		t.Fatalf("caps missing OneDrive features: %+v", c)
	}
	if c.SupportsServerSideCopy {
		t.Fatalf("caps must not claim server-side copy")
	}
	if c.MaxFileSize != 250<<30 {
		t.Fatalf("MaxFileSize=%d, want 250 GiB", c.MaxFileSize)
	}
	if c.MinPartSize != 5<<20 {
		t.Fatalf("MinPartSize=%d, want 5 MiB", c.MinPartSize)
	}
	if c.MaxPathLen != 400 {
		t.Fatalf("MaxPathLen=%d, want 400", c.MaxPathLen)
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(storage.Config{}); err == nil {
		t.Fatalf("New without token: want an error")
	}
}

func TestListPaginationAndStat(t *testing.T) {
	p, _ := newTestProvider(t, func(m *mockGraph) {
		m.addItem("photos", "root", true, nil)
		for i := 0; i < 4; i++ {
			m.addItem(fmt.Sprintf("file%d.txt", i), "root", false, []byte(fmt.Sprintf("content-%d", i)))
		}
	})
	ctx := context.Background()
	nodes, err := p.List(ctx, "")
	if err != nil {
		t.Fatalf("List root: %v", err)
	}
	if len(nodes) != 5 {
		t.Fatalf("List root: got %d nodes, want 5 (pagination must be consumed)", len(nodes))
	}
	st, err := p.Stat(ctx, "file2.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if st.Name != "file2.txt" || st.Size != 9 {
		t.Fatalf("Stat: %+v", st)
	}
	if st.HashKind != "sha1" || len(st.Hash) != 40 {
		t.Fatalf("Stat hash: kind=%q hash=%q, want sha1 hex", st.HashKind, st.Hash)
	}
	rc, err := p.OpenRead(ctx, "file2.txt")
	if err != nil {
		t.Fatalf("OpenRead: %v", err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != "content-2" {
		t.Fatalf("OpenRead content=%q", string(data))
	}
	if _, err := p.Stat(ctx, "missing.txt"); err == nil {
		t.Fatalf("Stat missing: want an error")
	}
}

func TestUploadSmallAndSession(t *testing.T) {
	p, _ := newTestProvider(t, nil)
	ctx := context.Background()
	small := []byte("hello onedrive")
	n, err := p.Upload(ctx, "small.txt", bytes.NewReader(small), int64(len(small)))
	if err != nil {
		t.Fatalf("Upload small: %v", err)
	}
	if n.Size != int64(len(small)) {
		t.Fatalf("Upload small size=%d", n.Size)
	}
	// Overwrite the same name via the path PUT.
	bigger := []byte("hello onedrive, again")
	if _, err := p.Upload(ctx, "small.txt", bytes.NewReader(bigger), int64(len(bigger))); err != nil {
		t.Fatalf("Upload overwrite: %v", err)
	}
	rc, err := p.OpenRead(ctx, "small.txt")
	if err != nil {
		t.Fatalf("OpenRead after overwrite: %v", err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(data, bigger) {
		t.Fatalf("overwrite content=%q", string(data))
	}
	// Large file: 5 MiB + 123 bytes, two chunks through the session.
	large := make([]byte, (5<<20)+123)
	for i := range large {
		large[i] = byte(i % 251)
	}
	n, err = p.Upload(ctx, "big.bin", bytes.NewReader(large), int64(len(large)))
	if err != nil {
		t.Fatalf("Upload session: %v", err)
	}
	if n.Size != int64(len(large)) {
		t.Fatalf("Upload session size=%d, want %d", n.Size, len(large))
	}
	rc, err = p.OpenRead(ctx, "big.bin")
	if err != nil {
		t.Fatalf("OpenRead big: %v", err)
	}
	data, _ = io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(data, large) {
		t.Fatalf("session upload content mismatch: got %d bytes", len(data))
	}
}

func TestUploadUnknownSize(t *testing.T) {
	p, _ := newTestProvider(t, nil)
	ctx := context.Background()
	// Unknown size, small: buffered, simple PUT.
	if _, err := p.Upload(ctx, "unk.txt", bytes.NewReader([]byte("abc")), -1); err != nil {
		t.Fatalf("Upload unknown small: %v", err)
	}
	// Unknown size, large: spooled to a temp file, then session.
	large := make([]byte, (5<<20)+7)
	if _, err := p.Upload(ctx, "unkbig.bin", bytes.NewReader(large), -1); err != nil {
		t.Fatalf("Upload unknown large: %v", err)
	}
	st, err := p.Stat(ctx, "unkbig.bin")
	if err != nil {
		t.Fatalf("Stat unkbig: %v", err)
	}
	if st.Size != int64(len(large)) {
		t.Fatalf("Stat unkbig size=%d", st.Size)
	}
}

func TestMkdirRenameDelete(t *testing.T) {
	p, _ := newTestProvider(t, nil)
	ctx := context.Background()
	if err := p.Mkdir(ctx, "docs"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	// Idempotent: creating it again is success.
	if err := p.Mkdir(ctx, "docs"); err != nil {
		t.Fatalf("Mkdir again: %v", err)
	}
	if _, err := p.Upload(ctx, "docs/a.txt", bytes.NewReader([]byte("a")), 1); err != nil {
		t.Fatalf("Upload into docs: %v", err)
	}
	if err := p.Rename(ctx, "docs/a.txt", "docs/b.txt"); err != nil {
		t.Fatalf("Rename same folder: %v", err)
	}
	if err := p.Mkdir(ctx, "archive"); err != nil {
		t.Fatalf("Mkdir archive: %v", err)
	}
	if err := p.Rename(ctx, "docs/b.txt", "archive/b.txt"); err != nil {
		t.Fatalf("Rename across folders: %v", err)
	}
	if _, err := p.Stat(ctx, "archive/b.txt"); err != nil {
		t.Fatalf("Stat after move: %v", err)
	}
	if err := p.Delete(ctx, "archive/b.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := p.Stat(ctx, "archive/b.txt"); err == nil {
		t.Fatalf("Stat after delete: want an error")
	}
}

func TestSearchDeltaShareVersionsThumbnail(t *testing.T) {
	var changedID, deletedID string
	p, m := newTestProvider(t, func(m *mockGraph) {
		f := m.addItem("quarterly-report.docx", "root", false, []byte("report"))
		changedID = f.id
		d := m.addItem("old-notes.txt", "root", false, []byte("old"))
		deletedID = d.id
	})
	m.deltaChanged = []string{changedID}
	m.deltaDeleted = []string{deletedID}
	ctx := context.Background()

	hits, err := p.Search(ctx, "quarterly")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].Name != "quarterly-report.docx" {
		t.Fatalf("Search: %+v", hits)
	}
	if hits[0].Ref != "quarterly-report.docx" {
		t.Fatalf("Search ref=%q, want operable display path", hits[0].Ref)
	}

	_, cursor, err := p.Delta(ctx, "")
	if err != nil {
		t.Fatalf("Delta latest: %v", err)
	}
	if cursor == "" {
		t.Fatalf("Delta latest: empty cursor")
	}
	changes, cursor2, err := p.Delta(ctx, cursor)
	if err != nil {
		t.Fatalf("Delta: %v", err)
	}
	if cursor2 == "" {
		t.Fatalf("Delta: empty new cursor")
	}
	if len(changes) != 2 {
		t.Fatalf("Delta: got %d changes, want 2", len(changes))
	}
	removed := 0
	for _, n := range changes {
		if n.Extra["removed"] == "true" {
			removed++
		}
	}
	if removed != 1 {
		t.Fatalf("Delta: %d removed, want 1", removed)
	}

	link, err := p.Share(ctx, "quarterly-report.docx")
	if err != nil {
		t.Fatalf("Share: %v", err)
	}
	if !strings.HasPrefix(link, "https://1drv.ms/") {
		t.Fatalf("Share link=%q", link)
	}

	versions, err := p.Versions(ctx, "quarterly-report.docx")
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(versions) != 2 || versions[0].Extra["versionId"] != "v1" {
		t.Fatalf("Versions: %+v", versions)
	}
	if err := p.Restore(ctx, "quarterly-report.docx", "v1"); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	thumb, err := p.Thumbnail(ctx, "quarterly-report.docx")
	if err != nil {
		t.Fatalf("Thumbnail: %v", err)
	}
	if !strings.Contains(thumb, "/thumb/large-") {
		t.Fatalf("Thumbnail=%q, want the large URL", thumb)
	}
}

func TestAuthRefreshAnd429(t *testing.T) {
	m := newMockGraph()
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	m.base = srv.URL
	m.addItem("a.txt", "root", false, []byte("a"))
	ctx := context.Background()

	// 401 on a stale token: the shared client refreshes once via
	// the mock token endpoint and replays with the new token.
	p, err := New(storage.Config{
		Token: "expired-token",
		Extra: map[string]string{
			"api_base":      srv.URL,
			"refresh_token": "rt-1",
			"token_url":     srv.URL + "/token",
			"client_id":     "test-client",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := p.List(ctx, ""); err != nil {
		t.Fatalf("List after 401 refresh: %v", err)
	}

	// One 429 with Retry-After: retried and succeeds.
	m.fail429 = 1
	if _, err := p.List(ctx, ""); err != nil {
		t.Fatalf("List after one 429: %v", err)
	}

	// Repeated 429s: give up with a plain-language error, no loop.
	m.fail429 = 10
	_, err = p.List(ctx, "")
	if err == nil || !strings.Contains(err.Error(), "rate limited (429)") {
		t.Fatalf("List after repeated 429s: err=%v, want rate-limited give-up", err)
	}
}

func TestPathTooLong(t *testing.T) {
	p, _ := newTestProvider(t, nil)
	ctx := context.Background()
	long := strings.Repeat("a", 401)
	if _, err := p.Upload(ctx, long, bytes.NewReader([]byte("x")), 1); err == nil {
		t.Fatalf("Upload 401-char path: want an error")
	} else if !strings.Contains(err.Error(), "路径太长") {
		t.Fatalf("Upload 401-char path: err=%v, want plain-language path error", err)
	}
	if err := p.Mkdir(ctx, long); err == nil {
		t.Fatalf("Mkdir 401-char path: want an error")
	}
}

func TestRootStat(t *testing.T) {
	p, _ := newTestProvider(t, nil)
	st, err := p.Stat(context.Background(), "")
	if err != nil {
		t.Fatalf("Stat root: %v", err)
	}
	if !st.IsDir || st.Ref != "" {
		t.Fatalf("Stat root: %+v", st)
	}
}
