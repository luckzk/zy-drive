// SPDX-License-Identifier: MPL-2.0
// Package storage defines the unified Provider interface, the capability
// table and the registry. Application, sync and transfer code only import
// this package — never a concrete driver package. Driver registration
// happens once, in the entry points (cmd/*).
package storage

import (
	"context"
	"errors"
	"io"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage/upload"
)

// ErrNotImplemented is returned by placeholder drivers (e.g. Dropbox in
// V0.1) and by optional capabilities a driver does not support.
var ErrNotImplemented = errors.New("storage: not implemented")

// ProviderError wraps a driver failure with the operation that failed.
// It never contains secrets: no tokens, no presigned URLs, no keys.
type ProviderError struct {
	Op       string // e.g. "s3.List"
	Provider string
	Err      error
}

func (e *ProviderError) Error() string {
	return "storage: " + e.Provider + "." + e.Op + ": " + e.Err.Error()
}

func (e *ProviderError) Unwrap() error { return e.Err }

// Config is the per-account configuration a driver factory receives.
// Secrets travel here only in memory; they are never logged or persisted
// by this package.
type Config struct {
	Endpoint  string            `json:"endpoint,omitempty"`  // S3-compatible endpoint
	Region    string            `json:"region,omitempty"`    // S3 region
	AccessKey string            `json:"accessKey,omitempty"` // S3 AK
	SecretKey string            `json:"secretKey,omitempty"` // S3 SK, memory only
	Token     string            `json:"token,omitempty"`     // OAuth access token, memory only
	Extra     map[string]string `json:"extra,omitempty"`
	// MountID is the mount this config belongs to ("" while the
	// mount is unsaved). Drivers that persist per-mount state
	// (Drime resume sessions) key it by this id. json:"-" on
	// purpose: the app's test memo key (testMemoKey) hashes the
	// whole Config, and a mount id minted after the probe must
	// not change the memo of the config that was just tested.
	MountID string `json:"-"`
	// ResumeStore persists resumable-upload sessions (SQLite,
	// internal/cache). nil means resume is disabled: a driver
	// that implements ResumableUploader falls back to a fresh
	// session that aborts on failure, exactly like plain Upload.
	// json:"-" for the same memo reason as MountID (it holds a
	// *cache.DB, which must never be marshalled into a hash).
	ResumeStore upload.Store `json:"-"`
}

// Provider is the small core interface every cloud implements. Only
// operations every cloud has belong here; anything else is an optional
// interface below, probed with a type assertion so weak drivers are
// never forced to fake an implementation.
type Provider interface {
	// List returns the direct children of ref. Pagination is consumed
	// inside the driver; callers always get the full listing.
	List(ctx context.Context, ref string) ([]domain.Node, error)
	// Stat returns metadata for one node.
	Stat(ctx context.Context, ref string) (domain.Node, error)
	// OpenRead streams a file for download. Callers must Close it.
	// Cross-cloud copy pipes OpenRead into Upload via io.Pipe so files
	// are never staged on local disk.
	OpenRead(ctx context.Context, ref string) (io.ReadCloser, error)
	// Upload writes a file. size may be -1 when unknown. The driver
	// picks the upload strategy (single-part vs multipart) from Caps.
	Upload(ctx context.Context, ref string, r io.Reader, size int64) (domain.Node, error)
	// Mkdir creates a folder. On S3 (no real folders) this creates a
	// zero-byte placeholder object with a trailing slash.
	Mkdir(ctx context.Context, ref string) error
	// Rename moves/renames within the same provider.
	Rename(ctx context.Context, src, dst string) error
	// Delete removes one node. Trash vs permanent delete is declared
	// in Caps().DeleteToTrash.
	Delete(ctx context.Context, ref string) error
	// Caps returns the static capability table for this provider.
	Caps() domain.Caps
}

// Searcher is the optional server-side search interface.
type Searcher interface {
	Search(ctx context.Context, query string) ([]domain.Node, error)
}

// DeltaSource is the optional incremental-change interface. Drivers
// without it fall back to timed full refresh of a directory.
type DeltaSource interface {
	// Delta returns changed nodes since cursor, plus the new cursor.
	// An empty cursor means "start from now" (no history replay).
	Delta(ctx context.Context, cursor string) (changes []domain.Node, newCursor string, err error)
}

// Sharer is the optional share-link interface. Forms differ per cloud;
// the UI only shows the share menu when this is implemented.
type Sharer interface {
	Share(ctx context.Context, ref string) (url string, err error)
}

// Versioner is the optional history-versions interface.
type Versioner interface {
	Versions(ctx context.Context, ref string) ([]domain.Node, error)
	Restore(ctx context.Context, ref, versionID string) error
}

// Thumber is the optional thumbnail interface.
type Thumber interface {
	Thumbnail(ctx context.Context, ref string) (url string, err error)
}

// QuotaSource is the optional storage-quota interface. Quota
// returns (used, total) in bytes. total <= 0 means the provider
// reports no total (unlimited plan, or the API only reports used):
// callers must not invent a total from it. Drivers that have no
// quota API at all (S3/R2) simply do not implement this interface,
// and the UI says "不限量" instead of showing a made-up number.
type QuotaSource interface {
	Quota(ctx context.Context) (used, total int64, err error)
}

// SeekableSource is what UploadResumable reads from: a local
// file (or any source that supports random access). It needs
// ReadAt for part reads (io.SectionReader) and Seek+Read for
// verification reads (which the app layer deliberately does not
// count toward progress or the limiter). *os.File satisfies it,
// and so does the app layer's resumableReader wrapper.
type SeekableSource interface {
	io.Reader
	io.Seeker
	io.ReaderAt
}

// ResumableUploader is the optional interface for drivers whose
// multipart upload can continue after an interruption (Drime,
// v0.9.1). UploadResumable reads the file from a seekable source
// and uploads only the parts that are not already confirmed on the
// server, resuming a persisted session when one exists; ResumeState
// reports how many bytes are already confirmed uploaded, so the
// caller can seed its progress display. The session (server
// upload id + finished part numbers) lives in Config.ResumeStore
// and survives restarts; without a store the driver falls back to
// a fresh session that aborts on failure, exactly like plain
// Upload. Callers only use this path when the source is seekable
// (a local file): cross-cloud pipe copies keep plain Upload,
// because a pipe cannot re-read the parts it already sent.
type ResumableUploader interface {
	ResumeState(ctx context.Context, ref string, size int64) (doneBytes int64, err error)
	UploadResumable(ctx context.Context, ref string, rs SeekableSource, size int64) (domain.Node, error)
}

// DirectURLer is the optional interface for drivers that can hand
// the browser a short-lived direct download URL for a file
// (presigned URL, temporary link, or the provider's own file
// link). The URL must work without extra headers so <video>,
// <img> and <iframe> can load it directly; it is short-lived by
// nature, so callers fetch it fresh for each preview. Drivers
// without one (Drime, Box, MEGA, local) are served by the
// loopback preview proxy instead, which streams OpenRead.
type DirectURLer interface {
	DirectURL(ctx context.Context, ref string) (string, error)
}

// SessionExporter is the optional interface for drivers whose
// login yields a storable session handle (MEGA, v0.9.1): the app
// persists the exported session instead of the user's password.
// sessionID is the opaque server session; secret is the master
// key, base64-encoded. Both are secrets: they travel only in
// memory and are stored encrypted like any other credential.
type SessionExporter interface {
	ExportSession(ctx context.Context) (sessionID, secret string, err error)
}
