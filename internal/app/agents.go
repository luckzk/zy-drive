package app

import (
	"fmt"

	"github.com/luckzk/zy-drive/internal/domain"
)

// agents.go holds the multi-Agent registry (2026-11-24 decision).
// The desktop keeps the list; each entry is one headless mcm-agent.
// Agents are stored in the cache (agents table) and mirrored in
// memory, exactly like mounts. Secrets are referenced by TokenRef
// only; the API token itself is never held here in plaintext.

// ListAgents returns the configured Agents in creation order.
func (a *App) ListAgents() []*domain.Agent {
	out := make([]*domain.Agent, 0, len(a.agents))
	for _, ag := range a.agents {
		out = append(out, ag)
	}
	return out
}

// AddAgent registers an Agent and persists it. The first Agent added
// becomes the default unless another one already is.
func (a *App) AddAgent(ag *domain.Agent) error {
	if ag.ID == "" {
		return fmt.Errorf("app: agent id is required")
	}
	if ag.Address == "" {
		return fmt.Errorf("app: agent address is required")
	}
	if len(a.agents) == 0 {
		ag.Default = true
	}
	if a.DB != nil {
		if err := a.DB.SaveAgent(ag); err != nil {
			return err
		}
	}
	a.agents[ag.ID] = ag
	return nil
}

// RemoveAgent unregisters an Agent. Jobs naming it keep their AgentID;
// the UI must re-point them before running.
func (a *App) RemoveAgent(id string) error {
	if _, ok := a.agents[id]; !ok {
		return fmt.Errorf("app: unknown agent %q", id)
	}
	delete(a.agents, id)
	if a.DB != nil {
		return a.DB.DeleteAgent(id)
	}
	return nil
}

// DefaultAgent returns the default Agent, or nil when none is set.
func (a *App) DefaultAgent() *domain.Agent {
	for _, ag := range a.agents {
		if ag.Default {
			return ag
		}
	}
	return nil
}

// AgentStatus summarises the Agent list for the sidebar card.
func (a *App) AgentStatus() domain.AgentStatus {
	st := domain.AgentStatus{Total: len(a.agents)}
	for _, ag := range a.agents {
		if ag.Online {
			st.Online++
		}
		if ag.Default {
			st.Default = ag.Name
		}
	}
	return st
}

// AgentForJob resolves which Agent a job runs on: the named Agent,
// the default Agent for RunOnServer, or nil for local jobs.
func (a *App) AgentForJob(job *domain.SyncJob) (*domain.Agent, error) {
	switch job.RunOn {
	case domain.RunOnLocal:
		return nil, nil
	case domain.RunOnAgent:
		ag, ok := a.agents[job.AgentID]
		if !ok {
			return nil, fmt.Errorf("app: job %q names unknown agent %q", job.Name, job.AgentID)
		}
		return ag, nil
	case domain.RunOnServer:
		if job.AgentID != "" {
			ag, ok := a.agents[job.AgentID]
			if !ok {
				return nil, fmt.Errorf("app: job %q names unknown agent %q", job.Name, job.AgentID)
			}
			return ag, nil
		}
		return a.DefaultAgent(), nil
	default:
		return nil, fmt.Errorf("app: unknown runOn %q", job.RunOn)
	}
}
