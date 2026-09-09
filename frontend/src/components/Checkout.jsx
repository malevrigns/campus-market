import { useEffect, useRef, useState } from 'react'
import { Modal } from 'antd'
import { AlipayCircleOutlined, ArrowRightOutlined, CheckOutlined, LockOutlined } from '@ant-design/icons'
import { api, CAMPUSES, money, newKey, STATUS } from '../lib/api'
import { Button, ErrorState, ProductImage } from './ui'

export default function PaymentCheckout({ product, order: initialOrder, config, onClose, onOrders }) {
  const [order, setOrder] = useState(initialOrder || null),
    [campus, setCampus] = useState(product?.campus || initialOrder?.campus || CAMPUSES[0])
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(null),
    [url, setUrl] = useState('')
  const idem = useRef(newKey()),
    mounted = useRef(true)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])
  useEffect(() => {
    if (!order?.id || order.status !== 'pending') return
    const ac = new AbortController()
    const poll = () =>
      api(`/orders/${order.id}`, { signal: ac.signal })
        .then((o) => {
          setOrder(o)
          setError(null)
        })
        .catch((e) => {
          if (!ac.signal.aborted) setError(e)
        })
    const timer = setInterval(poll, 3500)
    window.addEventListener('focus', poll)
    return () => {
      clearInterval(timer)
      ac.abort()
      window.removeEventListener('focus', poll)
    }
  }, [order?.id, order?.status])
  const create = async () => {
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      const o = await api('/orders', {
        method: 'POST',
        body: { product_id: product.id, campus, idempotency_key: idem.current },
      })
      if (mounted.current) setOrder(o)
    } catch (e) {
      if (mounted.current) setError(e)
    } finally {
      if (mounted.current) setBusy(false)
    }
  }
  const pay = async () => {
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      const d = await api(`/orders/${order.id}/pay`, {
        method: 'POST',
        body: { method: 'alipay', mobile: window.matchMedia('(max-width: 767px)').matches },
      })
      const target = new URL(d.checkout_url)
      if (
        target.protocol !== 'https:' ||
        !['openapi.alipay.com', 'openapi-sandbox.dl.alipaydev.com'].includes(target.hostname)
      )
        throw new Error('支付地址无效，请联系平台')
      setUrl(d.checkout_url)
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  const paid = order && ['paid', 'handover', 'completed'].includes(order.status)
  return (
    <Modal
      open
      onCancel={busy ? undefined : onClose}
      footer={null}
      title={paid ? '付款已确认' : '确认这一次心动'}
      width={480}
      className="checkout-modal"
    >
      <div className="checkout-progress">
        <span className="active">01 确认订单</span>
        <span className={order ? 'active' : ''}>02 安全付款</span>
        <span className={paid ? 'active' : ''}>03 约好面交</span>
      </div>
      {paid && (
        <div className="payment-success">
          <span>
            <CheckOutlined />
          </span>
          <h2>好物，向你而来。</h2>
          <p>服务器已确认收款，联系卖家约个面交时间吧。</p>
        </div>
      )}
      <div className="checkout-product">
        <ProductImage src={order?.image || product?.image} alt={order?.title || product?.title} />
        <div>
          <strong>{order?.title || product?.title}</strong>
          <small>{order ? STATUS[order.status] : product?.condition}</small>
          <span>{money(order?.amount_ct ?? product?.price_ct ?? 0)}</span>
        </div>
      </div>
      <div className="form-stack checkout-fields">
        <label>
          面交校区
          <select value={campus} onChange={(e) => setCampus(e.target.value)} disabled={!!order}>
            {CAMPUSES.map((c) => (
              <option key={c}>{c}</option>
            ))}
          </select>
        </label>
        {order && (
          <div className="order-reference">
            <span>订单编号</span>
            <code>{order.id}</code>
          </div>
        )}
        <div className="fee-row">
          <span>{order ? '订单金额' : '商品标价'}</span>
          <strong>{money(order?.amount_ct ?? product?.price_ct ?? 0)}</strong>
        </div>
      </div>
      {!paid && (
        <>
          <div className="payment-method">
            <AlipayCircleOutlined />
            <div>
              <strong>支付宝</strong>
              <small>{config?.payments?.alipay ? '前往支付宝完成付款' : '暂未开通在线支付'}</small>
            </div>
            <LockOutlined />
          </div>
          <p className="checkout-explainer">
            {order
              ? '付款结果以平台确认的信息为准。若支付后状态未更新，请稍后查看订单，不要重复付款。'
              : '提交后为你保留商品 15 分钟。订单金额以提交后显示的信息为准。'}
          </p>
        </>
      )}
      {error && <ErrorState error={error} />}
      {paid || order?.status === 'cancelled' ? (
        <Button className="full-width" onClick={onOrders}>
          查看我的订单
          <ArrowRightOutlined />
        </Button>
      ) : !order ? (
        <Button className="full-width" busy={busy} onClick={create}>
          确认订单
          <ArrowRightOutlined />
        </Button>
      ) : url ? (
        <>
          <a
            className="button button-primary full-width"
            href={url}
            target="_blank"
            rel="noopener noreferrer"
          >
            前往支付宝付款
            <ArrowRightOutlined />
          </a>
          <Button variant="ghost" className="full-width" onClick={onOrders}>
            稍后在订单中查看结果
          </Button>
        </>
      ) : (
        <Button className="full-width" busy={busy} disabled={!config?.payments?.alipay} onClick={pay}>
          {config?.payments?.alipay ? '获取支付入口' : '在线支付暂未开通'}
        </Button>
      )}
      {order && !paid && (
        <button className="text-button checkout-later" onClick={onOrders}>
          保存订单，稍后处理
        </button>
      )}
    </Modal>
  )
}
