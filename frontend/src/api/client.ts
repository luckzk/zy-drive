// SPDX-License-Identifier: MIT
// api client — the only layer allowed to talk to the Go backend.
// Desktop (mygo window): calls go through mygo.Bind via the generated
// client in ../mygo.ts (DesktopService, from internal/app/bind.go).
// Dev shell: plain HTTP to the cmd/desktop dev server on
// 127.0.0.1:18080 — only /api/health exists there, so the Bind methods
// throw outside a mygo window. Callers must catch that and show a
// human error, never fake data. The UI never calls a cloud API
// directly.
import { DesktopService } from '../mygo'
import type { AddMountInput, Agent, AgentStatus, AgentTestResult, BatchResult, DrimeWorkspace, ExportResult, Mount, MountQuota, Node, OAuthStart, OAuthTokens, Plan, ProviderKind, Run, RunResponse, SearchResult, Settings, SyncJob, Transfer, UploadResult, UploadSkipped, VersionInfo } from '../mygo'

export type { AddMountInput, Agent, AgentStatus, AgentTestResult, BatchResult, DrimeWorkspace, ExportResult, Mount, MountQuota, Node, OAuthStart, OAuthTokens, Plan, ProviderKind, Run, RunResponse, SearchResult, Settings, SyncJob, Transfer, UploadResult, UploadSkipped, VersionInfo }

async function get<T>(path: string): Promise<T> {
  const res = await fetch(path)
  if (!res.ok) throw new Error(`api ${path}: ${res.status}`)
  return res.json() as Promise<T>
}

function nonNull<T>(xs: (T | null)[] | null | undefined): T[] {
  return (xs ?? []).filter((x): x is T => x !== null)
}

/** friendlyError turns a backend/Bind error into plain language. */
export function friendlyError(e: unknown): string {
  const msg = e instanceof Error ? e.message : String(e)
  const m = msg.replace(/^(app|cache|sync|transfer|storage):\s*/g, '')
  if (/mygo\s*:\s*runtime|runtime not found|not in a mygo|mygo runtime|bind|WebView|ipc/i.test(msg) || /Failed to fetch|NetworkError|Load failed/i.test(msg)) {
    return '未能连接到桌面后端。请在 ZyDrive 桌面窗口中打开，不要用浏览器直接访问。'
  }
  if (/auth_expired|unauthorized|401|token expired|reauthor/i.test(m)) return '授权已过期，请重新授权后再试。'
  if (/unknown mount/i.test(m)) return '找不到这个网盘，它可能已被移除。'
  if (/unknown agent/i.test(m)) return '找不到这个 Agent，它可能已被删除。'
  if (/connection refused/i.test(m)) return '连接被拒绝：对方服务没在运行，或地址/端口不对。'
  if (/i\/o timeout|timeout/i.test(m)) return '连接超时：网络不通或对方没有响应。'
  if (/no such host/i.test(m)) return '找不到这个主机：检查地址是否写对。'
  return m || '操作失败，请重试。'
}

/** SideSel is one side of a sync job as the UI holds it. */
export interface SideSel {
  /** mount id, or 'local' for a local folder */
  mountId: string
  /** driver ref; for local sides a 'local://<abs path>' ref */
  ref: string
  /** human label, e.g. 'DisplayName / path' or '本地 /abs/path' */
  label: string
  /** Multi-selected entries on this side (batch creation): each is
      a driver ref (or 'local://<abs path>' for local sides). A
      folder entry is recursive — it contains all its contents.
      Empty or absent = no multi-select; the single sync root
      (ref) rules. One entry per job at creation time (SyncJob is
      single-source / single-target), so a batch is N jobs. */
  multi?: { ref: string; label: string }[]
}

export const api = {
  health: () => get<{ ok: boolean; version: string }>('/api/health'),
  listMounts: async () => nonNull(await DesktopService.listMounts()),
  mountQuotas: async () => nonNull(await DesktopService.mountQuotas()),
  browse: async (mountID: string, ref: string) => nonNull(await DesktopService.browse(mountID, ref)),
  search: (mountID: string, query: string) => DesktopService.search(mountID, query),
  previewSync: (job: SyncJob) => DesktopService.previewSync(job),
  runSync: (job: SyncJob, plan: Plan) => DesktopService.runSync(job, plan),
  transfers: async () => nonNull(await DesktopService.transfers()),
  uploadFiles: async (mountID: string, ref: string, paths: string[]): Promise<{ transfers: Transfer[]; skipped: UploadSkipped[] }> => {
    const r = await DesktopService.uploadFiles(mountID, ref, paths)
    if (!r) return { transfers: [], skipped: [] }
    return { transfers: nonNull(r.transfers), skipped: nonNull(r.skipped) }
  },
  listAgents: async () => nonNull(await DesktopService.listAgents()),
  agentStatus: () => DesktopService.agentStatus(),
  validateJob: (job: SyncJob) => DesktopService.validateJob(job),
  listSyncJobs: async () => nonNull(await DesktopService.listSyncJobs()),
  saveSyncJob: (job: SyncJob) => DesktopService.saveSyncJob(job),
  deleteSyncJob: (id: string) => DesktopService.deleteSyncJob(id),
  saveAgent: (ag: Agent) => DesktopService.saveAgent(ag),
  removeAgent: (id: string) => DesktopService.removeAgent(id),
  setDefaultAgent: (id: string) => DesktopService.setDefaultAgent(id),
  testAgent: (id: string) => DesktopService.testAgent(id),
  updateMountGroup: (id: string, group: string) => DesktopService.updateMountGroup(id, group),
  updateMountRoot: (id: string, bucket: string, prefix: string) => DesktopService.updateMountRoot(id, bucket, prefix),
  mkdir: (mountID: string, ref: string) => DesktopService.mkdir(mountID, ref),
  addMount: (input: AddMountInput) => DesktopService.addMount(input),
  removeMount: (id: string) => DesktopService.removeMount(id),
  testMountConfig: (input: AddMountInput) => DesktopService.testMountConfig(input),
  testMount: (id: string) => DesktopService.testMount(id),
  beginOAuth: (provider: ProviderKind, clientID: string) => DesktopService.beginOAuth(provider, clientID),
  pollOAuthCallback: (state: string) => DesktopService.pollOAuthCallback(state),
  cancelOAuth: (state: string) => DesktopService.cancelOAuth(state),
  builtinOAuthClientID: (provider: ProviderKind) => DesktopService.builtinOAuthClientID(provider),
  // The user's OWN OAuth app for a provider (separate store,
  // custom-oauth-clients.json) — never the builtin. The secret,
  // when saved, comes back in memory only, for the wizard to
  // pass to the exchange; the frontend never persists it.
  getCustomOAuthApp: (provider: ProviderKind) => DesktopService.getCustomOAuthApp(provider),
  saveCustomOAuthApp: (provider: ProviderKind, clientID: string, clientSecret: string) =>
    DesktopService.saveCustomOAuthApp(provider, clientID, clientSecret),
  deleteCustomOAuthApp: (provider: ProviderKind) => DesktopService.deleteCustomOAuthApp(provider),
  exchangeOAuthCode: (provider: ProviderKind, clientID: string, clientSecret: string, code: string, state: string, tokenURL: string) =>
    DesktopService.exchangeOAuthCode(provider, clientID, clientSecret, code, state, tokenURL),
  getSettings: () => DesktopService.getSettings(),
  saveSettings: (s: Settings) => DesktopService.saveSettings(s),
  // V0.9.0: in-place re-authorization (same mount, new credentials),
  // Drime workspace picker, batch transfer, Agent run control.
  updateMountCredentials: (id: string, input: AddMountInput) => DesktopService.updateMountCredentials(id, input),
  listDrimeWorkspaces: async (accessToken: string) => nonNull(await DesktopService.listDrimeWorkspaces(accessToken)),
  batchDownload: (mountID: string, refs: string[], destDir: string) => DesktopService.batchDownload(mountID, refs, destDir),
  batchCopy: (srcMount: string, refs: string[], dstMount: string, dstRef: string) =>
    DesktopService.batchCopy(srcMount, refs, dstMount, dstRef),
  retryTransfer: (id: string) => DesktopService.retryTransfer(id),
  retryFailedTransfers: () => DesktopService.retryFailedTransfers(),
  pushJobToAgent: (jobID: string) => DesktopService.pushJobToAgent(jobID),
  triggerAgentJob: (jobID: string) => DesktopService.triggerAgentJob(jobID),
  listJobRuns: async (jobID: string) => nonNull(await DesktopService.listJobRuns(jobID)),
  exportAgentBundle: (agentID: string, passphrase: string) => DesktopService.exportAgentBundle(agentID, passphrase),
  saveAgentToken: (agentID: string, token: string) => DesktopService.saveAgentToken(agentID, token),
  // Settings → 网络: what the OS has configured (for display),
  // and a one-shot connectivity test through the current setup.
  getSystemProxy: () => DesktopService.getSystemProxy(),
  testProxy: () => DesktopService.testProxy(),
  checkDownloadDir: (path: string) => DesktopService.checkDownloadDir(path),
  getOpenAtLogin: () => DesktopService.getOpenAtLogin(),
  setOpenAtLogin: (on: boolean) => DesktopService.setOpenAtLogin(on),
  getVersions: () => DesktopService.getVersions(),
}
