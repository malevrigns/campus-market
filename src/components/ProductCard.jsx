import { HeartOutlined, HeartFilled, EyeOutlined } from '@ant-design/icons'
import { CATEGORY_COLOR } from '../data/products'
import CountUp from './CountUp'
import Reveal from './Reveal'

// Vision Pro：成色用克制的单色系（浅→深表示新旧，不用彩虹）
const CONDITION_TONE = {
  '全新':   '#e8e8ff',
  '九九新': '#c9ccff',
  '九成新': '#a9adff',
  '八成新': '#8b8dff',
  '七成新': '#6f74d6',
}

export default function ProductCard({ p, index, liked, onLike, onOpen }) {
  const catColor = CATEGORY_COLOR[p.category]
  const condColor = CONDITION_TONE[p.condition] || '#a1a1aa'

  return (
    <Reveal delay={(index % 4) * 70}>
      <div
        className="glass glass-hover rounded-2xl overflow-hidden group cursor-pointer flex flex-col h-full"
        onClick={() => onOpen(p)}
      >
        {/* 图片区 */}
        <div className="relative aspect-[4/3] overflow-hidden bg-white/[0.02]">
          <img
            src={p.img}
            alt={p.title}
            loading="lazy"
            className="w-full h-full object-cover transition-transform duration-[600ms] ease-out group-hover:scale-[1.04]"
          />
          <div className="absolute inset-0 bg-gradient-to-t from-black/55 via-transparent to-transparent opacity-70" />

          {/* 成色徽章 —— 柔和单色 */}
          <span
            className="absolute top-3 left-3 px-2.5 py-1 rounded-lg text-[11px] font-medium backdrop-blur-md tracking-wide"
            style={{ background: `${condColor}1f`, border: `1px solid ${condColor}40`, color: condColor }}
          >
            {p.condition}
          </span>

          {/* hover 快捷操作 */}
          <div className="absolute top-3 right-3 flex flex-col gap-2 opacity-0 translate-x-2 group-hover:opacity-100 group-hover:translate-x-0 transition-all duration-300">
            <button
              onClick={(e) => { e.stopPropagation(); onLike(p.id) }}
              className="w-9 h-9 rounded-full glass grid place-items-center text-zinc-200 hover:scale-110 transition-all duration-300"
              aria-label="收藏"
            >
              {liked ? <HeartFilled style={{ fontSize: 15, color: '#1a40ff' }} /> : <HeartOutlined style={{ fontSize: 15 }} />}
            </button>
          </div>

          {/* hover 底部信息 */}
          <div className="absolute bottom-3 left-3 right-3 flex gap-2 opacity-0 translate-y-2 group-hover:opacity-100 group-hover:translate-y-0 transition-all duration-300">
            <span className="glass rounded-lg px-3 py-1 text-[11px] text-zinc-300 font-mono">{p.campus}</span>
          </div>
        </div>

        {/* 内容区 */}
        <div className="p-4 flex flex-col flex-1">
          <h3 className="font-medium text-[15px] leading-snug line-clamp-1 text-zinc-100 transition-colors duration-300 group-hover:text-white">
            {p.title}
          </h3>

          <div className="mt-2.5 flex gap-1.5 flex-wrap">
            <span
              className="px-2 py-0.5 rounded-md text-[11px] font-medium"
              style={{ background: `${catColor}18`, border: `1px solid ${catColor}33`, color: catColor }}
            >
              {p.category}
            </span>
            {p.tags.slice(0, 2).map((t) => (
              <span key={t} className="px-2 py-0.5 rounded-md text-[11px] text-zinc-500 bg-white/[0.04] border border-white/[0.08]">
                {t}
              </span>
            ))}
          </div>

          <div className="mt-auto pt-3.5 flex items-end justify-between gap-2">
            <div className="flex items-baseline gap-2 min-w-0">
              <span className="text-white font-semibold font-mono text-xl leading-none tabular-nums">¥{p.price}</span>
              {p.oldPrice && <span className="text-zinc-600 text-xs line-through tabular-nums">¥{p.oldPrice}</span>}
            </div>
            <span className="text-[11px] text-zinc-600 flex items-center gap-1 shrink-0 font-mono tabular-nums">
              <EyeOutlined /> <CountUp to={p.views} duration={900} />
            </span>
          </div>
        </div>
      </div>
    </Reveal>
  )
}
