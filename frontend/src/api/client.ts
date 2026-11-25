// api client — the only layer allowed to talk to the Go backend.
// Desktop (mygo window): calls go through mygo.Bind via the generated
// client in ../mygo.ts (DesktopService, from internal/app/bind.go).
// Dev shell: plain HTTP to the cmd/desktop dev server on
// 127.0.0.1:18080 — only /api/health exists there, so the Bind methods
// throw outside a mygo window. Callers must catch that and show a
// human error, never fake data. The UI never calls a cloud API
// directly.
import { DesktopService } from '../mygo'
import type { AddMountInput, Agent, AgentStatus, AgentTestResult, Mount, Node, OAuthStart, OAuthTokens, Plan, ProviderKind, Run, SearchResult, Settings, SyncJob, Transfer, UploadResult, UploadSkipped, VersionInfo } from '../mygo'

export type { AddMountInput, Agent, AgentStatus, AgentTestResult, Mount, Node, OAuthStart, OAuthTokens, Plan, ProviderKind, Run, SearchResult, Settings, SyncJob, Transfer, UploadResult, UploadSkipped, VersionInfo }

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
  if (/runs on an Agent; agent execution/i.test(m)) return '这个任务要在 Agent 上跑，Agent 执行在 V0.2 接入，当前只能在本机跑。'
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
}

export const api = {
  health: () => get<{ ok: boolean; version: string }>('/api/health'),
  listMounts: async () => nonNull(await DesktopService.listMounts()),
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
  beginOAuth: (provider: ProviderKind, clientID: string) => DesktopService.beginOAuth(provider, clientID),
  exchangeOAuthCode: (provider: ProviderKind, clientID: string, clientSecret: string, code: string, state: string, tokenURL: string) =>
    DesktopService.exchangeOAuthCode(provider, clientID, clientSecret, code, state, tokenURL),
  getSettings: () => DesktopService.getSettings(),
  saveSettings: (s: Settings) => DesktopService.saveSettings(s),
  checkDownloadDir: (path: string) => DesktopService.checkDownloadDir(path),
  getOpenAtLogin: () => DesktopService.getOpenAtLogin(),
  setOpenAtLogin: (on: boolean) => DesktopService.setOpenAtLogin(on),
  getVersions: () => DesktopService.getVersions(),
}
