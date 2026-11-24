package domain

import "time"

// Agent is one server-side execution node (2026-11-24 decision:
// settings hold a *list* of Agents, not a single one). The desktop is
// the control plane; each Agent is a headless mcm-agent process on
// some machine, reachable over HTTPS with an API token. A sync job
// names one Agent by ID (SyncJob.AgentID) or runs locally.
//
// The API token itself is never stored here: TokenRef is the encrypted
// secret's ID in the secrets table, exactly like AccountID for mounts.
type Agent struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`    // e.g. "家里的服务器", "OCI 新加坡"
	Address         string     `json:"address"` // host:port or domain, e.g. "192.168.1.10:8080"
	TokenRef        string     `json:"tokenRef,omitempty"`
	Online          bool       `json:"online"`
	Version         string     `json:"version,omitempty"`
	ProtocolVersion string     `json:"protocolVersion,omitempty"`
	JobsRunning     int        `json:"jobsRunning"`
	LastHeartbeat   *time.Time `json:"lastHeartbeat,omitempty"`
	Default         bool       `json:"default"` // the default executor when a job names no Agent
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

// AgentStatus is the sidebar summary: how many Agents are up.
type AgentStatus struct {
	Total   int    `json:"total"`
	Online  int    `json:"online"`
	Default string `json:"default,omitempty"` // name of the default Agent
}
