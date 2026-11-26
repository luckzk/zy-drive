// SPDX-License-Identifier: MPL-2.0
// Package drive implements storage.Provider for Google Drive using
// the Drive API v3 over plain HTTP (no third-party SDK).
//
// Drive is ID-based: a file's identity is its file ID, names may repeat
// in different folders and a rename does not change the ID. This driver
// owns the path<->ID mapping: it resolves display paths by walking
// from the root folder ID (cfg.Extra["rootId"], default "root") with
// name queries, and keeps an in-memory path->ID cache per instance
// (one instance per Mount, so two Drive accounts never share IDs).
// The cache is a pure accelerator: every entry is re-derivable from
// the API, and it is invalidated on Mkdir/Rename/Delete/Upload.
//
// Special cases handled here, not in generic code:
//   - Google Docs/Sheets/Slides have no bytes of their own: OpenRead
//     exports them (docx/xlsx/pptx) via files.export.
//   - Delete moves to trash (files.update trashed=true); Caps
//     DeleteToTrash is true.
//   - Upload: multipart for small files, resumable session (8 MiB
//     chunks) for large or unknown-size files.
//   - Delta: changes.list with a startPageToken.
package drive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
	shttp "github.com/luckzk/zy-drive/internal/storage/http"
	"github.com/luckzk/zy-drive/internal/storage/oauth"
)

const (
	defaultAPIBase    = "https://www.googleapis.com"
	defaultUploadBase = "https://www.googleapis.com"
	folderMime        = "application/vnd.google-apps.folder"
	// multipartLimit: files up to this size use one multipart call.
	multipartLimit = 5 << 20
	chunkSize      = 8 << 20
)

var exportMimes = map[string]string{
	"application/vnd.google-apps.document":     "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"application/vnd.google-apps.spreadsheet":  "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"application/vnd.google-apps.presentation": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"application/vnd.google-apps.drawing":      "image/png",
}

// Provider is a Drive-backed storage.Provider. One instance per Mount.
type Provider struct {
	apiBase    string
	uploadBase string
	rootID     string
	tokens     *oauth.TokenSource
	client     *shttp.Client

	mu    sync.Mutex
	idMap map[string]string // display path ("/a/b") -> file ID
}

// New builds a Drive provider. cfg.Token is the OAuth access token.
// cfg.Extra may carry refresh_token / client_id / client_secret /
// rootId (default "root"). cfg.Extra["api_base"] / ["upload_base"]
// override the API endpoints (tests and self-hosted/proxied
// deployments; normally empty).
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
		return nil, fmt.Errorf("drive: access token or refresh token is required")
	}
	var refresher oauth.Refresher
	if cfg.Extra["refresh_token"] != "" {
		tokenURL := cfg.Extra["token_url"]
		if tokenURL == "" {
			tokenURL = "https://oauth2.googleapis.com/token"
		}
		refresher = oauth.FormRefresher(tokenURL,
			cfg.Extra["client_id"], cfg.Extra["client_secret"])
	}
	ts := oauth.NewTokenSource(&oauth.Token{
		AccessToken:  cfg.Token,
		RefreshToken: cfg.Extra["refresh_token"],
	}, refresher)
	p := &Provider{
		apiBase:    strings.TrimSuffix(apiBase, "/"),
		uploadBase: strings.TrimSuffix(uploadBase, "/"),
		rootID:     cfg.Extra["rootId"],
		tokens:     ts,
		idMap:      map[string]string{},
	}
	if p.rootID == "" {
		p.rootID = "root"
	}
	p.client = shttp.New(nil)
	p.client.OnAuthExpired = func(ctx context.Context) error {
		_, err := ts.Refresh(ctx)
		return err
	}
	return p, nil
}

// Caps declares Drive capabilities.
func (p *Provider) Caps() domain.Caps {
	return domain.Caps{
		CanMkdir: true, CanRename: true, CanDelete: true, DeleteToTrash: true,
		SupportsServerSideCopy:  false, // Drive copy is files.copy, not same semantics
		SupportsVersions:        true,
		SupportsShare:           true,
		SupportsSearch:          true,
		SupportsDelta:           true, // changes.list
		SupportsThumbnail:       true,
		SupportsResumableUpload: true,
		SupportsEncryption:      false,
		SupportsStorageClass:    false,
		SupportsPresignedURL:    false,
		IDBased:                 true,
		MaxFileSize:             5 << 40, // 5 TB
		MinPartSize:             chunkSize,
		MaxPathLen:              0,
	}
}

// --- wire types ---

type driveFile struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	MimeType      string `json:"mimeType"`
	Size          int64  `json:"size,string,omitempty"`
	ModifiedTime  string `json:"modifiedTime"`
	MD5Checksum   string `json:"md5Checksum"`
	Trashed       bool   `json:"trashed"`
	WebViewLink   string `json:"webViewLink"`
	ThumbnailLink string `json:"thumbnailLink"`
}

func (f driveFile) node(displayPath string) domain.Node {
	n := domain.Node{
		Ref:   f.ID,
		Path:  displayPath,
		Name:  f.Name,
		IsDir: f.MimeType == folderMime,
		Size:  -1,
		Mime:  f.MimeType,
	}
	if !n.IsDir {
		n.Size = f.Size
		n.Hash = f.MD5Checksum
		if f.MD5Checksum != "" {
			n.HashKind = "md5"
		}
	}
	if t, err := time.Parse(time.RFC3339, f.ModifiedTime); err == nil {
		n.ModTime = t
	}
	if f.ThumbnailLink != "" {
		n.ThumbURL = f.ThumbnailLink
	}
	return n
}

const fileFields = "id,name,mimeType,size,modifiedTime,md5Checksum,trashed,webViewLink,thumbnailLink"

// --- request helpers ---

func (p *Provider) do(ctx context.Context, op string, req *http.Request) (*http.Response, error) {
	token, err := p.tokens.Token(ctx)
	if err != nil {
		return nil, p.wrap(op, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := p.client.Do(ctx, req)
	if err != nil {
		return nil, p.wrap(op, err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, p.wrap(op, p.readError(resp))
	}
	return resp, nil
}

func (p *Provider) getJSON(ctx context.Context, op, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return p.wrap(op, err)
	}
	resp, err := p.do(ctx, op, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return p.wrap(op, json.NewDecoder(resp.Body).Decode(out))
}

func (p *Provider) readError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var body struct {
		Error struct {
			Message string `json:"message"`
			Code    int    `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &body)
	if body.Error.Message != "" {
		return fmt.Errorf("drive api %d: %s", resp.StatusCode, body.Error.Message)
	}
	return fmt.Errorf("drive api %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
}

func (p *Provider) wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return &storage.ProviderError{Op: op, Provider: "drive", Err: err}
}

// --- path <-> ID ---

func cleanRef(ref string) string { return "/" + strings.Trim(ref, "/") }

// resolve returns the file ID for a display path, walking from the
// root with name queries. "" and "/" resolve to the root folder ID.
func (p *Provider) resolve(ctx context.Context, ref string) (string, error) {
	ref = cleanRef(ref)
	if ref == "/" {
		return p.rootID, nil
	}
	p.mu.Lock()
	if id, ok := p.idMap[ref]; ok {
		p.mu.Unlock()
		return id, nil
	}
	p.mu.Unlock()
	segments := strings.Split(strings.Trim(ref, "/"), "/")
	parent := p.rootID
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
			return "", err
		}
		p.mu.Lock()
		p.idMap[cur] = id
		p.mu.Unlock()
		parent = id
	}
	return parent, nil
}

// findChild finds one child of parentID by exact name.
func (p *Provider) findChild(ctx context.Context, parentID, name string) (string, error) {
	q := fmt.Sprintf("name = '%s' and '%s' in parents and trashed = false",
		strings.ReplaceAll(name, "'", "\\'"), parentID)
	u := p.apiBase + "/drive/v3/files?q=" + url.QueryEscape(q) +
		"&fields=" + url.QueryEscape("files(id,mimeType)") + "&pageSize=1"
	var resp struct {
		Files []driveFile `json:"files"`
	}
	if err := p.getJSON(ctx, "Stat", u, &resp); err != nil {
		return "", err
	}
	if len(resp.Files) == 0 {
		return "", p.wrap("Stat", fmt.Errorf("drive: no such file or folder: %s", name))
	}
	return resp.Files[0].ID, nil
}

func (p *Provider) invalidate(paths ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ref := range paths {
		ref = cleanRef(ref)
		for cached := range p.idMap {
			if cached == ref || strings.HasPrefix(cached, ref+"/") {
				delete(p.idMap, cached)
			}
		}
	}
}

// listChildren returns the direct children of a folder ID.
func (p *Provider) listChildren(ctx context.Context, parentID string) ([]driveFile, error) {
	q := fmt.Sprintf("'%s' in parents and trashed = false", parentID)
	var out []driveFile
	pageToken := ""
	for {
		u := p.apiBase + "/drive/v3/files?q=" + url.QueryEscape(q) +
			"&fields=" + url.QueryEscape("nextPageToken,files("+fileFields+")") +
			"&pageSize=100"
		if pageToken != "" {
			u += "&pageToken=" + url.QueryEscape(pageToken)
		}
		var resp struct {
			NextPageToken string      `json:"nextPageToken"`
			Files         []driveFile `json:"files"`
		}
		if err := p.getJSON(ctx, "List", u, &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Files...)
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	return out, nil
}

func (p *Provider) getFile(ctx context.Context, op, id string) (driveFile, error) {
	u := p.apiBase + "/drive/v3/files/" + url.PathEscape(id) +
		"?fields=" + url.QueryEscape(fileFields)
	var f driveFile
	if err := p.getJSON(ctx, op, u, &f); err != nil {
		return driveFile{}, err
	}
	if f.Trashed {
		return driveFile{}, p.wrap(op, fmt.Errorf("drive: file is in trash: %s", id))
	}
	return f, nil
}

// --- Provider ---

// List returns direct children of ref.
func (p *Provider) List(ctx context.Context, ref string) ([]domain.Node, error) {
	id, err := p.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	children, err := p.listChildren(ctx, id)
	if err != nil {
		return nil, err
	}
	base := cleanRef(ref)
	out := make([]domain.Node, 0, len(children))
	for _, f := range children {
		childPath := path.Join(base, f.Name)
		if base == "/" {
			childPath = "/" + f.Name
		}
		out = append(out, f.node(childPath))
		p.mu.Lock()
		p.idMap[childPath] = f.ID
		p.mu.Unlock()
	}
	return out, nil
}

// Stat returns metadata for one node.
func (p *Provider) Stat(ctx context.Context, ref string) (domain.Node, error) {
	if strings.Trim(ref, "/") == "" {
		return domain.Node{Ref: p.rootID, Path: "/", IsDir: true, Size: -1, Mime: folderMime}, nil
	}
	id, err := p.resolve(ctx, ref)
	if err != nil {
		return domain.Node{}, err
	}
	f, err := p.getFile(ctx, "Stat", id)
	if err != nil {
		return domain.Node{}, err
	}
	return f.node(cleanRef(ref)), nil
}

// OpenRead streams a file. Google-native files are exported to the
// matching Office format (docx/xlsx/pptx/png).
func (p *Provider) OpenRead(ctx context.Context, ref string) (io.ReadCloser, error) {
	id, err := p.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	f, err := p.getFile(ctx, "OpenRead", id)
	if err != nil {
		return nil, err
	}
	var u string
	if mime, ok := exportMimes[f.MimeType]; ok {
		u = p.apiBase + "/drive/v3/files/" + url.PathEscape(id) +
			"/export?mimeType=" + url.QueryEscape(mime)
	} else {
		u = p.apiBase + "/drive/v3/files/" + url.PathEscape(id) + "?alt=media"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, p.wrap("OpenRead", err)
	}
	resp, err := p.do(ctx, "OpenRead", req)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Upload writes a file (create or overwrite by name in the parent).
func (p *Provider) Upload(ctx context.Context, ref string, r io.Reader, size int64) (domain.Node, error) {
	ref = cleanRef(ref)
	name := path.Base(ref)
	parentRef := path.Dir(ref)
	if parentRef == "." {
		parentRef = "/"
	}
	parentID, err := p.resolve(ctx, parentRef)
	if err != nil {
		return domain.Node{}, err
	}
	// Overwrite when a file of the same name already exists.
	existingID, _ := p.findChild(ctx, parentID, name)
	if size >= 0 && size <= multipartLimit {
		data, err := io.ReadAll(io.LimitReader(r, multipartLimit+1))
		if err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		f, err := p.uploadMultipart(ctx, parentID, name, existingID, data)
		if err != nil {
			return domain.Node{}, err
		}
		p.mu.Lock()
		p.idMap[ref] = f.ID
		p.mu.Unlock()
		return f.node(ref), nil
	}
	f, err := p.uploadResumable(ctx, parentID, name, existingID, r, size)
	if err != nil {
		return domain.Node{}, err
	}
	p.mu.Lock()
	p.idMap[ref] = f.ID
	p.mu.Unlock()
	return f.node(ref), nil
}

func (p *Provider) uploadMultipart(ctx context.Context, parentID, name, existingID string, data []byte) (driveFile, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	meta := map[string]any{"name": name}
	if existingID == "" {
		meta["parents"] = []string{parentID}
	}
	metaJSON, _ := json.Marshal(meta)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Type", "application/json; charset=UTF-8")
	part, err := mw.CreatePart(h)
	if err != nil {
		return driveFile{}, p.wrap("Upload", err)
	}
	if _, err := part.Write(metaJSON); err != nil {
		return driveFile{}, p.wrap("Upload", err)
	}
	h2 := make(textproto.MIMEHeader)
	h2.Set("Content-Type", mimeOf(name))
	part2, err := mw.CreatePart(h2)
	if err != nil {
		return driveFile{}, p.wrap("Upload", err)
	}
	if _, err := part2.Write(data); err != nil {
		return driveFile{}, p.wrap("Upload", err)
	}
	if err := mw.Close(); err != nil {
		return driveFile{}, p.wrap("Upload", err)
	}
	var u string
	var method string
	if existingID == "" {
		u = p.uploadBase + "/upload/drive/v3/files?uploadType=multipart&fields=" + url.QueryEscape(fileFields)
		method = http.MethodPost
	} else {
		u = p.uploadBase + "/upload/drive/v3/files/" + url.PathEscape(existingID) +
			"?uploadType=multipart&fields=" + url.QueryEscape(fileFields)
		method = http.MethodPatch
	}
	req, err := http.NewRequestWithContext(ctx, method, u, &buf)
	if err != nil {
		return driveFile{}, p.wrap("Upload", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := p.do(ctx, "Upload", req)
	if err != nil {
		return driveFile{}, err
	}
	defer resp.Body.Close()
	var f driveFile
	if err := json.NewDecoder(resp.Body).Decode(&f); err != nil {
		return driveFile{}, p.wrap("Upload", err)
	}
	return f, nil
}

func (p *Provider) uploadResumable(ctx context.Context, parentID, name, existingID string, r io.Reader, size int64) (driveFile, error) {
	meta := map[string]any{"name": name}
	if existingID == "" {
		meta["parents"] = []string{parentID}
	}
	metaJSON, _ := json.Marshal(meta)
	var u string
	var method string
	if existingID == "" {
		u = p.uploadBase + "/upload/drive/v3/files?uploadType=resumable&fields=" + url.QueryEscape(fileFields)
		method = http.MethodPost
	} else {
		u = p.uploadBase + "/upload/drive/v3/files/" + url.PathEscape(existingID) +
			"?uploadType=resumable&fields=" + url.QueryEscape(fileFields)
		method = http.MethodPatch
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(metaJSON))
	if err != nil {
		return driveFile{}, p.wrap("Upload", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	if size >= 0 {
		req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(size, 10))
	}
	req.Header.Set("X-Upload-Content-Type", mimeOf(name))
	resp, err := p.do(ctx, "Upload", req)
	if err != nil {
		return driveFile{}, err
	}
	resp.Body.Close()
	sessionURL := resp.Header.Get("Location")
	if sessionURL == "" {
		return driveFile{}, p.wrap("Upload", fmt.Errorf("drive: resumable upload returned no session URL"))
	}
	if !strings.HasPrefix(sessionURL, "http") {
		// Some endpoints return a relative Location; resolve it
		// against the upload base (the mock server does this).
		if base, err := url.Parse(p.uploadBase + "/"); err == nil {
			if rel, err := url.Parse(sessionURL); err == nil {
				sessionURL = base.ResolveReference(rel).String()
			}
		}
	}
	// PUT chunks until the reader is exhausted.
	var offset int64
	buf := make([]byte, chunkSize)
	for {
		n, readErr := io.ReadFull(r, buf)
		if n > 0 {
			chunk := buf[:n]
			last := readErr == io.EOF || readErr == io.ErrUnexpectedEOF || (size >= 0 && offset+int64(n) >= size)
			f, done, err := p.putChunk(ctx, sessionURL, offset, chunk, size, last)
			if err != nil {
				return driveFile{}, err
			}
			if done {
				return f, nil
			}
			offset += int64(n)
		}
		if readErr == io.EOF {
			if offset == 0 {
				// Empty file: one zero-length final chunk.
				f, done, err := p.putChunk(ctx, sessionURL, 0, []byte{}, size, true)
				if err != nil {
					return driveFile{}, err
				}
				if done {
					return f, nil
				}
			}
			return driveFile{}, p.wrap("Upload", fmt.Errorf("drive: resumable upload ended without completion"))
		}
		if readErr != nil && readErr != io.ErrUnexpectedEOF {
			return driveFile{}, p.wrap("Upload", readErr)
		}
	}
}

func (p *Provider) putChunk(ctx context.Context, sessionURL string, offset int64, chunk []byte, total int64, last bool) (driveFile, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, sessionURL, bytes.NewReader(chunk))
	if err != nil {
		return driveFile{}, false, p.wrap("Upload", err)
	}
	totalStr := "*"
	if total >= 0 {
		totalStr = strconv.FormatInt(total, 10)
	}
	if last && total < 0 {
		totalStr = strconv.FormatInt(offset+int64(len(chunk)), 10)
	}
	req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%s", offset, offset+int64(len(chunk))-1, totalStr))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := p.do(ctx, "Upload", req)
	if err != nil {
		// A 308 Resume Incomplete is not an error: the session is
		// still open. do() only errors on >=400, and 308 is <400,
		// so reaching here means the chunk was accepted or finished.
		return driveFile{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		var f driveFile
		if err := json.NewDecoder(resp.Body).Decode(&f); err != nil {
			return driveFile{}, false, p.wrap("Upload", err)
		}
		return f, true, nil
	}
	// 308: chunk accepted, more to come.
	_, _ = io.Copy(io.Discard, resp.Body)
	return driveFile{}, false, nil
}

// Mkdir creates a folder.
func (p *Provider) Mkdir(ctx context.Context, ref string) error {
	ref = cleanRef(ref)
	name := path.Base(ref)
	parentRef := path.Dir(ref)
	if parentRef == "." {
		parentRef = "/"
	}
	parentID, err := p.resolve(ctx, parentRef)
	if err != nil {
		return err
	}
	if id, err := p.findChild(ctx, parentID, name); err == nil {
		// Already exists: idempotent mkdir.
		p.mu.Lock()
		p.idMap[ref] = id
		p.mu.Unlock()
		return nil
	}
	meta, _ := json.Marshal(map[string]any{
		"name": name, "mimeType": folderMime, "parents": []string{parentID},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.apiBase+"/drive/v3/files?fields="+url.QueryEscape(fileFields), bytes.NewReader(meta))
	if err != nil {
		return p.wrap("Mkdir", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.do(ctx, "Mkdir", req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var f driveFile
	if err := json.NewDecoder(resp.Body).Decode(&f); err != nil {
		return p.wrap("Mkdir", err)
	}
	p.mu.Lock()
	p.idMap[ref] = f.ID
	p.mu.Unlock()
	return nil
}

// Rename moves/renames within the same provider.
func (p *Provider) Rename(ctx context.Context, src, dst string) error {
	src, dst = cleanRef(src), cleanRef(dst)
	id, err := p.resolve(ctx, src)
	if err != nil {
		return err
	}
	srcParentRef := path.Dir(src)
	if srcParentRef == "." {
		srcParentRef = "/"
	}
	dstParentRef := path.Dir(dst)
	if dstParentRef == "." {
		dstParentRef = "/"
	}
	srcParentID, err := p.resolve(ctx, srcParentRef)
	if err != nil {
		return err
	}
	dstParentID, err := p.resolve(ctx, dstParentRef)
	if err != nil {
		return err
	}
	meta, _ := json.Marshal(map[string]any{"name": path.Base(dst)})
	u := p.apiBase + "/drive/v3/files/" + url.PathEscape(id) + "?fields=" + url.QueryEscape(fileFields)
	if srcParentID != dstParentID {
		u += "&addParents=" + url.QueryEscape(dstParentID) + "&removeParents=" + url.QueryEscape(srcParentID)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, u, bytes.NewReader(meta))
	if err != nil {
		return p.wrap("Rename", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.do(ctx, "Rename", req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	p.invalidate(src, dst)
	return nil
}

// Delete moves a node to trash.
func (p *Provider) Delete(ctx context.Context, ref string) error {
	id, err := p.resolve(ctx, ref)
	if err != nil {
		return err
	}
	meta, _ := json.Marshal(map[string]any{"trashed": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		p.apiBase+"/drive/v3/files/"+url.PathEscape(id), bytes.NewReader(meta))
	if err != nil {
		return p.wrap("Delete", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.do(ctx, "Delete", req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	p.invalidate(ref)
	return nil
}

// --- optional interfaces ---

// Search implements storage.Searcher via files.list name contains.
func (p *Provider) Search(ctx context.Context, query string) ([]domain.Node, error) {
	q := fmt.Sprintf("name contains '%s' and trashed = false", strings.ReplaceAll(query, "'", "\\'"))
	u := p.apiBase + "/drive/v3/files?q=" + url.QueryEscape(q) +
		"&fields=" + url.QueryEscape("files("+fileFields+")") + "&pageSize=100"
	var resp struct {
		Files []driveFile `json:"files"`
	}
	if err := p.getJSON(ctx, "Search", u, &resp); err != nil {
		return nil, err
	}
	out := make([]domain.Node, 0, len(resp.Files))
	for _, f := range resp.Files {
		out = append(out, f.node("/"+f.Name))
	}
	return out, nil
}

// Delta implements storage.DeltaSource via changes.list.
func (p *Provider) Delta(ctx context.Context, cursor string) ([]domain.Node, string, error) {
	if cursor == "" {
		var start struct {
			StartPageToken string `json:"startPageToken"`
		}
		if err := p.getJSON(ctx, "Delta", p.apiBase+"/drive/v3/changes/startPageToken", &start); err != nil {
			return nil, "", err
		}
		cursor = start.StartPageToken
		// No history to replay from a fresh token.
		return nil, cursor, nil
	}
	u := p.apiBase + "/drive/v3/changes?pageToken=" + url.QueryEscape(cursor) +
		"&fields=" + url.QueryEscape("nextPageToken,newStartPageToken,changes(fileId,removed,file("+fileFields+"))")
	var resp struct {
		NextPageToken     string `json:"nextPageToken"`
		NewStartPageToken string `json:"newStartPageToken"`
		Changes           []struct {
			FileID  string    `json:"fileId"`
			Removed bool      `json:"removed"`
			File    driveFile `json:"file"`
		} `json:"changes"`
	}
	if err := p.getJSON(ctx, "Delta", u, &resp); err != nil {
		return nil, "", err
	}
	out := make([]domain.Node, 0, len(resp.Changes))
	for _, c := range resp.Changes {
		n := c.File.node("/" + c.File.Name)
		if c.Removed {
			if n.Extra == nil {
				n.Extra = map[string]string{}
			}
			n.Extra["removed"] = "true"
		}
		out = append(out, n)
	}
	next := resp.NewStartPageToken
	if next == "" {
		next = resp.NextPageToken
	}
	return out, next, nil
}

// Share implements storage.Sharer: anyone-with-link read, then the
// file's webViewLink.
func (p *Provider) Share(ctx context.Context, ref string) (string, error) {
	id, err := p.resolve(ctx, ref)
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]any{"type": "anyone", "role": "reader"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.apiBase+"/drive/v3/files/"+url.PathEscape(id)+"/permissions", bytes.NewReader(body))
	if err != nil {
		return "", p.wrap("Share", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.do(ctx, "Share", req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	f, err := p.getFile(ctx, "Share", id)
	if err != nil {
		return "", err
	}
	return f.WebViewLink, nil
}

// Versions implements storage.Versioner via revisions.list.
// Every version node names the same file (Ref/Path/Name of the
// head) and carries the revision id in Extra["versionId"] —
// Restore needs exactly that id, and generic code never reads
// Extra (see provider-capabilities.md §三.6). Revisions come
// back oldest first; the list is returned in that order.
func (p *Provider) Versions(ctx context.Context, ref string) ([]domain.Node, error) {
	id, err := p.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	var out []domain.Node
	pageToken := ""
	for {
		u := p.apiBase + "/drive/v3/files/" + url.PathEscape(id) + "/revisions" +
			"?fields=" + url.QueryEscape("nextPageToken,revisions(id,modifiedTime,size,md5Checksum)") +
			"&pageSize=100"
		if pageToken != "" {
			u += "&pageToken=" + url.QueryEscape(pageToken)
		}
		var resp struct {
			NextPageToken string `json:"nextPageToken"`
			Revisions     []struct {
				ID           string `json:"id"`
				ModifiedTime string `json:"modifiedTime"`
				Size         int64  `json:"size,string,omitempty"`
				MD5Checksum  string `json:"md5Checksum"`
			} `json:"revisions"`
		}
		if err := p.getJSON(ctx, "Versions", u, &resp); err != nil {
			return nil, err
		}
		for _, r := range resp.Revisions {
			n := domain.Node{
				Ref:   id,
				Path:  cleanRef(ref),
				Name:  path.Base(cleanRef(ref)),
				Size:  r.Size,
				Extra: map[string]string{"versionId": r.ID},
			}
			if r.MD5Checksum != "" {
				n.Hash = r.MD5Checksum
				n.HashKind = "md5"
			}
			if t, err := time.Parse(time.RFC3339, r.ModifiedTime); err == nil {
				n.ModTime = t
			}
			out = append(out, n)
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	return out, nil
}

// Restore implements storage.Versioner for Drive. Drive API v3
// has NO server-side restore endpoint — the revisions resource
// only offers get/list/update/delete (verified against the v3
// reference: developers.google.com/drive/v3/reference, REST
// Resource v3.revisions). So restore downloads the revision
// (revisions.get alt=media) and re-uploads it over the same file,
// which creates a new head version; the old head stays in the
// revision history, so a restore is itself undoable by restoring
// the previous head. Google-native files (Docs/Sheets/Slides)
// have no downloadable revision bytes — alt=media fails for
// them, and the error surfaces as-is instead of a silent no-op.
func (p *Provider) Restore(ctx context.Context, ref, versionID string) error {
	id, err := p.resolve(ctx, ref)
	if err != nil {
		return err
	}
	u := p.apiBase + "/drive/v3/files/" + url.PathEscape(id) +
		"/revisions/" + url.PathEscape(versionID) + "?alt=media"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return p.wrap("Restore", err)
	}
	resp, err := p.do(ctx, "Restore", req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return p.wrap("Restore", err)
	}
	if _, err := p.Upload(ctx, ref, bytes.NewReader(data), int64(len(data))); err != nil {
		return err
	}
	return nil
}

// Thumbnail implements storage.Thumber via the file's thumbnailLink.
func (p *Provider) Thumbnail(ctx context.Context, ref string) (string, error) {
	id, err := p.resolve(ctx, ref)
	if err != nil {
		return "", err
	}
	f, err := p.getFile(ctx, "Thumbnail", id)
	if err != nil {
		return "", err
	}
	if f.ThumbnailLink == "" {
		return "", p.wrap("Thumbnail", fmt.Errorf("drive: no thumbnail for %s", ref))
	}
	return f.ThumbnailLink, nil
}

// Quota implements storage.QuotaSource via about.get
// (GET /drive/v3/about?fields=storageQuota): storageQuota.limit
// and .usage are decimal strings in bytes. An empty limit means
// the account reports no cap (unlimited Workspace storage): it
// is returned as -1, never guessed.
func (p *Provider) Quota(ctx context.Context) (used, total int64, err error) {
	var resp struct {
		StorageQuota struct {
			Limit string `json:"limit"`
			Usage string `json:"usage"`
		} `json:"storageQuota"`
	}
	if err := p.getJSON(ctx, "Quota", p.apiBase+"/drive/v3/about?fields=storageQuota", &resp); err != nil {
		return 0, 0, err
	}
	used, err = strconv.ParseInt(resp.StorageQuota.Usage, 10, 64)
	if err != nil {
		return 0, 0, p.wrap("Quota", fmt.Errorf("drive: bad storageQuota.usage %q", resp.StorageQuota.Usage))
	}
	if resp.StorageQuota.Limit == "" {
		return used, -1, nil
	}
	total, err = strconv.ParseInt(resp.StorageQuota.Limit, 10, 64)
	if err != nil {
		return 0, 0, p.wrap("Quota", fmt.Errorf("drive: bad storageQuota.limit %q", resp.StorageQuota.Limit))
	}
	return used, total, nil
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
)
