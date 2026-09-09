import { Select, Empty } from 'antd'
import { HeartFilled } from '@ant-design/icons'
import { CATEGORIES } from '../data/products'
import ProductCard from './ProductCard'
import Reveal from './Reveal'

const SORTS = [
  { value: 'new', label: '最新发布' },
  { value: 'hot', label: '最多浏览' },
  { value: 'price-asc', label: '价格从低到高' },
  { value: 'price-desc', label: '价格从高到低' },
]

function SkeletonCard() {
  return (
    <div className="glass rounded-2xl overflow-hidden">
      <div className="aspect-[4/3] shimmer" />
      <div className="p-4 space-y-3">
        <div className="h-4 w-3/4 rounded shimmer" />
        <div className="h-3 w-1/2 rounded shimmer" />
        <div className="flex justify-between items-center pt-1">
          <div className="h-6 w-16 rounded shimmer" />
          <div className="h-3 w-10 rounded shimmer" />
        </div>
      </div>
    </div>
  )
}

export default function Market({ products, loading, liked, onLike, onOpen, category, onCategory, sort, onSort, favOnly, onToggleFavOnly }) {
  return (
    <section className="pt-28 sm:pt-32">
      {/* 区块标题 */}
      <Reveal>
        <div className="flex items-end justify-between flex-wrap gap-6 mb-10">
          <div>
            <div className="text-xs tracking-[.35em] text-violet-400 font-semibold mb-3">MARKETPLACE</div>
            <h2 className="font-display text-4xl sm:text-5xl font-bold tracking-tight text-white">
              好物<span className="text-gradient">集市</span>
            </h2>
          </div>
          <div className="flex items-center gap-3">
            <button
              onClick={onToggleFavOnly}
              className={`flex items-center gap-1.5 px-4 py-2 rounded-full text-sm transition-all duration-300 ${
                favOnly
                  ? 'text-white'
                  : 'glass text-zinc-400 hover:text-pink-300 hover:border-pink-400/40'
              }`}
              style={favOnly ? { background: '#1a40ff', boxShadow: '0 0 20px rgba(236,72,153,.4)' } : {}}
            >
              <HeartFilled style={{ fontSize: 13 }} /> 只看收藏
            </button>
            <Select
              value={sort}
              onChange={onSort}
              options={SORTS}
              variant="filled"
              style={{ minWidth: 140 }}
              styles={{
                popup: { root: { background: '#14141d', border: '1px solid rgba(255,255,255,.1)' } },
              }}
            />
          </div>
        </div>
      </Reveal>

      {/* 分类胶囊 */}
      <Reveal delay={100}>
        <div className="flex flex-wrap gap-2.5 mb-12">
          {CATEGORIES.map((c) => {
            const active = category === c
            return (
              <button
                key={c}
                onClick={() => onCategory(c)}
                className={`px-5 py-2 rounded-full text-sm font-medium transition-all duration-300 ${
                  active ? 'text-white scale-[1.03]' : 'glass text-zinc-400 hover:text-white hover:border-white/25'
                }`}
                style={
                  active
                    ? {
                        background: '#1a40ff',
                        border: '1px solid transparent',
                        boxShadow: '0 0 22px rgba(168,85,247,.45)',
                      }
                    : {}
                }
              >
                {c}
              </button>
            )
          })}
        </div>
      </Reveal>

      {/* 骨架屏 / 瀑布流网格 */}
      {loading ? (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-6">
          {Array.from({ length: 8 }).map((_, i) => <SkeletonCard key={i} />)}
        </div>
      ) : products.length === 0 ? (
        <div className="glass rounded-3xl py-20">
          <Empty
            description={<span className="text-zinc-500">{favOnly ? '还没有收藏任何好物，去挑一件吧～' : '没有找到匹配的闲置，换个关键词试试'}</span>}
          />
        </div>
      ) : (
        <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-6">
          {products.map((p, i) => (
            <ProductCard key={p.id} p={p} index={i} liked={liked.has(p.id)} onLike={onLike} onOpen={onOpen} />
          ))}
        </div>
      )}
    </section>
  )
}