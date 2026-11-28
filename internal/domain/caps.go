// SPDX-License-Identifier: MPL-2.0
package domain

// Caps is the capability table. UI and the sync engine decide what to
// show and what to do by reading Caps only — e.g. a menu item is hidden
// when the capability is absent, never shown greyed-out and dead.
// Professional features (versions, sharing, encryption, presigned URLs)
// live behind Caps: present when the provider supports them, hidden
// otherwise, and collapsed under "Advanced" by default.
type Caps struct {
	CanMkdir      bool `json:"canMkdir"`
	CanRename     bool `json:"canRename"`
	CanDelete     bool `json:"canDelete"`
	DeleteToTrash bool `json:"deleteToTrash"` // false = permanent delete

	SupportsServerSideCopy  bool `json:"supportsServerSideCopy"`
	SupportsVersions        bool `json:"supportsVersions"`
	SupportsShare           bool `json:"supportsShare"`
	SupportsSearch          bool `json:"supportsSearch"` // server-side search
	SupportsDelta           bool `json:"supportsDelta"`  // incremental cursor
	SupportsThumbnail       bool `json:"supportsThumbnail"`
	SupportsResumableUpload bool `json:"supportsResumableUpload"`
	SupportsEncryption      bool `json:"supportsEncryption"` // SSE / SSE-KMS / SSE-C (S3)
	SupportsStorageClass    bool `json:"supportsStorageClass"`
	SupportsPresignedURL    bool `json:"supportsPresignedUrl"`
	// SupportsMount is whether the mount can be exposed as a
	// folder in the FUSE volume ~/ZyDrive (v0.19.0, P0: only
	// Drive, Dropbox and OneDrive set it — the rest get the
	// switch hidden, never greyed-out, per project rules).
	SupportsMount bool `json:"supportsMount"`

	IDBased bool `json:"idBased"` // true for Drive / OneDrive

	// Limits, all explicit per driver — no global constants.
	MaxFileSize   int64    `json:"maxFileSize"`  // 0 = unknown / unlimited
	MinPartSize   int64    `json:"minPartSize"`  // multipart chunk size
	MaxPathLen    int      `json:"maxPathLen"`   // 0 = unknown
	IllegalChars  string   `json:"illegalChars"` // characters forbidden in names
	ReservedNames []string `json:"reservedNames,omitempty"`
}
