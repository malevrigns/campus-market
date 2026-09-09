import { useEffect, useState } from 'react'
import { App as AntApp, Modal } from 'antd'
import { PlusOutlined, ReloadOutlined, SettingOutlined, ThunderboltOutlined } from '@ant-design/icons'
import { api, dateTime } from '../lib/api'
import { Button, EmptyState, ErrorState, Loading, PageHeading } from './ui'

const kindLabels = { model: '模型', tool: '内置工具', mcp: 'MCP 工具' }
const newDraft = {
  name: '',
  description: '',
  system_prompt: '',
  welcome: '',
  suggestions: [],
  model_capability_id: 'cap_default_model',
  tool_capability_ids: [],
  max_iterations: 5,
  max_tokens: 1800,
  timeout_seconds: 60,
  enabled: true,
}
export default function AgentAdmin() {
  const { message, modal } = AntApp.useApp()
  const [catalog, setCatalog] = useState(null),
    [error, setError] = useState(null),
    [tab, setTab] = useState('agents'),
    [busy, setBusy] = useState('')
  const [editing, setEditing] = useState(null),
    [capEditing, setCapEditing] = useState(null),
    [versions, setVersions] = useState(null),
    [audit, setAudit] = useState([])
  const load = async (signal) => {
    try {
      const data = await api('/admin/agent-catalog', { signal })
      setCatalog(data)
      setError(null)
    } catch (e) {
      if (!signal?.aborted) setError(e)
    }
  }
  useEffect(() => {
    const ac = new AbortController()
    load(ac.signal)
    return () => ac.abort()
  }, [])
  const operate = async (id, path, method, body, success) => {
    if (busy) return
    setBusy(id)
    try {
      await api(path, { method, body })
      message.success(success)
      await load()
      setVersions(null)
    } catch (e) {
      message.error(e.message)
    } finally {
      setBusy('')
    }
  }
  const history = async (agent) => {
    try {
      const data = await api(`/admin/agents/${agent.id}/versions`)
      setVersions({ agent, items: data.items })
    } catch (e) {
      message.error(e.message)
    }
  }
  const showAudit = async () => {
    setTab('audit')
    try {
      const data = await api('/admin/agent-audit')
      setAudit(data.items)
    } catch (e) {
      setError(e)
    }
  }
  return (
    <div className="agent-admin">
      <PageHeading
        eyebrow="AGENT STUDIO"
        title="让助手，各有所长"
        description="组合模型与工具，发布属于校园的 AI 助手。"
      >
        <Button variant="secondary" onClick={() => load()}>
          <ReloadOutlined />
          刷新配置
        </Button>
      </PageHeading>
      <div className="category-tabs">
        <button className={tab === 'agents' ? 'active' : ''} onClick={() => setTab('agents')}>
          助手管理
        </button>
        <button className={tab === 'capabilities' ? 'active' : ''} onClick={() => setTab('capabilities')}>
          能力目录
        </button>
        <button className={tab === 'audit' ? 'active' : ''} onClick={showAudit}>
          变更记录
        </button>
      </div>
      {error && <ErrorState error={error} retry={() => load()} />}
      {!catalog ? (
        !error && <Loading />
      ) : (
        <>
          {tab === 'agents' && (
            <>
              <div className="studio-toolbar">
                <p>草稿保存后需发布。发布立即生效，进行中的对话运行保留原版本。</p>
                <Button onClick={() => setEditing({ draft: { ...newDraft } })}>
                  <PlusOutlined />
                  新建助手
                </Button>
              </div>
              <div className="studio-grid">
                {catalog.agents.map((a) => (
                  <article className="studio-card" key={a.id}>
                    <div className="studio-card-top">
                      <span className="agent-mini-orb">✳</span>
                      <span className={`studio-state ${a.enabled ? '' : 'off'}`}>
                        {!a.enabled
                          ? '已停用'
                          : a.published_version
                            ? `已发布 v${a.published_version}`
                            : '草稿'}
                      </span>
                    </div>
                    <h2>{a.draft.name}</h2>
                    {a.published_version > 0 && a.has_unpublished_changes && (
                      <small className="draft-notice">有未发布更改 · 以下显示当前草稿</small>
                    )}
                    <p>{a.draft.description || '为校园生活提供更多帮助。'}</p>
                    <div className="studio-tags">
                      <span>
                        {catalog.capabilities.find((c) => c.id === a.draft.model_capability_id)?.name ||
                          '模型待配置'}
                      </span>
                      <span>{a.draft.tool_capability_ids.length} 项工具</span>
                      <span>{a.draft.max_iterations} 轮上限</span>
                    </div>
                    <small className="muted">
                      草稿修订 {a.revision} · {dateTime(a.updated_at)}
                    </small>
                    <div className="studio-actions">
                      <Button variant="secondary" onClick={() => setEditing(a)} disabled={!!busy}>
                        编辑草稿
                      </Button>
                      <Button
                        busy={busy === a.id}
                        onClick={() =>
                          operate(
                            a.id,
                            `/admin/agents/${a.id}/publish`,
                            'POST',
                            { revision: a.revision },
                            '助手已发布，新请求将使用新版本',
                          )
                        }
                      >
                        发布
                      </Button>
                    </div>
                    <div className="studio-text-actions">
                      <button onClick={() => history(a)} disabled={!a.published_version}>
                        版本历史
                      </button>
                      <button
                        disabled={!!busy}
                        onClick={() =>
                          operate(
                            a.id,
                            `/admin/agents/${a.id}`,
                            'PUT',
                            { ...a.draft, enabled: !a.enabled, revision: a.revision },
                            a.enabled ? '助手已停用' : '助手已启用',
                          )
                        }
                      >
                        {a.enabled ? '停用' : '启用'}
                      </button>
                      <button
                        disabled={!!busy}
                        onClick={() =>
                          modal.confirm({
                            title: `移除「${a.draft.name}」？`,
                            content: '用户历史会保留，助手将停止接受新请求。',
                            okText: '移除',
                            cancelText: '取消',
                            onOk: () =>
                              operate(
                                a.id,
                                `/admin/agents/${a.id}`,
                                'DELETE',
                                { revision: a.revision },
                                '助手已移除',
                              ),
                          })
                        }
                      >
                        移除
                      </button>
                    </div>
                  </article>
                ))}
              </div>
              {catalog.agents.length === 0 && (
                <EmptyState
                  title="从一个新的想法开始"
                  description="创建助手，选择模型，再组合它需要的工具。"
                />
              )}
            </>
          )}
          {tab === 'capabilities' && (
            <>
              <div className="studio-toolbar">
                <p>模型、内置工具与可信 MCP 工具。修改能力后，重新发布引用它的助手。</p>
                <Button
                  onClick={() =>
                    setCapEditing({
                      kind: 'model',
                      name: '',
                      description: '',
                      enabled: true,
                      config: { provider: 'default', model: '' },
                    })
                  }
                >
                  <PlusOutlined />
                  添加能力
                </Button>
              </div>
              <div className="capability-list">
                {catalog.capabilities.map((c) => (
                  <article key={c.id}>
                    <span className="capability-icon">
                      {c.kind === 'model' ? <SettingOutlined /> : <ThunderboltOutlined />}
                    </span>
                    <div className="capability-copy">
                      <strong>
                        <span>{c.name}</span>
                        <span className="tiny-label">{kindLabels[c.kind]}</span>
                      </strong>
                      <p>{c.description}</p>
                      <small>
                        修订 {c.revision} · {c.enabled ? '已启用' : '已停用'}
                        {c.kind === 'model' && ` · ${c.config.model || '连接默认模型'}`}
                      </small>
                    </div>
                    <div className="capability-actions">
                      <Button variant="secondary" onClick={() => setCapEditing(c)}>
                        编辑
                      </Button>
                      <button
                        disabled={!!busy}
                        onClick={() =>
                          operate(
                            c.id,
                            `/admin/capabilities/${c.id}`,
                            'PUT',
                            {
                              kind: c.kind,
                              name: c.name,
                              description: c.description,
                              enabled: !c.enabled,
                              config: c.config,
                              revision: c.revision,
                            },
                            c.enabled ? '能力已停用，运行时将拒绝调用' : '能力已启用',
                          )
                        }
                      >
                        {c.enabled ? '停用' : '启用'}
                      </button>
                      <button
                        disabled={!!busy}
                        onClick={() =>
                          modal.confirm({
                            title: `删除「${c.name}」？`,
                            content: '仍被引用的能力无法删除，可先停用。',
                            okText: '删除',
                            cancelText: '取消',
                            onOk: () =>
                              operate(c.id, `/admin/capabilities/${c.id}`, 'DELETE', undefined, '能力已删除'),
                          })
                        }
                      >
                        删除
                      </button>
                    </div>
                  </article>
                ))}
              </div>
            </>
          )}
          {tab === 'audit' && (
            <div className="studio-audit">
              {audit.length === 0 ? (
                <EmptyState title="尚无变更记录" description="创建、修改、发布和停用操作将在这里留痕。" />
              ) : (
                audit.map((row, i) => (
                  <article key={i}>
                    <strong>
                      {{
                        'agent.create': '创建助手',
                        'agent.update': '更新助手',
                        'agent.publish': '发布助手',
                        'agent.archive': '移除助手',
                        'capability.create': '创建能力',
                        'capability.update': '更新能力',
                        'capability.delete': '删除能力',
                      }[row.action] || row.action}
                    </strong>
                    <code>{row.target_id}</code>
                    <span>版本 / 修订 {row.revision}</span>
                    <small>{dateTime(row.created_at)}</small>
                  </article>
                ))
              )}
            </div>
          )}
        </>
      )}
      {editing && (
        <AgentEditor
          agent={editing}
          catalog={catalog}
          onClose={() => setEditing(null)}
          onSaved={async () => {
            setEditing(null)
            await load()
            message.success('草稿已保存，发布后对用户生效')
          }}
        />
      )}
      {capEditing && (
        <CapabilityEditor
          capability={capEditing}
          catalog={catalog}
          onClose={() => setCapEditing(null)}
          onSaved={async () => {
            setCapEditing(null)
            await load()
            message.success('能力已保存')
          }}
        />
      )}
      <Modal open={!!versions} onCancel={() => setVersions(null)} footer={null} title="发布版本" width={600}>
        {versions?.items.map((v) => (
          <div className="version-row" key={v.version}>
            <div>
              <strong>
                v{v.version} · {v.snapshot.name}
              </strong>
              <p>
                {dateTime(v.published_at)} · {v.snapshot.tools.length} 项工具
              </p>
            </div>
            <Button
              variant="secondary"
              disabled={!!busy || v.version === versions.agent.published_version}
              onClick={() =>
                modal.confirm({
                  title: `按 v${v.version} 的配置重新发布？`,
                  content: '将创建一个新版本，已有运行记录保持不变。',
                  okText: '恢复并发布',
                  cancelText: '取消',
                  onOk: () =>
                    operate(
                      versions.agent.id,
                      `/admin/agents/${versions.agent.id}/publish`,
                      'POST',
                      { revision: versions.agent.revision, source_version: v.version },
                      '已按历史配置发布新版本',
                    ),
                })
              }
            >
              恢复此版本
            </Button>
          </div>
        ))}
      </Modal>
    </div>
  )
}

function AgentEditor({ agent, catalog, onClose, onSaved }) {
  const [draft, setDraft] = useState({ ...newDraft, ...agent.draft }),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(null)
  const change = (key, value) => setDraft((d) => ({ ...d, [key]: value }))
  const save = async (e) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      await api(`/admin/agents${agent.id ? `/${agent.id}` : ''}`, {
        method: agent.id ? 'PUT' : 'POST',
        body: {
          ...draft,
          suggestions: draft.suggestions.map((p) => p.trim()).filter(Boolean),
          ...(agent.id ? { revision: agent.revision } : {}),
        },
      })
      await onSaved()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      open
      onCancel={busy ? undefined : onClose}
      footer={null}
      width={690}
      title={agent.id ? '编辑助手草稿' : '新建助手'}
    >
      <form className="form-stack studio-form" onSubmit={save}>
        <div className="form-grid">
          <label>
            助手名称
            <input
              required
              minLength={2}
              maxLength={40}
              value={draft.name}
              onChange={(e) => change('name', e.target.value)}
            />
          </label>
          <label>
            欢迎语
            <input
              maxLength={160}
              value={draft.welcome}
              onChange={(e) => change('welcome', e.target.value)}
            />
          </label>
        </div>
        <label>
          助手介绍
          <input
            maxLength={300}
            value={draft.description}
            onChange={(e) => change('description', e.target.value)}
          />
        </label>
        <label>
          系统提示词
          <textarea
            required
            maxLength={6000}
            rows={5}
            value={draft.system_prompt}
            placeholder="说明助手的角色、专长和回答方式。平台权限边界始终生效。"
            onChange={(e) => change('system_prompt', e.target.value)}
          />
        </label>
        <label>
          模型能力
          <select
            aria-label="模型能力"
            value={draft.model_capability_id}
            onChange={(e) => change('model_capability_id', e.target.value)}
          >
            {catalog.capabilities
              .filter((c) => c.kind === 'model')
              .map((c) => (
                <option value={c.id} key={c.id}>
                  {c.name}
                  {c.enabled ? '' : '（已停用）'}
                </option>
              ))}
          </select>
        </label>
        <fieldset className="tool-checkboxes">
          <legend>绑定工具（最多 8 项）</legend>
          {catalog.capabilities
            .filter((c) => c.kind !== 'model')
            .map((c) => (
              <label key={c.id}>
                <input
                  type="checkbox"
                  checked={draft.tool_capability_ids.includes(c.id)}
                  onChange={(e) =>
                    change(
                      'tool_capability_ids',
                      e.target.checked
                        ? [...draft.tool_capability_ids, c.id]
                        : draft.tool_capability_ids.filter((id) => id !== c.id),
                    )
                  }
                />
                <span>
                  <strong>{c.name}</strong>
                  <small>
                    {kindLabels[c.kind]} · {c.enabled ? '只读' : '已停用'}
                  </small>
                </span>
              </label>
            ))}
        </fieldset>
        <label>
          建议问题（每行一条，最多 4 条）
          <textarea
            rows={3}
            value={draft.suggestions.join('\n')}
            onChange={(e) => change('suggestions', e.target.value.split('\n'))}
          />
        </label>
        <div className="form-grid studio-limits">
          <label>
            最多推理轮数
            <input
              type="number"
              required
              min={1}
              max={8}
              value={draft.max_iterations}
              onChange={(e) => change('max_iterations', Number(e.target.value))}
            />
          </label>
          <label>
            每轮输出 token 上限
            <input
              type="number"
              required
              min={128}
              max={4096}
              value={draft.max_tokens}
              onChange={(e) => change('max_tokens', Number(e.target.value))}
            />
          </label>
          <label>
            运行时限（秒）
            <input
              type="number"
              required
              min={10}
              max={90}
              value={draft.timeout_seconds}
              onChange={(e) => change('timeout_seconds', Number(e.target.value))}
            />
          </label>
        </div>
        <label className="checkbox-line">
          <input
            type="checkbox"
            checked={draft.enabled}
            onChange={(e) => change('enabled', e.target.checked)}
          />
          启用助手（停用会立即阻止新调用）
        </label>
        {error && <ErrorState error={error} />}
        <Button busy={busy} type="submit">
          保存草稿
        </Button>
      </form>
    </Modal>
  )
}
function CapabilityEditor({ capability, catalog, onClose, onSaved }) {
  const [value, setValue] = useState(capability),
    [tools, setTools] = useState([]),
    [discovering, setDiscovering] = useState(false),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(null)
  const field = (key, data) => setValue((v) => ({ ...v, [key]: data })),
    config = (key, data) => setValue((v) => ({ ...v, config: { ...v.config, [key]: data } }))
  const discover = async () => {
    setDiscovering(true)
    setError(null)
    try {
      const d = await api(`/admin/mcp/${value.config.server_id}/tools`)
      setTools(d.items)
    } catch (e) {
      setError(e)
    } finally {
      setDiscovering(false)
    }
  }
  const save = async (e) => {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await api(`/admin/capabilities${value.id ? `/${value.id}` : ''}`, {
        method: value.id ? 'PUT' : 'POST',
        body: {
          kind: value.kind,
          name: value.name,
          description: value.description,
          config: value.config,
          enabled: value.enabled,
          ...(value.id ? { revision: value.revision } : {}),
        },
      })
      await onSaved()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      open
      onCancel={busy ? undefined : onClose}
      footer={null}
      title={value.id ? '编辑能力' : '添加原子能力'}
      width={570}
    >
      <form className="form-stack studio-form" onSubmit={save}>
        <label>
          能力类型
          <select
            value={value.kind}
            disabled={!!value.id}
            onChange={(e) => {
              field('kind', e.target.value)
              field(
                'config',
                e.target.value === 'model'
                  ? { provider: 'default', model: '' }
                  : e.target.value === 'tool'
                    ? { tool: 'search_products' }
                    : { server_id: catalog.mcp_servers[0] || '', tool: '', read_only: false },
              )
              setTools([])
            }}
          >
            {Object.entries(kindLabels).map(([key, label]) => (
              <option value={key} key={key}>
                {label}
              </option>
            ))}
          </select>
        </label>
        <label>
          能力名称
          <input
            required
            minLength={2}
            maxLength={60}
            value={value.name}
            onChange={(e) => field('name', e.target.value)}
          />
        </label>
        <label>
          能力描述
          <textarea
            maxLength={600}
            rows={2}
            value={value.description}
            onChange={(e) => field('description', e.target.value)}
          />
        </label>
        {value.kind === 'model' && (
          <>
            <label>
              后端模型连接
              <select value={value.config.provider} onChange={(e) => config('provider', e.target.value)}>
                {catalog.providers.map((p) => (
                  <option value={p.id} key={p.id}>
                    {p.id} · {p.ready ? '已配置' : '未配置密钥'}
                  </option>
                ))}
              </select>
            </label>
            <label>
              模型名称
              <input
                maxLength={128}
                value={value.config.model || ''}
                placeholder="留空使用该连接的默认模型"
                onChange={(e) => config('model', e.target.value)}
              />
            </label>
            <p className="muted">连接地址与密钥由后端配置；这里选择模型与显示名称。</p>
          </>
        )}
        {value.kind === 'tool' && (
          <label>
            内置实现
            <select value={value.config.tool} onChange={(e) => config('tool', e.target.value)}>
              <option value="search_products">商品搜索（只读）</option>
              <option value="my_orders">当前用户订单（只读）</option>
            </select>
          </label>
        )}
        {value.kind === 'mcp' && (
          <>
            <label>
              可信 MCP 服务
              <select
                required
                value={value.config.server_id}
                onChange={(e) => {
                  config('server_id', e.target.value)
                  config('tool', '')
                  setTools([])
                }}
              >
                <option value="">选择后端配置的连接</option>
                {catalog.mcp_servers.map((id) => (
                  <option key={id}>{id}</option>
                ))}
              </select>
            </label>
            <Button
              variant="secondary"
              type="button"
              busy={discovering}
              disabled={!value.config.server_id}
              onClick={discover}
            >
              读取工具目录
            </Button>
            <label>
              绑定工具
              <select required value={value.config.tool} onChange={(e) => config('tool', e.target.value)}>
                <option value="">选择工具</option>
                {tools.length === 0 && value.config.tool && <option>{value.config.tool}</option>}
                {tools.map((t) => (
                  <option key={t.name} disabled={!t.annotations?.readOnlyHint} value={t.name}>
                    {t.name}
                    {t.annotations?.readOnlyHint ? '' : '（未声明只读）'}
                  </option>
                ))}
              </select>
            </label>
            <label className="checkbox-line">
              <input
                type="checkbox"
                required
                checked={!!value.config.read_only}
                onChange={(e) => config('read_only', e.target.checked)}
              />
              我确认此工具只读取允许公开的资料，不操作账号或资金
            </label>
            <p className="muted">
              服务凭据必须限制为公开只读数据。工具声明不能替代管理员对服务端权限的核查。
            </p>
          </>
        )}
        <label className="checkbox-line">
          <input
            type="checkbox"
            checked={value.enabled}
            onChange={(e) => field('enabled', e.target.checked)}
          />
          启用能力
        </label>
        {error && <ErrorState error={error} />}
        <Button busy={busy} type="submit">
          保存能力
        </Button>
      </form>
    </Modal>
  )
}
