// Package agentapi is the desktop ↔ Agent protocol. Types are defined
// once here and shared by both ends (desktop client and Agent server),
// so the two sides cannot drift apart. The Agent API has five core
// endpoints plus SSE progress (see server.go).
package agentapi

import (
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
)

// ProtocolVersion is negotiated at startup: desktop calls /api/health
// first and refuses to push jobs when the protocol is incompatible.
const ProtocolVersion = "1"

// EngineVersion identifies the sync/transfer engine build.
const EngineVersion = "0.1.0"

// HealthResponse is GET /api/health.
type HealthResponse struct {
	Online          bool      `json:"online"`
	AgentVersion    string    `json:"agentVersion"`
	ProtocolVersion string    `json:"protocolVersion"`
	EngineVersion   string    `json:"engineVersion"`
	Time            time.Time `json:"time"`
}

// JobRequest is POST /api/jobs and PUT /api/jobs/:id. Credentials are
// referenced, never sent in plaintext: EncryptedToken is ciphertext
// encrypted with the Agent's transport key.
type JobRequest struct {
	Job            domain.SyncJob `json:"job"`
	EncryptedToken string         `json:"encryptedToken,omitempty"`
}

// JobResponse is the stored job as the Agent sees it.
type JobResponse struct {
	Job       domain.SyncJob `json:"job"`
	LastRunAt *time.Time     `json:"lastRunAt,omitempty"`
	NextRunAt *time.Time     `json:"nextRunAt,omitempty"`
}

// RunResponse is one entry of GET /api/jobs/:id/runs.
type RunResponse struct {
	Run   domain.Run       `json:"run"`
	Items []domain.RunItem `json:"items,omitempty"`
}

// Event is one SSE message on GET /api/jobs/:id/events.
type Event struct {
	Type    string `json:"type"` // "progress" | "plan" | "done" | "error"
	RunID   string `json:"runId"`
	Message string `json:"message,omitempty"`
	Done    int64  `json:"done,omitempty"`
	Total   int64  `json:"total,omitempty"`
}
