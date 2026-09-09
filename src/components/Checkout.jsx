import { useState } from 'react'
import { Modal, Spin } from 'antd'
import { CloseOutlined, CheckCircleFilled, WalletOutlined, WechatOutlined, AlipayCircleOutlined, SafetyCertificateOutlined, ClockCircleOutlined, EnvironmentOutlined } from '@ant-design/icons'
import { ripple } from '../utils/ripple'

const PAY_METHODS = [
  { id: 'wallet', name: '校园钱包', icon: <WalletOutlined />, balance: 2680, desc: '余额 ¥2,680.00', color: '#1a40ff' },
  { id: 'wechat', name: '微信支付', icon: <WechatOutlined />, desc: '推荐使用', color: '#34d399' },
  { id: 'alipay', name: '支付宝', icon: <AlipayCircleOutlined />, desc: '花呗分期可用', color: '#60a5fa' },
]

export default function Checkout({ p, onClose, onPaid }) {
  const [method, setMethod] = useState('wallet')
  const [campus, setCampus] = useState(p ? p.campus : '')
  const [stage, setStage] = useState('confirm') // confirm | paying | done
  const [orderId, setOrderId] = useState('')

  if (!p) return null

  const serviceFee = Math.round(p.price * 0.02)
  const total = p.price + serviceFee
  const campuses = ['东区 · 一食堂门口', '西区 · 图书馆前', '南区 · 宿舍区中心', '北区 · 快递站旁']

  const doPay = () => {
    if (stage !== 'confirm') return
    setStage('paying')
    setTimeout(() => {
      const oid = 'CM' + Date.now().toString().slice(-10) + Math.floor(Math.random() * 90 + 10)
      setOrderId(oid)
      setStage('done')
    }, 2200)
  }

  const finish = () => {
    onClose && onClose()
    onPaid && onPaid({ orderId, product: p, method: PAY_METHODS.find((m) => m.id === method).name, campus, total })
  }

  return (
    <Modal
      open={!!p}
      onCancel={() => stage !== 'paying' && onClose()}
      footer={null}
      width={520}
      centered
      closable={false}
      styles={{ body: { padding: 0, background: '#0a0a12' }, content: { background: '#0a0a12' }, mask: { background: 'rgba(5,5,8,.7)' } }}
    >
      {/* 头部 */}
      <div className="flex items-center justify-between px-6 py-4 border-b border-white/[0.06]">
        <div className="flex items-center gap-2">
          <SafetyCertificateOutlined style={{ color: '#1a40ff' }} />
          <span className="text-sm font-semibold text-white tracking-wide">{stage === 'done' ? '支付完成' : '安全结算'}</span>
          {stage === 'done' && <CheckCircleFilled style={{ color: '#34d399', fontSize: 15 }} />}
        </div>
        {stage !== 'paying' && (
          <button onClick={onClose} className="w-8 h-8 rounded-full glass grid place-items-center text-zinc-400 hover:text-white transition-colors">
            <CloseOutlined style={{ fontSize: 12 }} />
          </button>
        )}
      </div>

      {stage === 'done' ? (
        /* ===== 支付成功页 ===== */
        <div className="px-6 py-8 text-center">
          <div className="mx-auto w-16 h-16 rounded-full bg-emerald-400/10 border border-emerald-400/30 grid place-items-center mb-5 animate-fade-up">
            <CheckCircleFilled style={{ fontSize: 34, color: '#34d399' }} />
          </div>
          <h3 className="text-xl font-semibold text-white">支付成功</h3>
          <p className="mt-2 text-sm text-zinc-500">订单已进入担保交易，面交验货后货款将转给卖家</p>

          {/* 订单信息 */}
          <div className="mt-6 glass rounded-xl p-4 text-left space-y-3">
            <Row label="订单编号" value={<span className="font-mono tabular-nums">{orderId}</span>} />
            <Row label="商品" value={p.title} />
            <Row label="支付方式" value={PAY_METHODS.find((m) => m.id === method).name} />
            <Row label="面交地点" value={campus} />
            <Row label="实付金额" value={<span className="font-mono tabular-nums text-emerald-400">¥{total}</span>} last />
          </div>

          {/* 状态时间线 */}
          <div className="mt-5 flex items-center justify-between px-2">
            <Step done title="已下单" time="刚刚" />
            <Line active />
            <Step active title="待面交" time="24h 内" />
            <Line />
            <Step title="已完成" time="" />
          </div>

          <button className="btn-primary ripple mt-7 w-full !h-12 rounded-xl text-base" onClick={(e) => { ripple(e); finish() }}>
            查看我的订单
          </button>
        </div>
      ) : (
        /* ===== 确认支付页 ===== */
        <div className="px-6 py-5 max-h-[70vh] overflow-y-auto chat-scroll">
          {/* 商品摘要 */}
          <div className="flex gap-3 glass rounded-xl p-3">
            <img src={p.img} alt={p.title} className="w-16 h-16 rounded-lg object-cover shrink-0" />
            <div className="flex-1 min-w-0">
              <div className="text-sm text-zinc-200 line-clamp-1">{p.title}</div>
              <div className="mt-1 text-xs text-zinc-500">{p.condition} · {p.category}</div>
              <div className="mt-1.5 font-mono tabular-nums text-white font-semibold">¥{p.price}</div>
            </div>
          </div>

          {/* 面交地点 */}
          <div className="mt-5">
            <label className="text-xs text-zinc-500 flex items-center gap-1.5"><EnvironmentOutlined /> 面交地点（校区）</label>
            <div className="mt-2 grid grid-cols-2 gap-2">
              {campuses.map((c) => (
                <button key={c} onClick={() => setCampus(c)}
                  className={`rounded-lg px-3 py-2 text-xs transition-all duration-200 border ${campus === c ? 'bg-[#1a40ff]/12 border-[#1a40ff]/50 text-white' : 'bg-white/[0.03] border-white/[0.08] text-zinc-400 hover:border-white/20'}`}>
                  {c}
                </button>
              ))}
            </div>
          </div>

          {/* 支付方式 */}
          <div className="mt-5">
            <label className="text-xs text-zinc-500">支付方式</label>
            <div className="mt-2 space-y-2">
              {PAY_METHODS.map((m) => (
                <button key={m.id} onClick={() => setMethod(m.id)}
                  className={`w-full flex items-center gap-3 rounded-xl px-4 py-3 transition-all duration-200 border ${method === m.id ? 'bg-[#1a40ff]/10 border-[#1a40ff]/50' : 'bg-white/[0.03] border-white/[0.08] hover:border-white/20'}`}>
                  <span style={{ color: m.color, fontSize: 20 }}>{m.icon}</span>
                  <div className="flex-1 text-left">
                    <div className="text-sm text-white font-medium">{m.name}</div>
                    <div className="text-[11px] text-zinc-500 mt-0.5">{m.desc}</div>
                  </div>
                  <span className={`w-4 h-4 rounded-full border-2 grid place-items-center ${method === m.id ? 'border-[#1a40ff]' : 'border-zinc-600'}`}>
                    {method === m.id && <span className="w-2 h-2 rounded-full bg-[#1a40ff]" />}
                  </span>
                </button>
              ))}
            </div>
          </div>

          {/* 费用明细 */}
          <div className="mt-5 space-y-2 text-sm">
            <FeeRow label="商品价格" value={`¥${p.price}`} />
            <FeeRow label="平台服务费 (2%)" value={`¥${serviceFee}`} />
            <div className="border-t border-white/[0.06] pt-2.5 flex items-center justify-between">
              <span className="text-zinc-400 text-sm">应付总额</span>
              <span className="font-mono tabular-nums text-2xl font-semibold text-white">¥{total}</span>
            </div>
          </div>

          {/* 担保提示 */}
          <div className="mt-4 flex items-start gap-2 text-[11px] text-zinc-500 leading-relaxed">
            <SafetyCertificateOutlined style={{ color: '#1a40ff', marginTop: 1, fontSize: 13 }} />
            <span>付款后资金由平台托管，双方面交验货确认后自动打款给卖家。若交易纠纷可申请平台介入。</span>
          </div>

          {/* 支付按钮 */}
          <button className="btn-primary ripple mt-5 w-full !h-13 py-3.5 rounded-xl text-base" onClick={(e) => { ripple(e); doPay() }}>
            {stage === 'paying' ? (
              <span className="inline-flex items-center gap-2"><Spin size="small" /> 正在处理支付…</span>
            ) : (
              <>确认支付 <span className="font-mono tabular-nums">¥{total}</span></>
            )}
          </button>
        </div>
      )}
    </Modal>
  )
}

function Row({ label, value, last }) {
  return (
    <div className={`flex items-center justify-between gap-3 ${last ? '' : ''}`}>
      <span className="text-xs text-zinc-500 shrink-0">{label}</span>
      <span className="text-xs text-zinc-200 text-right truncate">{value}</span>
    </div>
  )
}
function FeeRow({ label, value }) {
  return (
    <div className="flex items-center justify-between">
      <span className="text-zinc-500 text-xs">{label}</span>
      <span className="font-mono tabular-nums text-zinc-300 text-xs">{value}</span>
    </div>
  )
}
function Step({ done, active, title, time }) {
  return (
    <div className="flex flex-col items-center gap-1.5">
      <span className={`w-6 h-6 rounded-full grid place-items-center text-[10px] border ${done ? 'bg-emerald-400/20 border-emerald-400/50 text-emerald-400' : active ? 'bg-[#1a40ff]/20 border-[#1a40ff]/50 text-[#9aa3ff]' : 'bg-white/[0.04] border-white/10 text-zinc-600'}`}>
        {done ? <CheckCircleFilled style={{ fontSize: 13 }} /> : <ClockCircleOutlined style={{ fontSize: 12 }} />}
      </span>
      <span className={`text-[10px] ${active || done ? 'text-zinc-300' : 'text-zinc-600'}`}>{title}</span>
      {time && <span className="text-[9px] text-zinc-600 font-mono">{time}</span>}
    </div>
  )
}
function Line({ active }) {
  return <div className={`flex-1 h-px mx-1 ${active ? 'bg-gradient-to-r from-emerald-400/50 to-[#1a40ff]/40' : 'bg-white/10'}`} />
}
