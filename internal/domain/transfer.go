package domain

import "time"

// TransferKind is what a Transfer moves.
type TransferKind string

const (
	TransferUpload   TransferKind = "upload"
	TransferDownload TransferKind = "download"
	TransferCopy     TransferKind = "copy" // cross-cloud copy, streamed, never staged on disk
)

// TransferStatus is persisted to SQLite so a killed process can resume
// or clearly mark the transfer interrupted — never pretend it is running.
type TransferStatus string

const (
	TransferQueued      TransferStatus = "queued"
	TransferRunning     TransferStatus = "running"
	TransferPaused      TransferStatus = "paused"
	TransferCompleted   TransferStatus = "completed"
	TransferFailed      TransferStatus = "failed"
	TransferCancelled   TransferStatus = "cancelled"
	TransferInterrupted TransferStatus = "interrupted"
)

// Transfer is one one-off move: an upload, a download or a cross-cloud
// copy. Sync jobs decompose their plans into Transfers, so pause, speed
// limit, retry and progress exist in exactly one implementation.
type Transfer struct {
	ID       string         `json:"id"`
	Kind     TransferKind   `json:"kind"`
	JobID    string         `json:"jobId,omitempty"` // owning SyncJob, if any
	RunID    string         `json:"runId,omitempty"`
	SrcMount string         `json:"srcMount"`
	SrcRef   string         `json:"srcRef"`
	DstMount string         `json:"dstMount"`
	DstRef   string         `json:"dstRef"`
	Size     int64          `json:"size"`
	Done     int64          `json:"done"`
	Status   TransferStatus `json:"status"`
	SpeedBps int64          `json:"speedBps"`
	Retries  int            `json:"retries"`
	Error    string         `json:"error,omitempty"`
	// SessionID references a resumable upload session persisted by the
	// transfer engine, so an interrupted upload continues, not restarts.
	SessionID string    `json:"sessionId,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
