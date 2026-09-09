import { useEffect, useRef, useState } from 'react'
import { Input, Avatar } from 'antd'
import { CloseOutlined, SendOutlined } from '@ant-design/icons'
import { SELLER_REPLIES } from '../data/products'
import { ripple } from '../utils/ripple'

const QUICK = ['还在吗？', '可以刀吗？', '能当面交易吗？', '实物和照片一致吗？']

const now = () =>
  new Date().toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })

export default function ChatPanel({ seller, onClose }) {
  const [msgs, setMsgs] = useState([])
  const [input, setInput] = useState('')
  const [typing, setTyping] = useState(false)
  const replyIdx = useRef(0)
  const listRef = useRef(null)

  // 切换卖家时重置会话 + 欢迎语
  useEffect(() => {
    if (seller) {
      setMsgs([
        { from: 'seller', text: `你好呀～我是 ${seller.name}，这件闲置还在，随便问！`, time: now() },
      ])
      setInput('')
    }
  }, [seller])

  useEffect(() => {
    listRef.current?.scrollTo({ top: listRef.current.scrollHeight, behavior: 'smooth' })
  }, [msgs, typing])

  const send = (text) => {
    const content = (text ?? input).trim()
    if (!content || !seller) return
    setMsgs((m) => [...m, { from: 'me', text: content, time: now() }])
    setInput('')
    setTyping(true)
    // 模拟卖家自动回复（IM 对接位：此处替换为 WebSocket / 后端接口）
    setTimeout(() => {
      setTyping(false)
      setMsgs((m) => [
        ...m,
        { from: 'seller', text: SELLER_REPLIES[replyIdx.current++ % SELLER_REPLIES.length], time: now() },
      ])
    }, 900 + Math.random() * 800)
  }

  return (
    <div
      className={`fixed bottom-0 right-0 z-[60] w-full sm:w-[380px] transition-all duration-500 ease-out ${
        seller ? 'translate-y-0 opacity-100' : 'translate-y-10 opacity-0 pointer-events-none'
      }`}
    >
      <div className="glass-strong h-[70vh] sm:h-[540px] m-0 sm:m-0 sm:rounded-t-3xl rounded-t-3xl flex flex-col overflow-hidden border border-white/10 sm:border-b-0 shadow-[0_-20px_80px_rgba(168,85,247,.25)]"
        style={{ background: 'rgba(13,13,20,.92)' }}
      >
        {/* 头部 */}
        <div className="flex items-center gap-3 px-4 py-3.5 border-b border-white/8">
          <div className="relative">
            <Avatar size={38} src={seller?.avatar} />
            <span className="absolute bottom-0 right-0 w-2.5 h-2.5 rounded-full bg-emerald-400 border-2 border-[#0d0d14]" />
          </div>
          <div className="flex-1 min-w-0">
            <div className="text-sm font-semibold text-white truncate">{seller?.name || '卖家'}</div>
            <div className="text-[11px] text-emerald-400">在线 · 平均 3 分钟内回复</div>
          </div>
          <button
            onClick={onClose}
            className="w-8 h-8 rounded-full glass grid place-items-center text-zinc-400 hover:text-white hover:border-white/30 transition-all duration-300"
          >
            <CloseOutlined style={{ fontSize: 12 }} />
          </button>
        </div>

        {/* 消息列表 */}
        <div ref={listRef} className="chat-scroll flex-1 overflow-y-auto p-4 space-y-3">
          {msgs.map((m, i) => (
            <div key={i} className={`flex ${m.from === 'me' ? 'justify-end' : 'justify-start'}`}>
              <div
                className={`max-w-[78%] px-3.5 py-2.5 rounded-2xl text-sm leading-relaxed ${
                  m.from === 'me'
                    ? 'text-white rounded-br-md'
                    : 'glass text-zinc-200 rounded-bl-md'
                }`}
                style={
                  m.from === 'me'
                    ? { background: '#1a40ff', boxShadow: '0 4px 16px rgba(168,85,247,.3)' }
                    : {}
                }
              >
                <div>{m.text}</div>
                <div className={`text-[10px] mt-1 ${m.from === 'me' ? 'text-white/50 text-right' : 'text-zinc-500'}`}>{m.time}</div>
              </div>
            </div>
          ))}
          {typing && (
            <div className="flex justify-start">
              <div className="glass rounded-2xl rounded-bl-md px-4 py-3 flex gap-1.5 items-center">
                <span className="typing-dot" /><span className="typing-dot" /><span className="typing-dot" />
              </div>
            </div>
          )}
        </div>

        {/* 快捷短语 */}
        <div className="px-4 pb-2 flex gap-2 overflow-x-auto chat-scroll">
          {QUICK.map((q) => (
            <button
              key={q}
              onClick={() => send(q)}
              className="shrink-0 px-3 py-1.5 rounded-full text-xs text-zinc-400 bg-white/5 border border-white/10 hover:text-violet-300 hover:border-violet-400/50 transition-all duration-300"
            >
              {q}
            </button>
          ))}
        </div>

        {/* 输入区（安全区适配） */}
        <div
          className="flex items-center gap-2 px-4 pt-2 border-t border-white/8"
          style={{ paddingBottom: 'max(12px, env(safe-area-inset-bottom))' }}
        >
          <Input
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onPressEnter={() => send()}
            placeholder="和卖家聊聊…"
            style={{ background: 'rgba(255,255,255,.05)', border: '1px solid rgba(255,255,255,.1)', borderRadius: 999, color: '#fafafa', height: 40 }}
          />
          <button
            onClick={(e) => { ripple(e); send() }}
            className="ripple shrink-0 w-10 h-10 rounded-full grid place-items-center text-white transition-all duration-300 hover:scale-105 active:scale-95"
            style={{ background: '#1a40ff', boxShadow: '0 0 16px rgba(168,85,247,.4)' }}
            aria-label="发送"
          >
            <SendOutlined style={{ fontSize: 14 }} />
          </button>
        </div>
      </div>
    </div>
  )
}