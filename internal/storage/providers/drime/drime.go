// Package drime implements storage.Provider for Drime Cloud using the
// official Drime API v1 over plain HTTP (no third-party SDK).
//
// API base: https://app.drime.cloud/api/v1, Bearer personal access
// token (Settings → Developers → Create token) or OAuth2 access token.
// Docs snapshot (2026-11-17, ~/workspace/ts-spaces/
// drime-api-wen-dang-jing-tai-zhan) and the verified helper
// ~/workspace/custom-connectors/custom.drime/bin/drime.py agree on
// the wire shape; this driver follows both, it does not copy rclone.
//
//   - Entries are ID-based: files and folders carry integer ids, a
//     parent_id (null at the root) and a hash (URL-safe id, NOT a
//     content hash). Refs in this driver are display paths
//     ("a/b/c.jpg", "" is the root), resolved to ids through a
//     path -> id cache, exactly like the Drive driver.
//   - List: GET /drive/file-entries?workspaceId=N&perPage=100&
//     page=N&parentIds[]=ID, paginated via current_page/last_page.
//   - Stat: GET /file-entries/{id}/model (without /model the same
//     URL returns the file CONTENT, not metadata).
//   - Download: GET /file-entries/{id} streams the content.
//   - Upload: POST /uploads (multipart/form-data) under 5 MiB;
//     at or above 5 MiB: POST /s3/multipart/create, sign parts in
//     batches of 8 (POST /s3/multipart/batch-sign-part-urls), PUT
//     each 5 MiB part to its presigned R2 URL (no credential),
//     POST /s3/multipart/complete, then POST /s3/entries to
//     register. Sizes are verified after upload.
//   - Mkdir: POST /folders?workspaceId=N {name, parentId}.
//   - Rename: PUT /file-entries/{id} {name} for a same-folder
//     rename; POST /file-entries/move {entryIds, destinationId}
//     when the parent changes (destinationId null = root).
//   - Delete: POST /file-entries/delete {entryIds} moves to trash
//     (deleteForever is never set: DeleteToTrash is true).
//   - Search: GET /drive/file-entries?query=... on the same list
//     endpoint; result paths are rebuilt by walking parent ids.
//   - Share: GET /file-entries/{id}/shareable-link, creating one
//     (POST .../shareable-link) when none exists; the public URL is
//     https://app.drime.cloud/drive/shares/{hash}.
//   - Thumbnail: the entry's thumbnail_url (signed, 7 days).
//   - No versions API that maps to storage.Versioner (file-backup
//     is backup history, restore is for trashed entries), and no
//     delta endpoint: both are honestly absent from Caps.
//   - Drime gives no content hash by default (file_hash is the
//     client-declared SHA-256, or an MD5 after an in-place replace,
//     or null); Node.Hash is only set when file_hash looks like a
//     64-hex SHA-256 or 32-hex MD5, and is never compared across
//     clouds when the algorithm differs.
//
// Rate limits: Drime 429s have historically meant a long penalty
// window (~15h). The shared retry client waits 60s and retries a
// 429 exactly once, then gives up with a plain-language error —
// never a loop. Multipart part PUTs go to R2, not to Drime, and
// retry per part (re-sign, up to 3 attempts). A failed multipart
// upload is best-effort aborted: unlike drime.py this driver keeps
// no resume sidecar, so leaving an orphaned server-side upload
// behind would only leak parts.
package drime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/netproxy"
	"github.com/luckzk/zy-drive/internal/storage"
	shttp "github.com/luckzk/zy-drive/internal/storage/http"
	"github.com/luckzk/zy-drive/internal/storage/oauth"
)

const (
	defaultAPIBase = "https://app.drime.cloud/api/v1"
	defaultTokenURL = "https://app.drime.cloud/oauth/token"
	// singleUploadLimit is Drime's simple-upload ceiling: files of
	// 5 MiB or more must use multipart (docs + drime.py agree).
	singleUploadLimit = 5 << 20
	// partSize is the multipart chunk size (5 MiB, per docs).
	partSize = 5 << 20
	// signBatch is how many part URLs are signed at once.
	signBatch = 8
	// partAttempts is the per-part PUT budget (re-sign between tries).
	partAttempts = 3
)

// drimePolicy is the retry discipline for Drime API calls: network
// errors and 5xx back off exponentially (max 2 retries); a 429
// waits 60s and is retried exactly once, then the call fails with a
// plain-language rate-limit error. Never a loop: a second 429 means
// Drime wants us gone for a while (~15h penalty seen in practice).
var drimePolicy = &shttp.RetryPolicy{
	MaxRetries:    2,
	BaseBackoff:   500 * time.Millisecond,
	MaxBackoff:    30 * time.Second,
	RetryOn429:    true,
	Backoff429:    60 * time.Second,
	Max429Retries: 1,
}

// Provider is a Drime-backed storage.Provider. One instance per
// Mount (never a per-type singleton): each account has its own
// token, its own TokenSource and its own http client, so two Drime
// accounts can never share state.
type Provider struct {
	apiBase     string
	workspaceID int
	tokens      *oauth.TokenSource
	client      *shttp.Client
	partClient  *http.Client // presigned R2 PUTs: no credential, no retry wrapper

	mu    sync.Mutex
	idMap map[string]int64 // display path ("/a/b") -> entry id
}

// New builds a Drime provider. cfg.Token is the Bearer token: a
// personal access token (never expires) or an OAuth access token
// (expires in 1h). cfg.Extra may carry refresh_token / client_id /
// token_url for OAuth refresh; without them an expired OAuth token
// is fatal and the user must re-authorise. cfg.Extra["api_base"]
// overrides the API endpoint (tests; normally empty).
// cfg.Extra["workspace_id"] picks a non-personal workspace
// (default 0 = personal workspace).
func New(cfg storage.Config) (storage.Provider, error) {
	apiBase := defaultAPIBase
	if v := cfg.Extra["api_base"]; v != "" {
		apiBase = v
	}
	return newWithBase(cfg, apiBase)
}

func newWithBase(cfg storage.Config, apiBase string) (*Provider, error) {
	if cfg.Token == "" && cfg.Extra["refresh_token"] == "" {
		return nil, fmt.Errorf("drime: access token or refresh token is required")
	}
	ws := 0
	if v := strings.TrimSpace(cfg.Extra["workspace_id"]); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("drime: workspace_id must be a non-negative integer, got %q", v)
		}
		ws = n
	}
	var refresher oauth.Refresher
	if cfg.Extra["refresh_token"] != "" {
		tokenURL := cfg.Extra["token_url"]
		if tokenURL == "" {
			tokenURL = defaultTokenURL
		}
		// Drime desktop apps are public clients: no client secret
		// is ever sent (Drime refuses one from a public client).
		refresher = oauth.FormRefresher(tokenURL,
			cfg.Extra["client_id"], cfg.Extra["client_secret"])
	}
	ts := oauth.NewTokenSource(&oauth.Token{
		AccessToken:  cfg.Token,
		RefreshToken: cfg.Extra["refresh_token"],
	}, refresher)
	p := &Provider{
		apiBase:     strings.TrimSuffix(apiBase, "/"),
		workspaceID: ws,
		tokens:      ts,
		idMap:       map[string]int64{},
		partClient:  &http.Client{Timeout: 180 * time.Second},
	}
	p.client = shttp.New(drimePolicy)
	// The Authorization header is attached by the transport on
	// EVERY attempt, not when the request is built: after a 401 the
	// shared client refreshes the token once and replays the
	// request, and the replay must carry the NEW token — a header
	// baked in at build time would replay the stale one and 401
	// again. (Dropbox/Drive build the header up front; their
	// 401-refresh path has exactly that flaw. Drime does it right.)
	p.client.HTTP = &http.Client{
		Timeout:   120 * time.Second,
		Transport: authTransport{base: netproxy.Transport(), tokens: ts},
	}
	// Refresh only when a refresher exists: a personal access token
	// never expires, so a 401 on it means wrong/revoked — the
	// original 401 must reach the caller, not a bogus refresh error.
	if refresher != nil {
		p.client.OnAuthExpired = func(ctx context.Context) error {
			_, err := ts.Refresh(ctx)
			return err
		}
	}
	return p, nil
}

// authTransport attaches the Bearer token on every attempt, so a
// retried request (after a 401 refresh) carries the fresh token.
type authTransport struct {
	base   http.RoundTripper
	tokens *oauth.TokenSource
}

func (t authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.tokens.Token(req.Context())
	if err != nil {
		return nil, err
	}
	req2 := req.Clone(req.Context())
	req2.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(req2)
}

// Caps declares Drime capabilities, from the API docs (2026-11-17
// snapshot). Absent on purpose: Versions (file-backup is backup
// history, not a versions API), Delta (no delta endpoint), server-
// side copy (a duplicate endpoint exists but is not wired),
// encryption (Vault files stay encrypted server-side; the API
// cannot decrypt them).
func (p *Provider) Caps() domain.Caps {
	return domain.Caps{
		CanMkdir: true, CanRename: true, CanDelete: true, DeleteToTrash: true,
		SupportsServerSideCopy:  false,
		SupportsVersions:        false,
		SupportsShare:           true,
		SupportsSearch:          true,
		SupportsDelta:           false,
		SupportsThumbnail:       true,
		SupportsResumableUpload: true, // multipart; resume across restarts is not implemented
		SupportsEncryption:      false,
		SupportsStorageClass:    false,
		SupportsPresignedURL:    true,
		IDBased:                 true,
		MaxFileSize:             0, // no documented limit
		MinPartSize:             partSize,
		MaxPathLen:              255, // file names limited to 255 bytes UTF-8
	}
}

// --- wire types ---

type entry struct {
	ID                 int64   `json:"id"`
	Name               string  `json:"name"`
	Type               string  `json:"type"`
	Hash               string  `json:"hash"`
	FileHash           string  `json:"file_hash"`
	FileSize           int64   `json:"file_size"`
	Mime               string  `json:"mime"`
	Extension          string  `json:"extension"`
	ParentID           *int64  `json:"parent_id"`
	WorkspaceID        int     `json:"workspace_id"`
	URL                string  `json:"url"`
	ThumbnailURL       string  `json:"thumbnail_url"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
	ClientLastModified *int64  `json:"client_last_modified"`
}

func (e entry) node(ref string) domain.Node {
	n := domain.Node{
		Ref:      strings.Trim(ref, "/"),
		Path:     "/" + strings.Trim(ref, "/"),
		Name:     e.Name,
		IsDir:    e.Type == "folder",
		Size:     -1,
		Mime:     e.Mime,
		ThumbURL: e.ThumbnailURL,
		Extra:    map[string]string{},
	}
	if e.Type != "folder" {
		n.Size = e.FileSize
		if e.Mime == "" {
			n.Mime = mimeOf(e.Name)
		}
	}
	if h, kind := hashOf(e.FileHash); h != "" {
		n.Hash = h
		n.HashKind = kind
	}
	if e.ID != 0 {
		n.Extra["drimeId"] = strconv.FormatInt(e.ID, 10)
	}
	if e.Hash != "" {
		n.Extra["drimeHash"] = e.Hash
	}
	if t, err := parseTime(e.UpdatedAt); err == nil {
		n.ModTime = t
	} else if t, err := parseTime(e.CreatedAt); err == nil {
		n.ModTime = t
	}
	if n.Ref == "" {
		n.Path = "/"
		n.Name = ""
	}
	return n
}

// hashOf trusts file_hash only when it looks like a real digest:
// 64 hex = SHA-256 (after Verify File Integrity, or client-declared
// at upload), 32 hex = MD5 (in-place content replace). Anything
// else is not a hash we can compare, so it is dropped.
func hashOf(h string) (string, string) {
	h = strings.ToLower(strings.TrimSpace(h))
	if len(h) == 64 && isHex(h) {
		return h, "sha256"
	}
	if len(h) == 32 && isHex(h) {
		return h, "md5"
	}
	return "", ""
}

func isHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return len(s) > 0
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

// --- request helpers ---

// newRequest builds a request. GetBody is always set (http.NoBody
// for bodyless requests) so the shared client's 401-refresh replay
// works for GETs as well as POSTs. The Authorization header is NOT
// set here: authTransport attaches the current token on every
// attempt (see newWithBase).
func (p *Provider) newRequest(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	var bodyBytes []byte
	var err error
	if body != nil {
		bodyBytes, err = io.ReadAll(body)
		if err != nil {
			return nil, err
		}
	}
	var rdr io.Reader
	if bodyBytes != nil {
		rdr = bytes.NewReader(bodyBytes)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, err
	}
	if bodyBytes != nil {
		b := bodyBytes
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
	} else {
		req.GetBody = func() (io.ReadCloser, error) { return http.NoBody, nil }
	}
	return req, nil
}

// doJSON sends a JSON request and decodes the response into out.
// A non-2xx status is a Drime API error, never a decode attempt.
func (p *Provider) doJSON(ctx context.Context, op, method, url string, arg any, out any) error {
	var body io.Reader
	if arg != nil {
		buf, err := json.Marshal(arg)
		if err != nil {
			return p.wrap(op, err)
		}
		body = bytes.NewReader(buf)
	}
	req, err := p.newRequest(ctx, method, url, body)
	if err != nil {
		return p.wrap(op, err)
	}
	if arg != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(ctx, req)
	if err != nil {
		return p.wrap(op, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return p.wrap(op, p.readError(resp))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return p.wrap(op, err)
		}
	}
	return nil
}

func (p *Provider) readError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var body struct {
		Message string         `json:"message"`
		Errors  map[string]any `json:"errors"`
	}
	_ = json.Unmarshal(data, &body)
	msg := body.Message
	if msg == "" {
		msg = strings.TrimSpace(string(data))
	}
	if msg == "" {
		msg = resp.Status
	}
	return fmt.Errorf("drime api %d: %s", resp.StatusCode, msg)
}

func (p *Provider) wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return &storage.ProviderError{Op: op, Provider: "drime", Err: err}
}

// --- path <-> ID ---

func cleanRef(ref string) string { return "/" + strings.Trim(ref, "/") }

func splitRef(ref string) (parent, name string) {
	ref = strings.Trim(ref, "/")
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return "", ref
}

// resolve returns the entry ID for a display path, walking from the
// root with per-folder listings. "" and "/" resolve to the root
// (id 0 is the sentinel: no real entry has id 0).
func (p *Provider) resolve(ctx context.Context, ref string) (int64, error) {
	ref = cleanRef(ref)
	if ref == "/" {
		return 0, nil
	}
	p.mu.Lock()
	if id, ok := p.idMap[ref]; ok {
		p.mu.Unlock()
		return id, nil
	}
	p.mu.Unlock()
	segments := strings.Split(strings.Trim(ref, "/"), "/")
	var parent int64 // 0 = root
	cur := ""
	for _, seg := range segments {
		cur += "/" + seg
		p.mu.Lock()
		if id, ok := p.idMap[cur]; ok {
			p.mu.Unlock()
			parent = id
			continue
		}
		p.mu.Unlock()
		id, err := p.findChild(ctx, parent, seg)
		if err != nil {
			return 0, err
		}
		p.mu.Lock()
		p.idMap[cur] = id
		p.mu.Unlock()
		parent = id
	}
	return parent, nil
}

// findChild finds one child of parentID by exact name. parentID 0
// means the workspace root.
func (p *Provider) findChild(ctx context.Context, parentID int64, name string) (int64, error) {
	children, err := p.listChildren(ctx, parentID)
	if err != nil {
		return 0, err
	}
	for _, e := range children {
		if e.Name == name {
			return e.ID, nil
		}
	}
	return 0, p.wrap("Stat", fmt.Errorf("drime: no such file or folder: %s", name))
}

func (p *Provider) invalidate(refs ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ref := range refs {
		ref = cleanRef(ref)
		for cached := range p.idMap {
			if cached == ref || strings.HasPrefix(cached, ref+"/") {
				delete(p.idMap, cached)
			}
		}
	}
}

// listChildren returns the direct children of a folder id (0=root),
// consuming pagination inside.
func (p *Provider) listChildren(ctx context.Context, parentID int64) ([]entry, error) {
	var out []entry
	page := 1
	for {
		u := fmt.Sprintf("%s/drive/file-entries?workspaceId=%d&perPage=100&page=%d",
			p.apiBase, p.workspaceID, page)
		if parentID != 0 {
			u += "&parentIds[]=" + strconv.FormatInt(parentID, 10)
		}
		var resp struct {
			Data       []entry `json:"data"`
			CurrentPage int    `json:"current_page"`
			LastPage    int    `json:"last_page"`
		}
		if err := p.doJSON(ctx, "List", http.MethodGet, u, nil, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Data...)
		if resp.LastPage == 0 || page >= resp.LastPage {
			break
		}
		page++
	}
	return out, nil
}

// getModel fetches one entry's metadata by id.
func (p *Provider) getModel(ctx context.Context, op string, id int64) (entry, error) {
	var resp struct {
		FileEntry entry `json:"fileEntry"`
	}
	u := fmt.Sprintf("%s/file-entries/%d/model?workspaceId=%d", p.apiBase, id, p.workspaceID)
	if err := p.doJSON(ctx, op, http.MethodGet, u, nil, &resp); err != nil {
		return entry{}, err
	}
	return resp.FileEntry, nil
}

// --- Provider ---

// List returns direct children of ref. Pagination is consumed inside.
func (p *Provider) List(ctx context.Context, ref string) ([]domain.Node, error) {
	parentID, err := p.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	children, err := p.listChildren(ctx, parentID)
	if err != nil {
		return nil, err
	}
	parentRef := strings.Trim(ref, "/")
	out := make([]domain.Node, 0, len(children))
	for _, e := range children {
		childRef := e.Name
		if parentRef != "" {
			childRef = parentRef + "/" + e.Name
		}
		out = append(out, e.node(childRef))
		p.mu.Lock()
		p.idMap[cleanRef(childRef)] = e.ID
		p.mu.Unlock()
	}
	return out, nil
}

// Stat returns metadata for one node.
func (p *Provider) Stat(ctx context.Context, ref string) (domain.Node, error) {
	if strings.Trim(ref, "/") == "" {
		return domain.Node{Ref: "", Path: "/", IsDir: true, Size: -1}, nil
	}
	id, err := p.resolve(ctx, ref)
	if err != nil {
		return domain.Node{}, err
	}
	e, err := p.getModel(ctx, "Stat", id)
	if err != nil {
		return domain.Node{}, err
	}
	return e.node(ref), nil
}

// OpenRead streams a file for download. A Stat first confirms the
// ref is a file: GET /file-entries/{id} on a folder would return a
// ZIP, which is not file content.
func (p *Provider) OpenRead(ctx context.Context, ref string) (io.ReadCloser, error) {
	id, err := p.resolve(ctx, ref)
	if err != nil {
		return nil, p.wrap("OpenRead", err)
	}
	e, err := p.getModel(ctx, "OpenRead", id)
	if err != nil {
		return nil, p.wrap("OpenRead", err)
	}
	if e.Type == "folder" {
		return nil, p.wrap("OpenRead", fmt.Errorf("drime: %s is a folder, not a file", ref))
	}
	u := fmt.Sprintf("%s/file-entries/%d", p.apiBase, id)
	req, err := p.newRequest(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, p.wrap("OpenRead", err)
	}
	resp, err := p.client.Do(ctx, req)
	if err != nil {
		return nil, p.wrap("OpenRead", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, p.wrap("OpenRead", p.readError(resp))
	}
	return resp.Body, nil
}

// Upload writes a file. Under 5 MiB it is one POST /uploads call;
// at or above 5 MiB it is the multipart flow (create, sign in
// batches of 8, PUT parts to presigned R2 URLs, complete, register).
// Unknown size (-1) is buffered up to 5 MiB in memory; beyond that
// it is spooled to a temp file so the multipart create still gets
// an exact size. Callers that know the size (every transfer in the
// app does) never touch disk.
func (p *Provider) Upload(ctx context.Context, ref string, r io.Reader, size int64) (domain.Node, error) {
	parentRef, name := splitRef(ref)
	if name == "" {
		return domain.Node{}, p.wrap("Upload", fmt.Errorf("drime: cannot upload to %q: no file name", ref))
	}
	parentID, err := p.resolve(ctx, parentRef)
	if err != nil {
		return domain.Node{}, err
	}
	var pid *int64
	if parentID != 0 {
		pid = &parentID
	}
	if size >= 0 && size < singleUploadLimit {
		data, err := io.ReadAll(io.LimitReader(r, singleUploadLimit))
		if err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		return p.simpleUpload(ctx, ref, name, pid, data)
	}
	if size < 0 {
		// Unknown size: buffer up to the simple limit; spill to a
		// temp file only when the file turns out to be large.
		data, err := io.ReadAll(io.LimitReader(r, singleUploadLimit+1))
		if err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		if int64(len(data)) <= singleUploadLimit {
			return p.simpleUpload(ctx, ref, name, pid, data)
		}
		tmp, err := p.spool(r, data)
		if err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		defer func() { _ = os.Remove(tmp) }()
		f, err := os.Open(tmp)
		if err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		return p.multipartUpload(ctx, ref, name, pid, f, st.Size())
	}
	return p.multipartUpload(ctx, ref, name, pid, r, size)
}

// spool writes the already-buffered head plus the rest of r to a
// temp file and returns its path.
func (p *Provider) spool(r io.Reader, head []byte) (string, error) {
	f, err := os.CreateTemp("", "zy-drive-drime-upload-*")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(head); err != nil {
		f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		_ = os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// simpleUpload is POST /uploads (multipart/form-data), files < 5 MiB.
// The whole body is in memory, so the shared client's retries can
// replay it. The returned entry's size is verified.
func (p *Provider) simpleUpload(ctx context.Context, ref, name string, parentID *int64, data []byte) (domain.Node, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", name)
	if err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	if _, err := part.Write(data); err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	if parentID != nil {
		_ = w.WriteField("parentId", strconv.FormatInt(*parentID, 10))
	}
	_ = w.WriteField("workspaceId", strconv.Itoa(p.workspaceID))
	if err := w.Close(); err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	bodyBytes := buf.Bytes()
	req, err := p.newRequest(ctx, http.MethodPost, p.apiBase+"/uploads", bytes.NewReader(bodyBytes))
	if err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := p.client.Do(ctx, req)
	if err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return domain.Node{}, p.wrap("Upload", p.readError(resp))
	}
	var out struct {
		FileEntry entry `json:"fileEntry"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	if out.FileEntry.FileSize != int64(len(data)) {
		return domain.Node{}, p.wrap("Upload", fmt.Errorf(
			"drime: size mismatch after upload: Drime says %d, local is %d",
			out.FileEntry.FileSize, len(data)))
	}
	p.mu.Lock()
	p.idMap[cleanRef(ref)] = out.FileEntry.ID
	p.mu.Unlock()
	return out.FileEntry.node(ref), nil
}

// multipartUpload runs the create → sign → PUT parts → complete →
// register flow for files >= 5 MiB. Parts are PUT sequentially to
// their presigned R2 URLs (no credential); a failed part is
// re-signed and retried (up to partAttempts). On any failure the
// server-side upload is best-effort aborted — this driver keeps no
// resume state, so an orphan would only leak parts.
func (p *Provider) multipartUpload(ctx context.Context, ref, name string, parentID *int64, r io.Reader, size int64) (domain.Node, error) {
	mime := mimeOf(name)
	ext := ""
	if i := strings.LastIndex(name, "."); i >= 0 && i < len(name)-1 {
		ext = strings.ToLower(name[i+1:])
	}
	createBody := map[string]any{
		"filename": name, "mime": mime, "size": size,
		"extension": ext, "workspaceId": p.workspaceID,
	}
	if parentID != nil {
		createBody["parentId"] = *parentID
	}
	var created struct {
		Key      string `json:"key"`
		UploadID string `json:"uploadId"`
	}
	if err := p.doJSON(ctx, "Upload", http.MethodPost, p.apiBase+"/s3/multipart/create", createBody, &created); err != nil {
		return domain.Node{}, err
	}
	if created.Key == "" || created.UploadID == "" {
		return domain.Node{}, p.wrap("Upload", fmt.Errorf("drime: multipart create returned no key/uploadId"))
	}
	node, err := p.multipartRun(ctx, ref, name, mime, ext, parentID, r, size, created.Key, created.UploadID)
	if err != nil {
		// Best effort: do not leave an orphaned multipart upload.
		_ = p.doJSON(ctx, "Upload", http.MethodPost, p.apiBase+"/s3/multipart/abort",
			map[string]any{"key": created.Key, "uploadId": created.UploadID}, nil)
		return domain.Node{}, err
	}
	return node, nil
}

func (p *Provider) multipartRun(ctx context.Context, ref, name, mime, ext string, parentID *int64, r io.Reader, size int64, key, uploadID string) (domain.Node, error) {
	totalParts := int((size + partSize - 1) / partSize)
	done := make(map[int]string, totalParts)
	buf := make([]byte, partSize)
	for batchStart := 1; batchStart <= totalParts; batchStart += signBatch {
		batchEnd := batchStart + signBatch - 1
		if batchEnd > totalParts {
			batchEnd = totalParts
		}
		var numbers []int
		for n := batchStart; n <= batchEnd; n++ {
			numbers = append(numbers, n)
		}
		urls, err := p.signParts(ctx, key, uploadID, numbers)
		if err != nil {
			return domain.Node{}, err
		}
		for _, n := range numbers {
			chunk, readErr := io.ReadFull(r, buf)
			if chunk > 0 {
				etag, err := p.putPart(ctx, key, uploadID, n, urls[n], buf[:chunk])
				if err != nil {
					return domain.Node{}, err
				}
				done[n] = etag
			}
			if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
				break
			}
			if readErr != nil {
				return domain.Node{}, p.wrap("Upload", readErr)
			}
		}
	}
	if len(done) != totalParts {
		return domain.Node{}, p.wrap("Upload", fmt.Errorf("drime: uploaded %d of %d parts", len(done), totalParts))
	}
	parts := make([]map[string]any, 0, totalParts)
	for n := 1; n <= totalParts; n++ {
		parts = append(parts, map[string]any{"PartNumber": n, "ETag": done[n]})
	}
	var completed struct {
		Location string `json:"location"`
	}
	if err := p.doJSON(ctx, "Upload", http.MethodPost, p.apiBase+"/s3/multipart/complete",
		map[string]any{"key": key, "uploadId": uploadID, "parts": parts}, &completed); err != nil {
		return domain.Node{}, err
	}
	uuidName := key[strings.LastIndex(key, "/")+1:]
	regBody := map[string]any{
		"filename": uuidName, "size": size, "clientName": name,
		"clientMime": mime, "clientExtension": ext, "workspaceId": p.workspaceID,
	}
	if parentID != nil {
		regBody["parentId"] = *parentID
	}
	var reg struct {
		FileEntry entry `json:"fileEntry"`
	}
	if err := p.doJSON(ctx, "Upload", http.MethodPost, p.apiBase+"/s3/entries", regBody, &reg); err != nil {
		return domain.Node{}, err
	}
	if reg.FileEntry.FileSize != size {
		return domain.Node{}, p.wrap("Upload", fmt.Errorf(
			"drime: size mismatch after register: Drime says %d, local is %d",
			reg.FileEntry.FileSize, size))
	}
	p.mu.Lock()
	p.idMap[cleanRef(ref)] = reg.FileEntry.ID
	p.mu.Unlock()
	return reg.FileEntry.node(ref), nil
}

// signParts signs one batch of part numbers.
func (p *Provider) signParts(ctx context.Context, key, uploadID string, numbers []int) (map[int]string, error) {
	var resp struct {
		URLs []struct {
			PartNumber int    `json:"partNumber"`
			URL        string `json:"url"`
		} `json:"urls"`
	}
	if err := p.doJSON(ctx, "Upload", http.MethodPost, p.apiBase+"/s3/multipart/batch-sign-part-urls",
		map[string]any{"key": key, "uploadId": uploadID, "partNumbers": numbers}, &resp); err != nil {
		return nil, err
	}
	out := make(map[int]string, len(resp.URLs))
	for _, u := range resp.URLs {
		out[u.PartNumber] = u.URL
	}
	for _, n := range numbers {
		if out[n] == "" {
			return nil, p.wrap("Upload", fmt.Errorf("drime: no signed URL for part %d", n))
		}
	}
	return out, nil
}

// putPart PUTs one chunk to its presigned R2 URL (no credential).
// On failure it re-signs that part and retries, up to partAttempts.
func (p *Provider) putPart(ctx context.Context, key, uploadID string, n int, url string, chunk []byte) (string, error) {
	var lastErr error
	curURL := url
	for attempt := 0; attempt < partAttempts; attempt++ {
		if attempt > 0 {
			urls, err := p.signParts(ctx, key, uploadID, []int{n})
			if err != nil {
				return "", err
			}
			curURL = urls[n]
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, curURL, bytes.NewReader(chunk))
		if err != nil {
			return "", p.wrap("Upload", err)
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.ContentLength = int64(len(chunk))
		resp, err := p.partClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		etag := resp.Header.Get("ETag")
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("drime: part %d PUT returned %s", n, resp.Status)
			continue
		}
		if etag == "" {
			lastErr = fmt.Errorf("drime: part %d PUT returned no ETag", n)
			continue
		}
		return etag, nil
	}
	return "", p.wrap("Upload", fmt.Errorf("drime: part %d failed after %d attempts: %w", n, partAttempts, lastErr))
}

// Mkdir creates a folder. A folder that already exists is success:
// the error path re-checks with findChild, so a real failure (no
// permission, bad parent) still surfaces.
func (p *Provider) Mkdir(ctx context.Context, ref string) error {
	parentRef, name := splitRef(ref)
	if name == "" {
		return p.wrap("Mkdir", fmt.Errorf("drime: cannot create folder %q: no name", ref))
	}
	parentID, err := p.resolve(ctx, parentRef)
	if err != nil {
		return err
	}
	body := map[string]any{"name": name}
	if parentID != 0 {
		body["parentId"] = parentID
	}
	var out struct {
		Folder entry `json:"folder"`
	}
	u := fmt.Sprintf("%s/folders?workspaceId=%d", p.apiBase, p.workspaceID)
	if err := p.doJSON(ctx, "Mkdir", http.MethodPost, u, body, &out); err != nil {
		if id, ferr := p.findChild(ctx, parentID, name); ferr == nil {
			p.mu.Lock()
			p.idMap[cleanRef(ref)] = id
			p.mu.Unlock()
			return nil
		}
		return err
	}
	p.mu.Lock()
	p.idMap[cleanRef(ref)] = out.Folder.ID
	p.mu.Unlock()
	return nil
}

// Rename moves/renames within the same provider. A same-folder
// rename is one PUT; a cross-folder move is POST /move plus a PUT
// when the name also changes.
func (p *Provider) Rename(ctx context.Context, src, dst string) error {
	id, err := p.resolve(ctx, src)
	if err != nil {
		return err
	}
	srcParentRef, srcName := splitRef(src)
	dstParentRef, dstName := splitRef(dst)
	if dstName == "" {
		return p.wrap("Rename", fmt.Errorf("drime: cannot rename to %q: no file name", dst))
	}
	srcParentID, err := p.resolve(ctx, srcParentRef)
	if err != nil {
		return err
	}
	dstParentID, err := p.resolve(ctx, dstParentRef)
	if err != nil {
		return err
	}
	if srcParentID != dstParentID {
		var dstID any
		if dstParentID != 0 {
			dstID = dstParentID
		}
		u := fmt.Sprintf("%s/file-entries/move?workspaceId=%d", p.apiBase, p.workspaceID)
		var out struct {
			Entries []entry `json:"entries"`
		}
		if err := p.doJSON(ctx, "Rename", http.MethodPost, u,
			map[string]any{"entryIds": []int64{id}, "destinationId": dstID}, &out); err != nil {
			return err
		}
	}
	if srcName != dstName {
		var out struct {
			FileEntry entry `json:"fileEntry"`
		}
		u := fmt.Sprintf("%s/file-entries/%d?workspaceId=%d", p.apiBase, id, p.workspaceID)
		if err := p.doJSON(ctx, "Rename", http.MethodPut, u,
			map[string]any{"name": dstName}, &out); err != nil {
			return err
		}
	}
	p.invalidate(src, dst)
	return nil
}

// Delete moves one node to trash (never permanent: DeleteToTrash).
func (p *Provider) Delete(ctx context.Context, ref string) error {
	id, err := p.resolve(ctx, ref)
	if err != nil {
		return err
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := p.doJSON(ctx, "Delete", http.MethodPost, p.apiBase+"/file-entries/delete",
		map[string]any{"entryIds": []int64{id}, "deleteForever": false}, &out); err != nil {
		return err
	}
	p.invalidate(ref)
	return nil
}

// --- optional interfaces ---

// Search implements storage.Searcher on the list endpoint's query
// filter. Result paths are rebuilt by walking parent ids, so the
// returned refs are operable (unlike name-only refs).
func (p *Provider) Search(ctx context.Context, query string) ([]domain.Node, error) {
	var all []entry
	page := 1
	for {
		u := fmt.Sprintf("%s/drive/file-entries?workspaceId=%d&perPage=50&page=%d&query=%s",
			p.apiBase, p.workspaceID, page, url.QueryEscape(query))
		var resp struct {
			Data      []entry `json:"data"`
			LastPage  int     `json:"last_page"`
		}
		if err := p.doJSON(ctx, "Search", http.MethodGet, u, nil, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Data...)
		if resp.LastPage == 0 || page >= resp.LastPage {
			break
		}
		page++
	}
	out := make([]domain.Node, 0, len(all))
	for _, e := range all {
		ref, err := p.rebuildPath(ctx, e)
		if err != nil {
			ref = e.Name // fallback: root-level ref, may not resolve
		}
		out = append(out, e.node(ref))
	}
	return out, nil
}

// rebuildPath walks parent ids up to the root to rebuild a display
// path for a search hit. Up to 32 levels; anything deeper or any
// failed lookup falls back to the bare name.
func (p *Provider) rebuildPath(ctx context.Context, e entry) (string, error) {
	parts := []string{e.Name}
	cur := e.ParentID
	for depth := 0; depth < 32 && cur != nil; depth++ {
		parent, err := p.getModel(ctx, "Search", *cur)
		if err != nil {
			return "", err
		}
		parts = append([]string{parent.Name}, parts...)
		cur = parent.ParentID
	}
	return strings.Join(parts, "/"), nil
}

// Share implements storage.Sharer via the shareable link: an
// existing link is reused, otherwise one is created (download
// allowed, edit not).
func (p *Provider) Share(ctx context.Context, ref string) (string, error) {
	id, err := p.resolve(ctx, ref)
	if err != nil {
		return "", err
	}
	var got struct {
		Link *struct {
			Hash string `json:"hash"`
		} `json:"link"`
	}
	u := fmt.Sprintf("%s/file-entries/%d/shareable-link", p.apiBase, id)
	if err := p.doJSON(ctx, "Share", http.MethodGet, u, nil, &got); err != nil {
		// No link yet (404) is not a failure: create one below.
		// Any other error (auth, rate limit) must surface.
		if !strings.Contains(err.Error(), "404") {
			return "", err
		}
	} else if got.Link != nil && got.Link.Hash != "" {
		return "https://app.drime.cloud/drive/shares/" + got.Link.Hash, nil
	}
	var created struct {
		Link struct {
			Hash string `json:"hash"`
		} `json:"link"`
	}
	if err := p.doJSON(ctx, "Share", http.MethodPost, u,
		map[string]any{"allow_download": true, "allow_edit": false}, &created); err != nil {
		return "", err
	}
	if created.Link.Hash == "" {
		return "", p.wrap("Share", fmt.Errorf("drime: share link created but no hash returned"))
	}
	return "https://app.drime.cloud/drive/shares/" + created.Link.Hash, nil
}

// Thumbnail implements storage.Thumber from the entry's signed
// thumbnail_url (valid 7 days, null when the file has none).
func (p *Provider) Thumbnail(ctx context.Context, ref string) (string, error) {
	id, err := p.resolve(ctx, ref)
	if err != nil {
		return "", err
	}
	e, err := p.getModel(ctx, "Thumbnail", id)
	if err != nil {
		return "", err
	}
	if e.ThumbnailURL == "" {
		return "", p.wrap("Thumbnail", fmt.Errorf("drime: %s has no thumbnail", ref))
	}
	return e.ThumbnailURL, nil
}

// Quota implements storage.QuotaSource via GET /user/space-usage
// (Drime API docs, "Get Space Usage"): {"used": <bytes>,
// "available": <bytes>} for this mount's workspace. The total is
// used + available — the API reports no separate total field, so
// this sum is the documented total, not an invention.
func (p *Provider) Quota(ctx context.Context) (used, total int64, err error) {
	var resp struct {
		Used      int64 `json:"used"`
		Available int64 `json:"available"`
	}
	u := fmt.Sprintf("%s/user/space-usage?workspaceId=%d", p.apiBase, p.workspaceID)
	if err := p.doJSON(ctx, "Quota", http.MethodGet, u, nil, &resp); err != nil {
		return 0, 0, err
	}
	return resp.Used, resp.Used + resp.Available, nil
}

func mimeOf(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".mp4":
		return "video/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".pdf":
		return "application/pdf"
	case ".txt", ".md":
		return "text/plain"
	case ".zip":
		return "application/zip"
	default:
		return "application/octet-stream"
	}
}

var (
	_ storage.Provider    = (*Provider)(nil)
	_ storage.Searcher    = (*Provider)(nil)
	_ storage.Sharer      = (*Provider)(nil)
	_ storage.Thumber     = (*Provider)(nil)
	_ storage.QuotaSource = (*Provider)(nil)
)
