// SPDX-License-Identifier: MPL-2.0
// Package onedrive implements storage.Provider for Microsoft
// OneDrive (personal, work/school, and the OneDrive for Business
// document library behind it) through the Microsoft Graph API v1.0
// over plain HTTP (no third-party SDK).
//
// API base: https://graph.microsoft.com/v1.0, Bearer OAuth2 token.
// Docs: learn.microsoft.com Graph REST 1.0 (driveItem, delta,
// createUploadSession, upload content, search, createLink,
// versions, thumbnails), and the Microsoft identity platform docs
// for the authorization code flow.
//
//   - OneDrive is ID-based: a driveItem has a stable id and a
//     parentReference (id + path like "/drive/root:/Photos").
//     Refs in this driver are display paths ("a/b/c.jpg", "" is
//     the root), resolved to ids through a path -> id cache, like
//     the Drive and Drime drivers. Resolution is one call:
//     GET /me/drive/root:{path} (path addressing), not a walk.
//   - List: GET /me/drive/items/{id}/children, pages followed via
//     @odata.nextLink.
//   - Stat: GET /me/drive/items/{id} ($select=id,name,size,file,
//     folder,parentReference,lastModifiedDateTime,webUrl).
//   - Download: the item's @microsoft.graph.downloadUrl (a
//     pre-authenticated short-lived URL), GET directly with a plain
//     client — never through Graph, never with the bearer token.
//     Falls back to GET /me/drive/items/{id}/content.
//   - Upload: files up to 4 MiB are one PUT to
//     /me/drive/items/{parentId}:/{name}:/content (Graph accepts
//     250 MB in one PUT, but Microsoft's guidance is to use an
//     upload session above 4 MiB, and this driver follows it).
//     Larger files: POST .../createUploadSession (conflictBehavior
//     replace), then PUT 5 MiB chunks (a multiple of 320 KiB) to
//     the pre-authenticated uploadUrl with Content-Range and NO
//     Authorization header. Final chunk returns the driveItem.
//     A chunk PUT that answers 409 resourceModified (or 404/410,
//     session gone) is not a failure on its own: the driver first
//     checks whether the file is already complete on the server
//     (size, and SHA-1 when the server carries one) and otherwise
//     resumes from the session's nextExpectedRanges, restarting
//     with a fresh session when the old one is dead and the source
//     can be re-read. Unknown size is buffered up to 4 MiB, then
//     spooled to a temp file so the session gets an exact total
//     (same pattern as Drime). OneDrive's per-file ceiling is 250 GB.
//   - Mkdir: POST /me/drive/items/{parentId}/children
//     {name, folder:{}, @microsoft.graph.conflictBehavior:"fail"};
//     a 409 is re-checked (already exists = success).
//   - Rename/move: one PATCH /me/drive/items/{id} with name and/or
//     parentReference.id.
//   - Delete: DELETE /me/drive/items/{id} (204; the item goes to
//     the recycle bin, so DeleteToTrash is true).
//   - Search: GET /me/drive/root/search(q='...'); display paths
//     are rebuilt from parentReference.path.
//   - Delta: GET /me/drive/root/delta, following @odata.nextLink
//     until @odata.deltaLink; the cursor IS the deltaLink URL.
//     Deleted items carry the "deleted" facet.
//   - Share: POST /me/drive/items/{id}/createLink
//     {type:"view", scope:"anonymous"} -> link.webUrl.
//   - Versions: GET /me/drive/items/{id}/versions, restore via
//     POST .../versions/{versionId}/restoreVersion.
//   - Thumbnail: GET /me/drive/items/{id}/thumbnails, large URL
//     first, then medium, then small.
//   - Path length: OneDrive caps the full path at about 400
//     characters; longer refs are rejected client-side with a
//     plain-language error instead of a cryptic Graph 400.
//   - Hash: file.hashes.sha1Hash when present (a real content
//     hash). quickXorHash is OneDrive-specific and is NOT exposed
//     as a comparable hash.
//
// Auth: standard OAuth 2.0 authorization code flow against the
// Microsoft identity platform (login.microsoftonline.com/common,
// so personal and work/school accounts both work). Scopes:
// Files.ReadWrite.All + offline_access (the latter is what makes
// Microsoft issue a refresh token). Desktop apps are public
// clients: register the app under "Mobile and desktop
// applications", no client secret, PKCE S256 (RFC 7636) — the
// same generic PKCE machinery as Drime, because Microsoft's own
// guidance for public/native clients is authorization code +
// PKCE. A confidential (web) registration still works too: if a
// client secret is stored, it is sent on refresh/exchange like
// Dropbox/Google. Token refresh is the shared FormRefresher.
//
// Rate limits: Graph throttles with 429/503 and a Retry-After
// header. The shared client honours Retry-After, then waits 60s
// and retries a 429 at most twice; a third 429 fails with a
// plain-language error — never a loop. 5xx retries use exponential
// backoff (max 3). Upload-session chunk PUTs retry the same way
// (up to 3 attempts per chunk, re-reading Retry-After).
package onedrive

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	defaultAPIBase  = "https://graph.microsoft.com/v1.0"
	defaultTokenURL = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
	// simpleUploadLimit is the small-upload ceiling: files up to
	// 4 MiB go in one PUT /content; larger files use an upload
	// session (Microsoft's own guidance draws the line at 4 MiB,
	// even though a single PUT accepts up to 250 MB).
	simpleUploadLimit = 4 << 20
	// chunkSize is the upload-session fragment size: 5 MiB, a
	// multiple of the 320 KiB fragment alignment Graph requires,
	// and under the 60 MiB per-fragment ceiling.
	chunkSize = 5 << 20
	// maxFileSize is OneDrive's per-file ceiling (250 GB).
	maxFileSize = 250 << 30
	// maxPathLen is OneDrive's full-path ceiling (~400 chars).
	maxPathLen = 400
	// chunkAttempts is the per-chunk PUT budget on the session URL.
	chunkAttempts = 3
	// maxChunkConflicts is the number of 409 resourceModified (or
	// 404/410 session-gone) recoveries allowed per upload session
	// before giving up: a chunk that keeps conflicting is a real
	// problem (e.g. the file keeps changing on OneDrive), not a
	// lost response, so the upload fails instead of looping.
	maxChunkConflicts = 3
	// maxSessionRestarts is the number of fresh upload sessions a
	// single Upload may start after the first one dies mid-upload.
	// One restart is enough to recover from an expired session;
	// more would risk re-uploading a large file in a loop.
	maxSessionRestarts = 1
)

// graphPolicy is the retry discipline for Graph API calls: network
// errors and 5xx back off exponentially (max 3 retries); a 429
// honours Retry-After when present, otherwise waits 60s, and is
// retried at most twice — then the call fails with a plain-language
// rate-limit error. Never a loop.
var graphPolicy = &shttp.RetryPolicy{
	MaxRetries:    3,
	BaseBackoff:   500 * time.Millisecond,
	MaxBackoff:    30 * time.Second,
	RetryOn429:    true,
	Backoff429:    60 * time.Second,
	Max429Retries: 2,
}

// Provider is a OneDrive-backed storage.Provider. One instance per
// Mount (never a per-type singleton): each account has its own
// token, its own TokenSource and its own http client, so two
// OneDrive accounts can never share state.
type Provider struct {
	apiBase string
	tokens  *oauth.TokenSource
	client  *shttp.Client
	plain   *http.Client // downloadUrl / uploadUrl: no credential, no retry wrapper

	mu    sync.Mutex
	idMap map[string]string // display path ("/a/b") -> driveItem id
}

// New builds a OneDrive provider. cfg.Token is the OAuth access
// token. cfg.Extra may carry refresh_token / client_id /
// client_secret (confidential registrations only; public clients
// leave it empty) / token_url. cfg.Extra["api_base"] overrides the
// Graph endpoint (tests; normally empty).
func New(cfg storage.Config) (storage.Provider, error) {
	apiBase := defaultAPIBase
	if v := cfg.Extra["api_base"]; v != "" {
		apiBase = v
	}
	return newWithBase(cfg, apiBase)
}

func newWithBase(cfg storage.Config, apiBase string) (*Provider, error) {
	if cfg.Token == "" && cfg.Extra["refresh_token"] == "" {
		return nil, fmt.Errorf("onedrive: access token or refresh token is required")
	}
	var refresher oauth.Refresher
	if cfg.Extra["refresh_token"] != "" {
		tokenURL := cfg.Extra["token_url"]
		if tokenURL == "" {
			tokenURL = defaultTokenURL
		}
		// Public (desktop) clients send no secret; confidential
		// (web) registrations send theirs. FormRefresher only
		// includes client_secret when it is non-empty.
		refresher = oauth.FormRefresher(tokenURL,
			cfg.Extra["client_id"], cfg.Extra["client_secret"])
	}
	ts := oauth.NewTokenSource(&oauth.Token{
		AccessToken:  cfg.Token,
		RefreshToken: cfg.Extra["refresh_token"],
	}, refresher)
	p := &Provider{
		apiBase: strings.TrimSuffix(apiBase, "/"),
		tokens:  ts,
		idMap:   map[string]string{},
		plain:   &http.Client{Timeout: 180 * time.Second},
	}
	p.client = shttp.New(graphPolicy)
	// The Authorization header is attached by the transport on
	// EVERY attempt, not when the request is built: after a 401
	// the shared client refreshes the token once and replays the
	// request, and the replay must carry the NEW token — a header
	// baked in at build time would replay the stale one and 401
	// again. (Same pattern as the Drime driver; the Drive driver
	// still has that flaw.)
	p.client.HTTP = &http.Client{
		Timeout:   120 * time.Second,
		Transport: authTransport{base: netproxy.Transport(), tokens: ts},
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

// Caps declares OneDrive capabilities, from the Graph docs.
// Absent on purpose: server-side copy (Graph has a copy action,
// but it is async with a monitor URL, not the synchronous
// semantics the transfer engine expects) and encryption / storage
// class (not a OneDrive concept).
func (p *Provider) Caps() domain.Caps {
	return domain.Caps{
		CanMkdir: true, CanRename: true, CanDelete: true, DeleteToTrash: true,
		SupportsServerSideCopy:  false,
		SupportsVersions:        true,
		SupportsShare:           true,
		SupportsSearch:          true,
		SupportsDelta:           true,
		SupportsThumbnail:       true,
		SupportsResumableUpload: true, // upload sessions; resume across restarts is not implemented
		SupportsEncryption:      false,
		SupportsStorageClass:    false,
		SupportsPresignedURL:    true, // @microsoft.graph.downloadUrl
		IDBased:                 true,
		MaxFileSize:             maxFileSize,
		MinPartSize:             chunkSize,
		MaxPathLen:              maxPathLen,
		IllegalChars:            `"/\:*?<>|`,
		ReservedNames:           []string{"CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9"},
	}
}

// --- wire types ---

const itemFields = "id,name,size,file,folder,parentReference,createdDateTime,lastModifiedDateTime,webUrl"

type driveItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	File *struct {
		MimeType string `json:"mimeType"`
		Hashes   struct {
			SHA1Hash     string `json:"sha1Hash"`
			QuickXorHash string `json:"quickXorHash"`
		} `json:"hashes"`
	} `json:"file"`
	Folder *struct {
		ChildCount int `json:"childCount"`
	} `json:"folder"`
	ParentReference *struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	} `json:"parentReference"`
	CreatedDateTime      string    `json:"createdDateTime"`
	LastModifiedDateTime string    `json:"lastModifiedDateTime"`
	WebURL               string    `json:"webUrl"`
	DownloadURL          string    `json:"@microsoft.graph.downloadUrl"`
	Deleted              *struct{} `json:"deleted,omitempty"`
}

func (it driveItem) node(displayPath string) domain.Node {
	n := domain.Node{
		Ref:   strings.Trim(displayPath, "/"),
		Path:  "/" + strings.Trim(displayPath, "/"),
		Name:  it.Name,
		IsDir: it.Folder != nil,
		Size:  -1,
	}
	if it.File != nil {
		n.Size = it.Size
		n.Mime = it.File.MimeType
		// Only the SHA-1 is a real content hash. quickXorHash is
		// OneDrive-specific and must never be compared across
		// clouds as if it were a digest.
		if h := it.File.Hashes.SHA1Hash; h != "" {
			n.Hash = strings.ToLower(h)
			n.HashKind = "sha1"
		}
	}
	if it.ID != "" {
		if n.Extra == nil {
			n.Extra = map[string]string{}
		}
		n.Extra["onedriveId"] = it.ID
	}
	if it.Deleted != nil {
		if n.Extra == nil {
			n.Extra = map[string]string{}
		}
		n.Extra["removed"] = "true"
	}
	if t, err := time.Parse(time.RFC3339, it.LastModifiedDateTime); err == nil {
		n.ModTime = t
	} else if t, err := time.Parse(time.RFC3339, it.CreatedDateTime); err == nil {
		n.ModTime = t
	}
	if n.Ref == "" {
		n.Path = "/"
		n.Name = ""
	}
	return n
}

// displayPathOf rebuilds a display path from a driveItem's
// parentReference.path ("/drive/root:/a/b") plus its name. The root
// item itself has no parent path and maps to "".
func displayPathOf(it driveItem) string {
	if it.ParentReference == nil || it.ParentReference.Path == "" {
		return it.Name
	}
	parent := strings.TrimPrefix(it.ParentReference.Path, "/drive/root:")
	parent = strings.Trim(parent, "/")
	if parent == "" {
		return it.Name
	}
	return parent + "/" + it.Name
}

// --- request helpers ---

// newRequest builds a request. GetBody is always set so the shared
// client's 401-refresh replay works. The Authorization header is
// NOT set here: authTransport attaches the current token on every
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
// A non-2xx status is a Graph API error, never a decode attempt.
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
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &body)
	code := body.Error.Code
	msg := body.Error.Message
	if msg == "" {
		msg = strings.TrimSpace(string(data))
	}
	if msg == "" {
		msg = resp.Status
	}
	if code != "" {
		return fmt.Errorf("graph api %d (%s): %s", resp.StatusCode, code, msg)
	}
	return fmt.Errorf("graph api %d: %s", resp.StatusCode, msg)
}

func (p *Provider) wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return &storage.ProviderError{Op: op, Provider: "onedrive", Err: err}
}

// checkPath rejects refs OneDrive cannot store: a full path over
// ~400 characters, or a name containing characters OneDrive
// forbids. The caller gets a plain-language error instead of a
// cryptic Graph 400.
func checkPath(ref string) error {
	ref = strings.Trim(ref, "/")
	if len(ref) > maxPathLen {
		return fmt.Errorf("onedrive: 路径太长（%d 字符，OneDrive 上限约 400 字符）：请缩短文件夹名或把文件往上层放", len(ref))
	}
	for _, seg := range strings.Split(ref, "/") {
		if seg == "" {
			continue
		}
		if strings.ContainsAny(seg, `"/\:*?<>|`) {
			return fmt.Errorf("onedrive: 文件名 %q 含有 OneDrive 不允许的字符（\" : * ? < > |），请改名后再试", seg)
		}
	}
	return nil
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

// escapePath escapes one path segment at a time for Graph's
// path-addressing syntax (/me/drive/root:/a/b/c:).
func escapePath(ref string) string {
	parts := strings.Split(strings.Trim(ref, "/"), "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// resolve returns the driveItem id for a display path. "" and "/"
// resolve to the root sentinel "root" (a real id Graph accepts in
// /me/drive/items/root/...). Resolution is one path-addressing
// GET, not a walk: GET /me/drive/root:{path}.
func (p *Provider) resolve(ctx context.Context, ref string) (string, error) {
	ref = cleanRef(ref)
	if ref == "/" {
		return "root", nil
	}
	p.mu.Lock()
	if id, ok := p.idMap[ref]; ok {
		p.mu.Unlock()
		return id, nil
	}
	p.mu.Unlock()
	u := p.apiBase + "/me/drive/root:/" + escapePath(ref) +
		"?$select=" + url.QueryEscape("id")
	var it driveItem
	if err := p.doJSON(ctx, "Stat", http.MethodGet, u, nil, &it); err != nil {
		return "", err
	}
	if it.ID == "" {
		return "", p.wrap("Stat", fmt.Errorf("onedrive: no such file or folder: %s", ref))
	}
	p.mu.Lock()
	p.idMap[ref] = it.ID
	p.mu.Unlock()
	return it.ID, nil
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

// getItem fetches one item's metadata by id.
func (p *Provider) getItem(ctx context.Context, op, id string) (driveItem, error) {
	u := p.apiBase + "/me/drive/items/" + url.PathEscape(id) +
		"?$select=" + url.QueryEscape(itemFields)
	var it driveItem
	if err := p.doJSON(ctx, op, http.MethodGet, u, nil, &it); err != nil {
		return driveItem{}, err
	}
	return it, nil
}

// listChildren returns the direct children of a folder id,
// consuming @odata.nextLink pagination inside.
func (p *Provider) listChildren(ctx context.Context, parentID string) ([]driveItem, error) {
	var out []driveItem
	u := p.apiBase + "/me/drive/items/" + url.PathEscape(parentID) + "/children" +
		"?$select=" + url.QueryEscape(itemFields) + "&$top=200"
	for u != "" {
		var resp struct {
			Value    []driveItem `json:"value"`
			NextLink string      `json:"@odata.nextLink"`
		}
		if err := p.doJSON(ctx, "List", http.MethodGet, u, nil, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Value...)
		u = resp.NextLink
	}
	return out, nil
}

// --- Provider ---

// List returns direct children of ref. Pagination is consumed inside.
func (p *Provider) List(ctx context.Context, ref string) ([]domain.Node, error) {
	if err := checkPath(ref); err != nil {
		return nil, p.wrap("List", err)
	}
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
	for _, it := range children {
		childRef := it.Name
		if parentRef != "" {
			childRef = parentRef + "/" + it.Name
		}
		out = append(out, it.node(childRef))
		p.mu.Lock()
		p.idMap[cleanRef(childRef)] = it.ID
		p.mu.Unlock()
	}
	return out, nil
}

// Stat returns metadata for one node.
func (p *Provider) Stat(ctx context.Context, ref string) (domain.Node, error) {
	if err := checkPath(ref); err != nil {
		return domain.Node{}, p.wrap("Stat", err)
	}
	if strings.Trim(ref, "/") == "" {
		return domain.Node{Ref: "", Path: "/", IsDir: true, Size: -1}, nil
	}
	id, err := p.resolve(ctx, ref)
	if err != nil {
		return domain.Node{}, err
	}
	it, err := p.getItem(ctx, "Stat", id)
	if err != nil {
		return domain.Node{}, err
	}
	return it.node(ref), nil
}

// downloadURL fetches one item's @microsoft.graph.downloadUrl —
// a pre-authenticated short-lived URL. Shared by OpenRead (which
// GETs it) and DirectURL (which hands it to the webview). An empty
// URL is not an error here: the caller falls back to GET
// .../content (OpenRead) or to the loopback proxy (DirectURL).
func (p *Provider) downloadURL(ctx context.Context, op, ref string) (string, error) {
	if err := checkPath(ref); err != nil {
		return "", p.wrap(op, err)
	}
	id, err := p.resolve(ctx, ref)
	if err != nil {
		return "", err
	}
	u := p.apiBase + "/me/drive/items/" + url.PathEscape(id) +
		"?$select=" + url.QueryEscape(itemFields+",@microsoft.graph.downloadUrl")
	var it driveItem
	if err := p.doJSON(ctx, op, http.MethodGet, u, nil, &it); err != nil {
		return "", err
	}
	if it.Folder != nil {
		return "", p.wrap(op, fmt.Errorf("onedrive: %s is a folder, not a file", ref))
	}
	return it.DownloadURL, nil
}

// DirectURL implements storage.DirectURLer: the item's
// @microsoft.graph.downloadUrl, handed to the webview for in-app
// preview. Empty (the item carries none) is an error — the caller
// falls back to the loopback preview proxy, which streams OpenRead.
func (p *Provider) DirectURL(ctx context.Context, ref string) (string, error) {
	u, err := p.downloadURL(ctx, "DirectURL", ref)
	if err != nil {
		return "", err
	}
	if u == "" {
		return "", p.wrap("DirectURL", fmt.Errorf("onedrive: no download URL for %q", ref))
	}
	return u, nil
}

// OpenRead streams a file for download. The item's
// @microsoft.graph.downloadUrl is a pre-authenticated short-lived
// URL: it is GET directly with a plain client (no bearer token,
// no Graph round-trip). Falls back to GET .../content when the
// item carries no download URL.
func (p *Provider) OpenRead(ctx context.Context, ref string) (io.ReadCloser, error) {
	u, err := p.downloadURL(ctx, "OpenRead", ref)
	if err != nil {
		return nil, err
	}
	if u != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, p.wrap("OpenRead", err)
		}
		resp, err := p.plain.Do(req)
		if err != nil {
			return nil, p.wrap("OpenRead", err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			defer resp.Body.Close()
			return nil, p.wrap("OpenRead", fmt.Errorf("onedrive: download URL returned %s", resp.Status))
		}
		return resp.Body, nil
	}
	id, err := p.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	req, err := p.newRequest(ctx, http.MethodGet, p.apiBase+"/me/drive/items/"+url.PathEscape(id)+"/content", nil)
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

// Upload writes a file. Up to 4 MiB it is one PUT /content; larger
// (or unknown-size, once spooled) it is an upload session with
// 5 MiB chunks. Unknown size is buffered up to 4 MiB in memory;
// beyond that it is spooled to a temp file so the session gets an
// exact total.
func (p *Provider) Upload(ctx context.Context, ref string, r io.Reader, size int64) (domain.Node, error) {
	if err := checkPath(ref); err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	parentRef, name := splitRef(ref)
	if name == "" {
		return domain.Node{}, p.wrap("Upload", fmt.Errorf("onedrive: cannot upload to %q: no file name", ref))
	}
	parentID, err := p.resolve(ctx, parentRef)
	if err != nil {
		return domain.Node{}, err
	}
	if size >= 0 && size <= simpleUploadLimit {
		data, err := io.ReadAll(io.LimitReader(r, simpleUploadLimit+1))
		if err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		return p.simpleUpload(ctx, ref, parentID, name, data)
	}
	if size < 0 {
		data, err := io.ReadAll(io.LimitReader(r, simpleUploadLimit+1))
		if err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		if int64(len(data)) <= simpleUploadLimit {
			return p.simpleUpload(ctx, ref, parentID, name, data)
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
		return p.sessionUpload(ctx, ref, parentID, name, f, st.Size())
	}
	return p.sessionUpload(ctx, ref, parentID, name, r, size)
}

// spool writes the already-buffered head plus the rest of r to a
// temp file and returns its path.
func (p *Provider) spool(r io.Reader, head []byte) (string, error) {
	f, err := os.CreateTemp("", "zy-drive-onedrive-upload-*")
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

// simpleUpload is one PUT /content (files up to 4 MiB). A PUT to a
// path address creates the file or replaces an existing file of
// the same name.
func (p *Provider) simpleUpload(ctx context.Context, ref, parentID, name string, data []byte) (domain.Node, error) {
	u := p.apiBase + "/me/drive/items/" + url.PathEscape(parentID) + ":/" +
		url.PathEscape(name) + ":/content"
	req, err := p.newRequest(ctx, http.MethodPut, u, bytes.NewReader(data))
	if err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	req.Header.Set("Content-Type", mimeOf(name))
	resp, err := p.client.Do(ctx, req)
	if err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return domain.Node{}, p.wrap("Upload", p.readError(resp))
	}
	var it driveItem
	if err := json.NewDecoder(resp.Body).Decode(&it); err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	if it.Size != int64(len(data)) {
		return domain.Node{}, p.wrap("Upload", fmt.Errorf(
			"onedrive: size mismatch after upload: OneDrive says %d, local is %d",
			it.Size, len(data)))
	}
	p.mu.Lock()
	p.idMap[cleanRef(ref)] = it.ID
	p.mu.Unlock()
	return it.node(ref), nil
}

// sessionUpload runs the createUploadSession -> PUT chunks flow
// for files over 4 MiB. See sessionUploadAttempt for the 409
// recovery: a chunk PUT that conflicts is verified and resumed,
// never reported as a failure for a file that is already there.
func (p *Provider) sessionUpload(ctx context.Context, ref, parentID, name string, r io.Reader, size int64) (domain.Node, error) {
	return p.sessionUploadAttempt(ctx, ref, parentID, name, r, size, 0)
}

// sessionUploadAttempt runs one upload session: create it, then PUT
// 5 MiB chunks (a multiple of the 320 KiB alignment Graph
// requires); the final chunk returns the driveItem. A chunk PUT
// carries no Authorization header: the uploadUrl is already
// pre-authenticated, and sending a bearer token with it can 401.
// This driver keeps no resume state across restarts: a failed
// session is abandoned server-side (it expires on its own).
//
// A 409 resourceModified on a chunk PUT is NOT a final failure.
// The usual cause is a retried chunk: putChunk retries a chunk
// whose first attempt landed but whose response was lost, so the
// second PUT's range no longer matches the session — the chunk,
// or the whole file, is already on the server. Recovery, in
// order (the same re-check pattern as Mkdir's 409 handling):
//
//  1. verifyUploadComplete: if the target already holds the whole
//     file (size, and SHA-1 when the server carries one), the
//     upload is a success.
//  2. queryUploadSession: ask the session for nextExpectedRanges
//     and resume from the offset the server expects, with the
//     SAME session — re-sending only the unwritten tail of the
//     chunk that conflicted, never blind-retrying the same PUT.
//  3. If the session is dead (GET 404/410), restartSessionOrFail
//     starts a fresh session from the beginning, but only when the
//     source can be re-read; a forward-only reader fails with a
//     clear error instead of a false success or a loop.
//
// Recovery is capped (maxChunkConflicts per session, at most
// maxSessionRestarts fresh sessions), so a persistently conflicting
// upload always terminates with an error.
func (p *Provider) sessionUploadAttempt(ctx context.Context, ref, parentID, name string, r io.Reader, size int64, restarts int) (domain.Node, error) {
	body := map[string]any{
		"item": map[string]any{
			"@microsoft.graph.conflictBehavior": "replace",
			"name":                              name,
		},
	}
	u := p.apiBase + "/me/drive/items/" + url.PathEscape(parentID) + ":/" +
		url.PathEscape(name) + ":/createUploadSession"
	var session struct {
		UploadURL string `json:"uploadUrl"`
	}
	if err := p.doJSON(ctx, "Upload", http.MethodPost, u, body, &session); err != nil {
		return domain.Node{}, err
	}
	if session.UploadURL == "" {
		return domain.Node{}, p.wrap("Upload", fmt.Errorf("onedrive: createUploadSession returned no uploadUrl"))
	}
	buf := make([]byte, chunkSize)
	hash := sha1.New()
	var offset, bytesRead int64
	var conflicts int
	var pending []byte // unwritten tail of an already-read chunk
	var pendingLast bool
	for {
		var chunk []byte
		var chunkOffset int64
		var last bool
		var readErr error
		if pending != nil {
			chunk, chunkOffset, last = pending, offset, pendingLast
			pending = nil
		} else {
			n, rerr := io.ReadFull(r, buf)
			readErr = rerr
			if n > 0 {
				hash.Write(buf[:n])
				bytesRead += int64(n)
				chunk = buf[:n]
				chunkOffset = offset
				last = readErr == io.EOF || readErr == io.ErrUnexpectedEOF || offset+int64(n) >= size
			} else if readErr == io.EOF {
				if offset == 0 {
					// Empty file through the session path: one
					// zero-length final chunk.
					chunk, chunkOffset, last = []byte{}, 0, true
				} else {
					return domain.Node{}, p.wrap("Upload", fmt.Errorf("onedrive: upload session ended without completion"))
				}
			} else if readErr != nil {
				return domain.Node{}, p.wrap("Upload", readErr)
			} else {
				// io.ReadFull with a non-empty buffer never
				// returns (0, nil); treat it as a broken source
				// rather than spinning.
				return domain.Node{}, p.wrap("Upload", fmt.Errorf("onedrive: upload source returned no data without an error"))
			}
		}
		it, done, err := p.putChunk(ctx, session.UploadURL, chunkOffset, chunk, size, last)
		if err != nil {
			var conflict *chunkConflictError
			var gone *chunkSessionGoneError
			isConflict := errors.As(err, &conflict)
			isGone := !isConflict && errors.As(err, &gone)
			if !isConflict && !isGone {
				return domain.Node{}, err
			}
			conflicts++
			if conflicts > maxChunkConflicts {
				return p.restartSessionOrFail(ctx, ref, parentID, name, r, size, restarts,
					fmt.Errorf("onedrive: chunk upload at offset %d failed repeatedly (%d recoveries spent)", chunkOffset, maxChunkConflicts))
			}
			// 1. The file may already be complete on the server:
			// a retried chunk PUT can conflict after its first
			// attempt landed and completed the file but the
			// response was lost. Verify before anything else.
			if node, ok := p.verifyUploadComplete(ctx, ref, size, hex.EncodeToString(hash.Sum(nil)), bytesRead); ok {
				if id := node.Extra["onedriveId"]; id != "" {
					p.mu.Lock()
					p.idMap[cleanRef(ref)] = id
					p.mu.Unlock()
				}
				return node, nil
			}
			// A 404/410 from the chunk PUT already says the
			// session is gone: no status query can revive it.
			if isGone {
				return p.restartSessionOrFail(ctx, ref, parentID, name, r, size, restarts,
					fmt.Errorf("onedrive: upload session is gone (chunk PUT returned 404/410)"))
			}
			// 2. Ask the live session where it stands and resume
			// from there with the SAME session.
			expected, dead, qerr := p.queryUploadSession(ctx, session.UploadURL)
			if qerr != nil {
				return domain.Node{}, qerr
			}
			if dead {
				return p.restartSessionOrFail(ctx, ref, parentID, name, r, size, restarts,
					fmt.Errorf("onedrive: upload session expired during upload"))
			}
			end := chunkOffset + int64(len(chunk))
			switch {
			case expected == end:
				// The whole chunk already landed; only its
				// response was lost. Carry on with the next
				// chunk — the bytes are already consumed from
				// the reader.
				offset = expected
			case expected > chunkOffset && expected < end:
				// Part of the chunk landed; re-send only the
				// unwritten tail, at the offset the server
				// expects. The bytes are still in buf.
				pending = chunk[expected-chunkOffset:]
				pendingLast = last
				offset = expected
			case expected == chunkOffset:
				// Nothing of this chunk landed; the server still
				// expects exactly this range, so re-send it
				// once with the corrected expectation. A repeat
				// conflict spends another recovery from the
				// capped budget above, so this cannot loop.
				pending = chunk
				pendingLast = last
			default:
				return domain.Node{}, p.wrap("Upload", fmt.Errorf(
					"onedrive: upload session expects offset %d, but the upload is at %d: cannot resume", expected, chunkOffset))
			}
			continue
		}
		if done {
			if it.Size != size {
				return domain.Node{}, p.wrap("Upload", fmt.Errorf(
					"onedrive: size mismatch after upload: OneDrive says %d, local is %d",
					it.Size, size))
			}
			p.mu.Lock()
			p.idMap[cleanRef(ref)] = it.ID
			p.mu.Unlock()
			return it.node(ref), nil
		}
		if len(chunk) == 0 {
			// The empty file's single zero-length chunk was
			// accepted without completing the upload: the
			// session cannot finish this file.
			return domain.Node{}, p.wrap("Upload", fmt.Errorf("onedrive: upload session ended without completion"))
		}
		offset = chunkOffset + int64(len(chunk))
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return domain.Node{}, p.wrap("Upload", readErr)
		}
	}
}

// verifyUploadComplete checks whether the target already holds the
// complete file, for the 409 recovery path. It only reports success
// when the whole file was read (bytesRead == size, so hashHex is
// the SHA-1 of the complete file) and the server item has the
// expected size — and, when the server carries a SHA-1, the same
// hash. A size-only match against an earlier, different file of
// the same name must never pass as success. Any lookup failure
// means "not verified", never an error: the caller falls through
// to the session resume.
func (p *Provider) verifyUploadComplete(ctx context.Context, ref string, size int64, hashHex string, bytesRead int64) (domain.Node, bool) {
	if bytesRead != size {
		return domain.Node{}, false
	}
	node, err := p.Stat(ctx, ref)
	if err != nil {
		return domain.Node{}, false
	}
	if node.IsDir || node.Size != size {
		return domain.Node{}, false
	}
	if node.HashKind == "sha1" && node.Hash != "" && node.Hash != hashHex {
		return domain.Node{}, false
	}
	return node, true
}

// queryUploadSession asks a live upload session where it stands. It
// GETs the pre-authenticated uploadUrl (no Authorization header,
// like the chunk PUTs) and parses nextExpectedRanges. dead is true
// when the session is gone (404/410): the caller must start a fresh
// session instead of resuming this one.
func (p *Provider) queryUploadSession(ctx context.Context, uploadURL string) (expected int64, dead bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uploadURL, nil)
	if err != nil {
		return 0, false, p.wrap("Upload", err)
	}
	resp, err := p.plain.Do(req)
	if err != nil {
		return 0, false, p.wrap("Upload", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return 0, true, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, false, p.wrap("Upload", p.readError(resp))
	}
	var status struct {
		NextExpectedRanges []string `json:"nextExpectedRanges"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return 0, false, p.wrap("Upload", err)
	}
	if len(status.NextExpectedRanges) == 0 {
		return 0, false, p.wrap("Upload", fmt.Errorf("onedrive: upload session returned no nextExpectedRanges"))
	}
	// Ranges look like "5242880-" or "0-1048575"; the first range's
	// start is where the server wants the upload to continue.
	first := status.NextExpectedRanges[0]
	startStr, _, _ := strings.Cut(first, "-")
	expected, err = strconv.ParseInt(strings.TrimSpace(startStr), 10, 64)
	if err != nil || expected < 0 {
		return 0, false, p.wrap("Upload", fmt.Errorf("onedrive: upload session returned an unparsable nextExpectedRanges %q", first))
	}
	return expected, false, nil
}

// restartSessionOrFail starts the upload over with a fresh session
// when the current one can no longer be resumed — but only when the
// source can be re-read from the beginning. A forward-only reader
// that is already part-consumed cannot restart: failing clearly
// beats silently reporting success or looping forever. At most
// maxSessionRestarts fresh sessions are started per Upload.
func (p *Provider) restartSessionOrFail(ctx context.Context, ref, parentID, name string, r io.Reader, size int64, restarts int, cause error) (domain.Node, error) {
	if restarts >= maxSessionRestarts {
		return domain.Node{}, p.wrap("Upload", fmt.Errorf("%w: no fresh-session restart is left", cause))
	}
	rs, ok := r.(io.Seeker)
	if !ok {
		return domain.Node{}, p.wrap("Upload", fmt.Errorf("%w: upload cannot resume from a non-seekable source (the reader cannot be re-read from the beginning), please retry the upload", cause))
	}
	if _, err := rs.Seek(0, io.SeekStart); err != nil {
		return domain.Node{}, p.wrap("Upload", fmt.Errorf("%w: upload cannot resume: seeking the source back to the beginning failed: %v", cause, err))
	}
	return p.sessionUploadAttempt(ctx, ref, parentID, name, r, size, restarts+1)
}

// putChunk PUTs one fragment to the session uploadUrl. A 202 means
// "accepted, send the next range"; 200/201 carries the finished
// driveItem. Retries (up to chunkAttempts) honour Retry-After when
// present. No Authorization header is ever sent here.
//
// A 409 resourceModified is NOT retried and NOT a plain failure:
// it is returned as a *chunkConflictError so sessionUpload can
// verify the target and resume from the server's nextExpectedRanges
// instead of blind-retrying the same chunk. A 404/410 (the session
// is gone) is likewise returned typed, as *chunkSessionGoneError.
func (p *Provider) putChunk(ctx context.Context, uploadURL string, offset int64, chunk []byte, total int64, last bool) (driveItem, bool, error) {
	var lastErr error
	for attempt := 0; attempt < chunkAttempts; attempt++ {
		if attempt > 0 {
			wait := time.Second << uint(attempt-1)
			if wait > 30*time.Second {
				wait = 30 * time.Second
			}
			select {
			case <-ctx.Done():
				return driveItem{}, false, p.wrap("Upload", ctx.Err())
			case <-time.After(wait):
			}
		}
		end := offset + int64(len(chunk)) - 1
		totalStr := strconv.FormatInt(total, 10)
		if total < 0 {
			totalStr = strconv.FormatInt(offset+int64(len(chunk)), 10)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(chunk))
		if err != nil {
			return driveItem{}, false, p.wrap("Upload", err)
		}
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%s", offset, end, totalStr))
		req.Header.Set("Content-Type", "application/octet-stream")
		req.ContentLength = int64(len(chunk))
		resp, err := p.plain.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		switch resp.StatusCode {
		case http.StatusOK, http.StatusCreated:
			var it driveItem
			err := json.NewDecoder(resp.Body).Decode(&it)
			resp.Body.Close()
			if err != nil {
				return driveItem{}, false, p.wrap("Upload", err)
			}
			return it, true, nil
		case http.StatusAccepted:
			// Chunk accepted; the body names the next expected
			// ranges. We track the offset ourselves, so it is
			// only drained here.
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return driveItem{}, false, nil
		default:
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			lastErr = fmt.Errorf("onedrive: chunk PUT returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
			// 429/503: honour Retry-After, then retry the chunk.
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
				if ra := resp.Header.Get("Retry-After"); ra != "" {
					if secs, err := strconv.Atoi(ra); err == nil && secs > 0 && secs <= 300 {
						select {
						case <-ctx.Done():
							return driveItem{}, false, p.wrap("Upload", ctx.Err())
						case <-time.After(time.Duration(secs) * time.Second):
						}
						continue
					}
				}
				continue
			}
			// 409 resourceModified: the chunk's range no longer
			// matches what the session expects — the chunk may
			// already be written (a retried PUT whose first
			// attempt landed but whose response was lost) or the
			// session's bound item changed. Never blind-retry the
			// same chunk: return a typed conflict so
			// sessionUpload can verify the target and resume from
			// the server's nextExpectedRanges instead.
			if resp.StatusCode == http.StatusConflict && graphErrorCode(data) == "resourceModified" {
				return driveItem{}, false, p.wrap("Upload", &chunkConflictError{msg: lastErr.Error()})
			}
			// 404/410: the session is gone (it completed and was
			// deleted, or it expired). The file may already be
			// complete, so this is typed too — sessionUpload
			// verifies the target before restarting.
			if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
				return driveItem{}, false, p.wrap("Upload", &chunkSessionGoneError{msg: lastErr.Error()})
			}
			// Any other status is final: retrying the same chunk
			// cannot fix it.
			return driveItem{}, false, p.wrap("Upload", lastErr)
		}
	}
	return driveItem{}, false, p.wrap("Upload", fmt.Errorf("onedrive: chunk at offset %d failed after %d attempts: %w", offset, chunkAttempts, lastErr))
}

// chunkConflictError is returned by putChunk when the server
// answers a chunk PUT with 409 resourceModified: the chunk's range
// no longer matches the session's expected ranges. sessionUpload
// recovers from it (verify the target, then resume from the
// server's nextExpectedRanges); it is never a blind retry.
type chunkConflictError struct{ msg string }

func (e *chunkConflictError) Error() string { return e.msg }

// chunkSessionGoneError is returned by putChunk when the server
// answers a chunk PUT with 404/410: the upload session no longer
// exists. sessionUpload recovers from it (verify the target, then
// restart with a fresh session when the source can be re-read).
type chunkSessionGoneError struct{ msg string }

func (e *chunkSessionGoneError) Error() string { return e.msg }

// graphErrorCode pulls the error.code out of a Graph error body
// ({"error":{"code":...}}); "" when the body carries no code.
func graphErrorCode(data []byte) string {
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return ""
	}
	return body.Error.Code
}

// Mkdir creates a folder. A folder that already exists is success:
// a 409 re-checks with resolve, so a real failure (no permission,
// bad parent) still surfaces.
func (p *Provider) Mkdir(ctx context.Context, ref string) error {
	if err := checkPath(ref); err != nil {
		return p.wrap("Mkdir", err)
	}
	parentRef, name := splitRef(ref)
	if name == "" {
		return p.wrap("Mkdir", fmt.Errorf("onedrive: cannot create folder %q: no name", ref))
	}
	parentID, err := p.resolve(ctx, parentRef)
	if err != nil {
		return err
	}
	body := map[string]any{
		"name":                              name,
		"folder":                            map[string]any{},
		"@microsoft.graph.conflictBehavior": "fail",
	}
	u := p.apiBase + "/me/drive/items/" + url.PathEscape(parentID) + "/children"
	var it driveItem
	if err := p.doJSON(ctx, "Mkdir", http.MethodPost, u, body, &it); err != nil {
		if strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "nameAlreadyExists") {
			if id, rerr := p.resolve(ctx, ref); rerr == nil {
				p.mu.Lock()
				p.idMap[cleanRef(ref)] = id
				p.mu.Unlock()
				return nil
			}
		}
		return err
	}
	p.mu.Lock()
	p.idMap[cleanRef(ref)] = it.ID
	p.mu.Unlock()
	return nil
}

// Rename moves/renames within the same provider. One PATCH carries
// the new name and/or the new parent id.
func (p *Provider) Rename(ctx context.Context, src, dst string) error {
	if err := checkPath(dst); err != nil {
		return p.wrap("Rename", err)
	}
	id, err := p.resolve(ctx, src)
	if err != nil {
		return err
	}
	srcParentRef, _ := splitRef(src)
	dstParentRef, dstName := splitRef(dst)
	if dstName == "" {
		return p.wrap("Rename", fmt.Errorf("onedrive: cannot rename to %q: no file name", dst))
	}
	srcParentID, err := p.resolve(ctx, srcParentRef)
	if err != nil {
		return err
	}
	dstParentID, err := p.resolve(ctx, dstParentRef)
	if err != nil {
		return err
	}
	body := map[string]any{"name": dstName}
	if srcParentID != dstParentID {
		body["parentReference"] = map[string]any{"id": dstParentID}
	}
	u := p.apiBase + "/me/drive/items/" + url.PathEscape(id)
	var it driveItem
	if err := p.doJSON(ctx, "Rename", http.MethodPatch, u, body, &it); err != nil {
		return err
	}
	p.invalidate(src, dst)
	return nil
}

// Delete removes one node. OneDrive moves it to the recycle bin
// (DeleteToTrash is true), it is not permanently deleted.
func (p *Provider) Delete(ctx context.Context, ref string) error {
	id, err := p.resolve(ctx, ref)
	if err != nil {
		return err
	}
	req, err := p.newRequest(ctx, http.MethodDelete, p.apiBase+"/me/drive/items/"+url.PathEscape(id), nil)
	if err != nil {
		return p.wrap("Delete", err)
	}
	resp, err := p.client.Do(ctx, req)
	if err != nil {
		return p.wrap("Delete", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
		return p.wrap("Delete", p.readError(resp))
	}
	p.invalidate(ref)
	return nil
}

// --- optional interfaces ---

// Search implements storage.Searcher via /me/drive/root/search.
// Display paths come from parentReference.path, so results are
// operable refs.
func (p *Provider) Search(ctx context.Context, query string) ([]domain.Node, error) {
	q := strings.ReplaceAll(query, "'", "''")
	u := p.apiBase + "/me/drive/root/search(q='" + url.QueryEscape(q) + "')" +
		"?$select=" + url.QueryEscape(itemFields) + "&$top=100"
	var out []domain.Node
	for u != "" {
		var resp struct {
			Value    []driveItem `json:"value"`
			NextLink string      `json:"@odata.nextLink"`
		}
		if err := p.doJSON(ctx, "Search", http.MethodGet, u, nil, &resp); err != nil {
			return nil, err
		}
		for _, it := range resp.Value {
			out = append(out, it.node(displayPathOf(it)))
			p.mu.Lock()
			p.idMap[cleanRef(displayPathOf(it))] = it.ID
			p.mu.Unlock()
		}
		u = resp.NextLink
	}
	return out, nil
}

// Delta implements storage.DeltaSource via /me/drive/root/delta.
// The cursor IS the @odata.deltaLink URL from the previous call:
// an empty cursor asks for token=latest (no history replay, just
// the current token). Deleted items carry the "deleted" facet and
// are marked Extra["removed"]="true".
func (p *Provider) Delta(ctx context.Context, cursor string) ([]domain.Node, string, error) {
	var u string
	if cursor == "" {
		u = p.apiBase + "/me/drive/root/delta?token=latest"
		var resp struct {
			DeltaLink string `json:"@odata.deltaLink"`
		}
		if err := p.doJSON(ctx, "Delta", http.MethodGet, u, nil, &resp); err != nil {
			return nil, "", err
		}
		if resp.DeltaLink == "" {
			return nil, "", p.wrap("Delta", fmt.Errorf("onedrive: delta returned no deltaLink"))
		}
		return nil, resp.DeltaLink, nil
	}
	u = cursor
	var out []domain.Node
	for {
		var resp struct {
			Value     []driveItem `json:"value"`
			NextLink  string      `json:"@odata.nextLink"`
			DeltaLink string      `json:"@odata.deltaLink"`
		}
		if err := p.doJSON(ctx, "Delta", http.MethodGet, u, nil, &resp); err != nil {
			return nil, "", err
		}
		for _, it := range resp.Value {
			// The root item itself (no parent path, folder facet
			// with a "root" marker) is not a change to report.
			if it.ParentReference == nil && it.Folder != nil && it.Name == "root" {
				continue
			}
			out = append(out, it.node(displayPathOf(it)))
		}
		if resp.DeltaLink != "" {
			return out, resp.DeltaLink, nil
		}
		if resp.NextLink == "" {
			return nil, "", p.wrap("Delta", fmt.Errorf("onedrive: delta ended with neither nextLink nor deltaLink"))
		}
		u = resp.NextLink
	}
}

// Share implements storage.Sharer via createLink: a view-only
// anyone link (no sign-in required), returning link.webUrl.
func (p *Provider) Share(ctx context.Context, ref string) (string, error) {
	id, err := p.resolve(ctx, ref)
	if err != nil {
		return "", err
	}
	var out struct {
		Link struct {
			WebURL string `json:"webUrl"`
		} `json:"link"`
	}
	u := p.apiBase + "/me/drive/items/" + url.PathEscape(id) + "/createLink"
	if err := p.doJSON(ctx, "Share", http.MethodPost, u,
		map[string]any{"type": "view", "scope": "anonymous"}, &out); err != nil {
		return "", err
	}
	if out.Link.WebURL == "" {
		return "", p.wrap("Share", fmt.Errorf("onedrive: share link created but no webUrl returned"))
	}
	return out.Link.WebURL, nil
}

// Versions implements storage.Versioner via the versions
// collection; Restore posts restoreVersion for one version id.
func (p *Provider) Versions(ctx context.Context, ref string) ([]domain.Node, error) {
	id, err := p.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	u := p.apiBase + "/me/drive/items/" + url.PathEscape(id) + "/versions" +
		"?$select=" + url.QueryEscape("id,lastModifiedDateTime,size")
	var resp struct {
		Value []struct {
			ID                   string `json:"id"`
			LastModifiedDateTime string `json:"lastModifiedDateTime"`
			Size                 int64  `json:"size"`
		} `json:"value"`
	}
	if err := p.doJSON(ctx, "Versions", http.MethodGet, u, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]domain.Node, 0, len(resp.Value))
	for _, v := range resp.Value {
		n := domain.Node{
			Ref:   strings.Trim(ref, "/"),
			Path:  "/" + strings.Trim(ref, "/"),
			Name:  path.Base(strings.Trim(ref, "/")),
			Size:  v.Size,
			Extra: map[string]string{"versionId": v.ID},
		}
		if t, err := time.Parse(time.RFC3339, v.LastModifiedDateTime); err == nil {
			n.ModTime = t
		}
		out = append(out, n)
	}
	return out, nil
}

func (p *Provider) Restore(ctx context.Context, ref, versionID string) error {
	id, err := p.resolve(ctx, ref)
	if err != nil {
		return err
	}
	u := p.apiBase + "/me/drive/items/" + url.PathEscape(id) +
		"/versions/" + url.PathEscape(versionID) + "/restoreVersion"
	if err := p.doJSON(ctx, "Restore", http.MethodPost, u, nil, nil); err != nil {
		return err
	}
	p.invalidate(ref)
	return nil
}

// Thumbnail implements storage.Thumber via the thumbnails
// collection: large first, then medium, then small.
func (p *Provider) Thumbnail(ctx context.Context, ref string) (string, error) {
	id, err := p.resolve(ctx, ref)
	if err != nil {
		return "", err
	}
	u := p.apiBase + "/me/drive/items/" + url.PathEscape(id) + "/thumbnails"
	var resp struct {
		Value []struct {
			Large struct {
				URL string `json:"url"`
			} `json:"large"`
			Medium struct {
				URL string `json:"url"`
			} `json:"medium"`
			Small struct {
				URL string `json:"url"`
			} `json:"small"`
		} `json:"value"`
	}
	if err := p.doJSON(ctx, "Thumbnail", http.MethodGet, u, nil, &resp); err != nil {
		return "", err
	}
	if len(resp.Value) == 0 {
		return "", p.wrap("Thumbnail", fmt.Errorf("onedrive: %s has no thumbnail", ref))
	}
	th := resp.Value[0]
	switch {
	case th.Large.URL != "":
		return th.Large.URL, nil
	case th.Medium.URL != "":
		return th.Medium.URL, nil
	case th.Small.URL != "":
		return th.Small.URL, nil
	default:
		return "", p.wrap("Thumbnail", fmt.Errorf("onedrive: %s has no thumbnail", ref))
	}
}

// Quota implements storage.QuotaSource via GET /me/drive
// ($select=quota only): {"quota": {"used": <bytes>, "total":
// <bytes>, "remaining": <bytes>}}. A zero total is returned as
// -1: no total is invented.
func (p *Provider) Quota(ctx context.Context) (used, total int64, err error) {
	var resp struct {
		Quota struct {
			Used  int64 `json:"used"`
			Total int64 `json:"total"`
		} `json:"quota"`
	}
	if err := p.doJSON(ctx, "Quota", http.MethodGet, p.apiBase+"/me/drive?$select=quota", nil, &resp); err != nil {
		return 0, 0, err
	}
	total = resp.Quota.Total
	if total <= 0 {
		total = -1
	}
	return resp.Quota.Used, total, nil
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
	_ storage.DeltaSource = (*Provider)(nil)
	_ storage.Sharer      = (*Provider)(nil)
	_ storage.Versioner   = (*Provider)(nil)
	_ storage.Thumber     = (*Provider)(nil)
	_ storage.QuotaSource = (*Provider)(nil)
)
