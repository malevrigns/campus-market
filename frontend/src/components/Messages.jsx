import { useCallback, useEffect, useRef, useState } from 'react'
import {
  ArrowLeftOutlined,
  CheckOutlined,
  MessageOutlined,
  ReloadOutlined,
  SendOutlined,
} from '@ant-design/icons'
import { api, dateTime, newKey } from '../lib/api'
import { Avatar, Button, EmptyState, ErrorState, Loading, ProductImage } from './ui'
export default function Messages({ user, selected, onSelect, onUnread }) {
  const [convs, setConvs] = useState([]),
    [messages, setMessages] = useState([]),
    [pending, setPending] = useState([]),
    [input, setInput] = useState('')
  const [error, setError] = useState(null),
    [loading, setLoading] = useState(true),
    [connected, setConnected] = useState(false),
    [sending, setSending] = useState(false)
  const cursor = useRef(0),
    current = useRef(selected),
    list = useRef(null),
    syncRef = useRef(() => {}),
    draft = useRef({})
  current.current = selected
  const active = convs.find((c) => c.id === selected)
  const loadConvs = useCallback(
    async (signal) => {
      const d = await api('/conversations', { signal })
      setConvs(d.items)
      onUnread(d.items.reduce((sum, c) => sum + c.unread, 0))
    },
    [onUnread],
  )
  useEffect(() => {
    const ac = new AbortController()
    let alive = true,
      retry,
      attempts = 0,
      socket
    const refresh = () => {
      if (!alive) return
      loadConvs(ac.signal)
        .catch((e) => {
          if (alive) setError(e)
        })
        .finally(() => {
          if (alive) setLoading(false)
        })
      syncRef.current()
    }
    const connect = () => {
      socket = new WebSocket(`${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}/api/v1/ws`)
      socket.onopen = () => {
        attempts = 0
        setConnected(true)
        setError(null)
        refresh()
      }
      socket.onmessage = () => refresh()
      socket.onclose = () => {
        if (!alive) return
        setConnected(false)
        retry = setTimeout(connect, Math.min(30000, 1000 * 2 ** attempts++) + Math.random() * 300)
      }
      socket.onerror = () => socket.close()
    }
    refresh()
    connect()
    const timer = setInterval(refresh, 15000)
    window.addEventListener('online', refresh)
    window.addEventListener('focus', refresh)
    return () => {
      alive = false
      ac.abort()
      clearTimeout(retry)
      clearInterval(timer)
      socket?.close()
      window.removeEventListener('online', refresh)
      window.removeEventListener('focus', refresh)
    }
  }, [loadConvs, user.id])
  useEffect(() => {
    const ac = new AbortController()
    cursor.current = 0
    setMessages([])
    setPending([])
    setInput(draft.current[selected] || '')
    setError(null)
    let syncing = false
    const sync = async () => {
      if (!selected || syncing || ac.signal.aborted) return
      syncing = true
      try {
        let more = true
        while (more && !ac.signal.aborted) {
          const d = await api(`/conversations/${selected}/messages?after=${cursor.current}`, {
            signal: ac.signal,
          })
          if (current.current !== selected || ac.signal.aborted) return
          cursor.current = d.next_seq
          more = d.has_more
          setMessages((old) =>
            [...new Map([...old, ...d.items].map((m) => [m.seq, m])).values()].sort((a, b) => a.seq - b.seq),
          )
          setPending((old) =>
            old.filter(
              (p) => !d.items.some((m) => m.sender_id === user.id && m.client_msg_id === p.client_msg_id),
            ),
          )
        }
        if (!document.hidden && cursor.current > 0) {
          await api(`/conversations/${selected}/read`, {
            method: 'POST',
            body: { seq: cursor.current },
            signal: ac.signal,
          })
          await loadConvs(ac.signal)
        }
      } catch (e) {
        if (!ac.signal.aborted) setError(e)
      } finally {
        syncing = false
      }
    }
    syncRef.current = sync
    sync()
    return () => {
      ac.abort()
      syncRef.current = () => {}
    }
  }, [selected, user.id, loadConvs])
  useEffect(() => {
    list.current?.scrollTo({ top: list.current.scrollHeight, behavior: 'smooth' })
  }, [messages.length, pending.length])
  const send = async (retry) => {
    const text = retry?.content || input.trim()
    if (!text || !selected || sending) return
    const cid = selected,
      m = retry || { client_msg_id: newKey(), content: text, state: 'sending' }
    setSending(true)
    setError(null)
    if (!retry) {
      setPending((old) => [...old, m])
      setInput('')
      draft.current[cid] = ''
    } else
      setPending((old) =>
        old.map((p) => (p.client_msg_id === m.client_msg_id ? { ...p, state: 'sending' } : p)),
      )
    try {
      const sent = await api(`/conversations/${cid}/messages`, {
        method: 'POST',
        body: { client_msg_id: m.client_msg_id, content: m.content },
      })
      if (current.current === cid) {
        setPending((old) => old.filter((p) => p.client_msg_id !== m.client_msg_id))
        setMessages((old) =>
          [...new Map([...old, sent].map((x) => [x.seq, x])).values()].sort((a, b) => a.seq - b.seq),
        )
        syncRef.current()
      }
    } catch (e) {
      if (current.current === cid) {
        setPending((old) =>
          old.map((p) => (p.client_msg_id === m.client_msg_id ? { ...p, state: 'failed' } : p)),
        )
        setError(e)
      }
    } finally {
      setSending(false)
    }
  }
  return (
    <div className={`chat-workspace ${selected ? 'has-selection' : ''}`}>
      <aside className="conversation-sidebar">
        <div className="workspace-title">
          <div>
            <span className="eyebrow">STAY CONNECTED</span>
            <h1>
              消息<span className="lime">.</span>
            </h1>
          </div>
          <MessageOutlined />
        </div>
        <p className="connection-status">
          <span className={`status-dot ${connected ? '' : 'offline'}`} />
          {connected ? '实时连接已建立' : '正在重连 · 消息会自动同步'}
        </p>
        {loading ? (
          <Loading />
        ) : convs.length === 0 ? (
          <EmptyState title="从一句你好开始" description="在商品详情中点击「聊一聊」，与卖家建立会话。" />
        ) : (
          <div className="conversation-list">
            {convs.map((c) => (
              <button key={c.id} className={c.id === selected ? 'active' : ''} onClick={() => onSelect(c.id)}>
                <Avatar name={c.peer_name} />
                <div>
                  <strong>
                    {c.peer_name}
                    <small>{dateTime(c.updated_at)}</small>
                  </strong>
                  <p>{c.last_message || `聊聊「${c.product_title}」`}</p>
                </div>
                {c.unread > 0 && <span className="unread-badge">{Math.min(99, c.unread)}</span>}
              </button>
            ))}
          </div>
        )}
      </aside>
      <section className="chat-detail">
        {!selected ? (
          <EmptyState
            icon={<MessageOutlined />}
            title="让好物，成为连接的开始。"
            description="选择一位同学，聊聊你心动的好物。"
          />
        ) : (
          <>
            <header className="chat-header">
              <button
                className="icon-button mobile-back"
                aria-label="返回会话列表"
                onClick={() => onSelect(null)}
              >
                <ArrowLeftOutlined />
              </button>
              <Avatar name={active?.peer_name} />
              <div>
                <strong>{active?.peer_name || '同学'}</strong>
                <small>交易前，记得把细节聊清楚</small>
              </div>
            </header>
            {active && (
              <div className="chat-product">
                <ProductImage src={active.image} alt={active.product_title} />
                <div>
                  <small>正在聊这件好物</small>
                  <strong>{active.product_title}</strong>
                </div>
              </div>
            )}
            <div className="message-list" ref={list} role="log" aria-label="聊天记录">
              <div className="chat-safety">建议在校内公共场所面交，验货后再确认收货。</div>
              {messages.map((m) => (
                <div key={m.id} className={`message-row ${m.sender_id === user.id ? 'mine' : ''}`}>
                  <div className="message-bubble">
                    <p>{m.content}</p>
                    <small>
                      {dateTime(m.created_at)}
                      {m.sender_id === user.id && (
                        <>
                          <CheckOutlined />
                          已发送
                        </>
                      )}
                    </small>
                  </div>
                </div>
              ))}
              {pending.map((m) => (
                <div key={m.client_msg_id} className="message-row mine">
                  <div className="message-bubble pending">
                    <p>{m.content}</p>
                    {m.state === 'failed' ? (
                      <button onClick={() => send(m)} disabled={sending}>
                        <ReloadOutlined />
                        发送失败，点击重试
                      </button>
                    ) : (
                      <small>正在发送…</small>
                    )}
                  </div>
                </div>
              ))}
            </div>
            {error && <ErrorState error={error} retry={() => syncRef.current()} />}
            <form
              className="message-composer"
              onSubmit={(e) => {
                e.preventDefault()
                send()
              }}
            >
              <textarea
                aria-label="输入消息"
                placeholder="和同学聊聊…"
                value={input}
                maxLength={4000}
                rows={1}
                onChange={(e) => {
                  setInput(e.target.value)
                  draft.current[selected] = e.target.value
                }}
                onKeyDown={(e) => {
                  if (
                    e.key === 'Enter' &&
                    !e.shiftKey &&
                    !e.nativeEvent.isComposing &&
                    !window.matchMedia('(max-width: 767px)').matches
                  ) {
                    e.preventDefault()
                    send()
                  }
                }}
              />
              <Button type="submit" aria-label="发送消息" busy={sending} disabled={!input.trim()}>
                <SendOutlined />
              </Button>
            </form>
            <span className="composer-hint">聊清细节，让每一次交易更安心。</span>
          </>
        )}
      </section>
    </div>
  )
}
