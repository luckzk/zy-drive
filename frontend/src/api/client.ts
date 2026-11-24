// api client — the only layer allowed to talk to the Go backend.
// Desktop (mygo window): these calls go through mygo.Bind via the
// generated client in ../mygo.ts (DesktopService, from
// internal/app/bind.go). Dev shell: plain HTTP to the cmd/desktop
// dev server on 127.0.0.1:18080 — only /api/health exists there, so
// the Bind methods throw outside a mygo window until the dev shell
// grows the same surface. The UI never calls a cloud API directly.
import { DesktopService } from '../mygo'
import type { Agent, AgentStatus, Mount, Node, Plan, Run, SearchResult, SyncJob, Transfer } from '../mygo'

export type { Agent, AgentStatus, Mount, Node, Plan, Run, SearchResult, SyncJob, Transfer }

async function get<T>(path: string): Promise<T> {
  const res = await fetch(path)
  if (!res.ok) throw new Error(`api ${path}: ${res.status}`)
  return res.json() as Promise<T>
}

export const api = {
  health: () => get<{ ok: boolean; version: string }>('/api/health'),
  listMounts: () => DesktopService.listMounts(),
  browse: (mountID: string, ref: string) => DesktopService.browse(mountID, ref),
  search: (mountID: string, query: string) => DesktopService.search(mountID, query),
  previewSync: (job: SyncJob) => DesktopService.previewSync(job),
  runSync: (job: SyncJob, plan: Plan) => DesktopService.runSync(job, plan),
  transfers: () => DesktopService.transfers(),
  listAgents: () => DesktopService.listAgents(),
  agentStatus: () => DesktopService.agentStatus(),
  validateJob: (job: SyncJob) => DesktopService.validateJob(job),
}
