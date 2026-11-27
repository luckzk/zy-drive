// SPDX-License-Identifier: MPL-2.0
// Package box implements storage.Provider for Box using the
// official Box API v2 over plain HTTP (no third-party SDK).
//
// Endpoints (api.box.com/2.0 for metadata, upload.box.com/api/2.0
// for content). Auth is Bearer + OAuth2 refresh via
// internal/storage/oauth (single-flight TokenSource, refresh once
// on 401 via the shared http client hook). Box's token endpoint
// requires a client secret at exchange time, so there is no
// built-in one-click app for Box: the wizard uses the user's own
// app (Client ID + Secret), see docs/oauth-apps/box.md.
//   - List: GET /folders/{id}/items, paged by limit/offset.
//   - Upload: POST /files/content (multipart/form-data) up to
//     50 MiB; at or above 20 MiB the chunked upload session is
//     used instead (POST /files/upload_sessions, PUT parts with
//     Content-Range + SHA-1 Digest, POST .../commit). The session
//     lasts 7 days, but this driver does not persist it across
//     restarts (SupportsResumableUpload is false): a failed
//     chunked upload is aborted and retried from zero, like the
//     plain multipart path in the Drime driver.
//   - Download: GET /files/{id}/content (302 to a temporary URL;
//     the client follows it; the Authorization header is not
//     forwarded cross-host by Go, which is what the redirect
//     wants).
//   - Quota: GET /users/me (space_used / space_amount).
//   - Box is ID-based: files and folders carry string ids, refs
//     are display paths ("a/b/c.jpg", "" is the root, id "0"),
//     resolved to ids through a path -> id cache, like Drime.
//   - Delete moves to trash (Box's default); folders delete
//     recursively (recursive=true).
//   - Search: GET /search (content search; the driver filters to
//     the name when the server matches content too).
package box

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
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
	defaultAPIBase    = "https://api.box.com/2.0"
	defaultUploadBase = "https://upload.box.com/api/2.0"
	// chunkedMinSize is the smallest file that may use an upload
	// session (Box requires >= 20 MiB for sessions).
	chunkedMinSize = 20 << 20
	// listLimit is the page size for folder listings.
	listLimit = 1000
)

// Provider is a Box-backed storage.Provider. One instance per
// Mount (never a per-type singleton): each account has its own
// token, its own TokenSource and its own http client, so two Box
// accounts can never share state.
type Provider struct {
	apiBase    string
	uploadBase string
	tokens     *oauth.TokenSource
	client     *shttp.Client

	mu     sync.Mutex
	idMap  map[string]string // display path ("/a/b") -> Box id
	dirMap map[string]bool   // display path -> is a folder
}

// New builds a Box provider. cfg.Token is the OAuth access token
// (memory only). cfg.Extra may carry refresh_token / client_id /
// client_secret for refresh; without them an expired token is
// fatal and the user must re-authorise. cfg.Extra["api_base"] /
// ["upload_base"] override the API endpoints (tests; normally
// empty).
func New(cfg storage.Config) (storage.Provider, error) {
	apiBase, uploadBase := defaultAPIBase, defaultUploadBase
	if v := cfg.Extra["api_base"]; v != "" {
		apiBase = v
	}
	if v := cfg.Extra["upload_base"]; v != "" {
		uploadBase = v
	}
	return newWithBases(cfg, apiBase, uploadBase)
}

func newWithBases(cfg storage.Config, apiBase, uploadBase string) (*Provider, error) {
	if cfg.Token == "" && cfg.Extra["refresh_token"] == "" {
		return nil, fmt.Errorf("box: access token or refresh token is required")
	}
	var refresher oauth.Refresher
	if cfg.Extra["refresh_token"] != "" {
		tokenURL := cfg.Extra["token_url"]
		if tokenURL == "" {
			tokenURL = "https://api.box.com/oauth2/token"
		}
		refresher = oauth.FormRefresherWithProxy(tokenURL,
			cfg.Extra["client_id"], cfg.Extra["client_secret"], cfg.Proxy)
	}
	ts := oauth.NewTokenSource(&oauth.Token{
		AccessToken:  cfg.Token,
		RefreshToken: cfg.Extra["refresh_token"],
	}, refresher)
	p := &Provider{
		apiBase:    strings.TrimSuffix(apiBase, "/"),
		uploadBase: strings.TrimSuffix(uploadBase, "/"),
		tokens:     ts,
		idMap:      map[string]string{},
		dirMap:     map[string]bool{},
	}
	p.client = shttp.NewWithProxy(nil, cfg.Proxy)
	// The Authorization header is attached by the transport on
	// EVERY attempt, not when the request is built: after a 401
	// the shared client refreshes the token once and replays the
	// request, and the replay must carry the NEW token (see the
	// Drime driver, which does it the same way).
	p.client.HTTP = &http.Client{
		Timeout:   120 * time.Second,
		Transport: authTransport{base: netproxy.TransportOrShared(cfg.Proxy), tokens: ts},
	}
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

// Caps declares Box capabilities.
func (p *Provider) Caps() domain.Caps {
	return domain.Caps{
		CanMkdir: true, CanRename: true, CanDelete: true, DeleteToTrash: true,
		SupportsServerSideCopy:  false,
		SupportsVersions:        false,
		SupportsShare:           true,
		SupportsSearch:          true,
		SupportsDelta:           false,
		SupportsThumbnail:       false,
		SupportsResumableUpload: false, // upload sessions exist but are not persisted/resumed
		SupportsEncryption:      false,
		SupportsStorageClass:    false,
		SupportsPresignedURL:    false, // Box uses temporary download links instead
		IDBased:                 true,
		MaxFileSize:             50 << 30, // 50 GiB via upload sessions (Box limit)
		MinPartSize:             0,        // server picks the part size per session
		MaxPathLen:              255,      // file names limited to 255 characters
	}
}

// --- wire types ---

type boxItem struct {
	Type       string `json:"type"` // "file" or "folder"
	ID         string `json:"id"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	ModifiedAt string `json:"modified_at"`
	SHA1       string `json:"sha1"`
}

func (e boxItem) node(ref string) domain.Node {
	n := domain.Node{
		Ref:   ref,
		Path:  "/" + strings.Trim(ref, "/"),
		Name:  e.Name,
		IsDir: e.Type == "folder",
		Size:  -1,
		Mime:  mimeOf(e.Name),
		Extra: map[string]string{"boxId": e.ID},
	}
	if e.Type == "file" {
		n.Size = e.Size
		if e.SHA1 != "" {
			n.Hash = e.SHA1
			n.HashKind = "sha1"
		}
	}
	if t, err := time.Parse(time.RFC3339, e.ModifiedAt); err == nil {
		n.ModTime = t
	}
	if ref == "" {
		n.Path = "/"
		n.Name = ""
	}
	return n
}

// --- request helpers ---

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

// doJSON sends a JSON request to the Box API and decodes the
// response into out. A non-2xx status is a Box API error.
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
		Type    string `json:"type"`
		Status  int    `json:"status"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &body)
	msg := body.Message
	if msg == "" {
		msg = strings.TrimSpace(string(data))
	}
	if msg == "" {
		msg = resp.Status
	}
	return fmt.Errorf("box api %d: %s", resp.StatusCode, msg)
}

func (p *Provider) wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return &storage.ProviderError{Op: op, Provider: "box", Err: err}
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

// resolve returns the Box id and folder flag for a display path,
// walking from the root with per-folder listings. "" and "/" are
// the root (id "0"). Results are cached in idMap/dirMap; Rename
// and Delete invalidate the affected paths.
func (p *Provider) resolve(ctx context.Context, ref string) (string, bool, error) {
	ref = strings.Trim(ref, "/")
	if ref == "" {
		return "0", true, nil
	}
	p.mu.Lock()
	if id, ok := p.idMap[cleanRef(ref)]; ok {
		isDir := p.dirMap[cleanRef(ref)]
		p.mu.Unlock()
		return id, isDir, nil
	}
	p.mu.Unlock()
	// Walk down from the root, listing each parent once.
	parts := strings.Split(ref, "/")
	parentID := "0"
	var parentRef string
	for i, name := range parts {
		items, err := p.listChildren(ctx, parentID)
		if err != nil {
			return "", false, err
		}
		var found *boxItem
		for _, it := range items {
			if it.Name == name {
				c := it
				found = &c
				break
			}
		}
		if found == nil {
			return "", false, p.wrap("List", fmt.Errorf("box: no such file or folder: %q", ref))
		}
		curRef := parentRef + "/" + name
		if i == len(parts)-1 {
			p.mu.Lock()
			p.idMap[cleanRef(curRef)] = found.ID
			p.dirMap[cleanRef(curRef)] = found.Type == "folder"
			p.mu.Unlock()
			return found.ID, found.Type == "folder", nil
		}
		if found.Type != "folder" {
			return "", false, p.wrap("List", fmt.Errorf("box: %q is not a folder", curRef))
		}
		parentID = found.ID
		parentRef = curRef
	}
	return "", false, p.wrap("List", fmt.Errorf("box: no such file or folder: %q", ref))
}

func (p *Provider) invalidate(refs ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ref := range refs {
		ref = cleanRef(ref)
		for k := range p.idMap {
			if k == ref || strings.HasPrefix(k, ref+"/") {
				delete(p.idMap, k)
				delete(p.dirMap, k)
			}
		}
	}
}

func (p *Provider) listChildren(ctx context.Context, parentID string) ([]boxItem, error) {
	var out []boxItem
	offset := 0
	for {
		u := fmt.Sprintf("%s/folders/%s/items?limit=%d&offset=%d&fields=id,type,name,size,modified_at,sha1",
			p.apiBase, url.PathEscape(parentID), listLimit, offset)
		var resp struct {
			TotalCount int       `json:"total_count"`
			Entries    []boxItem `json:"entries"`
		}
		if err := p.doJSON(ctx, "List", http.MethodGet, u, nil, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Entries...)
		if len(out) >= resp.TotalCount || len(resp.Entries) == 0 {
			break
		}
		offset += len(resp.Entries)
	}
	return out, nil
}

// --- Provider methods ---

func (p *Provider) List(ctx context.Context, ref string) ([]domain.Node, error) {
	id, isDir, err := p.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	if !isDir {
		n, err := p.Stat(ctx, ref)
		if err != nil {
			return nil, err
		}
		return []domain.Node{n}, nil
	}
	items, err := p.listChildren(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Node, 0, len(items))
	base := strings.Trim(ref, "/")
	for _, it := range items {
		childRef := it.Name
		if base != "" {
			childRef = base + "/" + it.Name
		}
		out = append(out, it.node(childRef))
		p.mu.Lock()
		p.idMap[cleanRef(childRef)] = it.ID
		p.dirMap[cleanRef(childRef)] = it.Type == "folder"
		p.mu.Unlock()
	}
	return out, nil
}

func (p *Provider) Stat(ctx context.Context, ref string) (domain.Node, error) {
	id, isDir, err := p.resolve(ctx, ref)
	if err != nil {
		return domain.Node{}, err
	}
	kind := "files"
	if isDir {
		kind = "folders"
	}
	var item boxItem
	u := fmt.Sprintf("%s/%s/%s?fields=id,type,name,size,modified_at,sha1", p.apiBase, kind, url.PathEscape(id))
	if err := p.doJSON(ctx, "Stat", http.MethodGet, u, nil, &item); err != nil {
		return domain.Node{}, err
	}
	return item.node(strings.Trim(ref, "/")), nil
}

func (p *Provider) OpenRead(ctx context.Context, ref string) (io.ReadCloser, error) {
	id, isDir, err := p.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	if isDir {
		return nil, p.wrap("OpenRead", fmt.Errorf("box: %q is a folder", ref))
	}
	req, err := p.newRequest(ctx, http.MethodGet, p.apiBase+"/files/"+url.PathEscape(id)+"/content", nil)
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

// Mkdir creates a folder. A folder that already exists is success:
// the error path re-checks with resolve, so a real failure (no
// permission, bad parent) still surfaces.
func (p *Provider) Mkdir(ctx context.Context, ref string) error {
	parentRef, name := splitRef(ref)
	if name == "" {
		return p.wrap("Mkdir", fmt.Errorf("box: cannot create folder %q: no name", ref))
	}
	parentID, _, err := p.resolve(ctx, parentRef)
	if err != nil {
		return err
	}
	var out boxItem
	if err := p.doJSON(ctx, "Mkdir", http.MethodPost, p.apiBase+"/folders",
		map[string]any{"name": name, "parent": map[string]any{"id": parentID}}, &out); err != nil {
		if _, _, ferr := p.resolve(ctx, ref); ferr == nil {
			return nil
		}
		return err
	}
	p.mu.Lock()
	p.idMap[cleanRef(ref)] = out.ID
	p.dirMap[cleanRef(ref)] = true
	p.mu.Unlock()
	return nil
}

// Rename moves/renames within the same provider. A same-folder
// rename is one PUT; a cross-folder move adds the new parent id.
func (p *Provider) Rename(ctx context.Context, src, dst string) error {
	id, isDir, err := p.resolve(ctx, src)
	if err != nil {
		return err
	}
	srcParentRef, srcName := splitRef(src)
	dstParentRef, dstName := splitRef(dst)
	if dstName == "" {
		return p.wrap("Rename", fmt.Errorf("box: cannot rename to %q: no file name", dst))
	}
	dstParentID, _, err := p.resolve(ctx, dstParentRef)
	if err != nil {
		return err
	}
	kind := "files"
	if isDir {
		kind = "folders"
	}
	body := map[string]any{}
	if srcName != dstName {
		body["name"] = dstName
	}
	if srcParentRef != dstParentRef {
		body["parent"] = map[string]any{"id": dstParentID}
	}
	if len(body) == 0 {
		return nil
	}
	var out boxItem
	u := fmt.Sprintf("%s/%s/%s", p.apiBase, kind, url.PathEscape(id))
	if err := p.doJSON(ctx, "Rename", http.MethodPut, u, body, &out); err != nil {
		return err
	}
	p.invalidate(src, dst)
	return nil
}

// Delete moves one node to trash (Box's default; folders delete
// recursively).
func (p *Provider) Delete(ctx context.Context, ref string) error {
	id, isDir, err := p.resolve(ctx, ref)
	if err != nil {
		return err
	}
	kind := "files"
	u := p.apiBase + "/files/" + url.PathEscape(id)
	if isDir {
		kind = "folders"
		u = p.apiBase + "/folders/" + url.PathEscape(id) + "?recursive=true"
	}
	_ = kind
	if err := p.doJSON(ctx, "Delete", http.MethodDelete, u, nil, nil); err != nil {
		return err
	}
	p.invalidate(ref)
	return nil
}

// Search searches by name (Box's search also matches content; the
// driver keeps only name matches, so a content hit does not
// surprise the user with a file whose name says nothing).
func (p *Provider) Search(ctx context.Context, query string) ([]domain.Node, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	u := fmt.Sprintf("%s/search?query=%s&type=file&limit=100&offset=0&fields=id,type,name,size,modified_at,sha1",
		p.apiBase, url.QueryEscape(query))
	var resp struct {
		TotalCount int `json:"total_count"`
		Entries    []struct {
			boxItem
			PathCollection struct {
				Entries []struct {
					Name string `json:"name"`
				} `json:"entries"`
			} `json:"path_collection"`
		} `json:"entries"`
	}
	if err := p.doJSON(ctx, "Search", http.MethodGet, u, nil, &resp); err != nil {
		return nil, err
	}
	var out []domain.Node
	for _, e := range resp.Entries {
		if !strings.Contains(strings.ToLower(e.Name), strings.ToLower(query)) {
			continue
		}
		var parts []string
		for _, pe := range e.PathCollection.Entries {
			if pe.Name != "" {
				parts = append(parts, pe.Name)
			}
		}
		parts = append(parts, e.Name)
		ref := strings.Join(parts, "/")
		out = append(out, e.node(ref))
	}
	return out, nil
}

// Quota returns (used, total) in bytes from GET /users/me.
// space_amount <= 0 means the account reports no total.
func (p *Provider) Quota(ctx context.Context) (int64, int64, error) {
	var me struct {
		SpaceUsed   int64 `json:"space_used"`
		SpaceAmount int64 `json:"space_amount"`
	}
	if err := p.doJSON(ctx, "Quota", http.MethodGet, p.apiBase+"/users/me", nil, &me); err != nil {
		return 0, 0, err
	}
	return me.SpaceUsed, me.SpaceAmount, nil
}

// ShareLink returns a public shared link for a file, creating one
// (open access) when none exists.
func (p *Provider) ShareLink(ctx context.Context, ref string) (string, error) {
	id, isDir, err := p.resolve(ctx, ref)
	if err != nil {
		return "", err
	}
	if isDir {
		return "", p.wrap("Share", fmt.Errorf("box: cannot share a folder %q (files only)", ref))
	}
	var out struct {
		SharedLink struct {
			URL string `json:"url"`
		} `json:"shared_link"`
	}
	u := p.apiBase + "/files/" + url.PathEscape(id)
	if err := p.doJSON(ctx, "Share", http.MethodPost, u+"/shared_links",
		map[string]any{"shared_link": map[string]any{"access": "open"}}, &out); err != nil {
		return "", err
	}
	if out.SharedLink.URL == "" {
		return "", p.wrap("Share", fmt.Errorf("box: the server returned no shared link for %q", ref))
	}
	return out.SharedLink.URL, nil
}

func mimeOf(name string) string {
	ext := strings.ToLower(path.Ext(name))
	switch ext {
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
	case ".mov":
		return "video/quicktime"
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

// spool writes the already-buffered head plus the rest of r to a
// temp file and returns its path (for unknown-size uploads that
// turn out to be large).
func (p *Provider) spool(r io.Reader, head []byte) (string, error) {
	f, err := os.CreateTemp("", "zy-drive-box-upload-*")
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

// --- Upload ---

// Upload stores a file. Files under 20 MiB go up in one
// multipart/form-data call (buffered, so the shared client's
// retries can replay the body); 20 MiB and above use a chunked
// upload session, one part at a time (the server picks the part
// size). Unknown size is buffered up to 20 MiB in memory, then
// spooled to a temp file so the session still gets an exact size.
func (p *Provider) Upload(ctx context.Context, ref string, r io.Reader, size int64) (domain.Node, error) {
	parentRef, name := splitRef(ref)
	if name == "" {
		return domain.Node{}, p.wrap("Upload", fmt.Errorf("box: cannot upload to %q: no file name", ref))
	}
	parentID, _, err := p.resolve(ctx, parentRef)
	if err != nil {
		return domain.Node{}, err
	}
	if size >= 0 && size < chunkedMinSize {
		data, err := io.ReadAll(io.LimitReader(r, chunkedMinSize))
		if err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		return p.directUpload(ctx, ref, name, parentID, data)
	}
	if size < 0 {
		data, err := io.ReadAll(io.LimitReader(r, chunkedMinSize+1))
		if err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		if int64(len(data)) < chunkedMinSize {
			return p.directUpload(ctx, ref, name, parentID, data)
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
		return p.chunkedUpload(ctx, ref, name, parentID, f, st.Size())
	}
	return p.chunkedUpload(ctx, ref, name, parentID, r, size)
}

// directUpload is POST /files/content (multipart/form-data) for
// files under 20 MiB. The whole body is in memory, so the shared
// client's retries can replay it.
func (p *Provider) directUpload(ctx context.Context, ref, name, parentID string, data []byte) (domain.Node, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	attrs, _ := json.Marshal(map[string]any{"name": name, "parent": map[string]any{"id": parentID}})
	if err := w.WriteField("attributes", string(attrs)); err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	part, err := w.CreateFormFile("file", name)
	if err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	if _, err := part.Write(data); err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	if err := w.Close(); err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	bodyBytes := buf.Bytes()
	req, err := p.newRequest(ctx, http.MethodPost, p.uploadBase+"/files/content", bytes.NewReader(bodyBytes))
	if err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := p.client.Do(ctx, req)
	if err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return domain.Node{}, p.wrap("Upload", p.readError(resp))
	}
	var out struct {
		Entries []boxItem `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	if len(out.Entries) == 0 {
		return domain.Node{}, p.wrap("Upload", fmt.Errorf("box: upload returned no file entry"))
	}
	item := out.Entries[0]
	if item.Size != int64(len(data)) {
		return domain.Node{}, p.wrap("Upload", fmt.Errorf(
			"box: size mismatch after upload: Box says %d, local is %d",
			item.Size, len(data)))
	}
	p.mu.Lock()
	p.idMap[cleanRef(ref)] = item.ID
	p.dirMap[cleanRef(ref)] = false
	p.mu.Unlock()
	return item.node(strings.Trim(ref, "/")), nil
}

// chunkedUpload runs the upload-session flow for files >= 20 MiB:
// create a session, PUT each part (Content-Range + SHA-1 Digest),
// commit. On any failure the session is best-effort aborted.
func (p *Provider) chunkedUpload(ctx context.Context, ref, name, parentID string, r io.Reader, size int64) (domain.Node, error) {
	var created struct {
		ID         string `json:"id"`
		PartSize   int64  `json:"part_size"`
		TotalParts int    `json:"total_parts"`
	}
	if err := p.doJSON(ctx, "Upload", http.MethodPost, p.uploadBase+"/files/upload_sessions",
		map[string]any{"folder_id": parentID, "file_size": size, "file_name": name}, &created); err != nil {
		return domain.Node{}, err
	}
	if created.ID == "" || created.PartSize <= 0 {
		return domain.Node{}, p.wrap("Upload", fmt.Errorf("box: upload session create returned no id/part_size"))
	}
	node, err := p.chunkedRun(ctx, ref, name, r, size, created.ID, created.PartSize)
	if err != nil {
		// Best effort: do not leave an orphaned session.
		_ = p.doJSON(ctx, "Upload", http.MethodDelete, p.uploadBase+"/files/upload_sessions/"+url.PathEscape(created.ID), nil, nil)
		return domain.Node{}, err
	}
	return node, nil
}

func (p *Provider) chunkedRun(ctx context.Context, ref, name string, r io.Reader, size int64, sessionID string, partSize int64) (domain.Node, error) {
	totalParts := int((size + partSize - 1) / partSize)
	type committedPart struct {
		PartID string `json:"part_id"`
		Offset int64  `json:"offset"`
		Size   int64  `json:"size"`
		SHA1   string `json:"sha1"`
	}
	parts := make([]committedPart, 0, totalParts)
	buf := make([]byte, partSize)
	var offset int64
	for n := 0; n < totalParts; n++ {
		chunk, readErr := io.ReadFull(r, buf)
		if chunk == 0 && readErr != nil {
			return domain.Node{}, p.wrap("Upload", readErr)
		}
		data := buf[:chunk]
		sum := sha1.Sum(data)
		digest := base64.StdEncoding.EncodeToString(sum[:])
		end := offset + int64(chunk) - 1
		req, err := p.newRequest(ctx, http.MethodPut,
			p.uploadBase+"/files/upload_sessions/"+url.PathEscape(sessionID),
			bytes.NewReader(data))
		if err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, end, size))
		req.Header.Set("Digest", "sha="+digest)
		resp, err := p.client.Do(ctx, req)
		if err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		var partResp struct {
			Part struct {
				PartID string `json:"part_id"`
				Offset int64  `json:"offset"`
				Size   int64  `json:"size"`
				SHA1   string `json:"sha1"`
			} `json:"part"`
		}
		decodeErr := func() error {
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return p.readError(resp)
			}
			return json.NewDecoder(resp.Body).Decode(&partResp)
		}()
		if decodeErr != nil {
			return domain.Node{}, p.wrap("Upload", decodeErr)
		}
		if partResp.Part.PartID == "" {
			return domain.Node{}, p.wrap("Upload", fmt.Errorf("box: part %d upload returned no part_id", n+1))
		}
		parts = append(parts, committedPart{
			PartID: partResp.Part.PartID,
			Offset: partResp.Part.Offset,
			Size:   partResp.Part.Size,
			SHA1:   partResp.Part.SHA1,
		})
		offset += int64(chunk)
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			return domain.Node{}, p.wrap("Upload", readErr)
		}
	}
	if len(parts) != totalParts {
		return domain.Node{}, p.wrap("Upload", fmt.Errorf("box: uploaded %d of %d parts", len(parts), totalParts))
	}
	var item boxItem
	if err := p.doJSON(ctx, "Upload", http.MethodPost,
		p.uploadBase+"/files/upload_sessions/"+url.PathEscape(sessionID)+"/commit",
		map[string]any{"parts": parts}, &item); err != nil {
		return domain.Node{}, err
	}
	if item.Size != size {
		return domain.Node{}, p.wrap("Upload", fmt.Errorf(
			"box: size mismatch after commit: Box says %d, local is %d",
			item.Size, size))
	}
	p.mu.Lock()
	p.idMap[cleanRef(ref)] = item.ID
	p.dirMap[cleanRef(ref)] = false
	p.mu.Unlock()
	return item.node(strings.Trim(ref, "/")), nil
}
