// SPDX-License-Identifier: MPL-2.0
// Package domain holds the core models. It depends on no other internal
// package: every other package may depend on domain, never the reverse.
// If a model starts importing a driver or UI package, the extensibility
// of the whole project breaks at the root.
package domain

import "time"

// ProviderKind identifies a cloud backend. Vendor names are only allowed
// in driver directories, account settings pages and capability tables;
// sync, transfer and generic UI must never branch on these values.
type ProviderKind string

const (
	ProviderS3       ProviderKind = "s3"
	ProviderDropbox  ProviderKind = "dropbox"
	ProviderDrive    ProviderKind = "drive"
	ProviderOneDrive ProviderKind = "onedrive"
	ProviderDrime    ProviderKind = "drime"
	// ProviderLocal is a folder on this machine ("local://..."). It is
	// not a cloud; it only ever runs on the desktop itself.
	ProviderLocal ProviderKind = "local"
	// Reserved for later read-only import (write capabilities all off).
	ProviderFacebook  ProviderKind = "facebook"
	ProviderInstagram ProviderKind = "instagram"
)

// MountStatus is the health of one mount as shown in the sidebar.
type MountStatus string

const (
	MountOK          MountStatus = "ok"
	MountAuthExpired MountStatus = "auth_expired"
	MountOffline     MountStatus = "offline"
	MountError       MountStatus = "error"
)

// Mount is one account of one cloud as shown in the left sidebar, e.g.
// "工作 Drive". The UI is organised by Mount, not by vendor: one account
// may expose several mounts (e.g. two S3 buckets as two entries).
type Mount struct {
	ID          string       `json:"id"`
	DisplayName string       `json:"displayName"`
	Provider    ProviderKind `json:"provider"`
	AccountID   string       `json:"accountId"` // credential reference, never a secret
	// RootRef is the driver ref of the mount root, as the browse/sync/
	// upload code consumes it: "" (provider root) for S3, Dropbox
	// and Drive, the absolute path for local mounts. It is never a
	// bucket name: S3's driver takes the bucket from its own config,
	// so a bucket here would make the root list "bucket/" inside the
	// bucket instead of the bucket itself (fixed 2026-11-25).
	RootRef string `json:"rootRef"`
	// RootLabel is the display root shown in Settings ("bucket" or
	// "bucket/prefix" for S3, the absolute path for local, "" for
	// Dropbox/Drive). Display only: no browse/sync/upload code may
	// use it as a ref.
	RootLabel string `json:"rootLabel,omitempty"`
	// WorkspaceLabel names the Drime workspace this mount is bound
	// to, derived from the stored non-secret config — "个人空间"
	// when no workspace id was given (workspace 0), "工作区 8762"
	// otherwise. Display only, and it is also the scope the quota
	// numbers belong to: the Drime quota endpoint is workspace-
	// scoped (GET /user/space-usage?workspaceId=N), so the sidebar
	// shows this label next to the quota — a number without its
	// workspace is how a team-workspace mount ends up looking like
	// an account total (fixed 2026-11-25). "" for non-Drime mounts.
	WorkspaceLabel string `json:"workspaceLabel,omitempty"`
	// EndpointHost is the host of the S3-compatible endpoint
	// (e.g. "<account>.r2.cloudflarestorage.com"), derived from the
	// stored non-secret config. Display only: the sidebar uses it
	// to pick the R2 (Cloudflare) or AWS S3 brand mark. It is the
	// public endpoint URL, never a credential.
	EndpointHost string `json:"endpointHost,omitempty"`
	// Group is the sidebar group this mount belongs to (2026-11-24
	// decision). "" means "未分组". Groups are display-only: they
	// never affect tokens, caches or sync.
	Group  string      `json:"group,omitempty"`
	Caps   Caps        `json:"caps"`
	Status MountStatus `json:"status"`
	// Verified is whether the mount passed a connection probe at
	// least once (C, one-page form): mounts saved from the wizard
	// after a passed test are verified; mounts saved without a
	// passed test (user chose "先保存、后测试") are unverified and
	// the sidebar + Settings mark them "未验证" until TestMount
	// passes. Mounts saved before this field existed were all
	// probe-verified at save time, so the DB default is 1.
	Verified  bool      `json:"verified"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// MountQuota is one mount's storage usage as returned by the batch
// GetMountQuotas Bind (one call, per-mount isolation: a failure on
// one mount never blocks the others). Used/Total are bytes.
// Unlimited is set when the provider has no quota API at all
// (S3/R2): the UI shows "不限量", never a made-up number.
// IsLocalDisk is set for local-folder mounts: the numbers are the
// disk the folder sits on (total disk size, used disk space), not
// a cloud quota, and the UI says so. Error is non-empty when the
// quota fetch failed for this mount: the UI shows "容量暂不可用"
// for that mount only.
type MountQuota struct {
	MountID     string `json:"mountId"`
	Used        int64  `json:"used"`
	Total       int64  `json:"total"`
	Unlimited   bool   `json:"unlimited"`
	IsLocalDisk bool   `json:"isLocalDisk"`
	Error       string `json:"error,omitempty"`
}
