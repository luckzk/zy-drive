package domain

import "time"

// SyncDirection is the semantics of a sync job. They are written down
// and frozen — "mirror" must never silently become "copy".
type SyncDirection string

const (
	// DirectionCopy (Update): copy new/changed files from source to
	// target; extra files on the target are left alone. Never deletes.
	DirectionCopy SyncDirection = "copy"
	// DirectionMirror: target ends up identical to source; extra files
	// on the target are deleted — only after a plan preview with the
	// deletion list confirmed by the user.
	DirectionMirror SyncDirection = "mirror"
	// DirectionBidirectional: both sides are completed from each other
	// using the baseline snapshot from the last sync.
	DirectionBidirectional SyncDirection = "bidirectional"
)

// CompareMode selects how two snapshots are compared.
type CompareMode string

const (
	// CompareSizeTime is the default. Hash is only used when both sides
	// expose a reliable hash with the same algorithm.
	CompareSizeTime CompareMode = "size+time"
	CompareSize     CompareMode = "size"
	CompareChecksum CompareMode = "checksum"
)

// RunOn selects where a job executes. Same sync logic, two executors.
type RunOn string

const (
	RunOnLocal  RunOn = "local"  // desktop itself executes
	RunOnServer RunOn = "server" // Agent executes; desktop only configures (legacy: any Agent)
	// RunOnAgent executes on one specific Agent (SyncJob.AgentID).
	// 2026-11-24 decision: settings hold a list of Agents and a job
	// picks one of them; RunOnServer is kept for stored jobs that
	// predate the list and means "the default Agent".
	RunOnAgent RunOn = "agent"
)

// LocalMountID is the pseudo-mount for a local folder endpoint.
// A SyncJob side with SrcMount/DstMount == LocalMountID uses a
// "local://<absolute path>" ref and the local filesystem driver.
// A local side can only run on the desktop (RunOnLocal); see
// ValidateJob.
const LocalMountID = "local"

// IsLocalRef reports whether a ref points at a local folder.
func IsLocalRef(ref string) bool {
	return len(ref) >= 8 && ref[:8] == "local://"
}

// SyncJob is a persistent sync configuration. Triggering it produces a
// Run; it is not itself an execution.
type SyncJob struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	SrcMount  string        `json:"srcMount"`
	SrcRef    string        `json:"srcRef"`
	DstMount  string        `json:"dstMount"`
	DstRef    string        `json:"dstRef"`
	Direction SyncDirection `json:"direction"`
	Compare   CompareMode   `json:"compare"`
	Exclude   []string      `json:"exclude,omitempty"`  // patterns, applied after scan, before compare
	Schedule  string        `json:"schedule,omitempty"` // cron expression, empty = manual only
	RunOn     RunOn         `json:"runOn"`
	// AgentID names the specific Agent when RunOn is RunOnAgent (or
	// RunOnServer with an explicit choice). Empty means the default
	// Agent. Ignored when RunOn is RunOnLocal.
	AgentID   string     `json:"agentId,omitempty"`
	Enabled   bool       `json:"enabled"`
	LastRunAt *time.Time `json:"lastRunAt,omitempty"`
	LastRunOK bool       `json:"lastRunOk"`
	NextRunAt *time.Time `json:"nextRunAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// RunStatus classifies one execution of a SyncJob.
type RunStatus string

const (
	RunPending   RunStatus = "pending"
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
)

// Run is one actual execution of a SyncJob: plan, progress, result.
// Every Run has a unique ID tying together plan, transfer batch and errors.
type Run struct {
	ID        string       `json:"id"`
	JobID     string       `json:"jobId"`
	Status    RunStatus    `json:"status"`
	Plan      *PlanSummary `json:"plan,omitempty"`
	StartedAt time.Time    `json:"startedAt"`
	EndedAt   *time.Time   `json:"endedAt,omitempty"`
	Error     string       `json:"error,omitempty"`
}

// PlanSummary is the user-visible summary of a plan. The full item list
// lives in sync.Plan; this is what the history page reads.
type PlanSummary struct {
	ToUpload    int `json:"toUpload"`
	ToUpdate    int `json:"toUpdate"`
	ToDelete    int `json:"toDelete"`
	Conflicts   int `json:"conflicts"`
	FilteredOut int `json:"filteredOut"`
}

// RunItemAction is what the plan does with one file.
type RunItemAction string

const (
	ActionUpload   RunItemAction = "upload"
	ActionUpdate   RunItemAction = "update"
	ActionDelete   RunItemAction = "delete"
	ActionConflict RunItemAction = "conflict"
	ActionSkip     RunItemAction = "skip"
)

// RunItem is one line of a run's plan/result, kept forever so a mistaken
// delete can at least be traced to what was deleted and when.
type RunItem struct {
	RunID  string        `json:"runId"`
	Path   string        `json:"path"`
	Action RunItemAction `json:"action"`
	Size   int64         `json:"size"`
	Note   string        `json:"note,omitempty"`
	Done   bool          `json:"done"`
	Error  string        `json:"error,omitempty"`
}
