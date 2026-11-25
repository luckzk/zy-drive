// SPDX-License-Identifier: MPL-2.0
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
// --- Agent API token (V0.9.0) ---
//
// The token is the Agent API's password (set with --token when the
// Agent starts, or ZY_DRIVE_AGENT_TOKEN in its environment). It is
// stored encrypted under "agent:<id>" in the secrets table, exactly
// like mount credentials, and only decrypted in memory when the
// desktop talks to that Agent. It is never the same secret as any
// cloud credential.

// SaveAgentToken stores (or clears, when token is "") an Agent's
// API token and records the reference on the Agent row.
func (a *App) SaveAgentToken(agentID, token string) error {
	ag, ok := a.agents[agentID]
	if !ok {
		return fmt.Errorf("app: unknown agent %q", agentID)
	}
	if a.secrets == nil {
		return fmt.Errorf("app: 凭证存储还没初始化（主密钥缺失），请重启应用再试")
	}
	if strings.TrimSpace(token) == "" {
		if a.DB != nil {
			_ = a.DB.DeleteSecret(agentTokenSecretID(agentID))
		}
		ag.TokenRef = ""
	} else {
		ct, err := a.secrets.Encrypt([]byte(strings.TrimSpace(token)))
		if err != nil {
			return fmt.Errorf("app: 加密 Agent 令牌失败：%w", err)
		}
		if a.DB != nil {
			if err := a.DB.SaveSecret(agentTokenSecretID(agentID), ct); err != nil {
				return err
			}
		}
		ag.TokenRef = agentTokenSecretID(agentID)
	}
	ag.UpdatedAt = time.Now().UTC()
	if a.DB != nil {
		return a.DB.SaveAgent(ag)
	}
	return nil
}

// AgentToken decrypts an Agent's stored API token ("" when none is
// configured — the Agent then runs without auth, local dev only).
func (a *App) AgentToken(agentID string) (string, error) {
	ag, ok := a.agents[agentID]
	if !ok {
		return "", fmt.Errorf("app: unknown agent %q", agentID)
	}
	if ag.TokenRef == "" || a.DB == nil || a.secrets == nil {
		return "", nil
	}
	ct, err := a.DB.LoadSecret(ag.TokenRef)
	if err != nil {
		return "", err
	}
	plain, err := a.secrets.Decrypt(ct)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func agentTokenSecretID(agentID string) string { return "agent:" + agentID }

// agentBaseURL normalizes an Agent address into a base URL.
func agentBaseURL(ag *domain.Agent) string {
	addr := strings.TrimSpace(ag.Address)
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	return strings.TrimSuffix(addr, "/")
}

// agentClient builds a protocol-checked client for one Agent.
func (a *App) agentClient(ctx context.Context, ag *domain.Agent) (*agentapi.Client, error) {
	token, err := a.AgentToken(ag.ID)
	if err != nil {
		return nil, err
	}
	c := agentapi.NewClient(agentBaseURL(ag), token)
	if _, err := c.Health(ctx); err != nil {
		return nil, fmt.Errorf("app: Agent %q 不可达或协议不匹配：%w", ag.Name, err)
	}
	return c, nil
}

// PushJobToAgent pushes a job's config to its Agent (health-checked
// first, protocol version negotiated). Credentials are NOT pushed:
// the Agent gets them only from the encrypted bundle the user
// imports by hand — a push for a job whose mounts are not on the
// Agent yet succeeds, and the run then fails with a clear "请先导入
// 配置包" error instead of silently doing nothing.
func (a *App) PushJobToAgent(ctx context.Context, jobID string) error {
	var job *domain.SyncJob
	for _, j := range a.ListSyncJobs() {
		if j.ID == jobID {
			job = j
			break
		}
	}
	if job == nil {
		return fmt.Errorf("app: unknown job %q", jobID)
	}
	ag, err := a.AgentForJob(job)
	if err != nil {
		return err
	}
	if ag == nil {
		return fmt.Errorf("app: 任务 %q 在本机运行，不用推送到 Agent", job.Name)
	}
	c, err := a.agentClient(ctx, ag)
	if err != nil {
		return err
	}
	if _, err := c.PushJob(ctx, *job, ""); err != nil {
		return fmt.Errorf("app: 推送任务到 Agent %q 失败：%w", ag.Name, err)
	}
	return nil
}

// TriggerAgentJob pushes the latest job config and triggers an
// immediate run on its Agent. The run itself is async; the run
// record appears in ListJobRuns once the Agent reports it.
func (a *App) TriggerAgentJob(ctx context.Context, jobID string) error {
	if err := a.PushJobToAgent(ctx, jobID); err != nil {
		return err
	}
	var job *domain.SyncJob
	for _, j := range a.ListSyncJobs() {
		if j.ID == jobID {
			job = j
			break
		}
	}
	ag, err := a.AgentForJob(job)
	if err != nil {
		return err
	}
	c, err := a.agentClient(ctx, ag)
	if err != nil {
		return err
	}
	if err := c.RunJob(ctx, jobID); err != nil {
		return fmt.Errorf("app: 在 Agent %q 上启动任务失败：%w", ag.Name, err)
	}
	return nil
}

// ListJobRuns returns a job's run history. Local jobs read the
// local DB; Agent jobs ask the Agent (its DB is the source of truth
// for its runs — the desktop never guesses whether a scheduled run
// happened).
func (a *App) ListJobRuns(ctx context.Context, jobID string) ([]agentapi.RunResponse, error) {
	var job *domain.SyncJob
	for _, j := range a.ListSyncJobs() {
		if j.ID == jobID {
			job = j
			break
		}
	}
	if job == nil {
		return nil, fmt.Errorf("app: unknown job %q", jobID)
	}
	if job.RunOn == domain.RunOnLocal || a.DB == nil {
		if a.DB == nil {
			return []agentapi.RunResponse{}, nil
		}
		runs, err := a.DB.ListRuns(jobID, 50)
		if err != nil {
			return nil, err
		}
		out := make([]agentapi.RunResponse, 0, len(runs))
		for _, r := range runs {
			items, err := a.DB.ListRunItems(r.ID)
			if err != nil {
				return nil, err
			}
			out = append(out, agentapi.RunResponse{Run: *r, Items: items})
		}
		return out, nil
	}
	ag, err := a.AgentForJob(job)
	if err != nil {
		return nil, err
	}
	if ag == nil {
		return nil, fmt.Errorf("app: 任务 %q 没有指定 Agent", job.Name)
	}
	c, err := a.agentClient(ctx, ag)
	if err != nil {
		return nil, err
	}
	return c.ListRuns(ctx, jobID)
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
