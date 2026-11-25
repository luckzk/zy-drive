package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/luckzk/zy-drive/internal/agentapi"
	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/netproxy"
)

// agents.go holds the multi-Agent registry (2026-11-24 decision).
// The desktop keeps the list; each entry is one headless zy-drive-agent.
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

// SaveAgent upserts an Agent (add or edit). The first Agent added
// becomes the default unless another one already is.
func (a *App) SaveAgent(ag *domain.Agent) (*domain.Agent, error) {
	if err := a.AddAgent(ag); err != nil {
		return nil, err
	}
	return ag, nil
}

// SetDefaultAgent flags one Agent as default and clears the flag on
// every other Agent, in memory and in SQLite.
func (a *App) SetDefaultAgent(id string) error {
	ag, ok := a.agents[id]
	if !ok {
		return fmt.Errorf("app: unknown agent %q", id)
	}
	for _, other := range a.agents {
		other.Default = false
	}
	ag.Default = true
	if a.DB != nil {
		return a.DB.SaveAgent(ag)
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

// TestAgent calls an Agent's unauthenticated /api/health and reports
// whether it answers and which versions it runs. A reachable Agent
// updates its stored online/version/heartbeat fields; an unreachable
// one is marked offline. No credential is ever sent: health needs no
// token, and the API token (when one is configured) is only used for
// /api/jobs calls.
func (a *App) TestAgent(ctx context.Context, id string) (*AgentTestResult, error) {
	ag, ok := a.agents[id]
	if !ok {
		return nil, fmt.Errorf("app: unknown agent %q", id)
	}
	addr := strings.TrimSpace(ag.Address)
	if addr == "" {
		return nil, fmt.Errorf("app: agent %q has no address", ag.Name)
	}
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(addr, "/")+"/api/health", nil)
	if err != nil {
		return nil, err
	}
	httpClient := netproxy.NewClient(10)
	resp, err := httpClient.Do(req)
	if err != nil {
		ag.Online = false
		if a.DB != nil {
			_ = a.DB.SaveAgent(ag)
		}
		return &AgentTestResult{Online: false, Message: humanTestError(err)}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		ag.Online = false
		if a.DB != nil {
			_ = a.DB.SaveAgent(ag)
		}
		return &AgentTestResult{Online: false, Message: fmt.Sprintf("Agent 返回 HTTP %d", resp.StatusCode)}, nil
	}
	var h agentapi.HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return &AgentTestResult{Online: false, Message: "Agent 返回了无法解析的健康检查结果"}, nil
	}
	now := time.Now().UTC()
	ag.Online = h.Online
	ag.Version = h.AgentVersion
	ag.ProtocolVersion = h.ProtocolVersion
	ag.LastHeartbeat = &now
	if a.DB != nil {
		_ = a.DB.SaveAgent(ag)
	}
	msg := "连接正常"
	if h.ProtocolVersion != "" && h.ProtocolVersion != agentapi.ProtocolVersion {
		msg = fmt.Sprintf("已连接，但协议版本不一致（Agent %s，桌面 %s），请升级 Agent", h.ProtocolVersion, agentapi.ProtocolVersion)
	}
	return &AgentTestResult{Online: h.Online, Version: h.AgentVersion, ProtocolVersion: h.ProtocolVersion, Message: msg}, nil
}

// humanTestError turns a dial/timeout error into plain language.
func humanTestError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return "连接被拒绝：Agent 没在运行，或地址/端口不对"
	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "Client.Timeout"):
		return "连接超时：地址不可达或网络不通"
	case strings.Contains(msg, "no such host"):
		return "找不到这个主机：检查地址是否写对"
	default:
		return "连接失败：" + msg
	}
}
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
