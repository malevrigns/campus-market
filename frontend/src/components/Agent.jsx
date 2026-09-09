import { useEffect, useRef, useState } from 'react'
import {
  ArrowUpOutlined,
  CheckOutlined,
  HistoryOutlined,
  PlusOutlined,
  StopOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons'
import { api, newKey, runAgent } from '../lib/api'
import { Button, ErrorState, Loading } from './ui'
export default function Agent({ user }) {
  const [agents, setAgents] = useState([]),
    [selectedAgent, setSelectedAgent] = useState('')
  const active = agents.find((a) => a.id === selectedAgent)
  const available = !!active?.available
  const [threads, setThreads] = useState([]),
    [thread, setThread] = useState(null),
    [runs, setRuns] = useState([]),
    [input, setInput] = useState(''),
    [busy, setBusy] = useState(false),
    [loading, setLoading] = useState(false),
    [error, setError] = useState(null),
    [tool, setTool] = useState(null),
    [historyOpen, setHistoryOpen] = useState(false)
  const abort = useRef(null),
    historyAbort = useRef(null),
    list = useRef(null),
    mounted = useRef(true)
  const loadThreads = (signal) => api('/agent/threads', { signal }).then((d) => setThreads(d.items))
  const loadAgents = (signal) =>
    api('/agents', { signal }).then((d) => {
      setAgents(d.items)
      setSelectedAgent((current) => current || d.items[0]?.id || '')
    })
  useEffect(() => {
    mounted.current = true
    const ac = new AbortController()
    loadThreads(ac.signal).catch((e) => {
      if (!ac.signal.aborted) setError(e)
    })
    const refresh = () =>
      loadAgents(ac.signal).catch((e) => {
        if (!ac.signal.aborted) setError(e)
      })
    refresh()
    const timer = setInterval(refresh, 15000)
    window.addEventListener('focus', refresh)
    return () => {
      mounted.current = false
      ac.abort()
      abort.current?.abort()
      historyAbort.current?.abort()
      clearInterval(timer)
      window.removeEventListener('focus', refresh)
    }
  }, [])
  useEffect(() => {
    list.current?.scrollTo({ top: list.current.scrollHeight, behavior: 'smooth' })
  }, [runs])
  const open = async (id) => {
    if (busy) return
    historyAbort.current?.abort()
    const ac = new AbortController()
    historyAbort.current = ac
    setHistoryOpen(false)
    setLoading(true)
    setError(null)
    try {
      const d = await api(`/agent/threads/${id}`, { signal: ac.signal })
      setRuns(d.items)
      setThread(id)
      setSelectedAgent(d.agent_id)
    } catch (e) {
      if (!ac.signal.aborted) setError(e)
    } finally {
      if (!ac.signal.aborted) setLoading(false)
    }
  }
  const send = async (e) => {
    e.preventDefault()
    if (busy || loading || !input.trim() || !available) return
    const prompt = input.trim(),
      key = newKey()
    setBusy(true)
    setInput('')
    setError(null)
    setTool(null)
    setRuns((old) => [...old, { id: key, prompt, answer: '', status: 'running' }])
    const ac = new AbortController()
    abort.current = ac
    try {
      await runAgent(
        { prompt, agent_id: selectedAgent, idempotency_key: key, ...(thread ? { thread_id: thread } : {}) },
        ac.signal,
        (type, data) => {
          if (!mounted.current) return
          if (type === 'start') {
            setThread(data.thread_id)
            setRuns((old) =>
              old.map((r) =>
                r.id === key ? { ...r, agent_version: data.agent_version, agent_name: data.agent_name } : r,
              ),
            )
          }
          if (type === 'delta')
            setRuns((old) => old.map((r) => (r.id === key ? { ...r, answer: r.answer + data.text } : r)))
          if (type === 'tool') setTool(data)
          if (type === 'error') setError(new Error(data.message))
          if (type === 'done') {
            setRuns((old) => old.map((r) => (r.id === key ? data : r)))
            setTool(null)
          }
        },
      )
    } catch (e) {
      if (mounted.current) {
        const stopped = ac.signal.aborted
        if (!stopped) setError(e)
        setRuns((old) =>
          old.map((r) => (r.id === key ? { ...r, status: stopped ? 'cancelled' : 'failed' } : r)),
        )
      }
    } finally {
      if (mounted.current) {
        setBusy(false)
        setTool(null)
        loadThreads().catch(() => {})
      }
    }
  }
  return (
    <div className={`agent-workspace ${historyOpen ? 'show-history' : ''}`}>
      <aside className="agent-sidebar">
        <div className="workspace-title">
          <div>
            <span className="eyebrow">YOUR CAMPUS COPILOT</span>
            <h1>
              AI 助手<span className="lime">✳</span>
            </h1>
          </div>
        </div>
        <Button
          variant="secondary"
          disabled={busy}
          onClick={() => {
            historyAbort.current?.abort()
            setLoading(false)
            setThread(null)
            setRuns([])
            setError(null)
            setHistoryOpen(false)
          }}
        >
          <PlusOutlined />
          开启新对话
        </Button>
        <h3>最近的对话</h3>
        <div className="thread-list">
          {threads.length === 0 && <p className="muted">你的灵感，会留在这里。</p>}
          {threads.map((t) => (
            <button
              className={thread === t.id ? 'active' : ''}
              key={t.id}
              onClick={() => open(t.id)}
              disabled={busy}
            >
              <HistoryOutlined />
              <span>{t.title}</span>
            </button>
          ))}
        </div>
        <div className="agent-capabilities">
          <small>{active?.name || '这位助手'}的已发布工具</small>
          {active?.tools?.map((tool, i) => (
            <span key={i}>
              <CheckOutlined />
              {tool.name}
            </span>
          ))}
          {active?.tools?.length === 0 && <span>仅对话，不调用外部工具</span>}
          {user?.is_admin && (
            <a className="studio-link" href="#agent-admin">
              管理助手与能力 ↗
            </a>
          )}
        </div>
      </aside>
      <section className="agent-detail">
        <header className="agent-header">
          <div>
            <span className="agent-mini-orb">✳</span>
            <select
              aria-label="选择助手"
              className="agent-picker"
              value={selectedAgent}
              disabled={busy || loading}
              onChange={(e) => {
                historyAbort.current?.abort()
                setSelectedAgent(e.target.value)
                setThread(null)
                setRuns([])
                setError(null)
              }}
            >
              {!active && (
                <option value={selectedAgent}>{selectedAgent ? '助手已停用' : '选择一位助手'}</option>
              )}
              {agents.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                  {a.available ? '' : ' · 暂不可用'}
                </option>
              ))}
            </select>
            <span className="tiny-label">{active ? `v${active.version}` : 'AI'}</span>
          </div>
          <button className="icon-button" aria-label="查看对话历史" onClick={() => setHistoryOpen((v) => !v)}>
            <HistoryOutlined />
          </button>
        </header>
        <div className="agent-content" ref={list}>
          {loading ? (
            <Loading />
          ) : runs.length === 0 ? (
            <div className="agent-welcome">
              <div className="agent-welcome-orb">✳</div>
              <span className="eyebrow">LESS SEARCHING. MORE FINDING.</span>
              <h2>
                嗨，同学。
                <br />
                {active?.welcome || '选择一位助手，开始新的对话。'}
              </h2>
              <p>{active?.description || '每一位助手，都有自己的专长。'}</p>
              <div className="prompt-grid">
                {(active?.suggestions || []).map((p) => (
                  <button key={p} onClick={() => setInput(p)}>
                    {p}
                    <ArrowUpOutlined rotate={45} />
                  </button>
                ))}
              </div>
            </div>
          ) : (
            <div className="agent-runs">
              {runs.map((r) => (
                <article key={r.id}>
                  <div className="agent-question">{r.prompt}</div>
                  <div className="agent-answer">
                    <span className="agent-mini-orb">✳</span>
                    <div>
                      {r.agent_version > 0 && (
                        <small className="run-version">
                          {r.agent_name} · v{r.agent_version}
                        </small>
                      )}
                      {r.answer ? (
                        <p>{r.answer}</p>
                      ) : r.status === 'running' ? (
                        <span className="thinking">
                          正在为你寻找答案<span>•••</span>
                        </span>
                      ) : (
                        <p className="muted">这次没有生成回答。</p>
                      )}
                      {['cancelled', 'failed'].includes(r.status) && (
                        <small className="muted">
                          {r.status === 'cancelled' ? '已停止生成' : '回答未完成，可以重新发送问题'}
                        </small>
                      )}
                    </div>
                  </div>
                </article>
              ))}
              {tool && (
                <div className="tool-status">
                  <ThunderboltOutlined />
                  {tool.label || tool.name} ·{' '}
                  {tool.status === 'completed' ? '已完成' : tool.status === 'failed' ? '执行失败' : '进行中'}
                </div>
              )}
            </div>
          )}
        </div>
        <div className="agent-compose-area">
          {error && <ErrorState error={error} />}
          {!available && <div className="inline-notice">这位助手暂不可用，可以切换其他助手或稍后再来。</div>}
          <form onSubmit={send} className="agent-composer">
            <textarea
              aria-label="向助手提问"
              placeholder={`告诉${active?.name || '助手'}，你的需要和一点点想法…`}
              value={input}
              maxLength={2000}
              rows={2}
              onChange={(e) => setInput(e.target.value)}
            />
            {busy ? (
              <Button
                type="button"
                variant="secondary"
                aria-label="停止生成"
                onClick={() => abort.current?.abort()}
              >
                <StopOutlined />
              </Button>
            ) : (
              <Button type="submit" aria-label="发送问题" disabled={!input.trim() || !available || loading}>
                <ArrowUpOutlined />
              </Button>
            )}
          </form>
          <p className="agent-disclaimer">AI 建议仅供参考，商品与订单状态请以实际页面为准。</p>
        </div>
      </section>
    </div>
  )
}
