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
	RootRef     string       `json:"rootRef"`   // root location: path, bucket+prefix or folder ID
	// Group is the sidebar group this mount belongs to (2026-11-24
	// decision). "" means "未分组". Groups are display-only: they
	// never affect tokens, caches or sync.
	Group     string      `json:"group,omitempty"`
	Caps      Caps        `json:"caps"`
	Status    MountStatus `json:"status"`
	CreatedAt time.Time   `json:"createdAt"`
	UpdatedAt time.Time   `json:"updatedAt"`
}
