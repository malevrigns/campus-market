import { useEffect, useMemo, useState } from 'react'
import { ConfigProvider, theme, App as AntApp } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import Navbar from './components/Navbar'
import Hero from './components/Hero'
import Market from './components/Market'
import DetailDrawer from './components/DetailDrawer'
import PublishModal from './components/PublishModal'
import ChatPanel from './components/ChatPanel'
import Checkout from './components/Checkout'
import Aurora from './components/Aurora'
import { INITIAL_PRODUCTS } from './data/products'

export default function App() {
  return (
    <ConfigProvider
      locale={zhCN}
      theme={{
        algorithm: theme.darkAlgorithm,
        token: {
          colorBgBase: '#0a0a12',
          colorBgContainer: '#0e0e16',
          colorBgElevated: '#12121c',
          colorPrimary: '#1a40ff',
          colorInfo: '#1a40ff',
          colorSuccess: '#34d399',
          colorWarning: '#fbbf24',
          colorError: '#f43f5e',
          colorText: '#f5f5f7',
          colorTextSecondary: '#86868b',
          colorBorder: 'rgba(255,255,255,.10)',
          colorBorderSecondary: 'rgba(255,255,255,.06)',
          borderRadius: 12,
          fontFamily: "-apple-system, BlinkMacSystemFont, 'SF Pro Display', 'PingFang SC', system-ui, sans-serif",
        },
        components: {
          Drawer: { colorBgElevated: '#0d0d14', colorBgMask: 'rgba(5,5,10,.6)' },
          Modal: { colorBgElevated: '#0d0d14', colorBgMask: 'rgba(5,5,10,.65)' },
          Message: { colorBgElevated: 'rgba(20,20,29,.95)' },
        },
      }}
    >
      <AntApp>
        <Shell />
      </AntApp>
    </ConfigProvider>
  )
}

function Shell() {
  const { message } = AntApp.useApp()

  const [products, setProducts] = useState(INITIAL_PRODUCTS)
  const [loading, setLoading] = useState(true)
  const [search, setSearch] = useState('')
  const [category, setCategory] = useState('全部')
  const [sort, setSort] = useState('new')
  const [favOnly, setFavOnly] = useState(false)
  const [liked, setLiked] = useState(() => new Set())
  const [user, setUser] = useState(null)
  const [active, setActive] = useState(null)
  const [chatSeller, setChatSeller] = useState(null)
  const [publishOpen, setPublishOpen] = useState(false)
  const [purchase, setPurchase] = useState(null)

  // 模拟首屏数据加载 → 骨架屏 shimmer
  useEffect(() => {
    const t = setTimeout(() => setLoading(false), 900)
    return () => clearTimeout(t)
  }, [])

  const toggleLike = (id) => {
    setLiked((prev) => {
      const next = new Set(prev)
      next.has(id) ? next.delete(id) : next.add(id)
      return next
    })
  }

  const filtered = useMemo(() => {
    const kw = search.trim().toLowerCase()
    let list = products.filter(
      (p) =>
        (category === '全部' || p.category === category) &&
        (!favOnly || liked.has(p.id)) &&
        (!kw || (p.title + p.desc + p.tags.join('')).toLowerCase().includes(kw)),
    )
    if (sort === 'price-asc') list = [...list].sort((a, b) => a.price - b.price)
    else if (sort === 'price-desc') list = [...list].sort((a, b) => b.price - a.price)
    else if (sort === 'hot') list = [...list].sort((a, b) => b.views - a.views)
    else list = [...list].sort((a, b) => b.id - a.id)
    return list
  }, [products, category, favOnly, liked, search, sort])

  const handleLoginToggle = () => {
    if (user) {
      setUser(null)
      message.info('已退出登录')
    } else {
      setUser({ name: '林小满', avatar: 'https://i.pravatar.cc/100?img=32' })
      message.success('欢迎回来，林小满 ✨')
    }
  }

  const handlePublish = () => {
    if (!user) {
      message.warning('请先登录后再发布闲置～')
      return
    }
    setPublishOpen(true)
  }

  const submitPublish = (v) => {
    const p = {
      id: Date.now(),
      title: v.title,
      price: v.price,
      oldPrice: null,
      category: v.category,
      campus: v.campus,
      condition: v.condition,
      desc: v.desc || '这位同学很懒，什么都没有留下。',
      img: v.image?.[0]?.thumbUrl || `https://picsum.photos/seed/${Date.now() % 997}/640/480`,
      views: 1,
      likes: 0,
      time: '刚刚',
      tags: ['新上架'],
      seller: { name: user.name, avatar: user.avatar, online: true, deals: 0, rating: 5.0 },
    }
    setProducts((prev) => [p, ...prev])
    setPublishOpen(false)
    message.success('发布成功！你的闲置已上架 🎉')
  }

  const openChat = (seller) => {
    if (!user) {
      message.warning('登录后即可和卖家畅聊～')
      return
    }
    setActive(null)
    setChatSeller(seller)
  }

  const showFavs = () => {
    setFavOnly(true)
    document.getElementById('market')?.scrollIntoView({ behavior: 'smooth' })
  }

  return (
    <div className="min-h-screen">
      <Aurora />
      <Navbar
        search={search}
        onSearch={setSearch}
        favCount={liked.size}
        onPublish={handlePublish}
        user={user}
        onLoginToggle={handleLoginToggle}
        onShowFavs={showFavs}
      />
      <Hero total={products.length} />
      <main id="market" className="relative z-10 w-full max-w-[1680px] mx-auto px-4 sm:px-8 lg:px-14 pb-24">
        <Market
          products={filtered}
          loading={loading}
          liked={liked}
          onLike={toggleLike}
          onOpen={setActive}
          category={category}
          onCategory={(c) => { setCategory(c); setFavOnly(false) }}
          sort={sort}
          onSort={setSort}
          favOnly={favOnly}
          onToggleFavOnly={() => setFavOnly((v) => !v)}
        />
      </main>

      <footer className="relative z-10 border-t border-white/5 py-12 text-center">
        <div className="font-display font-bold text-lg text-white mb-2">
          校园集市<span className="text-gradient">.</span>
        </div>
        <p className="text-sm text-zinc-500">让闲置流动起来 · React 19 + Vite + Ant Design 5 + Tailwind CSS</p>
        <p className="text-xs text-zinc-600 mt-2">桌面端可用 Tauri / Electron 将同一套代码打包为跨平台客户端</p>
      </footer>

      <DetailDrawer
        p={active}
        onClose={() => setActive(null)}
        liked={active ? liked.has(active.id) : false}
        onLike={toggleLike}
        onChat={openChat}
        onBuy={(prod) => setPurchase(prod)}
      />
      <PublishModal open={publishOpen} onClose={() => setPublishOpen(false)} onSubmit={submitPublish} />
      <ChatPanel seller={chatSeller} onClose={() => setChatSeller(null)} />
      <Checkout
        p={purchase}
        onClose={() => setPurchase(null)}
        onPaid={(o) => message.success(`支付成功，订单 ${o.orderId}（${o.total}元 · ${o.method}）已生成，请按时面交验货`)}
      />
    </div>
  )
}