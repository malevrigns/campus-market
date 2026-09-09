import { useEffect, useState } from 'react'
import { Modal, App as AntApp } from 'antd'
import { EnvironmentOutlined, ReloadOutlined } from '@ant-design/icons'
import { api, money, dateTime, STATUS } from '../lib/api'
import { Button, EmptyState, ErrorState, Loading, PageHeading, ProductImage } from './ui'
export default function Orders({ user, config, onPay, revision }) {
  const { message, modal } = AntApp.useApp()
  const [orders, setOrders] = useState([]),
    [filter, setFilter] = useState('all'),
    [error, setError] = useState(null),
    [loading, setLoading] = useState(true),
    [busy, setBusy] = useState(''),
    [refund, setRefund] = useState(null),
    [detail, setDetail] = useState(null)
  const load = async (signal) => {
    try {
      const d = await api('/orders', { signal })
      setOrders(d.items)
      setError(null)
    } catch (e) {
      if (!signal?.aborted) setError(e)
    } finally {
      if (!signal?.aborted) setLoading(false)
    }
  }
  useEffect(() => {
    const ac = new AbortController()
    load(ac.signal)
    const timer = setInterval(() => load(ac.signal), 15000)
    return () => {
      ac.abort()
      clearInterval(timer)
    }
  }, [revision])
  const action = async (o, verb, body = {}) => {
    setBusy(o.id)
    try {
      await api(`/orders/${o.id}/${verb}`, { method: 'POST', body })
      message.success(verb === 'refund' ? '退款申请已记录，等待人工处理' : '订单已更新')
      setRefund(null)
      await load()
    } catch (e) {
      message.error(e.message)
    } finally {
      setBusy('')
    }
  }
  const confirm = (o, verb) =>
    modal.confirm({
      title: verb === 'handover' ? '确认已当面交给买家？' : '确认已收到并验好商品？',
      content: '请在实际交接完成后确认，订单将记录这次操作。',
      okText: '确认',
      cancelText: '再检查一下',
      onOk: () => action(o, verb),
    })
  const items = orders.filter(
    (o) =>
      filter === 'all' ||
      (filter === 'selling' && o.seller_id === user.id) ||
      (filter === 'buying' && o.buyer_id === user.id) ||
      o.status === filter,
  )
  return (
    <>
      <PageHeading
        eyebrow="EVERY FIND HAS A JOURNEY"
        title="我的订单"
        description="从一次心动，到一次顺利的交接。"
      >
        <Button variant="secondary" onClick={() => load()}>
          <ReloadOutlined />
          刷新
        </Button>
      </PageHeading>
      <div className="category-tabs order-tabs">
        {[
          ['all', '全部订单'],
          ['buying', '我买到的'],
          ['selling', '我卖出的'],
          ['pending', '待付款'],
          ['paid', '待面交'],
          ['completed', '已完成'],
        ].map(([id, name]) => (
          <button key={id} className={filter === id ? 'active' : ''} onClick={() => setFilter(id)}>
            {name}
          </button>
        ))}
      </div>
      {error && <ErrorState error={error} retry={() => load()} />}
      {loading ? (
        <Loading />
      ) : items.length === 0 ? (
        <EmptyState
          title="你的好物旅程还没开始"
          description="去集市逛逛，总有一件适合你。"
          action="发现好物"
          onAction={() => {
            location.hash = ''
          }}
        />
      ) : (
        <div className="order-list">
          {items.map((o) => (
            <article key={o.id} className="order-card">
              <div className="order-card-top">
                <span>
                  {o.buyer_id === user.id ? '我买到的' : '我卖出的'}
                  <i>·</i>
                  {dateTime(o.created_at)}
                </span>
                <span className={`order-status status-${o.status}`}>{STATUS[o.status]}</span>
              </div>
              <div className="order-card-body">
                <ProductImage src={o.image} alt={o.title} />
                <div>
                  <h3>{o.title}</h3>
                  <p>
                    <EnvironmentOutlined />
                    {o.campus}面交
                  </p>
                  <button
                    className="order-id"
                    onClick={async () => {
                      try {
                        setDetail(await api(`/orders/${o.id}`))
                      } catch (e) {
                        message.error(e.message)
                      }
                    }}
                  >
                    订单 {o.id.slice(0, 14)}… · 查看详情
                  </button>
                </div>
                <strong className="price">{money(o.amount_ct)}</strong>
              </div>
              <div className="order-actions">
                {o.status === 'pending' && o.buyer_id === user.id && (
                  <>
                    <Button variant="ghost" busy={busy === o.id} onClick={() => action(o, 'cancel')}>
                      取消订单
                    </Button>
                    <Button onClick={() => onPay(o)} disabled={!config?.payments?.alipay}>
                      继续付款
                    </Button>
                  </>
                )}
                {o.status === 'paid' && o.seller_id === user.id && (
                  <Button busy={busy === o.id} onClick={() => confirm(o, 'handover')}>
                    已交给买家
                  </Button>
                )}
                {o.status === 'handover' && o.buyer_id === user.id && (
                  <Button busy={busy === o.id} onClick={() => confirm(o, 'complete')}>
                    确认收货
                  </Button>
                )}
                {['paid', 'handover', 'completed'].includes(o.status) && o.buyer_id === user.id && (
                  <Button variant="ghost" onClick={() => setRefund(o)}>
                    申请退款
                  </Button>
                )}
                {o.status === 'refund_requested' && (
                  <span className="muted">申请已记录，等待人工处理；尚未退款。</span>
                )}
              </div>
            </article>
          ))}
        </div>
      )}
      <Modal open={!!refund} onCancel={() => setRefund(null)} footer={null} title="申请退款" destroyOnClose>
        <p className="modal-subtitle">说明问题，平台会记录申请并交由人工处理。</p>
        <form
          className="form-stack"
          onSubmit={(e) => {
            e.preventDefault()
            action(refund, 'refund', { reason: new FormData(e.currentTarget).get('reason') })
          }}
        >
          <label>
            退款原因
            <textarea name="reason" required minLength={2} maxLength={500} rows={4} />
          </label>
          <Button busy={busy === refund?.id} type="submit">
            提交申请
          </Button>
        </form>
      </Modal>
      <Modal open={!!detail} onCancel={() => setDetail(null)} footer={null} title="订单进度">
        {detail && (
          <>
            <code className="break-word">{detail.id}</code>
            <div className="timeline">
              {detail.events?.map((event, i) => (
                <div key={i}>
                  <span />
                  <strong>{STATUS[event.status]}</strong>
                  <small>{dateTime(event.created_at)}</small>
                </div>
              ))}
            </div>
          </>
        )}
      </Modal>
    </>
  )
}
