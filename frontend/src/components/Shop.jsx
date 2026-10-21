import { useEffect, useRef, useState } from 'react'
import {
  ArrowRightOutlined,
  ArrowUpOutlined,
  AudioOutlined,
  BookOutlined,
  CameraOutlined,
  CoffeeOutlined,
  EnvironmentOutlined,
  HeartFilled,
  HeartOutlined,
  SearchOutlined,
  SkinOutlined,
  ThunderboltOutlined,
  TrophyOutlined,
} from '@ant-design/icons'
import { api, CATEGORIES, CAMPUSES, money } from '../lib/api'
import { Avatar, Button, EmptyState, ErrorState, ProductImage } from './ui'
const categoryIcons = [
  <AudioOutlined />,
  <BookOutlined />,
  <CoffeeOutlined />,
  <SkinOutlined />,
  <TrophyOutlined />,
  <ThunderboltOutlined />,
]
export default function Shop({
  mode,
  search,
  onSearch,
  liked,
  onLike,
  onOpen,
  onPublish,
  revision,
  onAgent,
  user,
}) {
  const [category, setCategory] = useState(''),
    [campus, setCampus] = useState(''),
    [sort, setSort] = useState('new')
  const [data, setData] = useState({ items: [], total: 0 }),
    [loading, setLoading] = useState(true),
    [error, setError] = useState(null),
    [reload, setReload] = useState(0),
    [moreBusy, setMoreBusy] = useState(false)
  const [query, setQuery] = useState(search)
  const moreAbort = useRef(null)
  useEffect(() => {
    const timer = setTimeout(() => setQuery(search.trim()), 300)
    return () => clearTimeout(timer)
  }, [search])
  const params = () =>
    new URLSearchParams({
      q: query,
      category,
      campus,
      sort,
      ...(mode === 'favorites' ? { saved: '1' } : {}),
      ...(mode === 'selling' ? { mine: '1' } : {}),
    })
  useEffect(() => {
    const ac = new AbortController()
    moreAbort.current?.abort()
    setMoreBusy(false)
    setLoading(true)
    setError(null)
    api(`/products?${params()}`, { signal: ac.signal })
      .then(setData)
      .catch((e) => {
        if (!ac.signal.aborted) setError(e)
      })
      .finally(() => {
        if (!ac.signal.aborted) setLoading(false)
      })
    return () => {
      ac.abort()
      moreAbort.current?.abort()
    }
  }, [query, category, campus, sort, mode, revision, reload, user?.id])
  const loadMore = async () => {
    if (moreBusy) return
    const ac = new AbortController()
    moreAbort.current = ac
    setMoreBusy(true)
    try {
      const p = params()
      // 优先使用游标分页（后端 keyset 分页，翻页不重复不漏），旧接口回退到 offset。
      if (data.next_cursor) p.set('cursor', data.next_cursor)
      else p.set('offset', String(data.items.length))
      const d = await api(`/products?${p}`, { signal: ac.signal })
      if (ac.signal.aborted) return
      setData((old) => ({
        ...d,
        items: [...old.items, ...d.items.filter((p) => !old.items.some((x) => x.id === p.id))],
      }))
    } catch (e) {
      if (!ac.signal.aborted) setError(e)
    } finally {
      if (!ac.signal.aborted) setMoreBusy(false)
    }
  }
  const scrollMarket = () =>
    document.getElementById('market')?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  return (
    <>
      {mode === 'home' && (
        <section className="hero">
          <div className="hero-copy">
            <div className="eyebrow">
              <span className="small-spark">✳</span> A SECOND LIFE, A NEW STORY
            </div>
            <h1>
              好物，
              <br />
              不止
              <span className="hero-word">
                一程
                <svg viewBox="0 0 300 20" aria-hidden="true">
                  <path d="M3 12 Q145 -3 296 9M18 18 Q162 5 286 16" />
                </svg>
              </span>
              <span className="lime">。</span>
            </h1>
            <p>
              你的闲置，是另一位同学的心头好。
              <br />
              在熟悉的校园，让每一份喜欢继续。
            </p>
            <div className="hero-actions">
              <Button onClick={scrollMarket}>
                逛逛好物 <ArrowUpOutlined rotate={45} />
              </Button>
              <button className="text-button" onClick={onPublish}>
                让闲置出发 <span className="plus-circle">+</span>
              </button>
            </div>
            <div className="hero-footnote">
              <span className="stacked-circles">
                <span>同</span>
                <span>学</span>
                <span>↗</span>
              </span>
              <span>
                在这里，遇见物尽其用的生活。<small>BUY LESS. CHOOSE WELL. PASS IT ON.</small>
              </span>
            </div>
          </div>
          <div className="hero-visual">
            <div className="hero-photo">
              <ProductImage
                src="https://images.unsplash.com/photo-1452780212940-6f5c0d14d848?auto=format&fit=crop&w=1200&q=85"
                alt="复古相机，开启好物的下一段旅程"
                fetchPriority="high"
              />
              <span className="photo-caption">
                THE NEXT CHAPTER <span>CURATED / 01</span>
              </span>
            </div>
            <div className="round-stamp">
              GOOD FINDS
              <br />
              <span>↗</span>
              <br />
              NEW STORIES
            </div>
            <div className="floating-note">
              <span className="note-icon">
                <CameraOutlined />
              </span>
              <div>
                <small>换一种方式拥有</small>
                <strong>旧物，也有新风景。</strong>
              </div>
              <ArrowUpOutlined rotate={45} />
            </div>
            <span className="visual-coordinate">CURATED FOR YOUR CAMPUS — EST. 2026</span>
          </div>
        </section>
      )}
      {mode === 'home' && (
        <div className="value-strip">
          <span>
            <span>01</span>同校好物，触手可及
          </span>
          <span>
            <span>02</span>先聊清楚，再心动
          </span>
          <span>
            <span>03</span>当面验货，放心交接
          </span>
          <span className="value-leaf">✳ 让喜欢，循环发生</span>
        </div>
      )}
      <section id="market" className="market-section">
        <div className="market-heading">
          <div>
            <span className="eyebrow">
              {mode === 'favorites'
                ? 'YOUR LITTLE COLLECTION'
                : mode === 'selling'
                  ? 'YOUR LISTINGS'
                  : 'THE CAMPUS EDIT'}
            </span>
            <h2>
              {mode === 'favorites'
                ? '我的心动清单'
                : mode === 'selling'
                  ? '我发布的好物'
                  : '发现下一件心头好'}
              <span className="lime">↗</span>
            </h2>
          </div>
          <span className="result-count">
            {loading
              ? '正在寻找好物…'
              : `${data.total} 件${mode === 'favorites' ? '收藏' : '好物'}，等你发现`}
          </span>
        </div>
        <div className="market-tools">
          <label className="search-field">
            <SearchOutlined />
            <input
              aria-label="搜索好物"
              placeholder="搜一搜，发现你需要的好物"
              value={search}
              onChange={(e) => onSearch(e.target.value)}
              maxLength={100}
            />
            {search && (
              <button aria-label="清空搜索" onClick={() => onSearch('')}>
                ×
              </button>
            )}
          </label>
          <div className="filter-selects">
            <label>
              <EnvironmentOutlined />
              <select aria-label="校区筛选" value={campus} onChange={(e) => setCampus(e.target.value)}>
                <option value="">全部校区</option>
                {CAMPUSES.map((c) => (
                  <option key={c}>{c}</option>
                ))}
              </select>
            </label>
            <select aria-label="商品排序" value={sort} onChange={(e) => setSort(e.target.value)}>
              <option value="new">最新发布</option>
              <option value="price-asc">价格从低到高</option>
              <option value="price-desc">价格从高到低</option>
            </select>
          </div>
        </div>
        <div className="category-tabs" aria-label="商品分类">
          <button className={!category ? 'active' : ''} onClick={() => setCategory('')}>
            <span>✳</span>全部好物
          </button>
          {CATEGORIES.map((c, i) => (
            <button key={c} className={category === c ? 'active' : ''} onClick={() => setCategory(c)}>
              {categoryIcons[i]}
              {c}
            </button>
          ))}
        </div>
        {error && <ErrorState error={error} retry={() => setReload((n) => n + 1)} />}
        {loading ? (
          <div className="product-grid" aria-label="正在加载商品">
            {Array.from({ length: 8 }, (_, i) => (
              <div className="product-skeleton" key={i}>
                <div className="skeleton skeleton-photo" />
                <div className="skeleton skeleton-line" />
                <div className="skeleton skeleton-line short" />
              </div>
            ))}
          </div>
        ) : !error && data.items.length === 0 ? (
          <EmptyState
            title={mode === 'favorites' ? '还没有收藏好物' : '这里还有新的可能'}
            description="换个关键词试试，或者发布你的第一件闲置。"
            action="发布闲置"
            onAction={onPublish}
          />
        ) : (
          <div className="product-grid">
            {data.items
              .filter((p) => mode !== 'favorites' || liked.has(p.id))
              .map((p, i) => (
                <article
                  className="product-card"
                  key={p.id}
                  style={{ '--card-delay': `${Math.min(i, 7) * 35}ms` }}
                >
                  <div className="product-photo">
                    <button className="product-open" aria-label={`查看 ${p.title}`} onClick={() => onOpen(p)}>
                      <ProductImage src={p.image} alt={p.title} loading="lazy" />
                    </button>
                    <span className="condition-tag">{p.condition}</span>
                    <button
                      className={`save-button ${liked.has(p.id) ? 'saved' : ''}`}
                      aria-label={`${liked.has(p.id) ? '取消收藏' : '收藏'} ${p.title}`}
                      aria-pressed={liked.has(p.id)}
                      onClick={() => onLike(p.id)}
                    >
                      {liked.has(p.id) ? <HeartFilled /> : <HeartOutlined />}
                    </button>
                    {p.status !== 'available' && (
                      <span className="reserved-tag">
                        {p.status === 'reserved'
                          ? '已被预订'
                          : p.status === 'withdrawn'
                            ? '已下架'
                            : '已售出'}
                      </span>
                    )}
                    <span className="image-arrow">
                      <ArrowUpOutlined rotate={45} />
                    </span>
                  </div>
                  <div className="product-meta">
                    <span>{p.category}</span>
                    {p.demo && <small>展示商品</small>}
                  </div>
                  <button className="product-title" onClick={() => onOpen(p)}>
                    <h3>{p.title}</h3>
                  </button>
                  <div className="product-bottom">
                    <span className="price">{money(p.price_ct)}</span>
                    <span className="product-campus">
                      <EnvironmentOutlined />
                      {p.campus}
                    </span>
                  </div>
                  <div className="product-seller">
                    <Avatar name={p.seller_name} size="tiny" />
                    <span>{p.seller_name}</span>
                    <span className="seller-time">{p.demo ? '灵感精选' : '同学发布'}</span>
                  </div>
                </article>
              ))}
          </div>
        )}
        {!loading && data.has_more && (
          <div className="load-more">
            <Button variant="secondary" busy={moreBusy} onClick={loadMore}>
              再发现一些 <ArrowRightOutlined />
            </Button>
          </div>
        )}
      </section>
      {mode === 'home' && (
        <section className="agent-banner">
          <div className="agent-orb">✳</div>
          <div>
            <span className="eyebrow">A LITTLE HELP FROM AI</span>
            <h2>有点纠结？和小集聊聊。</h2>
            <p>找好物、比价格、准备验货清单，让每次选择更轻松。</p>
          </div>
          <Button variant="secondary" onClick={onAgent}>
            认识你的 AI 助手 <ArrowUpOutlined rotate={45} />
          </Button>
        </section>
      )}
    </>
  )
}
