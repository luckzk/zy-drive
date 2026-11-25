// SPDX-License-Identifier: MPL-2.0
package domain

import "time"

// Node is the unified file/folder model. Sync, transfer and UI only ever
// see Node; they never know whether the backend is path-based
// (Dropbox, S3: the path IS the identity) or ID-based
// (Drive, OneDrive: names may repeat, rename does not change the ID).
// Drivers own the path<->ref mapping and persist it in the cache.
type Node struct {
	// Ref is the driver-internal locator: a path for path-based backends,
	// an ID for ID-based backends. Used for operations, never displayed.
	Ref string `json:"ref"`
	// Path is the display logical path, e.g. /照片/2026/.
	Path string `json:"path"`
	// Name is the display name (last path segment).
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	// Size is -1 for directories; never 0 to fake an empty file.
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
	// Hash is only set when the driver can provide one; HashKind names
	// the algorithm. Hashes are never compared across clouds when the
	// algorithms differ (e.g. Dropbox content_hash vs S3 ETag).
	Hash     string `json:"hash,omitempty"`
	HashKind string `json:"hashKind,omitempty"`
	Mime     string `json:"mime,omitempty"`
	// ThumbURL is short-lived; never persisted in long-term cache.
	ThumbURL string `json:"thumbUrl,omitempty"`
	// Extra is a containment zone for vendor-specific fields.
	// Generic logic must never read it.
	Extra map[string]string `json:"extra,omitempty"`
}
