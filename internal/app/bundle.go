// SPDX-License-Identifier: MPL-2.0
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/luckzk/zy-drive/internal/agentapi"
	"github.com/luckzk/zy-drive/internal/domain"
	"github.com/luckzk/zy-drive/internal/storage"
)

// bundle.go is the encrypted config bundle: the ONLY way cloud
// credentials move from the desktop to a self-hosted Agent.
//
// The model (docs/monetization.md, user decision 2026-11-25):
// Agent scheduled runs are a Pro feature sold as functionality —
// we do not run a server, there is no account/subscription system,
// and credentials never pass through any of our machines. The user
// exports this bundle on the desktop, copies the file to their own
// server themselves (scp / USB / ...), and runs
// `zy-drive-agent import --file <bundle>` there, typing the same
// passphrase. The bundle is AES-256-GCM encrypted under a
// passphrase-derived key (PBKDF2-SHA256, 100k iterations); the
// Agent re-encrypts each credential with its own master key on
// import and never stores the passphrase.

// ExportResult is the single return value of ExportAgentBundle:
// the bundle file path, plus the jobs that were NOT exported (with
// the reason each) — a job that touches a local folder cannot run
// on a server, so it is skipped openly instead of failing the
// whole export or silently vanishing.
type ExportResult struct {
	Path    string   `json:"path"`
	Skipped []string `json:"skipped,omitempty"`
}

// ExportAgentBundle builds the encrypted bundle for one Agent —
// its jobs plus every mount those jobs touch — and writes it to a
// file. It returns the file path; the desktop shows it so the user
// knows what to copy to the server. Jobs that run locally are
// never included (they cannot run on a server), and neither are
// local-folder mounts (a server has no access to this Mac's disk):
// a job whose source or target is a local folder is skipped with a
// reason in ExportResult.Skipped.
func (a *App) ExportAgentBundle(ctx context.Context, agentID, passphrase string) (*ExportResult, error) {
	ag, ok := a.agents[agentID]
	if !ok {
		return nil, fmt.Errorf("app: unknown agent %q", agentID)
	}
	if a.secrets == nil {
		return nil, fmt.Errorf("app: 凭证存储还没初始化（主密钥缺失），请重启应用再试")
	}
	jobs := a.jobsForAgent(agentID)
	if len(jobs) == 0 {
		return nil, fmt.Errorf("app: Agent %q 还没有同步任务，先建一个跑在它上面的任务再导出", ag.Name)
	}
	res := &ExportResult{}
	var exportable []*domain.SyncJob
	for _, j := range jobs {
		if reason := a.jobAgentBlocker(j); reason != "" {
			res.Skipped = append(res.Skipped, fmt.Sprintf("任务 %q 没导出：%s", j.Name, reason))
			continue
		}
		exportable = append(exportable, j)
	}
	if len(exportable) == 0 {
		return nil, fmt.Errorf("app: Agent %q 的任务都用到了本地文件夹，服务器上跑不了，没有可导出的任务", ag.Name)
	}
	// Every mount the exportable jobs touch (cloud mounts only).
	mountIDs := map[string]bool{}
	for _, j := range exportable {
		for _, id := range []string{j.SrcMount, j.DstMount} {
			if id != "" && id != domain.LocalMountID {
				mountIDs[id] = true
			}
		}
	}
	persisted := map[string]persistedMount{}
	if a.DB != nil {
		for _, pm := range a.listPersistedMounts() {
			persisted[pm.Mount.ID] = pm
		}
	}
	bundle := &agentapi.Bundle{AgentName: ag.Name}
	for id := range mountIDs {
		h, ok := a.mounts[id]
		if !ok {
			return nil, fmt.Errorf("app: 任务用到的网盘 %q 在本机上找不到，先检查它还在不在", id)
		}
		pm, ok := persisted[id]
		if !ok {
			return nil, fmt.Errorf("app: 网盘 %q 没有保存的配置，导出不了", h.Mount.DisplayName)
		}
		creds, found, err := a.decryptMountCreds(h.Mount.AccountID)
		if err != nil {
			return nil, fmt.Errorf("app: 读网盘 %q 的凭证失败：%w", h.Mount.DisplayName, err)
		}
		if !found {
			return nil, fmt.Errorf("app: 网盘 %q 没有保存的凭证，导出不了", h.Mount.DisplayName)
		}
		bundle.Mounts = append(bundle.Mounts, agentapi.BundleMount{
			Mount:  *h.Mount,
			Config: pm.Config,
			Credentials: agentapi.BundleCredentials{
				AccessKey: creds.AccessKey, SecretKey: creds.SecretKey,
				AccessToken: creds.AccessToken, RefreshToken: creds.RefreshToken,
				ClientSecret: creds.ClientSecret,
				MegaSession:  creds.MegaSession, MegaMasterKey: creds.MegaMasterKey,
			},
		})
	}
	for _, j := range exportable {
		bundle.Jobs = append(bundle.Jobs, *j)
	}
	data, err := agentapi.EncryptBundle(bundle, passphrase)
	if err != nil {
		return nil, err
	}
	dir := ""
	if s, err := a.GetSettings(); err == nil && strings.TrimSpace(s.DownloadDir) != "" {
		dir = s.DownloadDir
	}
	if dir == "" {
		dir = os.TempDir()
	}
	name := fmt.Sprintf("zy-drive-agent-bundle-%s-%s.json", agentID, time.Now().Format("20060102-150405"))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, fmt.Errorf("app: 写配置包失败：%w", err)
	}
	res.Path = path
	return res, nil
}

// jobAgentBlocker returns why a job cannot run on a server Agent
// ("" when it can): either side is a local folder — the pseudo
// mount "local", or a mount whose provider is local. Such jobs
// stay desktop-only; the export skips them openly.
func (a *App) jobAgentBlocker(j *domain.SyncJob) string {
	for _, id := range []string{j.SrcMount, j.DstMount} {
		if id == domain.LocalMountID {
			return "用到了本地文件夹，服务器上没有这台电脑的磁盘"
		}
		if h, ok := a.mounts[id]; ok && h.Mount.Provider == domain.ProviderLocal {
			return fmt.Sprintf("用到了本地文件夹网盘 %q，服务器上没有这台电脑的磁盘", h.Mount.DisplayName)
		}
	}
	return ""
}

// jobsForAgent returns the stored jobs that execute on one Agent:
// RunOn=agent with that AgentID, plus RunOn=server jobs when this
// Agent is the default (RunOnServer predates the Agent list and
// means "the default Agent").
func (a *App) jobsForAgent(agentID string) []*domain.SyncJob {
	var out []*domain.SyncJob
	def := a.DefaultAgent()
	for _, j := range a.ListSyncJobs() {
		if j.RunOn == domain.RunOnLocal {
			continue
		}
		if j.AgentID == agentID {
			out = append(out, j)
			continue
		}
		if j.RunOn == domain.RunOnServer && j.AgentID == "" && def != nil && def.ID == agentID {
			out = append(out, j)
		}
	}
	return out
}

type persistedMount struct {
	Mount  *domain.Mount
	Config map[string]string
}

func (a *App) listPersistedMounts() []persistedMount {
	if a.DB == nil {
		return nil
	}
	persisted, err := a.DB.ListMounts()
	if err != nil {
		return nil
	}
	out := make([]persistedMount, 0, len(persisted))
	for _, pm := range persisted {
		out = append(out, persistedMount{Mount: pm.Mount, Config: pm.Config})
	}
	return out
}

// ImportBundle decrypts a bundle file and installs its mounts and
// jobs into this App's database. Every mount is built first (any
// failure aborts the whole import — no half-imported state); then
// each credential is re-encrypted with this machine's master key
// and the mount row + secret are saved in one transaction, and the
// live provider is swapped in. Jobs are validated and saved, and
// the caller (the Agent runner) re-registers them with the
// scheduler. Existing mounts/jobs with the same ID are replaced.
func (a *App) ImportBundle(data []byte, passphrase string) (*agentapi.Bundle, error) {
	if a.secrets == nil {
		return nil, fmt.Errorf("app: 凭证存储还没初始化（主密钥缺失），请重启应用再试")
	}
	bundle, err := agentapi.DecryptBundle(data, passphrase)
	if err != nil {
		return nil, err
	}
	// Phase 1 — build every provider before persisting anything.
	type prepared struct {
		mount  domain.Mount
		config map[string]string
		creds  mountCredentials
		prov   storage.Provider
	}
	preparedMounts := make([]prepared, 0, len(bundle.Mounts))
	for _, bm := range bundle.Mounts {
		if bm.Mount.Provider == domain.ProviderLocal {
			return nil, fmt.Errorf("app: 配置包里有本地文件夹网盘 %q，服务器上没有这台机器的磁盘，导不了", bm.Mount.DisplayName)
		}
		cfg := storage.Config{
			Endpoint:  bm.Config["endpoint"],
			Region:    bm.Config["region"],
			AccessKey: bm.Credentials.AccessKey,
			SecretKey: bm.Credentials.SecretKey,
			Token:     bm.Credentials.AccessToken,
			Extra:     map[string]string{},
		}
		for k, v := range bm.Config {
			cfg.Extra[k] = v
		}
		cfg.Extra["refresh_token"] = bm.Credentials.RefreshToken
		cfg.Extra["client_secret"] = bm.Credentials.ClientSecret
		// MEGA: the stored session (never the password).
		if bm.Credentials.MegaSession != "" {
			cfg.Extra["mega_session_id"] = bm.Credentials.MegaSession
		}
		if bm.Credentials.MegaMasterKey != "" {
			cfg.Extra["mega_master_key"] = bm.Credentials.MegaMasterKey
		}
		a.mountRuntime(&cfg, bm.Mount.ID)
		p, err := a.Registry.New(bm.Mount.Provider, cfg)
		if err != nil {
			return nil, fmt.Errorf("app: 配置包里的网盘 %q 建不起来：%w", bm.Mount.DisplayName, err)
		}
		creds := mountCredentials{
			AccessKey: bm.Credentials.AccessKey, SecretKey: bm.Credentials.SecretKey,
			AccessToken: bm.Credentials.AccessToken, RefreshToken: bm.Credentials.RefreshToken,
			ClientSecret: bm.Credentials.ClientSecret,
			MegaSession:  bm.Credentials.MegaSession, MegaMasterKey: bm.Credentials.MegaMasterKey,
		}
		preparedMounts = append(preparedMounts, prepared{mount: bm.Mount, config: bm.Config, creds: creds, prov: p})
	}
	// Phase 2 — persist + swap in.
	for _, pm := range preparedMounts {
		m := pm.mount
		m.Status = domain.MountOK
		m.UpdatedAt = time.Now().UTC()
		if a.DB != nil {
			plain, err := json.Marshal(pm.creds)
			if err != nil {
				return nil, fmt.Errorf("app: 导入网盘 %q：%w", m.DisplayName, err)
			}
			ct, err := a.secrets.Encrypt(plain)
			if err != nil {
				return nil, fmt.Errorf("app: 导入网盘 %q：加密凭证失败：%w", m.DisplayName, err)
			}
			if err := a.DB.SaveMountAndSecret(&m, pm.config, secretIDFor(m.ID), ct); err != nil {
				return nil, fmt.Errorf("app: 导入网盘 %q：%w", m.DisplayName, err)
			}
		}
		a.mounts[m.ID] = MountHandle{Mount: &m, Provider: pm.prov}
	}
	for i := range bundle.Jobs {
		j := bundle.Jobs[i]
		if _, err := a.SaveSyncJob(&j); err != nil {
			return nil, fmt.Errorf("app: 导入任务 %q：%w", j.Name, err)
		}
	}
	return bundle, nil
}
