// SPDX-License-Identifier: MPL-2.0

package drime

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/storage/upload"
)

// Resume: UploadResumable + ResumeState for Drime multipart
// uploads (v0.9.1).
//
// How it works: the first attempt creates a server session
// (uploadId + S3 key) and persists it in SQLite together with
// every finished part number and the MD5 of the bytes that were
// sent. A later attempt (same mount, same ref, same size) loads
// that session, asks the server which parts it still has
// (POST /s3/multipart/get-uploaded-parts), re-hashes the local
// ranges of those parts, and uploads only the parts that are
// missing or whose local bytes changed. On success the session
// row is deleted; on failure it stays, so the next attempt
// resumes again — the session is never aborted by this path.
//
// Drime documents no TTL for multipart upload ids (docs snapshot
// 2026-11-17, verified 2026-11-25): an upload id the server no
// longer knows (400/404/410 from get-uploaded-parts) is treated
// as expired — the local session is discarded and a fresh one is
// created, re-uploading the parts that are missing in the new
// session. The old server-side upload is left to expire on its
// own; nothing is ever deleted from the server by this path.
//
// The MD5 check is what makes resume safe against a file that
// changed between attempts: the session is keyed by (mount, ref,
// size), and a same-size edit would otherwise mix old parts with
// new ones into a corrupt file. The check uses the MD5 recorded
// locally at upload time, never the server's ETag (its format is
// provider-defined), so it works no matter what the server
// returns as an ETag.

// sessionIDFor derives the local session id from (mount, ref,
// size): the same file retried after a restart finds the same
// session. The id is only a local key; the server session is
// RemoteID (uploadId).
func sessionIDFor(mountID, ref string, size int64) string {
	h := sha256.Sum256([]byte(mountID + "\x00" + ref + "\x00" + strconv.FormatInt(size, 10)))
	return "drime-" + hex.EncodeToString(h[:])
}

// resumeLock serialises UploadResumable per ref. The returned
// function releases the lock; callers must defer it.
func (p *Provider) resumeLock(ref string) func() {
	p.resumeMu.Lock()
	mu, ok := p.resumeLocks[ref]
	if !ok {
		mu = &sync.Mutex{}
		p.resumeLocks[ref] = mu
	}
	p.resumeMu.Unlock()
	mu.Lock()
	return func() { mu.Unlock() }
}

// getUploadedParts asks the server which parts of a multipart
// session are already stored. The returned map is part number ->
// server ETag (opaque; used only for the complete call, never
// for verification). A 400/404/410 means the server forgot the
// session: errUploadSessionGone (check with errors.Is).
func (p *Provider) getUploadedParts(ctx context.Context, key, uploadID string) (map[int]string, error) {
	body, err := json.Marshal(map[string]any{"key": key, "uploadId": uploadID})
	if err != nil {
		return nil, p.wrap("Upload", err)
	}
	req, err := p.newRequest(ctx, http.MethodPost, p.apiBase+"/s3/multipart/get-uploaded-parts", bytes.NewReader(body))
	if err != nil {
		return nil, p.wrap("Upload", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(ctx, req)
	if err != nil {
		return nil, p.wrap("Upload", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusGone:
		return nil, errUploadSessionGone
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, p.wrap("Upload", p.readError(resp))
	}
	var out struct {
		Parts []struct {
			PartNumber int    `json:"PartNumber"`
			ETag       string `json:"ETag"`
			Size       int64  `json:"Size"`
		} `json:"parts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, p.wrap("Upload", err)
	}
	done := make(map[int]string, len(out.Parts))
	for _, pt := range out.Parts {
		if pt.PartNumber > 0 {
			done[pt.PartNumber] = pt.ETag
		}
	}
	return done, nil
}

// verifyDoneParts keeps only the server-reported parts whose
// local bytes still match the MD5 recorded when they were sent.
// A part is kept only when BOTH hold: the server has it, and the
// local range hashes to the recorded MD5. Anything else is
// dropped and re-uploaded. The returned map is part number ->
// server ETag, for the complete call.
func (p *Provider) verifyDoneParts(rs storage.SeekableSource, s *upload.Session, server map[int]string) (map[int]string, error) {
	done := make(map[int]string, len(server))
	for n, etag := range server {
		if n < 1 || n > len(s.Parts) {
			continue
		}
		part := s.Parts[n-1]
		if part.MD5 == "" {
			// No local record of what was sent (a session from
			// before MD5 was recorded, or a hand-edited row):
			// unverified means re-upload, never trust.
			continue
		}
		// Seek+Read on purpose, not SectionReader: the app layer
		// wraps the source in a resumableReader that counts only
		// ReadAt (the upload path) toward progress and the
		// limiter. Verification reads are local disk reads — they
		// must not move the progress bar or consume the rate
		// budget. (The part loop below reads through
		// SectionReader, i.e. ReadAt, and is counted.)
		if _, err := rs.Seek(int64(n-1)*partSize, io.SeekStart); err != nil {
			return nil, p.wrap("Upload", err)
		}
		h := md5.New()
		if _, err := io.CopyN(h, rs, part.Size); err != nil {
			return nil, p.wrap("Upload", err)
		}
		if hex.EncodeToString(h.Sum(nil)) == part.MD5 {
			done[n] = etag
		}
		// Mismatch: the local file changed since this part was
		// uploaded (same size, different bytes) — re-upload it.
	}
	return done, nil
}

// ResumeState reports how many bytes are already confirmed
// uploaded for (ref, size), from the persisted session. 0 means
// "nothing to resume" (no store, no session, or an empty one) —
// never an error for a missing session.
func (p *Provider) ResumeState(ctx context.Context, ref string, size int64) (int64, error) {
	if p.store == nil || p.mountID == "" || size < 0 {
		return 0, nil
	}
	s, err := p.store.LoadSession(p.mountID, ref, size)
	if err != nil {
		return 0, p.wrap("Upload", err)
	}
	if s == nil {
		return 0, nil
	}
	return s.DoneBytes(), nil
}

// UploadResumable implements storage.ResumableUploader for Drime.
// It uploads only the parts that are not already confirmed on the
// server, resuming a persisted session when one exists. Small
// files (< 5 MiB), unknown sizes, and mounts without a resume
// store fall back to plain Upload (fresh session, abort on
// failure) — resume is only meaningful for large files from a
// seekable source with a store to remember them in.
func (p *Provider) UploadResumable(ctx context.Context, ref string, rs storage.SeekableSource, size int64) (domain.Node, error) {
	if size < 0 || size < singleUploadLimit || p.store == nil || p.mountID == "" {
		return p.Upload(ctx, ref, rs, size)
	}
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
	mime := mimeOf(name)
	ext := ""
	if i := strings.LastIndex(name, "."); i >= 0 && i < len(name)-1 {
		ext = strings.ToLower(name[i+1:])
	}

	unlock := p.resumeLock(ref)
	defer unlock()

	// Load the persisted session, if any, and reconcile it with
	// the server. The server's part list is the truth for "what
	// is stored"; the local MD5s are the truth for "the bytes
	// are still the same".
	var s *upload.Session
	stored, err := p.store.LoadSession(p.mountID, ref, size)
	if err != nil {
		return domain.Node{}, p.wrap("Upload", err)
	}
	if stored != nil {
		switch {
		case stored.RemoteID == "" || stored.RemoteKey == "" ||
			stored.TotalSize != size || stored.PartSize != partSize:
			// Stale row (an interrupted create that never got a
			// server session, or a part size from an older
			// build): forget it and start fresh below.
			_ = p.store.DeleteSession(p.mountID, ref, size)
		default:
			server, err := p.getUploadedParts(ctx, stored.RemoteKey, stored.RemoteID)
			switch {
			case errors.Is(err, errUploadSessionGone):
				// Session expired server-side: forget it and
				// start fresh below. The old server upload is
				// left to expire on its own.
				_ = p.store.DeleteSession(p.mountID, ref, size)
			case err != nil:
				// Transient failure (network, 5xx, 429): keep
				// the local session and fail the attempt —
				// retrying later is cheaper than rebuilding
				// from scratch.
				return domain.Node{}, err
			default:
				done, err := p.verifyDoneParts(rs, stored, server)
				if err != nil {
					return domain.Node{}, err
				}
				// The verified set is the ONLY truth now: a
				// part the server has but whose bytes changed,
				// and a part the local record claims but the
				// server lost, both go back to not-done and are
				// re-uploaded below. (Merely adding the verified
				// flags would keep the stale Done flags from the
				// loaded row — exactly the silent-corruption
				// case the MD5 check exists to prevent.)
				stored.ResetDone(done)
				if err := p.store.SaveSession(p.mountID, stored); err != nil {
					return domain.Node{}, p.wrap("Upload", err)
				}
				s = stored
			}
		}
	}

	if s == nil {
		key, uploadID, err := p.createMultipart(ctx, name, mime, size, ext, pid)
		if err != nil {
			return domain.Node{}, err
		}
		ns, err := upload.NewSession(sessionIDFor(p.mountID, ref, size), ref, size, partSize)
		if err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		ns.RemoteID = uploadID
		ns.RemoteKey = key
		if err := p.store.SaveSession(p.mountID, ns); err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		s = ns
	}

	// Upload the missing parts, in batches of signBatch. The MD5
	// of each part is recorded BEFORE its PUT and persisted, so a
	// crash between PUT and MarkDone still leaves a verifiable
	// record; after each successful PUT the session is saved
	// again, so a crash loses at most the part in flight.
	var missing []int
	for _, part := range s.Parts {
		if !part.Done {
			missing = append(missing, part.Number)
		}
	}
	for start := 0; start < len(missing); start += signBatch {
		if err := ctx.Err(); err != nil {
			return domain.Node{}, p.wrap("Upload", err)
		}
		end := start + signBatch
		if end > len(missing) {
			end = len(missing)
		}
		batch := missing[start:end]
		urls, err := p.signParts(ctx, s.RemoteKey, s.RemoteID, batch)
		if err != nil {
			return domain.Node{}, err
		}
		for _, n := range batch {
			part := s.Parts[n-1]
			chunk := make([]byte, part.Size)
			sr := io.NewSectionReader(rs, int64(n-1)*partSize, part.Size)
			if _, err := io.ReadFull(sr, chunk); err != nil {
				return domain.Node{}, p.wrap("Upload", err)
			}
			sum := md5.Sum(chunk)
			md5hex := hex.EncodeToString(sum[:])
			s.SetPartMD5(n, md5hex)
			if err := p.store.SaveSession(p.mountID, s); err != nil {
				return domain.Node{}, p.wrap("Upload", err)
			}
			etag, err := p.putPart(ctx, s.RemoteKey, s.RemoteID, n, urls[n], chunk)
			if err != nil {
				return domain.Node{}, err
			}
			s.MarkUploaded(n, etag, md5hex)
			if err := p.store.SaveSession(p.mountID, s); err != nil {
				return domain.Node{}, p.wrap("Upload", err)
			}
		}
	}

	done := make(map[int]string, len(s.Parts))
	for _, part := range s.Parts {
		if part.Done {
			done[part.Number] = part.ETag
		}
	}
	node, err := p.completeAndRegister(ctx, ref, name, mime, ext, pid, size, s.RemoteKey, s.RemoteID, done)
	if err != nil {
		// The session stays persisted: the next attempt resumes
		// from here. Never abort — the server still has the
		// parts, and aborting would throw the work away.
		return domain.Node{}, err
	}
	_ = p.store.DeleteSession(p.mountID, ref, size)
	return node, nil
}
