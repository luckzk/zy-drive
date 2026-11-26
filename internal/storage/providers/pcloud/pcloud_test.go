// SPDX-License-Identifier: MPL-2.0
package pcloud

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
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

// mockPcloud is an in-memory pCloud API for tests. All endpoints
// are GET with query parameters (except /uploadfile, which is a
// multipart POST); errors are HTTP 200 with {"result": N}.
type mockEntry struct {
	id       int64
	name     string
	isFolder bool
	parentID int64
	size     int64
	content  []byte
	modified string
}

type mockPcloud struct {
	mu        sync.Mutex
	entries   map[int64]*mockEntry
	nextID    int64
	validTok  string
	quota     int64
	usedQuota int64
	// failUserinfo makes /userinfo return a non-zero result (used
	// to simulate "this data center does not serve this account").
	failUserinfo bool
	// directHost is the host getfilelink advertises (the test
	// server's own host, so the link is fetchable).
	directHost string
}

func newMockPcloud() *mockPcloud {
	return &mockPcloud{
		entries:   map[int64]*mockEntry{},
		validTok:  "test-token",
		quota:     20_000_000_000,
		usedQuota: 5_000_000_000,
	}
}

func (m *mockPcloud) addEntry(name string, isFolder bool, parentID int64, content []byte) *mockEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	e := &mockEntry{
		id: m.nextID, name: name, isFolder: isFolder, parentID: parentID,
		size: int64(len(content)), content: content,
		modified: "2026-11-25T00:00:00Z",
	}
	m.entries[e.id] = e
	return e
}

func (m *mockPcloud) metaJSON(e *mockEntry) map[string]any {
	v := map[string]any{
		"name": e.name, "isfolder": e.isFolder, "size": e.size,
		"modified": e.modified,
	}
	if e.isFolder {
		v["folderid"] = e.id
	} else {
		v["fileid"] = e.id
	}
	return v
}

func (m *mockPcloud) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (m *mockPcloud) writeErr(w http.ResponseWriter, result int, msg string) {
	m.writeJSON(w, map[string]any{"result": result, "error": msg})
}

func (m *mockPcloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The direct download link (/d/<id>) is unauthenticated —
	// the link itself is the credential, as in production.
	if !strings.HasPrefix(r.URL.Path, "/d/") && r.Header.Get("Authorization") != "Bearer "+m.validTok {
		m.writeErr(w, 1000, "Log in required.")
		return
	}
	p := r.URL.Path
	switch {
	case p == "/userinfo" && r.Method == http.MethodGet:
		if m.failUserinfo {
			m.writeErr(w, 5001, "internal error")
			return
		}
		m.writeJSON(w, map[string]any{
			"result": 0, "quota": m.quota, "usedquota": m.usedQuota,
			"locationid": 1, "email": "test@pcloud.test",
		})
	case p == "/listfolder" && r.Method == http.MethodGet:
		m.handleList(w, r)
	case p == "/createfolder" && r.Method == http.MethodGet:
		m.handleMkdir(w, r)
	case p == "/uploadfile" && r.Method == http.MethodPost:
		m.handleUpload(w, r)
	case p == "/getfilelink" && r.Method == http.MethodGet:
		m.handleGetFileLink(w, r)
	case strings.HasPrefix(p, "/d/") && r.Method == http.MethodGet:
		m.handleDirect(w, r)
	case p == "/deletefile" && r.Method == http.MethodGet:
		m.handleDelete(w, r, false)
	case p == "/deletefolder" && r.Method == http.MethodGet:
		m.handleDelete(w, r, true)
	case p == "/renamefile" && r.Method == http.MethodGet:
		m.handleRename(w, r, false)
	case p == "/renamefolder" && r.Method == http.MethodGet:
		m.handleRename(w, r, true)
	case p == "/movefile" && r.Method == http.MethodGet:
		m.handleMove(w, r, false)
	case p == "/movefolder" && r.Method == http.MethodGet:
		m.handleMove(w, r, true)
	case p == "/search" && r.Method == http.MethodGet:
		m.handleSearch(w, r)
	case p == "/stat" && r.Method == http.MethodGet:
		m.handleStat(w, r)
	case p == "/getfilepublink" && r.Method == http.MethodGet:
		m.handleShare(w, r)
	default:
		m.writeErr(w, 2000, "mock: no such route "+p)
	}
}

func (m *mockPcloud) children(parentID int64) []*mockEntry {
	var out []*mockEntry
	for _, e := range m.entries {
		if e.parentID == parentID {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func (m *mockPcloud) handleList(w http.ResponseWriter, r *http.Request) {
	folderID, _ := strconv.ParseInt(r.URL.Query().Get("folderid"), 10, 64)
	m.mu.Lock()
	defer m.mu.Unlock()
	var contents []map[string]any
	for _, e := range m.children(folderID) {
		contents = append(contents, m.metaJSON(e))
	}
	m.writeJSON(w, map[string]any{
		"result": 0,
		"metadata": map[string]any{
			"name": "root", "isfolder": true, "folderid": folderID,
			"contents": contents,
		},
	})
}

func (m *mockPcloud) handleMkdir(w http.ResponseWriter, r *http.Request) {
	folderID, _ := strconv.ParseInt(r.URL.Query().Get("folderid"), 10, 64)
	name := r.URL.Query().Get("name")
	m.mu.Lock()
	for _, e := range m.children(folderID) {
		if e.name == name && e.isFolder {
			m.mu.Unlock()
			m.writeErr(w, 2004, "File or folder already exists.")
			return
		}
	}
	m.mu.Unlock()
	e := m.addEntry(name, true, folderID, nil)
	m.writeJSON(w, map[string]any{"result": 0, "metadata": m.metaJSON(e)})
}

func (m *mockPcloud) handleUpload(w http.ResponseWriter, r *http.Request) {
	folderID, _ := strconv.ParseInt(r.URL.Query().Get("folderid"), 10, 64)
	// The driver promises an exact Content-Length (no chunked
	// encoding): verify it against the bytes actually received.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		m.writeErr(w, 2000, "read body: "+err.Error())
		return
	}
	if r.ContentLength > 0 && int64(len(body)) != r.ContentLength {
		m.writeErr(w, 2000, fmt.Sprintf("Content-Length %d but got %d bytes", r.ContentLength, len(body)))
		return
	}
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		m.writeErr(w, 2000, "bad content-type: "+err.Error())
		return
	}
	mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
	var name string
	var content []byte
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			m.writeErr(w, 2000, "bad multipart: "+err.Error())
			return
		}
		if part.FormName() == "file" {
			name = part.FileName()
			content, _ = io.ReadAll(part)
		}
	}
	if name == "" {
		m.writeErr(w, 2000, "no file part")
		return
	}
	e := m.addEntry(name, false, folderID, content)
	m.writeJSON(w, map[string]any{
		"result":   0,
		"metadata": []map[string]any{m.metaJSON(e)},
	})
}

func (m *mockPcloud) handleGetFileLink(w http.ResponseWriter, r *http.Request) {
	fileID, _ := strconv.ParseInt(r.URL.Query().Get("fileid"), 10, 64)
	m.mu.Lock()
	_, ok := m.entries[fileID]
	m.mu.Unlock()
	if !ok {
		m.writeErr(w, 2005, "File not found.")
		return
	}
	m.writeJSON(w, map[string]any{
		"result": 0,
		"hosts":  []string{m.directHost},
		"path":   fmt.Sprintf("/d/%d", fileID),
	})
}

func (m *mockPcloud) handleDirect(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/d/"), 10, 64)
	m.mu.Lock()
	e, ok := m.entries[id]
	m.mu.Unlock()
	if !ok || e.isFolder {
		m.writeErr(w, 2005, "File not found.")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(e.content)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(e.content)
}

func (m *mockPcloud) handleDelete(w http.ResponseWriter, r *http.Request, isFolder bool) {
	var id int64
	if isFolder {
		id, _ = strconv.ParseInt(r.URL.Query().Get("folderid"), 10, 64)
	} else {
		id, _ = strconv.ParseInt(r.URL.Query().Get("fileid"), 10, 64)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok || e.isFolder != isFolder {
		m.writeErr(w, 2005, "File or folder not found.")
		return
	}
	if isFolder {
		var del func(pid int64)
		del = func(pid int64) {
			for _, c := range m.entries {
				if c.parentID == pid {
					del(c.id)
					delete(m.entries, c.id)
				}
			}
		}
		del(id)
	}
	delete(m.entries, id)
	m.writeJSON(w, map[string]any{"result": 0})
}

func (m *mockPcloud) handleRename(w http.ResponseWriter, r *http.Request, isFolder bool) {
	var id int64
	if isFolder {
		id, _ = strconv.ParseInt(r.URL.Query().Get("folderid"), 10, 64)
	} else {
		id, _ = strconv.ParseInt(r.URL.Query().Get("fileid"), 10, 64)
	}
	name := r.URL.Query().Get("toname")
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok || e.isFolder != isFolder {
		m.writeErr(w, 2005, "File or folder not found.")
		return
	}
	e.name = name
	m.writeJSON(w, map[string]any{"result": 0, "metadata": m.metaJSON(e)})
}

func (m *mockPcloud) handleMove(w http.ResponseWriter, r *http.Request, isFolder bool) {
	var id int64
	if isFolder {
		id, _ = strconv.ParseInt(r.URL.Query().Get("folderid"), 10, 64)
	} else {
		id, _ = strconv.ParseInt(r.URL.Query().Get("fileid"), 10, 64)
	}
	toFolderID, _ := strconv.ParseInt(r.URL.Query().Get("tofolderid"), 10, 64)
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok || e.isFolder != isFolder {
		m.writeErr(w, 2005, "File or folder not found.")
		return
	}
	e.parentID = toFolderID
	m.writeJSON(w, map[string]any{"result": 0, "metadata": m.metaJSON(e)})
}

func (m *mockPcloud) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(r.URL.Query().Get("query"))
	m.mu.Lock()
	defer m.mu.Unlock()
	var entries []map[string]any
	for _, e := range m.entries {
		if e.isFolder || !strings.Contains(strings.ToLower(e.name), q) {
			continue
		}
		v := m.metaJSON(e)
		// Build the full path from the ancestors.
		var parts []string
		cur := e
		for cur.parentID != 0 {
			if parent, ok := m.entries[cur.parentID]; ok {
				parts = append([]string{parent.name}, parts...)
				cur = parent
			} else {
				break
			}
		}
		parts = append(parts, e.name)
		v["path"] = "/" + strings.Join(parts, "/")
		entries = append(entries, v)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i]["name"].(string) < entries[j]["name"].(string) })
	m.writeJSON(w, map[string]any{"result": 0, "entries": entries})
}

func (m *mockPcloud) handleStat(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("fileid"), 10, 64)
	m.mu.Lock()
	e, ok := m.entries[id]
	m.mu.Unlock()
	if !ok {
		m.writeErr(w, 2005, "File not found.")
		return
	}
	m.writeJSON(w, map[string]any{"result": 0, "metadata": m.metaJSON(e)})
}

func (m *mockPcloud) handleShare(w http.ResponseWriter, r *http.Request) {
	fileID, _ := strconv.ParseInt(r.URL.Query().Get("fileid"), 10, 64)
	m.mu.Lock()
	_, ok := m.entries[fileID]
	m.mu.Unlock()
	if !ok {
		m.writeErr(w, 2005, "File not found.")
		return
	}
	m.writeJSON(w, map[string]any{
		"result": 0, "link": fmt.Sprintf("https://pcloud.test/l/%d", fileID),
	})
}

// --- tests ---

func newTestProvider(t *testing.T, mock *mockPcloud) (*Provider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(mock)
	mock.directHost = srv.URL
	p, err := New(storage.Config{
		Token: "test-token",
		Extra: map[string]string{"api_base": srv.URL},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p.(*Provider), srv
}

func TestContract(t *testing.T) {
	mock := newMockPcloud()
	p, srv := newTestProvider(t, mock)
	defer srv.Close()
	contracttest.Run(t, func() (storage.Provider, error) { return p, nil })
}

func TestPcloudUploadSmall(t *testing.T) {
	mock := newMockPcloud()
	p, srv := newTestProvider(t, mock)
	defer srv.Close()
	content := []byte("hello pcloud\n")
	node, err := p.Upload(t.Context(), "hello.txt", bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if node.Ref != "hello.txt" || node.Size != int64(len(content)) {
		t.Fatalf("node: got %+v", node)
	}
	rc, err := p.OpenRead(t.Context(), "hello.txt")
	if err != nil {
		t.Fatalf("OpenRead: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, content) {
		t.Fatalf("round-trip mismatch")
	}
}

// nonSeeker hides io.ReadSeeker so the driver must take the
// spool path for a large upload.
type nonSeeker struct{ r io.Reader }

func (n nonSeeker) Read(p []byte) (int, error) { return n.r.Read(p) }

func TestPcloudUploadLargeSeekable(t *testing.T) {
	mock := newMockPcloud()
	p, srv := newTestProvider(t, mock)
	defer srv.Close()
	// 25 MiB through a bytes.Reader (a ReadSeeker): the driver
	// streams it with an exact Content-Length, no spool.
	size := int64(25 << 20)
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i % 251)
	}
	node, err := p.Upload(t.Context(), "big.bin", bytes.NewReader(content), size)
	if err != nil {
		t.Fatalf("Upload large seekable: %v", err)
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
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("large seekable round-trip mismatch (%d bytes, err %v)", len(got), err)
	}
}

func TestPcloudUploadLargeSpooled(t *testing.T) {
	mock := newMockPcloud()
	p, srv := newTestProvider(t, mock)
	defer srv.Close()
	// 25 MiB through a non-seekable reader: the driver spools to
	// a temp file, then streams with an exact Content-Length.
	size := int64(25 << 20)
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i % 251)
	}
	node, err := p.Upload(t.Context(), "big2.bin", nonSeeker{r: bytes.NewReader(content)}, size)
	if err != nil {
		t.Fatalf("Upload large spooled: %v", err)
	}
	if node.Size != size {
		t.Fatalf("node.Size: got %d, want %d", node.Size, size)
	}
	rc, err := p.OpenRead(t.Context(), "big2.bin")
	if err != nil {
		t.Fatalf("OpenRead: %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("large spooled round-trip mismatch (%d bytes, err %v)", len(got), err)
	}
}

func TestPcloudQuota(t *testing.T) {
	mock := newMockPcloud()
	p, srv := newTestProvider(t, mock)
	defer srv.Close()
	used, total, err := p.Quota(t.Context())
	if err != nil {
		t.Fatalf("Quota: %v", err)
	}
	if used != 5_000_000_000 || total != 20_000_000_000 {
		t.Fatalf("Quota: got (%d, %d), want (5000000000, 20000000000)", used, total)
	}
}

func TestPcloudDirectURL(t *testing.T) {
	mock := newMockPcloud()
	mock.addEntry("video.mp4", false, 0, []byte("video-bytes"))
	p, srv := newTestProvider(t, mock)
	defer srv.Close()
	link, err := p.DirectURL(t.Context(), "video.mp4")
	if err != nil {
		t.Fatalf("DirectURL: %v", err)
	}
	// The link must be fetchable (it is the preview streaming
	// path) and return exactly the file's bytes.
	resp, err := http.Get(link)
	if err != nil {
		t.Fatalf("GET direct link: %v", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, []byte("video-bytes")) {
		t.Fatalf("direct link content mismatch: %q", got)
	}
}

func TestPcloudSearch(t *testing.T) {
	mock := newMockPcloud()
	mock.addEntry("report.pdf", false, 0, []byte("pdf"))
	mock.addEntry("notes.txt", false, 0, []byte("txt"))
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

func TestPcloudMkdirExisting(t *testing.T) {
	mock := newMockPcloud()
	p, srv := newTestProvider(t, mock)
	defer srv.Close()
	if err := p.Mkdir(t.Context(), "dup"); err != nil {
		t.Fatalf("Mkdir 1: %v", err)
	}
	// The mock returns result 2004 ("already exists"); the driver
	// re-resolves and succeeds instead of surfacing the error.
	if err := p.Mkdir(t.Context(), "dup"); err != nil {
		t.Fatalf("Mkdir existing: %v", err)
	}
}

func TestPcloudRegionProbe(t *testing.T) {
	// The "US" mock fails userinfo (this account is not served
	// there); the "EU" mock answers. The driver must probe in
	// order and settle on the working host.
	usMock := newMockPcloud()
	usMock.failUserinfo = true
	usSrv := httptest.NewServer(usMock)
	defer usSrv.Close()
	euMock := newMockPcloud()
	euSrv := httptest.NewServer(euMock)
	defer euSrv.Close()
	euMock.directHost = euSrv.URL
	p, err := New(storage.Config{
		Token: "test-token",
		Extra: map[string]string{"probe_hosts": usSrv.URL + "," + euSrv.URL},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Quota goes through apiBase, which probes US then EU.
	used, total, err := p.(*Provider).Quota(t.Context())
	if err != nil {
		t.Fatalf("Quota after probe: %v", err)
	}
	if used != 5_000_000_000 || total != 20_000_000_000 {
		t.Fatalf("Quota: got (%d, %d)", used, total)
	}
}

func TestParseModified(t *testing.T) {
	cases := []struct {
		in   string
		want time.Time
	}{
		{"2026-11-25T00:00:00Z", time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)},
		{"Tue, 25 Nov 2026 00:00:00 +0000", time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)},
		{"not-a-date", time.Time{}},
		{"", time.Time{}},
	}
	for _, c := range cases {
		if got := parseModified(c.in); !got.Equal(c.want) {
			t.Fatalf("parseModified(%q): got %v, want %v", c.in, got, c.want)
		}
	}
}
