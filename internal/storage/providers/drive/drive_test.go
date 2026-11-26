// SPDX-License-Identifier: MPL-2.0
package drive

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/storage/contracttest"
)

// mockDrive is an in-memory Drive v3 backend for tests.
type mockFile struct {
	id       string
	name     string
	mime     string
	parent   string
	content  []byte
	trashed  bool
	modified string
}

// mockRevision is one saved pre-overwrite content of a file:
// overwriting a file pushes its old bytes here, so Versions /
// Restore have something real to return.
type mockRevision struct {
	id       string
	content  []byte
	modified string
}

type mockDrive struct {
	files  map[string]*mockFile
	seq    int
	revSeq int
	revs   map[string][]*mockRevision // file ID -> revisions, oldest first
	sess   map[string]*mockFile       // resumable session URL id -> pending file
	sessDB map[string][]byte
}

func newMockDrive() *mockDrive {
	m := &mockDrive{files: map[string]*mockFile{}, revs: map[string][]*mockRevision{}, sess: map[string]*mockFile{}, sessDB: map[string][]byte{}}
	m.files["root"] = &mockFile{id: "root", name: "My Drive", mime: folderMime, modified: "2026-11-24T00:00:00Z"}
	return m
}

// pushRevision saves f's current bytes as a new revision before
// an overwrite replaces them (no-op for a file with no content
// yet — a first upload has nothing to roll back to).
func (m *mockDrive) pushRevision(f *mockFile) {
	if f.content == nil {
		return
	}
	m.revSeq++
	m.revs[f.id] = append(m.revs[f.id], &mockRevision{
		id: "rev-" + strconv.Itoa(m.revSeq), content: f.content, modified: f.modified,
	})
}

func (m *mockDrive) nextID() string {
	m.seq++
	return "id-" + strconv.Itoa(m.seq)
}

func (m *mockDrive) fileJSON(f *mockFile) map[string]any {
	return map[string]any{
		"id": f.id, "name": f.name, "mimeType": f.mime,
		"size": strconv.Itoa(len(f.content)), "modifiedTime": f.modified,
		"md5Checksum": "md5-" + f.id, "trashed": f.trashed,
		"webViewLink":   "https://drive.google.com/file/d/" + f.id + "/view",
		"thumbnailLink": "https://drive.google.com/thumbnail?id=" + f.id,
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// matchQuery handles the two query shapes the driver sends:
// "name = 'x' and 'parent' in parents and trashed = false"
// "'parent' in parents and trashed = false"
// "name contains 'x' and trashed = false"
func (m *mockDrive) matchQuery(q string) []*mockFile {
	var out []*mockFile
	nameEq := ""
	if i := strings.Index(q, "name = '"); i >= 0 {
		rest := q[i+len("name = '"):]
		if j := strings.Index(rest, "'"); j >= 0 {
			nameEq = rest[:j]
		}
	}
	nameContains := ""
	if i := strings.Index(q, "name contains '"); i >= 0 {
		rest := q[i+len("name contains '"):]
		if j := strings.Index(rest, "'"); j >= 0 {
			nameContains = rest[:j]
		}
	}
	parent := ""
	if i := strings.Index(q, "' in parents"); i >= 0 {
		s := q[:i]
		if j := strings.LastIndex(s, "'"); j >= 0 {
			parent = s[j+1:]
		}
	}
	for _, f := range m.files {
		if f.trashed || f.id == "root" {
			continue
		}
		if nameEq != "" && f.name != nameEq {
			continue
		}
		if nameContains != "" && !strings.Contains(f.name, nameContains) {
			continue
		}
		if parent != "" && f.parent != parent {
			continue
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func (m *mockDrive) handler(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case p == "/drive/v3/changes/startPageToken":
		writeJSON(w, map[string]any{"startPageToken": "tok-1"})
	case p == "/drive/v3/about":
		writeJSON(w, map[string]any{"storageQuota": map[string]any{"limit": "1099511627776", "usage": "123456789"}})
	case p == "/drive/v3/changes":
		writeJSON(w, map[string]any{"newStartPageToken": "tok-2", "changes": []any{}})
	case p == "/drive/v3/files" && r.Method == http.MethodGet:
		matched := m.matchQuery(r.URL.Query().Get("q"))
		var arr []map[string]any
		for _, f := range matched {
			arr = append(arr, m.fileJSON(f))
		}
		if arr == nil {
			arr = []map[string]any{}
		}
		writeJSON(w, map[string]any{"files": arr})
	case p == "/drive/v3/files" && r.Method == http.MethodPost:
		var meta map[string]any
		_ = json.NewDecoder(r.Body).Decode(&meta)
		f := &mockFile{id: m.nextID(), name: meta["name"].(string), mime: folderMime, modified: "2026-11-24T00:00:00Z"}
		if parents, ok := meta["parents"].([]any); ok && len(parents) > 0 {
			f.parent, _ = parents[0].(string)
		}
		m.files[f.id] = f
		writeJSON(w, m.fileJSON(f))
	case strings.HasPrefix(p, "/drive/v3/files/") && strings.HasSuffix(p, "/permissions"):
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	case strings.HasPrefix(p, "/drive/v3/files/") && strings.HasSuffix(p, "/export"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/drive/v3/files/"), "/export")
		f := m.files[id]
		if f == nil || f.trashed {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("exported:" + f.name))
	case strings.HasPrefix(p, "/drive/v3/files/") && strings.Contains(p, "/revisions"):
		// /drive/v3/files/{id}/revisions[/{revId}]
		rest := strings.TrimPrefix(p, "/drive/v3/files/")
		parts := strings.Split(rest, "/")
		id, revID := parts[0], ""
		if len(parts) > 2 {
			revID = parts[2]
		}
		f := m.files[id]
		if f == nil || f.trashed {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"not found"}}`))
			return
		}
		if revID == "" {
			// revisions.list
			var arr []map[string]any
			for _, r := range m.revs[id] {
				arr = append(arr, map[string]any{
					"id": r.id, "modifiedTime": r.modified,
					"size": strconv.Itoa(len(r.content)), "md5Checksum": "md5-" + r.id,
				})
			}
			if arr == nil {
				arr = []map[string]any{}
			}
			writeJSON(w, map[string]any{"revisions": arr})
			return
		}
		// revisions.get alt=media: the revision's bytes.
		if r.URL.Query().Get("alt") != "media" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, r := range m.revs[id] {
			if r.id == revID {
				_, _ = w.Write(r.content)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"revision not found"}}`))
	case strings.HasPrefix(p, "/drive/v3/files/"):
		id := strings.TrimPrefix(p, "/drive/v3/files/")
		id = strings.SplitN(id, "/", 2)[0]
		f := m.files[id]
		if f == nil || f.trashed {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"not found"}}`))
			return
		}
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("alt") == "media" {
				_, _ = w.Write(f.content)
				return
			}
			writeJSON(w, m.fileJSON(f))
		case http.MethodPatch:
			var meta map[string]any
			_ = json.NewDecoder(r.Body).Decode(&meta)
			if n, ok := meta["name"].(string); ok {
				f.name = n
			}
			if t, ok := meta["trashed"].(bool); ok {
				f.trashed = t
			}
			writeJSON(w, m.fileJSON(f))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	case p == "/upload/drive/v3/files":
		m.handleUpload(w, r, "")
	case strings.HasPrefix(p, "/upload/drive/v3/files/"):
		id := strings.TrimPrefix(p, "/upload/drive/v3/files/")
		m.handleUpload(w, r, id)
	case strings.HasPrefix(p, "/upload/session/"):
		m.handleSession(w, r, strings.TrimPrefix(p, "/upload/session/"))
	case p == "/oauth2/token":
		writeJSON(w, map[string]any{"access_token": "new-access-token", "token_type": "bearer", "expires_in": 3600})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (m *mockDrive) handleUpload(w http.ResponseWriter, r *http.Request, existingID string) {
	uploadType := r.URL.Query().Get("uploadType")
	if uploadType == "resumable" {
		var meta map[string]any
		_ = json.NewDecoder(r.Body).Decode(&meta)
		sid := m.nextID()
		name, _ := meta["name"].(string)
		f := &mockFile{id: existingID, name: name, mime: "application/octet-stream", modified: "2026-11-24T00:00:00Z"}
		if f.id == "" {
			f.id = m.nextID()
		}
		if parents, ok := meta["parents"].([]any); ok && len(parents) > 0 {
			f.parent, _ = parents[0].(string)
		}
		m.sess[sid] = f
		m.sessDB[sid] = []byte{}
		w.Header().Set("Location", "/upload/session/"+sid)
		w.WriteHeader(http.StatusOK)
		return
	}
	// multipart
	ct := r.Header.Get("Content-Type")
	mt, params, err := mime.ParseMediaType(ct)
	if err != nil || !strings.HasPrefix(mt, "multipart/") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	var meta map[string]any
	var data []byte
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		b, _ := io.ReadAll(part)
		if strings.Contains(part.Header.Get("Content-Type"), "application/json") {
			_ = json.Unmarshal(b, &meta)
		} else {
			data = b
		}
	}
	name, _ := meta["name"].(string)
	var f *mockFile
	if existingID != "" {
		f = m.files[existingID]
		m.pushRevision(f)
		f.content = data
		f.name = name
	} else {
		f = &mockFile{id: m.nextID(), name: name, mime: "application/octet-stream", content: data, modified: "2026-11-24T00:00:00Z"}
		if parents, ok := meta["parents"].([]any); ok && len(parents) > 0 {
			f.parent, _ = parents[0].(string)
		}
		m.files[f.id] = f
	}
	writeJSON(w, m.fileJSON(f))
}

func (m *mockDrive) handleSession(w http.ResponseWriter, r *http.Request, sid string) {
	f, ok := m.sess[sid]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	data, _ := io.ReadAll(r.Body)
	cr := r.Header.Get("Content-Range") // bytes start-end/total
	// total from "bytes 0-99/100" or "bytes 0-99/*"
	total := int64(-1)
	if i := strings.LastIndex(cr, "/"); i >= 0 {
		if v, err := strconv.ParseInt(cr[i+1:], 10, 64); err == nil {
			total = v
		}
	}
	m.sessDB[sid] = append(m.sessDB[sid], data...)
	if total >= 0 && int64(len(m.sessDB[sid])) >= total {
		if old, ok := m.files[f.id]; ok {
			m.pushRevision(old)
		}
		f.content = m.sessDB[sid]
		m.files[f.id] = f
		writeJSON(w, m.fileJSON(f))
		return
	}
	w.WriteHeader(308)
}

func newTestProvider(t *testing.T) (*Provider, *httptest.Server) {
	t.Helper()
	md := newMockDrive()
	srv := httptest.NewServer(http.HandlerFunc(md.handler))
	p, err := newWithBases(storage.Config{Token: "test-token"}, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("newWithBases: %v", err)
	}
	return p, srv
}

func TestDriveContractAgainstMock(t *testing.T) {
	p, srv := newTestProvider(t)
	defer srv.Close()
	contracttest.Run(t, func() (storage.Provider, error) { return p, nil })
}

func TestDriveSearchShareDelta(t *testing.T) {
	p, srv := newTestProvider(t)
	defer srv.Close()
	ctx := t.Context()
	if _, err := p.Upload(ctx, "report.txt", strings.NewReader("hello"), 5); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	found, err := p.Search(ctx, "report")
	if err != nil || len(found) != 1 {
		t.Fatalf("Search: %v, %d results", err, len(found))
	}
	if _, err := p.Share(ctx, "report.txt"); err != nil {
		t.Fatalf("Share: %v", err)
	}
	if _, _, err := p.Delta(ctx, ""); err != nil {
		t.Fatalf("Delta: %v", err)
	}
	if _, _, err := p.Delta(ctx, "tok-1"); err != nil {
		t.Fatalf("Delta continue: %v", err)
	}
}

func TestDriveResumableUpload(t *testing.T) {
	p, srv := newTestProvider(t)
	defer srv.Close()
	// size -1 forces the resumable path.
	data := bytes.Repeat([]byte("y"), 100)
	node, err := p.Upload(t.Context(), "big.bin", bytes.NewReader(data), -1)
	if err != nil {
		t.Fatalf("resumable Upload: %v", err)
	}
	if node.Size != int64(len(data)) {
		t.Fatalf("resumable upload size: got %d want %d", node.Size, len(data))
	}
	rc, err := p.OpenRead(t.Context(), "big.bin")
	if err != nil {
		t.Fatalf("OpenRead: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, data) {
		t.Fatalf("resumable round-trip mismatch")
	}
}

func TestDriveGoogleDocExport(t *testing.T) {
	md := newMockDrive()
	doc := &mockFile{id: "doc-1", name: "企划书", mime: "application/vnd.google-apps.document", parent: "root", modified: "2026-11-24T00:00:00Z"}
	md.files["doc-1"] = doc
	srv := httptest.NewServer(http.HandlerFunc(md.handler))
	defer srv.Close()
	p, err := newWithBases(storage.Config{Token: "test-token"}, srv.URL, srv.URL)
	if err != nil {
		t.Fatalf("newWithBases: %v", err)
	}
	rc, err := p.OpenRead(t.Context(), "企划书")
	if err != nil {
		t.Fatalf("OpenRead doc: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "exported:企划书" {
		t.Fatalf("export content: %q", string(got))
	}
}

func TestDriveQuota(t *testing.T) {
	p, srv := newTestProvider(t)
	defer srv.Close()
	qs, ok := any(p).(storage.QuotaSource)
	if !ok {
		t.Fatal("drive provider must implement storage.QuotaSource")
	}
	used, total, err := qs.Quota(t.Context())
	if err != nil {
		t.Fatalf("Quota: %v", err)
	}
	if used != 123456789 || total != 1099511627776 {
		t.Fatalf("Quota: got used=%d total=%d, want 123456789 / 1099511627776", used, total)
	}
}

// TestDriveVersionsRestore: two uploads of the same file leave one
// revision behind; restoring it brings the old bytes back as the
// new head (Drive v3 has no server-side restore — the driver
// downloads the revision and re-uploads it, see Restore).
func TestDriveVersionsRestore(t *testing.T) {
	p, srv := newTestProvider(t)
	defer srv.Close()
	ctx := t.Context()
	if _, ok := any(p).(storage.Versioner); !ok {
		t.Fatal("drive provider must implement storage.Versioner")
	}
	if _, err := p.Upload(ctx, "report.txt", strings.NewReader("v1"), 2); err != nil {
		t.Fatalf("Upload v1: %v", err)
	}
	if vs, err := p.Versions(ctx, "report.txt"); err != nil || len(vs) != 0 {
		t.Fatalf("Versions after first upload: %v, %d versions, want 0", err, len(vs))
	}
	if _, err := p.Upload(ctx, "report.txt", strings.NewReader("v2"), 2); err != nil {
		t.Fatalf("Upload v2: %v", err)
	}
	vs, err := p.Versions(ctx, "report.txt")
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if len(vs) != 1 {
		t.Fatalf("Versions: got %d, want 1", len(vs))
	}
	if vs[0].Extra["versionId"] == "" {
		t.Fatalf("version node must carry Extra[versionId], got %+v", vs[0])
	}
	if vs[0].Name != "report.txt" || vs[0].Size != 2 {
		t.Fatalf("version node: %+v, want name report.txt size 2", vs[0])
	}
	if err := p.Restore(ctx, "report.txt", vs[0].Extra["versionId"]); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	rc, err := p.OpenRead(ctx, "report.txt")
	if err != nil {
		t.Fatalf("OpenRead after restore: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "v1" {
		t.Fatalf("content after restore: %q, want %q", got, "v1")
	}
	// Restoring is itself undoable: the v2 head became a revision.
	vs, err = p.Versions(ctx, "report.txt")
	if err != nil {
		t.Fatalf("Versions after restore: %v", err)
	}
	if len(vs) != 2 {
		t.Fatalf("Versions after restore: got %d, want 2", len(vs))
	}
	if err := p.Restore(ctx, "report.txt", "rev-nope"); err == nil {
		t.Fatal("Restore of an unknown revision must fail")
	}
}
