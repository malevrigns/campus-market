import { Drawer, Avatar, Button } from 'antd'
import { CloseOutlined, HeartOutlined, HeartFilled, MessageOutlined, EyeOutlined, FireOutlined, ShoppingCartOutlined, SafetyCertificateOutlined } from '@ant-design/icons'
import { CATEGORY_COLOR } from '../data/products'
import { ripple } from '../utils/ripple'

export default function DetailDrawer({ p, onClose, liked, onLike, onChat, onBuy }) {
  if (!p) {
    return <Drawer open={false} width={540} styles={{ body: { padding: 0, background: '#0a0a12' }, header: { display: 'none' } }} />
  }
  const catColor = CATEGORY_COLOR[p.category]

  return (
    <Drawer
      open={!!p}
      onClose={onClose}
      width={540}
      placement="right"
      closable={false}
      styles={{
        body: { padding: 0, background: '#0a0a12' },
        header: { display: 'none' },
        wrapper: { boxShadow: '-20px 0 80px rgba(5,5,8,.6)' },
      }}
    >
      {/* 大图 */}
      <div className="relative h-72 overflow-hidden">
        <img src={p.img} alt={p.title} className="w-full h-full object-cover" />
        <div className="absolute inset-0 bg-gradient-to-t from-[#0a0a12] via-transparent to-black/30" />
        <button
          onClick={onClose}
          className="absolute top-4 right-4 w-9 h-9 rounded-full glass grid place-items-center text-zinc-200 hover:scale-105 transition-all duration-300"
        >
          <CloseOutlined style={{ fontSize: 13 }} />
        </button>
        <div className="absolute bottom-4 left-5 flex gap-2">
          <span className="glass rounded-lg px-3 py-1 text-xs text-zinc-300 font-mono">{p.campus}</span>
          <span className="glass rounded-lg px-3 py-1 text-xs text-zinc-300 flex items-center gap-1 font-mono">
            <EyeOutlined /> {p.views}
          </span>
          <span className="glass rounded-lg px-3 py-1 text-xs text-zinc-300 flex items-center gap-1 font-mono">
            <FireOutlined /> {p.likes}
          </span>
        </div>
      </div>

      <div className="px-6 pb-6 -mt-2">
        {/* 价格 + 标题 */}
        <div className="flex items-start justify-between gap-4">
          <div>
            <div className="flex items-baseline gap-2.5">
              <span className="text-white font-semibold font-mono text-4xl tabular-nums leading-none">¥{p.price}</span>
              {p.oldPrice && <span className="text-zinc-600 text-sm line-through font-mono tabular-nums">¥{p.oldPrice}</span>}
            </div>
            <h3 className="mt-2.5 text-lg font-semibold text-white leading-snug">{p.title}</h3>
          </div>
        </div>

        {/* 标签 */}
        <div className="mt-3 flex flex-wrap gap-2">
          <span
            className="px-2.5 py-1 rounded-lg text-xs font-medium"
            style={{ background: `${catColor}18`, border: `1px solid ${catColor}33`, color: catColor }}
          >
            {p.category}
          </span>
          <span className="px-2.5 py-1 rounded-lg text-xs text-zinc-300 bg-white/[0.04] border border-white/[0.08]">{p.condition}</span>
          {p.tags.map((t) => (
            <span key={t} className="px-2.5 py-1 rounded-lg text-xs text-zinc-500 bg-white/[0.03] border border-white/[0.06]"># {t}</span>
          ))}
        </div>

        {/* 描述 */}
        <p className="mt-5 text-sm leading-relaxed text-zinc-400">{p.desc}</p>
        <div className="mt-3 text-xs text-zinc-600 font-mono">发布于 {p.time}</div>

        {/* 交易保障 */}
        <div className="mt-5 flex items-center gap-2 glass rounded-xl px-3.5 py-2.5">
          <SafetyCertificateOutlined style={{ color: '#1a40ff', fontSize: 15 }} />
          <span className="text-xs text-zinc-400">平台担保交易 · 面交验货 · 7 天可退</span>
        </div>

        {/* 卖家卡片 */}
        <div className="mt-4 glass rounded-2xl p-4 flex items-center gap-3">
          <div className="relative">
            <Avatar size={44} src={p.seller.avatar} />
            {p.seller.online && (
              <span className="absolute bottom-0 right-0 w-3 h-3 rounded-full bg-emerald-400 border-2 border-[#0a0a12]" />
            )}
          </div>
          <div className="flex-1 min-w-0">
            <div className="flex items-center gap-2">
              <span className="text-sm font-semibold text-white">{p.seller.name}</span>
              <span className={`text-[11px] ${p.seller.online ? 'text-emerald-400' : 'text-zinc-500'}`}>
                {p.seller.online ? '在线' : '离线'}
              </span>
            </div>
            <div className="text-xs text-zinc-500 mt-0.5 font-mono">
              成交 {p.seller.deals} 单 · 好评率 {p.seller.rating}
            </div>
          </div>
        </div>

        {/* 操作 */}
        <div className="mt-6 flex gap-3">
          <Button
            size="large"
            icon={liked ? <HeartFilled style={{ color: '#1a40ff' }} /> : <HeartOutlined />}
            onClick={() => onLike(p.id)}
            className="!rounded-xl !h-12 !px-4"
            style={{
              background: liked ? 'rgba(26,64,255,.1)' : 'rgba(255,255,255,.04)',
              border: `1px solid ${liked ? 'rgba(26,64,255,.4)' : 'rgba(255,255,255,.1)'}`,
              color: liked ? '#9aa3ff' : '#a1a1aa',
            }}
          >
            {liked ? '已藏' : '收藏'}
          </Button>
          <button
            className="btn-ghost ripple h-12 rounded-xl text-sm flex-1"
            onClick={(e) => { ripple(e); onChat(p.seller) }}
          >
            <MessageOutlined /> 私聊
          </button>
          <button
            className="btn-primary ripple h-12 rounded-xl text-base flex-[1.6]"
            onClick={(e) => { ripple(e); onBuy && onBuy(p) }}
          >
            <ShoppingCartOutlined /> 立即购买
          </button>
        </div>
      </div>
    </Drawer>
  )
}
