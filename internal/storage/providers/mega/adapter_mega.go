// SPDX-License-Identifier: MPL-2.0
// Package mega — go-mega adapter.
//
// This file is the ONLY place that imports
// github.com/t3rm1n4l/go-mega. It is a thin forwarding layer:
// every method maps one-to-one onto the library's API (verified
// against the library source, go-mega at commit c6acd6a,
// 2026-07-17 — the same commit Alist and rclone pin). The core
// driver logic lives in the untagged mega.go and is fully tested
// against a fake client (mega_test.go).
package mega

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	gomeg "github.com/t3rm1n4l/go-mega"

	"github.com/luckzk/zy-drive/internal/netproxy"
	"github.com/luckzk/zy-drive/internal/storage"
)

// New builds a MEGA provider and logs in. Credentials come from
// cfg.Extra (never from the password field — MEGA has no token):
//   - a saved session: mega_session_id + mega_master_key
//     (base64) — preferred, used on restart;
//   - otherwise: mega_email + mega_password, plus the optional
//     mega_twofactor code.
//
// The exported session is available through
// storage.SessionExporter so the app can store it encrypted at
// rest after the first password login.
func New(cfg storage.Config) (storage.Provider, error) {
	return newWithClient(cfg, nil)
}

// newWithClient is New with an injectable HTTP client (nil = the
// default client with the shared proxy transport). Tests pass a
// client pointed at a fake MEGA server.
func newWithClient(cfg storage.Config, client *http.Client) (storage.Provider, error) {
	m := gomeg.New()
	m.SetLogger(nil) // silence the library's log.Printf default
	if client == nil {
		client = &http.Client{
			Transport: netproxy.Transport(),
			Timeout:   120 * time.Second,
		}
	}
	m.SetClient(client)
	a := &megaAdapter{m: m}
	if sid := cfg.Extra["mega_session_id"]; sid != "" {
		keyB64 := cfg.Extra["mega_master_key"]
		if keyB64 == "" {
			return nil, fmt.Errorf("mega: saved session is missing the master key")
		}
		key, err := base64.StdEncoding.DecodeString(keyB64)
		if err != nil {
			return nil, fmt.Errorf("mega: saved master key is not valid base64: %w", err)
		}
		if err := a.LoginWithKeys(sid, key); err != nil {
			// The server rejected the session (expired,
			// revoked, or never valid): only a fresh password
			// login can revive the mount. A transport failure
			// is different — it is transient and retryable,
			// so it must NOT be marked "expired" (that would
			// send the user to re-authorize for a blip).
			// go-mega surfaces a rejected session as a
			// response error (the `f` command answers with an
			// error number like [-9], which fails to unmarshal
			// into the filesystem struct), while transport
			// failures surface as net/url errors — or, worse,
			// as "unexpected end of JSON input": api_request
			// swallows a transport error (its loop-local err
			// shadows the named return) and returns an empty
			// body with a nil error, so the unmarshal fails
			// on empty input. That is still "no server
			// answer", not a rejection.
			if isTransientMegaError(err) {
				return nil, fmt.Errorf("mega: saved session login failed: %w", err)
			}
			return nil, fmt.Errorf("mega: saved session expired or invalid, re-login required (%v): %w", err, storage.ErrSessionExpired)
		}
		return newProvider(a), nil
	}
	email := cfg.Extra["mega_email"]
	password := cfg.Extra["mega_password"]
	if email == "" || password == "" {
		return nil, fmt.Errorf("mega: email + password are required (or a saved session)")
	}
	var err error
	if code := cfg.Extra["mega_twofactor"]; code != "" {
		err = a.MultiFactorLogin(email, password, code)
	} else {
		err = a.Login(email, password)
	}
	if err != nil {
		return nil, fmt.Errorf("mega: login failed: %w", err)
	}
	return newProvider(a), nil
}

// isTransientMegaError reports whether err means "no usable
// server answer" rather than "the server rejected the session":
// a transport failure, an empty/malformed body (go-mega swallows
// transport errors and hands back an empty response, which fails
// as a JSON syntax error), or a non-JSON response (EBADRESP).
// A rejected session answers with a JSON error number, which is a
// *json.UnmarshalTypeError — deliberately NOT matched here.
func isTransientMegaError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var synErr *json.SyntaxError
	if errors.As(err, &synErr) {
		return true
	}
	if errors.Is(err, gomeg.EBADRESP) {
		return true
	}
	return false
}

// megaAdapter forwards megaClient onto *gomeg.Mega.
type megaAdapter struct{ m *gomeg.Mega }

func (a *megaAdapter) Login(email, password string) error {
	return a.m.Login(email, password)
}

func (a *megaAdapter) MultiFactorLogin(email, password, code string) error {
	return a.m.MultiFactorLogin(email, password, code)
}

func (a *megaAdapter) LoginWithKeys(sessionID string, masterKey []byte) error {
	return a.m.LoginWithKeys(sessionID, masterKey)
}

func (a *megaAdapter) Session() (string, []byte) {
	return a.m.GetSessionID(), a.m.GetMasterKey()
}

func (a *megaAdapter) Quota() (uint64, uint64, error) {
	q, err := a.m.GetQuota()
	if err != nil {
		return 0, 0, err
	}
	return q.Cstrg, q.Mstrg, nil
}

func (a *megaAdapter) Root() megaNode {
	return &megaNodeAdapter{n: a.m.FS.GetRoot()}
}

func (a *megaAdapter) Children(n megaNode) ([]megaNode, error) {
	nodes, err := a.m.FS.GetChildren(n.(*megaNodeAdapter).n)
	if err != nil {
		return nil, err
	}
	out := make([]megaNode, len(nodes))
	for i, node := range nodes {
		out[i] = &megaNodeAdapter{n: node}
	}
	return out, nil
}

func (a *megaAdapter) Lookup(root megaNode, names []string) ([]megaNode, error) {
	nodes, err := a.m.FS.PathLookup(root.(*megaNodeAdapter).n, names)
	if err != nil {
		return nil, err
	}
	out := make([]megaNode, len(nodes))
	for i, node := range nodes {
		out[i] = &megaNodeAdapter{n: node}
	}
	return out, nil
}

func (a *megaAdapter) Download(n megaNode) (megaDownload, error) {
	d, err := a.m.NewDownload(n.(*megaNodeAdapter).n)
	if err != nil {
		return nil, err
	}
	return &megaDownloadAdapter{d: d}, nil
}

func (a *megaAdapter) Upload(parent megaNode, name string, size int64) (megaUpload, error) {
	u, err := a.m.NewUpload(parent.(*megaNodeAdapter).n, name, size)
	if err != nil {
		return nil, err
	}
	return &megaUploadAdapter{u: u}, nil
}

func (a *megaAdapter) CreateDir(parent megaNode, name string) (megaNode, error) {
	n, err := a.m.CreateDir(name, parent.(*megaNodeAdapter).n)
	if err != nil {
		return nil, err
	}
	return &megaNodeAdapter{n: n}, nil
}

func (a *megaAdapter) Rename(n megaNode, name string) error {
	return a.m.Rename(n.(*megaNodeAdapter).n, name)
}

func (a *megaAdapter) Move(n megaNode, parent megaNode) error {
	return a.m.Move(n.(*megaNodeAdapter).n, parent.(*megaNodeAdapter).n)
}

func (a *megaAdapter) Delete(n megaNode, destroy bool) error {
	return a.m.Delete(n.(*megaNodeAdapter).n, destroy)
}

func (a *megaAdapter) ShareLink(n megaNode) (string, error) {
	return a.m.Link(n.(*megaNodeAdapter).n, true)
}

// megaNodeAdapter forwards megaNode onto *gomeg.Node.
type megaNodeAdapter struct{ n *gomeg.Node }

func (a *megaNodeAdapter) Type() int       { return a.n.GetType() }
func (a *megaNodeAdapter) Name() string    { return a.n.GetName() }
func (a *megaNodeAdapter) Size() int64     { return a.n.GetSize() }
func (a *megaNodeAdapter) Time() time.Time { return a.n.GetTimeStamp() }
func (a *megaNodeAdapter) Hash() string    { return a.n.GetHash() }

// megaDownloadAdapter forwards megaDownload onto *gomeg.Download.
type megaDownloadAdapter struct{ d *gomeg.Download }

func (a *megaDownloadAdapter) Chunks() int { return a.d.Chunks() }

func (a *megaDownloadAdapter) ChunkLocation(id int) (int64, int, error) {
	return a.d.ChunkLocation(id)
}

func (a *megaDownloadAdapter) DownloadChunk(id int) ([]byte, error) {
	return a.d.DownloadChunk(id)
}

func (a *megaDownloadAdapter) Finish() error { return a.d.Finish() }

// megaUploadAdapter forwards megaUpload onto *gomeg.Upload.
type megaUploadAdapter struct{ u *gomeg.Upload }

func (a *megaUploadAdapter) Chunks() int { return a.u.Chunks() }

func (a *megaUploadAdapter) ChunkLocation(id int) (int64, int, error) {
	return a.u.ChunkLocation(id)
}

func (a *megaUploadAdapter) UploadChunk(id int, chunk []byte) error {
	return a.u.UploadChunk(id, chunk)
}

func (a *megaUploadAdapter) Finish() (megaNode, error) {
	n, err := a.u.Finish()
	if err != nil {
		return nil, err
	}
	return &megaNodeAdapter{n: n}, nil
}
