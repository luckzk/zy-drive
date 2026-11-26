// SPDX-License-Identifier: MPL-2.0
// Package upload provides the resumable-upload session skeleton shared
// by all drivers. A session (ID + uploaded parts) is persisted by the
// transfer engine, so an interrupted upload continues from the last
// confirmed part instead of restarting from zero. Part sizes come from
// the driver's Caps, never from a global constant.
package upload

import (
	"context"
	"errors"
	"io"
	"sync"
)

// PartState is one uploaded chunk. MD5 is the hex MD5 of the
// part's bytes as they were sent (recorded BEFORE the PUT, so a
// crash between PUT and MarkDone still leaves a verifiable
// record): on resume the driver re-hashes the local range and
// only trusts a server-reported part when the hashes match, so a
// file edited in place between attempts (same size, different
// bytes) can never silently mix old and new parts. The server's
// ETag is NOT used for this check — its format is provider-
// defined (R2 returns an MD5, others may not), and the local MD5
// works either way. Empty MD5 means "unverified": re-upload.
type PartState struct {
	Number int    `json:"number"`
	Size   int64  `json:"size"`
	ETag   string `json:"etag,omitempty"`
	Done   bool   `json:"done"`
	MD5    string `json:"md5,omitempty"`
}

// Session is a resumable upload. Drivers translate it to their own
// session API (S3 MultipartUploadId, Dropbox upload_session_id,
// Drive resumable URI, OneDrive uploadSession URL).
type Session struct {
	ID        string      `json:"id"`
	Ref       string      `json:"ref"`
	PartSize  int64       `json:"partSize"`
	TotalSize int64       `json:"totalSize"`
	Parts     []PartState `json:"parts"`
	RemoteID  string      `json:"remoteId,omitempty"`  // driver-side session handle (Drime uploadId)
	RemoteKey string      `json:"remoteKey,omitempty"` // driver-side object key (Drime S3 key)

	mu sync.Mutex
}

// Store persists upload sessions across restarts. Keys are
// (mountID, ref, size): the same file uploaded again after a
// restart resumes the same session instead of starting over.
// LoadSession returns (nil, nil) when no session exists — that is
// the normal "nothing to resume" case, not an error. All methods
// must be safe for concurrent use. (No context: the SQLite store
// in internal/cache is context-free, like the rest of that
// package; a driver that needs cancellation cancels its own
// network calls, not the local write.)
type Store interface {
	SaveSession(mountID string, s *Session) error
	LoadSession(mountID, ref string, size int64) (*Session, error)
	DeleteSession(mountID, ref string, size int64) error
}

// NextPart returns the first part not yet confirmed done, or -1.
func (s *Session) NextPart() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.Parts {
		if !p.Done {
			return i
		}
	}
	return -1
}

// MarkDone records a completed part.
func (s *Session) MarkDone(number int, etag string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Parts {
		if s.Parts[i].Number == number {
			s.Parts[i].Done = true
			s.Parts[i].ETag = etag
		}
	}
}

// SetPartMD5 records the MD5 of a part's bytes before it is sent,
// so a crash between the PUT and MarkDone still leaves a
// verifiable record. Done is left untouched.
func (s *Session) SetPartMD5(number int, md5sum string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Parts {
		if s.Parts[i].Number == number {
			s.Parts[i].MD5 = md5sum
		}
	}
}

// MarkUploaded records a completed part together with the MD5 of
// the bytes that were sent (see PartState.MD5).
func (s *Session) MarkUploaded(number int, etag, md5sum string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Parts {
		if s.Parts[i].Number == number {
			s.Parts[i].Done = true
			s.Parts[i].ETag = etag
			s.Parts[i].MD5 = md5sum
		}
	}
}

// ResetDone replaces the done set with exactly the given parts
// (part number -> ETag). Parts not in the map become not-done
// with an empty ETag. The driver calls this after reconciling a
// persisted session with the server: a part the server has but
// whose local bytes changed, and a part the local record claims
// but the server lost, both go back to not-done. The MD5 is left
// alone — it records what was last sent for that part, and the
// next upload of the part overwrites it.
func (s *Session) ResetDone(done map[int]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Parts {
		if etag, ok := done[s.Parts[i].Number]; ok {
			s.Parts[i].Done = true
			s.Parts[i].ETag = etag
		} else {
			s.Parts[i].Done = false
			s.Parts[i].ETag = ""
		}
	}
}

// DoneBytes returns how many bytes are already confirmed uploaded.
func (s *Session) DoneBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for _, p := range s.Parts {
		if p.Done {
			n += p.Size
		}
	}
	return n
}

// NewSession splits totalSize into parts of partSize (last part may be
// smaller). partSize must come from the driver's Caps().MinPartSize.
func NewSession(id, ref string, totalSize, partSize int64) (*Session, error) {
	if partSize <= 0 {
		return nil, errors.New("upload: partSize must be > 0")
	}
	if totalSize < 0 {
		return nil, errors.New("upload: totalSize must be >= 0")
	}
	var parts []PartState
	var off int64
	num := 1
	for off < totalSize {
		sz := partSize
		if off+sz > totalSize {
			sz = totalSize - off
		}
		parts = append(parts, PartState{Number: num, Size: sz})
		off += sz
		num++
	}
	return &Session{ID: id, Ref: ref, PartSize: partSize, TotalSize: totalSize, Parts: parts}, nil
}

// Uploader is the driver-side end of a session: it uploads one part and
// completes the session. Kept intentionally small — drivers implement
// it against their own multipart APIs.
type Uploader interface {
	UploadPart(ctx context.Context, s *Session, part PartState, r io.Reader) (etag string, err error)
	Complete(ctx context.Context, s *Session) error
	Abort(ctx context.Context, s *Session) error
}
