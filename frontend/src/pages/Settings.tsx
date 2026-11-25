// Settings — Agent list (not a single Agent). Each row shows name,
// address, online/offline, version, protocol version, running jobs
// and last heartbeat, with add / edit / delete / test connection /
// set-default. The Agent API token is not stored from here: there is
// no Bind to save a token secret yet, and the Agent only needs the
// token for /api/jobs — test connection uses the unauthenticated
// /api/health, so it works without one.
import { useCallback, useEffect, useState } from 'react'
import type { Agent } from '../mygo'
import { api, friendlyError } from '../api/client'
import { formatTime, newId } from '../lib/format'

export default function SettingsPage() {
  const [agents, setAgents] = useState<Agent[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [editing, setEditing] = useState<Agent | null>(null)
  const [formName, setFormName] = useState('')
  const [formAddress, setFormAddress] = useState('')
  const [deletingId, setDeletingId] = useState<string | null>(null)
  const [testingId, setTestingId] = useState<string | null>(null)
  const [testMsg, setTestMsg] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      setAgents(await api.listAgents())
    } catch (e) {
      setError(friendlyError(e))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  const startAdd = () => {
    setEditing({
      id: '',
      name: '',
      address: '',
      online: false,
      jobsRunning: 0,
      default: false,
      createdAt: '',
      updatedAt: '',
    })
    setFormName('')
    setFormAddress('')
  }

  const startEdit = (a: Agent) => {
    setEditing(a)
    setFormName(a.name)
    setFormAddress(a.address)
  }

  const saveForm = async () => {
    if (!editing) return
    const name = formName.trim()
    const address = formAddress.trim()
    if (!name || !address) {
      setError('名字和地址都要填。')
      return
    }
    setBusy(true)
    setError(null)
    try {
      const saved = await api.saveAgent({
        ...editing,
        id: editing.id || newId('agent'),
        name,
        address,
      })
      if (!saved) throw new Error('保存 Agent 失败')
      setEditing(null)
      await load()
    } catch (e) {
      setError(friendlyError(e))
    } finally {
      setBusy(false)
    }
  }

  const remove = async (a: Agent) => {
    try {
      await api.removeAgent(a.id)
      setDeletingId(null)
      await load()
    } catch (e) {
      setError(friendlyError(e))
    }
  }

  const setDefault = async (id: string) => {
    try {
      await api.setDefaultAgent(id)
      await load()
    } catch (e) {
      setError(friendlyError(e))
    }
  }

  const test = async (a: Agent) => {
    setTestingId(a.id)
    try {
      const res = await api.testAgent(a.id)
      setTestMsg((prev) => ({ ...prev, [a.id]: res?.message ?? '无返回' }))
      await load()
    } catch (e) {
      setTestMsg((prev) => ({ ...prev, [a.id]: friendlyError(e) }))
    } finally {
      setTestingId(null)
    }
  }

  return (
    <div>
      <h1 style={{ marginTop: 0 }}>设置</h1>
      {error && <div className="error-box">{error}</div>}

      <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
        <h2 style={{ margin: 0 }}>Agent</h2>
        <span style={{ flex: 1 }} />
        <button className="btn-primary" onClick={startAdd}>
          添加 Agent
        </button>
      </div>
      <p className="muted" style={{ fontSize: 13 }}>
        Agent 是跑在服务器上的执行端：桌面配好同步任务后，Agent 在服务器上跑，文件不经过这台电脑。本地文件夹的任务只能在本机跑。
      </p>

      {loading ? (
        <p className="muted">加载中…</p>
      ) : agents.length === 0 ? (
        <div className="empty">
          <h2>还没有配置 Agent</h2>
          <p>没有 Agent 也能用：同步任务在本机跑。想让任务在服务器上跑，先在服务器上起一个 mcm-agent，再点“添加 Agent”。</p>
        </div>
      ) : (
        agents.map((a) => (
          <div key={a.id} className="agent-row">
            <div className="agent-main">
              <div className="agent-name">
                <span className={a.online ? 'dot dot-green' : 'dot dot-grey'} />
                {a.name}
                {a.default && <span className="badge badge-green" style={{ marginLeft: 8 }}>默认</span>}
                {!a.online && <span className="badge" style={{ marginLeft: 8 }}>离线</span>}
              </div>
              <div className="agent-meta">
                {a.address}
                {a.version ? ` · 版本 ${a.version}` : ''}
                {a.protocolVersion ? ` · 协议 ${a.protocolVersion}` : ''}
                {` · 运行中 ${a.jobsRunning} 个任务`}
                {a.lastHeartbeat ? ` · 上次心跳 ${formatTime(a.lastHeartbeat)}` : ' · 还没有心跳'}
              </div>
              {testMsg[a.id] && <div style={{ fontSize: 13, marginTop: 4 }}>{testMsg[a.id]}</div>}
            </div>
            <button className="btn btn-sm" disabled={testingId === a.id} onClick={() => void test(a)}>
              {testingId === a.id ? '测试中…' : '测试连接'}
            </button>
            {!a.default && (
              <button className="btn btn-sm" onClick={() => void setDefault(a.id)}>
                设为默认
              </button>
            )}
            <button className="btn btn-sm" onClick={() => startEdit(a)}>
              编辑
            </button>
            {deletingId === a.id ? (
              <>
                <button className="btn-danger btn-sm" onClick={() => void remove(a)}>
                  确认删除？
                </button>
                <button className="btn btn-sm" onClick={() => setDeletingId(null)}>
                  取消
                </button>
              </>
            ) : (
              <button className="btn btn-sm" onClick={() => setDeletingId(a.id)}>
                删除
              </button>
            )}
          </div>
        ))
      )}

      {editing && (
        <div className="dialog-backdrop" onClick={() => setEditing(null)}>
          <div className="dialog" onClick={(e) => e.stopPropagation()}>
            <h1>{editing.id ? '编辑 Agent' : '添加 Agent'}</h1>
            <div className="field">
              <label>名字</label>
              <input className="input" value={formName} onChange={(e) => setFormName(e.target.value)} placeholder="例如：家里的服务器" />
            </div>
            <div className="field">
              <label>地址</label>
              <input
                className="input"
                value={formAddress}
                onChange={(e) => setFormAddress(e.target.value)}
                placeholder="例如：192.168.1.10:8080 或 agent.example.com"
              />
              <div className="hint">填 Agent 的 HTTP 地址，不用加 http:// 也行。测试连接调的是这个地址的 /api/health，不需要 Token。</div>
            </div>
            <div className="notice-box">
              API Token 现在不在这里填：桌面端还没有保存 Token 的接口。Token 只在 Agent 启动时配，同步任务真正推到 Agent 上跑的功能在 V0.2 接入。
            </div>
            <div className="dialog-actions">
              <button className="btn" onClick={() => setEditing(null)}>
                取消
              </button>
              <button className="btn-primary" disabled={busy} onClick={() => void saveForm()}>
                保存
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
