// SPDX-License-Identifier: MPL-2.0
// Package dropbox implements storage.Provider for Dropbox using the
// official Dropbox API v2 over plain HTTP (no third-party SDK).
//
// Endpoints (api.dropboxapi.com for RPC, content.dropboxapi.com for
// upload/download). Auth is Bearer + OAuth2 refresh via
// internal/storage/oauth (single-flight TokenSource, refresh once on
// 401 via the shared http client hook).
//   - List: files/list_folder + files/list_folder/continue (cursor).
//   - Upload: single call files/upload up to 150 MiB; above that (or
//     unknown size) upload_session/start + append_v2 + finish, 4 MiB
//     chunks (Caps.MinPartSize).
//   - Hash: content_hash (SHA-256 based); never compared with S3 ETags
//     across clouds.
//   - Paths are case-insensitive for identity but case-preserving for
//     display; refs are stored without a leading slash, "" is the root.
package dropbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
	shttp "github.com/luckzk/zy-drive/internal/storage/http"
	"github.com/luckzk/zy-drive/internal/storage/oauth"
)

const (
	defaultAPIBase     = "https://api.dropboxapi.com"
	defaultContentBase = "https://content.dropboxapi.com"
	// singleUploadLimit is Dropbox's single-call upload limit (150 MiB).
	singleUploadLimit = 150 << 20
	chunkSize         = 4 << 20
)

// Provider is a Dropbox-backed storage.Provider. One instance per
// Mount (never a per-type singleton): each account has its own token,
// its own TokenSource and its own http client, so two Dropbox accounts
// can never share state.
type Provider struct {
	apiBase     string
	contentBase string
	tokens      *oauth.TokenSource
	client      *shttp.Client
}

// New builds a Dropbox provider. cfg.Token is the OAuth access token
// (memory only). cfg.Extra may carry refresh_token / client_id /
// client_secret for refresh; without them an expired token is fatal
// and the user must re-authorise. cfg.Extra["api_base"] /
// ["content_base"] override the API endpoints (tests and
// self-hosted/proxied deployments; normally empty).
func New(cfg storage.Config) (storage.Provider, error) {
	apiBase, contentBase := defaultAPIBase, defaultContentBase
	if v := cfg.Extra["api_base"]; v != "" {
		apiBase = v
	}
	if v := cfg.Extra["content_base"]; v != "" {
		contentBase = v
	}
	return newWithBases(cfg, apiBase, contentBase)
}

func newWithBases(cfg storage.Config, apiBase, contentBase string) (*Provider, error) {
	if cfg.Token == "" && cfg.Extra["refresh_token"] == "" {
		return nil, fmt.Errorf("dropbox: access token or refresh token is required")
	}
	var refresher oauth.Refresher
	if cfg.Extra["refresh_token"] != "" {
		tokenURL := cfg.Extra["token_url"]
		if tokenURL == "" {
			tokenURL = "https://api.dropboxapi.com/oauth2/token"
		}
		refresher = oauth.FormRefresherWithProxy(tokenURL,
			cfg.Extra["client_id"], cfg.Extra["client_secret"], cfg.Proxy)
	}
	ts := oauth.NewTokenSource(&oauth.Token{
		AccessToken:  cfg.Token,
		RefreshToken: cfg.Extra["refresh_token"],
	}, refresher)
	p := &Provider{apiBase: strings.TrimSuffix(apiBase, "/"), contentBase: strings.TrimSuffix(contentBase, "/"), tokens: ts}
	p.client = shttp.NewWithProxy(nil, cfg.Proxy)
	p.client.OnAuthExpired = func(ctx context.Context) error {
		_, err := ts.Refresh(ctx)
		return err
	}
	return p, nil
}

// Caps declares Dropbox capabilities.
func (p *Provider) Caps() domain.Caps {
	return domain.Caps{
		CanMkdir: true, CanRename: true, CanDelete: true, DeleteToTrash: true,
		SupportsServerSideCopy:  true,
		SupportsVersions:        true,
		SupportsShare:           true,
		SupportsSearch:          true,
		SupportsDelta:           true, // list_folder cursor + longpoll
		SupportsThumbnail:       true,
		SupportsResumableUpload: true,
		SupportsEncryption:      false,
		SupportsStorageClass:    false,
		SupportsPresignedURL:    false,   // Dropbox uses temporary links instead
		IDBased:                 false,   // path-based
		MaxFileSize:             2 << 40, // 2 TB via upload sessions
		MinPartSize:             chunkSize,
		MaxPathLen:              0, // no documented hard limit
	}
}

// --- wire types ---

type dbxEntry struct {
	Tag            string `json:".tag"`
	Name           string `json:"name"`
	PathDisplay    string `json:"path_display"`
	PathLower      string `json:"path_lower"`
	ID             string `json:"id"`
	Rev            string `json:"rev"`
	Size           int64  `json:"size"`
	ClientModified string `json:"client_modified"`
	ServerModified string `json:"server_modified"`
	ContentHash    string `json:"content_hash"`
	IsDownloadable bool   `json:"is_downloadable"`
}

func (e dbxEntry) node() domain.Node {
	ref := strings.Trim(e.PathDisplay, "/")
	n := domain.Node{
		Ref:   ref,
		Path:  "/" + ref,
		Name:  e.Name,
		IsDir: e.Tag == "folder",
		Size:  -1,
		Mime:  mimeOf(e.Name),
		Extra: map[string]string{},
	}
	if e.Tag == "file" {
		n.Size = e.Size
		n.Hash = e.ContentHash
		n.HashKind = "content_hash"
	}
	if e.ID != "" {
		n.Extra["dropboxId"] = e.ID
	}
	for _, f := range []string{e.ServerModified, e.ClientModified} {
		if t, err := time.Parse(time.RFC3339, f); err == nil {
			n.ModTime = t
			break
		}
	}
	if ref == "" {
		n.Path = "/"
		n.Name = ""
	}
	return n
}

type listResp struct {
	Entries []dbxEntry `json:"entries"`
	Cursor  string     `json:"cursor"`
	HasMore bool       `json:"has_more"`
}

// --- request helpers ---

// rpc calls an api.dropboxapi.com JSON endpoint and decodes resp into out.
func (p *Provider) rpc(ctx context.Context, op, endpoint string, arg any, out any) error {
	var buf bytes.Buffer
	if arg != nil {
		if err := json.NewEncoder(&buf).Encode(arg); err != nil {
			return p.wrap(op, err)
		}
	} else {
		// Void input (only Quota calls rpc with a nil arg):
		// Dropbox's documented form is a literal JSON `null`
		// body, not an empty body — every SDK sends `null`
		// for get_space_usage / get_current_account.
		buf.WriteString("null")
	}
	token, err := p.tokens.Token(ctx)
	if err != nil {
		return p.wrap(op, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.apiBase+endpoint, &buf)
	if err != nil {
		return p.wrap(op, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(ctx, req)
	if err != nil {
		return p.wrap(op, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return p.wrap(op, p.readError(resp))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return p.wrap(op, err)
		}
	}
	return nil
}

// contentArg builds a content-endpoint request with a Dropbox-API-Arg header.
func (p *Provider) contentReq(ctx context.Context, method, endpoint string, arg any, body io.Reader) (*http.Request, error) {
	argJSON, err := json.Marshal(arg)
	if err != nil {
		return nil, err
	}
	token, err := p.tokens.Token(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, p.contentBase+endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Dropbox-API-Arg", string(argJSON))
	return req, nil
}

func (p *Provider) readError(resp *http.Response) error {
	var body struct {
		ErrorSummary string `json:"error_summary"`
		Error        any    `json:"error"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = json.Unmarshal(data, &body)
	if body.ErrorSummary != "" {
		return fmt.Errorf("dropbox api %d: %s", resp.StatusCode, body.ErrorSummary)
	}
	return fmt.Errorf("dropbox api %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
}

// normRef converts a user ref to a Dropbox path: "" (root) or "/a/b".
func normRef(ref string) string {
	ref = strings.Trim(ref, "/")
	if ref == "" {
		return ""
	}
	return "/" + ref
}

func (p *Provider) wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return &storage.ProviderError{Op: op, Provider: "dropbox", Err: err}
}

// --- Provider ---

// List returns direct children of ref. Pagination is consumed inside.
func (p *Provider) List(ctx context.Context, ref string) ([]domain.Node, error) {
	var out []domain.Node
	var resp listResp
	if err := p.rpc(ctx, "List", "/2/files/list_folder",
		map[string]any{"path": normRef(ref), "recursive": false, "include_deleted": false}, &resp); err != nil {
		return nil, err
	}
	for _, e := range resp.Entries {
		out = append(out, e.node())
	}
	for resp.HasMore {
		var next listResp
		if err := p.rpc(ctx, "List", "/2/files/list_folder/continue",
			map[string]any{"cursor": resp.Cursor}, &next); err != nil {
			return nil, err
		}
		resp = next
		for _, e := range resp.Entries {
			out = append(out, e.node())
		}
	}
	return out, nil
}

// Stat returns metadata for one node.
func (p *Provider) Stat(ctx context.Context, ref string) (domain.Node, error) {
	if strings.Trim(ref, "/") == "" {
		return domain.Node{Ref: "", Path: "/", IsDir: true, Size: -1}, nil
	}
	var e dbxEntry
	if err := p.rpc(ctx, "Stat", "/2/files/get_metadata",
		map[string]any{"path": normRef(ref)}, &e); err != nil {
		return domain.Node{}, err
	}
	return e.node(), nil
}

// OpenRead streams a file for download.
func (p *Provider) OpenRead(ctx context.Context, ref string) (io.ReadCloser, error) {
	req, err := p.contentReq(ctx, http.MethodPost, "/2/files/download",
		map[string]any{"path": normRef(ref)}, nil)
	if err != nil {
		return nil, p.wrap("OpenRead", err)
	}
	resp, err := p.client.Do(ctx, req)
	if err != nil {
		return nil, p.wrap("OpenRead", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, p.wrap("OpenRead", p.readError(resp))
	}
	return resp.Body, nil
}

// Upload writes a file. Small files use one call; large or unknown-size
// files use an upload session with 4 MiB chunks.
func (p *Provider) Upload(ctx context.Context, ref string, r io.Reader, size int64) (domain.Node, error) {
	if size >= 0 && size <= singleUploadLimit {
		data, err := io.ReadAll(io.LimitReader(r, singleUploadLimit+1))
		if err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		req, err := p.contentReq(ctx, http.MethodPost, "/2/files/upload",
			map[string]any{"path": normRef(ref), "mode": "overwrite", "autorename": false},
			bytes.NewReader(data))
		if err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		// Replayable body so the shared retry client can retry.
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
		resp, err := p.client.Do(ctx, req)
		if err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return domain.Node{}, p.wrap("Upload", p.readError(resp))
		}
		var e dbxEntry
		if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		return e.node(), nil
	}
	return p.uploadSession(ctx, ref, r)
}

func (p *Provider) uploadSession(ctx context.Context, ref string, r io.Reader) (domain.Node, error) {
	// Start session.
	req, err := p.contentReq(ctx, http.MethodPost, "/2/files/upload_session/start",
		map[string]any{"close": false}, nil)
	if err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	resp, err := p.client.Do(ctx, req)
	if err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	var start struct {
		SessionID string `json:"session_id"`
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return domain.Node{}, p.wrap("Upload", p.readError(resp))
	}
	if err := json.NewDecoder(resp.Body).Decode(&start); err != nil {
		resp.Body.Close()
		return domain.Node{}, p.wrap("Upload", err)
	}
	resp.Body.Close()

	var offset int64
	buf := make([]byte, chunkSize)
	for {
		n, readErr := io.ReadFull(r, buf)
		if n > 0 {
			chunk := buf[:n]
			closeSession := readErr == io.EOF || readErr == io.ErrUnexpectedEOF
			// Append (or finish directly when this is the last chunk).
			if closeSession {
				return p.sessionFinish(ctx, ref, start.SessionID, offset, chunk)
			}
			if err := p.sessionAppend(ctx, start.SessionID, offset, chunk, false); err != nil {
				return domain.Node{}, err
			}
			offset += int64(n)
		}
		if readErr == io.EOF {
			// Empty file or exact multiple of chunkSize: finish with empty chunk.
			return p.sessionFinish(ctx, ref, start.SessionID, offset, nil)
		}
		if readErr != nil && readErr != io.ErrUnexpectedEOF {
			return domain.Node{}, p.wrap("Upload", readErr)
		}
	}
}

func (p *Provider) sessionAppend(ctx context.Context, sessionID string, offset int64, chunk []byte, close bool) error {
	req, err := p.contentReq(ctx, http.MethodPost, "/2/files/upload_session/append_v2",
		map[string]any{
			"cursor": map[string]any{"session_id": sessionID, "offset": offset},
			"close":  close,
		}, bytes.NewReader(chunk))
	if err != nil {
		return p.wrap("Upload", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := p.client.Do(ctx, req)
	if err != nil {
		return p.wrap("Upload", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return p.wrap("Upload", p.readError(resp))
	}
	return nil
}

func (p *Provider) sessionFinish(ctx context.Context, ref, sessionID string, offset int64, chunk []byte) (domain.Node, error) {
	if chunk == nil {
		chunk = []byte{}
	}
	req, err := p.contentReq(ctx, http.MethodPost, "/2/files/upload_session/finish",
		map[string]any{
			"cursor": map[string]any{"session_id": sessionID, "offset": offset},
			"commit": map[string]any{"path": normRef(ref), "mode": "overwrite", "autorename": false},
		}, bytes.NewReader(chunk))
	if err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := p.client.Do(ctx, req)
	if err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return domain.Node{}, p.wrap("Upload", p.readError(resp))
	}
	var e dbxEntry
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	return e.node(), nil
}

// Mkdir creates a folder.
func (p *Provider) Mkdir(ctx context.Context, ref string) error {
	var out map[string]any
	// create_folder_v2 returns 409 when the folder already exists; treat
	// that as success (idempotent mkdir).
	err := p.rpc(ctx, "Mkdir", "/2/files/create_folder_v2",
		map[string]any{"path": normRef(ref), "autorename": false}, &out)
	if err != nil && strings.Contains(err.Error(), "path/conflict/folder") {
		return nil
	}
	return err
}

// Rename moves/renames within the same provider (files and folders).
func (p *Provider) Rename(ctx context.Context, src, dst string) error {
	var out map[string]any
	return p.rpc(ctx, "Rename", "/2/files/move_v2",
		map[string]any{"from_path": normRef(src), "to_path": normRef(dst), "autorename": false}, &out)
}

// Copy implements storage.Copier via copy_v2: a same-account
// server-side copy — the bytes never leave Dropbox. It never
// deletes the source: moves delete it themselves, after the copy
// succeeded.
func (p *Provider) Copy(ctx context.Context, src, dst string) error {
	var out map[string]any
	return p.rpc(ctx, "Copy", "/2/files/copy_v2",
		map[string]any{"from_path": normRef(src), "to_path": normRef(dst), "autorename": false}, &out)
}

// Versions implements storage.Versioner via list_revisions: the
// entries are the file's older revisions, oldest first. Each
// version node names the same file and carries the revision id in
// Extra["versionId"] — Restore needs exactly that id.
func (p *Provider) Versions(ctx context.Context, ref string) ([]domain.Node, error) {
	var resp struct {
		Entries   []dbxEntry `json:"entries"`
		IsDeleted bool       `json:"is_deleted"`
	}
	if err := p.rpc(ctx, "Versions", "/2/files/list_revisions",
		map[string]any{"path": normRef(ref), "limit": 100}, &resp); err != nil {
		return nil, err
	}
	out := make([]domain.Node, 0, len(resp.Entries))
	for _, e := range resp.Entries {
		n := e.node()
		if n.Extra == nil {
			n.Extra = map[string]string{}
		}
		n.Extra["versionId"] = e.Rev
		out = append(out, n)
	}
	return out, nil
}

// Restore implements storage.Versioner via files/restore: the
// named revision becomes the new head, and the old head stays in
// the revision history, so a restore is itself undoable.
func (p *Provider) Restore(ctx context.Context, ref, versionID string) error {
	var e dbxEntry
	return p.rpc(ctx, "Restore", "/2/files/restore",
		map[string]any{"path": normRef(ref), "rev": versionID}, &e)
}

// Delete removes one node (Dropbox moves it to trash by default).
func (p *Provider) Delete(ctx context.Context, ref string) error {
	var out map[string]any
	return p.rpc(ctx, "Delete", "/2/files/delete_v2",
		map[string]any{"path": normRef(ref)}, &out)
}

// --- optional interfaces ---

// Search implements storage.Searcher via files/search_v2.
func (p *Provider) Search(ctx context.Context, query string) ([]domain.Node, error) {
	var resp struct {
		Matches []struct {
			Metadata dbxEntry `json:"metadata"`
		} `json:"matches"`
		HasMore bool   `json:"has_more"`
		Cursor  string `json:"cursor"`
	}
	if err := p.rpc(ctx, "Search", "/2/files/search_v2",
		map[string]any{"query": query}, &resp); err != nil {
		return nil, err
	}
	out := make([]domain.Node, 0, len(resp.Matches))
	for _, m := range resp.Matches {
		out = append(out, m.Metadata.node())
	}
	return out, nil
}

// Delta implements storage.DeltaSource via the list_folder cursor.
// An empty cursor starts a fresh recursive listing and returns its
// cursor; a non-empty cursor continues from it.
func (p *Provider) Delta(ctx context.Context, cursor string) ([]domain.Node, string, error) {
	var resp listResp
	if cursor == "" {
		if err := p.rpc(ctx, "Delta", "/2/files/list_folder",
			map[string]any{"path": "", "recursive": true, "include_deleted": true}, &resp); err != nil {
			return nil, "", err
		}
	} else {
		if err := p.rpc(ctx, "Delta", "/2/files/list_folder/continue",
			map[string]any{"cursor": cursor}, &resp); err != nil {
			return nil, "", err
		}
	}
	out := make([]domain.Node, 0, len(resp.Entries))
	for _, e := range resp.Entries {
		out = append(out, e.node())
	}
	return out, resp.Cursor, nil
}

// Share implements storage.Sharer via create_shared_link_with_settings.
func (p *Provider) Share(ctx context.Context, ref string) (string, error) {
	var resp struct {
		URL string `json:"url"`
	}
	if err := p.rpc(ctx, "Share", "/2/sharing/create_shared_link_with_settings",
		map[string]any{"path": normRef(ref)}, &resp); err != nil {
		return "", err
	}
	return resp.URL, nil
}

// DirectURL implements storage.DirectURLer via get_temporary_link:
// a short-lived (4 hour) link Dropbox mints for one file. The
// webview loads it directly for in-app preview (<img>/<video>/
// <iframe>), which needs a URL that works without extra headers.
func (p *Provider) DirectURL(ctx context.Context, ref string) (string, error) {
	return p.tempLink(ctx, "DirectURL", ref)
}

// tempLink is the shared get_temporary_link call behind Thumbnail
// and DirectURL. op names the caller for error context.
func (p *Provider) tempLink(ctx context.Context, op, ref string) (string, error) {
	var resp struct {
		Link string `json:"link"`
	}
	if err := p.rpc(ctx, op, "/2/files/get_temporary_link",
		map[string]any{"path": normRef(ref)}, &resp); err != nil {
		return "", err
	}
	if resp.Link == "" {
		return "", p.wrap(op, fmt.Errorf("dropbox: get_temporary_link returned no link for %q", ref))
	}
	return resp.Link, nil
}

// Thumbnail implements storage.Thumber via get_temporary_link.
func (p *Provider) Thumbnail(ctx context.Context, ref string) (string, error) {
	return p.tempLink(ctx, "Thumbnail", ref)
}

// Quota implements storage.QuotaSource via POST
// /2/users/get_space_usage: {"used": <bytes>, "allocation":
// {"allocated": <bytes>, ".tag": "individual"}}. A missing or
// zero allocated total (team plans report it differently) is
// returned as -1: no total is invented.
func (p *Provider) Quota(ctx context.Context) (used, total int64, err error) {
	var resp struct {
		Used       int64 `json:"used"`
		Allocation struct {
			Allocated int64 `json:"allocated"`
		} `json:"allocation"`
	}
	if err := p.rpc(ctx, "Quota", "/2/users/get_space_usage", nil, &resp); err != nil {
		return 0, 0, err
	}
	total = resp.Allocation.Allocated
	if total <= 0 {
		total = -1
	}
	return resp.Used, total, nil
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
	_ storage.DeltaSource = (*Provider)(nil)
	_ storage.Searcher    = (*Provider)(nil)
	_ storage.Sharer      = (*Provider)(nil)
	_ storage.Thumber     = (*Provider)(nil)
	_ storage.QuotaSource = (*Provider)(nil)
	_ storage.Versioner   = (*Provider)(nil)
	_ storage.Copier      = (*Provider)(nil)
)
