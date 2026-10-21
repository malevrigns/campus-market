import { useEffect, useRef, useState } from 'react'
import { App as AntApp, Modal } from 'antd'
import { FireOutlined, PlusOutlined, ReloadOutlined, ThunderboltOutlined } from '@ant-design/icons'
import { api, money, toCents, CAMPUSES } from '../lib/api'
import { Button, EmptyState, ErrorState, Loading, PageHeading, ProductImage } from './ui'
import './flash.css'

const pad = (n) => String(n).padStart(2, '0')
function countdown(ms) {
  const s = Math.max(0, Math.floor(ms / 1000))
  const d = Math.floor(s / 86400)
  const clock = `${pad(Math.floor((s % 86400) / 3600))}:${pad(Math.floor((s % 3600) / 60))}:${pad(s % 60)}`
  return d > 0 ? `${d} 天 ${clock}` : clock
}
// 倒计时以服务端时间为准：用户本机时间可能不准，不能决定能否开抢。
function useServerClock(offset) {
  const [now, setNow] = useState(() => Date.now() + offset)
  useEffect(() => {
    setNow(Date.now() + offset)
    const timer = setInterval(() => setNow(Date.now() + offset), 250)
    return () => clearInterval(timer)
  }, [offset])
  return now
}
const phaseOf = (sale, now) => (now < sale.start_at ? 'upcoming' : now >= sale.end_at ? 'ended' : 'active')
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

export default function FlashSales({ user, onRequireUser, onOrders, onOpenProduct }) {
  const { message } = AntApp.useApp()
  const [sales, setSales] = useState([]),
    [hot, setHot] = useState([]),
    [offset, setOffset] = useState(0),
    [loading, setLoading] = useState(true),
    [error, setError] = useState(null),
    [tickets, setTickets] = useState({}),
    [busy, setBusy] = useState(''),
    [createOpen, setCreateOpen] = useState(false)
  const alive = useRef(true)
  const now = useServerClock(offset)
  const load = async (signal) => {
    try {
      const [d, h] = await Promise.all([
        api('/flash-sales', { signal }),
        api('/products/hot?limit=8', { signal }).catch(() => ({ items: [] })),
      ])
      setOffset(d.server_time - Date.now())
      setSales(d.items)
      setHot(h.items)
      setError(null)
    } catch (e) {
      if (!signal?.aborted) setError(e)
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }
  useEffect(() => {
    alive.current = true
    const ac = new AbortController()
    load(ac.signal)
    const timer = setInterval(() => load(ac.signal), 5000)
    return () => {
      alive.current = false
      ac.abort()
      clearInterval(timer)
    }
  }, [])
  const saleIds = sales.map((s) => s.id).join(',')
  useEffect(() => {
    if (!user || !saleIds) return
    const ac = new AbortController()
    Promise.all(
      saleIds.split(',').map((id) =>
        api(`/flash-sales/${id}/attempts/me`, { signal: ac.signal })
          .then((t) => [id, t])
          .catch(() => null),
      ),
    ).then((list) => {
      if (!ac.signal.aborted) setTickets(Object.fromEntries(list.filter(Boolean)))
    })
    return () => ac.abort()
  }, [user?.id, saleIds])
  // 抢购接口只做 Redis 预扣并返回 202，订单由后台异步落库，这里轮询结果。
  const waitResult = async (id) => {
    for (let i = 0; i < 30 && alive.current; i++) {
      await sleep(700)
      try {
        const t = await api(`/flash-sales/${id}/attempts/me`)
        setTickets((m) => ({ ...m, [id]: t }))
        if (t.state !== 'queued') return t
      } catch {
        // 网络抖动时继续轮询
      }
    }
    return null
  }
  const grab = (sale) =>
    onRequireUser(async () => {
      setBusy(sale.id)
      try {
        const t = await api(`/flash-sales/${sale.id}/attempts`, { method: 'POST', body: {} })
        setTickets((m) => ({ ...m, [sale.id]: t }))
        const done = t.state === 'queued' ? await waitResult(sale.id) : t
        if (done?.state === 'success') message.success('抢到了！请在 15 分钟内完成支付')
        else if (done?.state === 'failed') message.warning('很遗憾，库存已被抢完')
        else if (!done) message.info('订单仍在生成中，稍后可在“我的订单”查看')
      } catch (e) {
        message.error(e.message)
      } finally {
        setBusy('')
        load()
      }
    })
  const active = sales.filter((s) => phaseOf(s, now) !== 'ended')
  return (
    <>
      <PageHeading
        eyebrow="FLASH SALE · LIMITED"
        title="限时抢购"
        description="社团义卖、毕业季清仓。每人限购一件，先到先得。"
      >
        <div className="flash-actions">
          {user?.is_admin && (
            <Button variant="secondary" onClick={() => setCreateOpen(true)}>
              <PlusOutlined />
              发起活动
            </Button>
          )}
          <Button variant="secondary" onClick={() => load()}>
            <ReloadOutlined />
            刷新
          </Button>
        </div>
      </PageHeading>
      {loading ? (
        <Loading />
      ) : error ? (
        <ErrorState error={error} retry={() => load()} />
      ) : active.length === 0 ? (
        <EmptyState
          icon={<ThunderboltOutlined />}
          title="暂时没有进行中的活动"
          description="新活动开始前会出现在这里，记得常来看看。"
        />
      ) : (
        <div className="flash-grid">
          {active.map((s) => (
            <FlashCard
              key={s.id}
              sale={s}
              now={now}
              ticket={tickets[s.id]}
              busy={busy === s.id}
              mine={user?.id === s.seller_id}
              onGrab={() => grab(s)}
              onOrders={onOrders}
            />
          ))}
        </div>
      )}
      {hot.length > 0 && (
        <section className="hot-section" aria-label="校园热榜">
          <h2>
            <FireOutlined /> 校园热榜
            <small>近 3 天浏览、收藏、下单热度</small>
          </h2>
          <ol className="hot-list">
            {hot.map((p, i) => (
              <li key={p.id}>
                <button onClick={() => onOpenProduct(p)}>
                  <span className={`hot-rank rank-${i + 1}`}>{i + 1}</span>
                  <ProductImage src={p.image} alt="" />
                  <span className="hot-title">{p.title}</span>
                  <span className="hot-price">{money(p.price_ct)}</span>
                  <span className="hot-score">热度 {Math.round(p.score)}</span>
                </button>
              </li>
            ))}
          </ol>
        </section>
      )}
      <CreateSale
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={() => {
          setCreateOpen(false)
          message.success('活动已创建并完成库存预热')
          load()
        }}
      />
    </>
  )
}

function FlashCard({ sale, now, ticket, busy, mine, onGrab, onOrders }) {
  const phase = phaseOf(sale, now)
  const left = sale.live_stock >= 0 ? sale.live_stock : sale.stock
  const sold = Math.max(0, sale.total_stock - left)
  const percent = Math.min(100, Math.round((sold / sale.total_stock) * 100))
  const state = ticket?.state
  let label = '立即抢购',
    disabled = false,
    action = onGrab
  if (state === 'success') {
    label = ticket.order_status === 'pending' ? '已抢到 · 去支付' : '已抢到 · 查看订单'
    action = onOrders
  } else if (state === 'queued') {
    label = '排队生成订单中…'
    disabled = true
  } else if (state === 'cancelled') {
    label = '订单已取消'
    disabled = true
  } else if (mine) {
    label = '我发起的活动'
    disabled = true
  } else if (phase === 'upcoming') {
    label = '即将开抢'
    disabled = true
  } else if (left <= 0) {
    label = '已抢光'
    disabled = true
  }
  return (
    <article className="flash-card">
      <div className="flash-photo">
        <ProductImage src={sale.image} alt={sale.title} loading="lazy" />
        <span className={`flash-phase phase-${phase}`}>
          {phase === 'upcoming' ? '距开抢' : '距结束'}{' '}
          {countdown((phase === 'upcoming' ? sale.start_at : sale.end_at) - now)}
        </span>
      </div>
      <div className="flash-body">
        <h3>{sale.title}</h3>
        <p className="flash-desc">{sale.description}</p>
        <div className="flash-price">
          <strong>{money(sale.price_ct)}</strong>
          <del>{money(sale.original_price_ct)}</del>
          <span>{sale.campus}</span>
        </div>
        <div className="flash-progress" aria-label={`已抢 ${percent}%`}>
          <span style={{ width: `${percent}%` }} />
        </div>
        <div className="flash-meta">
          <span>已抢 {percent}%</span>
          <span>
            剩余 {left} / {sale.total_stock}
          </span>
        </div>
        <Button className="flash-button" busy={busy} disabled={disabled} onClick={action}>
          <ThunderboltOutlined />
          {label}
        </Button>
        {state === 'failed' && <p className="flash-note">上次没抢到，库存释放后可以再试一次。</p>}
      </div>
    </article>
  )
}

function CreateSale({ open, onClose, onCreated }) {
  const { message } = AntApp.useApp()
  const [form, setForm] = useState({
    title: '',
    description: '',
    image: '',
    campus: CAMPUSES[0],
    price: '',
    original: '',
    stock: '50',
    startIn: '1',
    hours: '2',
  })
  const [busy, setBusy] = useState(false)
  const set = (k) => (e) => setForm((f) => ({ ...f, [k]: e.target.value }))
  const submit = async () => {
    setBusy(true)
    try {
      const start = Date.now() + Number(form.startIn) * 60000
      await api('/admin/flash-sales', {
        method: 'POST',
        body: {
          title: form.title,
          description: form.description,
          image: form.image,
          campus: form.campus,
          price_ct: toCents(form.price),
          original_price_ct: toCents(form.original),
          total_stock: Number(form.stock),
          start_at: start,
          end_at: start + Number(form.hours) * 3600000,
        },
      })
      onCreated()
    } catch (e) {
      message.error(e.message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal open={open} onCancel={onClose} footer={null} title="发起限时抢购" destroyOnClose>
      <div className="flash-form">
        <label>
          活动标题
          <input
            value={form.title}
            onChange={set('title')}
            maxLength={80}
            placeholder="例如：毕业季台灯 1 元抢"
          />
        </label>
        <label>
          说明
          <textarea value={form.description} onChange={set('description')} rows={3} maxLength={3000} />
        </label>
        <label>
          图片地址（HTTPS）
          <input value={form.image} onChange={set('image')} placeholder="https://..." />
        </label>
        <div className="flash-form-row">
          <label>
            抢购价（元）
            <input value={form.price} onChange={set('price')} inputMode="decimal" />
          </label>
          <label>
            原价（元）
            <input value={form.original} onChange={set('original')} inputMode="decimal" />
          </label>
          <label>
            库存
            <input value={form.stock} onChange={set('stock')} inputMode="numeric" />
          </label>
        </div>
        <div className="flash-form-row">
          <label>
            面交校区
            <select value={form.campus} onChange={set('campus')}>
              {CAMPUSES.map((c) => (
                <option key={c}>{c}</option>
              ))}
            </select>
          </label>
          <label>
            几分钟后开始
            <input value={form.startIn} onChange={set('startIn')} inputMode="numeric" />
          </label>
          <label>
            持续小时
            <input value={form.hours} onChange={set('hours')} inputMode="numeric" />
          </label>
        </div>
        <Button busy={busy} onClick={submit}>
          创建并预热库存
        </Button>
      </div>
    </Modal>
  )
}
