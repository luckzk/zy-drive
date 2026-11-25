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

// PartState is one uploaded chunk.
type PartState struct {
	Number int    `json:"number"`
	Size   int64  `json:"size"`
	ETag   string `json:"etag,omitempty"`
	Done   bool   `json:"done"`
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
	RemoteID  string      `json:"remoteId,omitempty"` // driver-side session handle

	mu sync.Mutex
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
