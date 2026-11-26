// SPDX-License-Identifier: MPL-2.0
package mega

import (
	"context"
	"io"
)

// downloadReader streams a MEGA download chunk by chunk and
// supports Seek: seeking re-fetches only the chunk that contains
// the new offset, so a preview Range request does not
// re-download the file from the start. Chunks are ≤ 1 MiB and
// ChunkLocation is a pure local computation in the library, so
// the chunk boundaries are resolved once up front.
type downloadReader struct {
	ctx    context.Context
	dl     megaDownload
	size   int64
	pos    int64
	bounds []chunkBound // chunk i starts at bounds[i].pos, length bounds[i].size
	curID  int
	cur    []byte // the currently loaded chunk's bytes (nil = not loaded)
	closed bool
}

type chunkBound struct {
	pos  int64
	size int
}

func newDownloadReader(ctx context.Context, dl megaDownload, size int64) (*downloadReader, error) {
	n := dl.Chunks()
	bounds := make([]chunkBound, n)
	for i := 0; i < n; i++ {
		pos, size, err := dl.ChunkLocation(i)
		if err != nil {
			return nil, err
		}
		bounds[i] = chunkBound{pos: pos, size: size}
	}
	return &downloadReader{ctx: ctx, dl: dl, size: size, bounds: bounds, curID: -1}, nil
}

// chunkFor returns the chunk index containing pos.
func (r *downloadReader) chunkFor(pos int64) int {
	// The common case is sequential reading: the current chunk
	// still contains the position.
	if r.curID >= 0 && r.curID < len(r.bounds) {
		b := r.bounds[r.curID]
		if pos >= b.pos && pos < b.pos+int64(b.size) {
			return r.curID
		}
	}
	for i, b := range r.bounds {
		if pos >= b.pos && pos < b.pos+int64(b.size) {
			return i
		}
	}
	return -1
}

func (r *downloadReader) loadChunk(id int) error {
	if r.curID == id && r.cur != nil {
		return nil
	}
	if err := r.ctx.Err(); err != nil {
		return err
	}
	chunk, err := r.dl.DownloadChunk(id)
	if err != nil {
		return err
	}
	r.curID = id
	r.cur = chunk
	return nil
}

func (r *downloadReader) Read(p []byte) (int, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	if r.pos >= r.size {
		return 0, io.EOF
	}
	id := r.chunkFor(r.pos)
	if id < 0 {
		return 0, io.EOF
	}
	if err := r.loadChunk(id); err != nil {
		return 0, err
	}
	b := r.bounds[id]
	off := int(r.pos - b.pos)
	n := copy(p, r.cur[off:])
	r.pos += int64(n)
	// The chunk is exhausted: drop it so the next Read fetches
	// the following chunk (a stale chunk must never be served
	// twice).
	if off+n >= len(r.cur) {
		r.cur = nil
	}
	return n, nil
}

func (r *downloadReader) Seek(offset int64, whence int) (int64, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	var pos int64
	switch whence {
	case io.SeekStart:
		pos = offset
	case io.SeekCurrent:
		pos = r.pos + offset
	case io.SeekEnd:
		pos = r.size + offset
	default:
		return 0, io.ErrClosedPipe
	}
	if pos < 0 {
		pos = 0
	}
	if pos > r.size {
		pos = r.size
	}
	// Invalidate the loaded chunk when the new position is
	// outside it; the next Read re-fetches the right chunk.
	if r.cur != nil {
		b := r.bounds[r.curID]
		if pos < b.pos || pos >= b.pos+int64(b.size) {
			r.cur = nil
		}
	}
	r.pos = pos
	return pos, nil
}

func (r *downloadReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	// Finish verifies the chunk MACs when the whole file was
	// downloaded, and returns nil for a partial download — so a
	// cancelled preview does not surface a spurious error.
	return r.dl.Finish()
}
