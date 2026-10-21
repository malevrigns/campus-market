import { lazy, Suspense, useCallback, useEffect, useRef, useState } from 'react'
import { App as AntApp, ConfigProvider, theme } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import {
  ArrowUpOutlined,
  AppstoreOutlined,
  FireOutlined,
  HeartOutlined,
  MessageOutlined,
  PlusOutlined,
  ThunderboltOutlined,
  UserOutlined,
} from '@ant-design/icons'
import { api } from './lib/api'
import { Avatar, Button, Loading } from './components/ui'
import Shop from './components/Shop'
import ProductDetail from './components/ProductDetail'
import { AuthModal, PublishModal, Profile } from './components/Account'
import Checkout from './components/Checkout'
import FloatingAssistant from './components/FloatingAssistant'
import { ThemeButton, useReducedMotion, useTheme } from './components/Theme'
import './theme.css'
import './widget.css'
const Messages = lazy(() => import('./components/Messages'))
const Orders = lazy(() => import('./components/Orders'))
const AgentAdmin = lazy(() => import('./components/AgentAdmin'))
const FlashSales = lazy(() => import('./components/FlashSales'))
const readPage = () =>
  ['messages', 'agent-admin', 'orders', 'profile', 'favorites', 'selling', 'flash'].includes(
    location.hash.slice(1),
  )
    ? location.hash.slice(1)
    : 'home'
const NAV = [
  { id: 'home', label: '发现', icon: <AppstoreOutlined /> },
  { id: 'messages', label: '消息', icon: <MessageOutlined /> },
  { id: 'publish', label: '发布', icon: <PlusOutlined /> },
  { id: 'agent', label: 'AI 助手', icon: <ThunderboltOutlined /> },
  { id: 'profile', label: '我的', icon: <UserOutlined /> },
]
export default function App() {
  const [mode, toggleTheme] = useTheme()
  const reducedMotion = useReducedMotion()
  const light = mode === 'light'
  return (
    <ConfigProvider
      locale={zhCN}
      theme={{
        algorithm: light ? theme.defaultAlgorithm : theme.darkAlgorithm,
        token: {
          motion: !reducedMotion,
          colorPrimary: light ? '#526d27' : '#d6ef86',
          colorBgBase: light ? '#f8f9f3' : '#11130f',
          colorBgContainer: light ? '#ffffff' : '#1b1e18',
          colorBgElevated: light ? '#ffffff' : '#1b1e18',
          colorText: light ? '#252e1d' : '#f4f5ed',
          colorTextSecondary: light ? '#606b54' : '#a1a797',
          colorBorder: light ? '#d3dbc7' : '#383d32',
          borderRadius: 16,
          fontFamily: 'Inter, PingFang SC, Microsoft YaHei, sans-serif',
        },
      }}
    >
      <AntApp>
        <Shell themeMode={mode} onToggleTheme={toggleTheme} />
      </AntApp>
    </ConfigProvider>
  )
}
function Shell({ themeMode, onToggleTheme }) {
  const { message } = AntApp.useApp()
  const [page, setPage] = useState(readPage),
    [user, setUser] = useState(null),
    [authReady, setAuthReady] = useState(false)
  const [config, setConfig] = useState(null),
    [authOpen, setAuthOpen] = useState(false),
    [publishOpen, setPublishOpen] = useState(false)
  const [active, setActive] = useState(null),
    [checkout, setCheckout] = useState(null),
    [liked, setLiked] = useState(new Set()),
    [revision, setRevision] = useState(0)
  const [search, setSearch] = useState(''),
    [conversation, setConversation] = useState(null),
    [unread, setUnread] = useState(0)
  const [assistantOpen, setAssistantOpen] = useState(false),
    [assistantIntent, setAssistantIntent] = useState(() => location.hash === '#agent')
  const afterAuth = useRef(null),
    likePending = useRef(new Set()),
    authRevision = useRef(0),
    currentPage = useRef(page)
  currentPage.current = page
  const navigate = useCallback((to) => {
    if (to === 'agent') {
      setAssistantIntent(true)
      return
    }
    location.hash = to === 'home' ? '' : to
    setPage(to)
    window.scrollTo({ top: 0, behavior: 'instant' })
  }, [])
  useEffect(() => {
    const fn = () => {
      if (location.hash === '#agent') {
        const previous = currentPage.current === 'home' ? '' : `#${currentPage.current}`
        history.replaceState(null, '', `${location.pathname}${location.search}${previous}`)
        setAssistantIntent(true)
      } else {
        setPage(readPage())
        if (readPage() === 'agent-admin') setAssistantOpen(false)
      }
    }
    if (location.hash === '#agent') fn()
    window.addEventListener('hashchange', fn)
    return () => window.removeEventListener('hashchange', fn)
  }, [])
  useEffect(() => {
    const ac = new AbortController()
    const revisionAtStart = authRevision.current
    api('/config', { signal: ac.signal })
      .then(setConfig)
      .catch(() => {})
    api('/auth/me', { signal: ac.signal, suppressAuthEvent: true })
      .then((u) => {
        if (!ac.signal.aborted && revisionAtStart === authRevision.current) setUser(u)
      })
      .catch(() => {})
      .finally(() => {
        if (!ac.signal.aborted) setAuthReady(true)
      })
    const expired = () => {
      authRevision.current += 1
      setUser(null)
      setAssistantOpen(false)
      setLiked(new Set())
      setUnread(0)
    }
    window.addEventListener('session-expired', expired)
    return () => {
      ac.abort()
      window.removeEventListener('session-expired', expired)
    }
  }, [])
  useEffect(() => {
    if (!assistantIntent || !authReady) return
    setAssistantIntent(false)
    if (user) setAssistantOpen(true)
    else {
      afterAuth.current = () => setAssistantOpen(true)
      setAuthOpen(true)
    }
  }, [assistantIntent, authReady, user])
  useEffect(() => {
    if (!user) return
    const ac = new AbortController()
    api('/favorites', { signal: ac.signal })
      .then((d) => setLiked(new Set(d.ids)))
      .catch(() => {})
    const sync = () =>
      api('/conversations', { signal: ac.signal })
        .then((d) => setUnread(d.items.reduce((n, c) => n + c.unread, 0)))
        .catch(() => {})
    sync()
    const timer = setInterval(sync, 15000)
    return () => {
      ac.abort()
      clearInterval(timer)
    }
  }, [user, page])
  const requireUser = (action) => {
    if (user) action()
    else {
      afterAuth.current = action
      setAuthOpen(true)
    }
  }
  const openProduct = async (p) => {
    try {
      setActive(await api(`/products/${p.id}`))
    } catch (e) {
      message.error(e.message)
    }
  }
  const toggleLike = async (id) => {
    if (!user) {
      setAuthOpen(true)
      return
    }
    if (likePending.current.has(id)) return
    likePending.current.add(id)
    try {
      await api(`/favorites/${id}`, { method: liked.has(id) ? 'DELETE' : 'PUT' })
      setLiked((prev) => {
        const next = new Set(prev)
        next.has(id) ? next.delete(id) : next.add(id)
        return next
      })
    } catch (e) {
      message.error(e.message)
    } finally {
      likePending.current.delete(id)
    }
  }
  const startChat = (p) =>
    requireUser(async () => {
      try {
        const c = await api('/conversations', { method: 'POST', body: { product_id: p.id } })
        setActive(null)
        setConversation(c.id)
        navigate('messages')
      } catch (e) {
        message.error(e.message)
      }
    })
  const publish = () => requireUser(() => setPublishOpen(true))
  const isWorkspace = page === 'messages'
  return (
    <div className={`app-shell ${isWorkspace ? 'workspace-shell' : ''}`}>
      <a className="skip-link" href="#main">
        跳转到主要内容
      </a>
      <header className="site-header">
        <a className="brand" href="#" onClick={() => navigate('home')}>
          <span className="brand-symbol">
            c<span>↗</span>
          </span>
          <span>
            校园集市<small>CAMPUS MARKET</small>
          </span>
        </a>
        <nav className="desktop-nav" aria-label="主导航">
          {[
            { id: 'home', name: '发现好物' },
            { id: 'flash', name: '限时抢购' },
            { id: 'messages', name: '消息' },
            { id: 'agent', name: 'AI 助手' },
          ].map((n) => (
            <button key={n.id} className={page === n.id ? 'active' : ''} onClick={() => navigate(n.id)}>
              {n.name}
              {n.id === 'agent' && <span className="tiny-label">BETA</span>}
              {n.id === 'messages' && unread > 0 && <span className="nav-count">{Math.min(unread, 99)}</span>}
            </button>
          ))}
        </nav>
        <div className="header-actions">
          <ThemeButton mode={themeMode} onToggle={onToggleTheme} />
          <button
            className="icon-button header-save"
            aria-label="我的收藏"
            onClick={() => navigate('favorites')}
          >
            <HeartOutlined />
          </button>
          <Button variant="secondary" className="header-publish" onClick={publish}>
            <PlusOutlined />
            发布闲置
          </Button>
          <button className="account-button" onClick={() => (user ? navigate('profile') : setAuthOpen(true))}>
            {user ? (
              <Avatar name={user.name} />
            ) : (
              <>
                <UserOutlined />
                <span>登录 / 注册</span>
              </>
            )}
          </button>
        </div>
      </header>
      {!isWorkspace && (
        <div className="announcement">
          <span>
            <span className="status-dot" />
            让闲置遇见下一位心动的同学
          </span>
          <button className="announcement-right" onClick={() => navigate('flash')}>
            <FireOutlined />
            限时抢购进行中 <span>↗</span>
          </button>
        </div>
      )}
      <main id="main" className={isWorkspace ? 'workspace-main' : 'main-container'}>
        {!['home', 'flash'].includes(page) && !authReady ? (
          <Loading />
        ) : !['home', 'flash'].includes(page) && !user ? (
          <div className="auth-gate">
            <span className="eyebrow">YOUR CAMPUS, CONNECTED</span>
            <h1>
              校园生活，
              <br />
              从这里连接。
            </h1>
            <p>登录后查看消息、收藏好物，和 AI 一起发现新的可能。</p>
            <Button onClick={() => setAuthOpen(true)}>
              登录 / 注册 <ArrowUpOutlined rotate={45} />
            </Button>
          </div>
        ) : (
          <Suspense fallback={<Loading />}>
            {['home', 'favorites', 'selling'].includes(page) && (
              <Shop
                mode={page}
                search={search}
                onSearch={setSearch}
                liked={liked}
                onLike={toggleLike}
                onOpen={openProduct}
                onPublish={publish}
                revision={revision}
                onAgent={() => navigate('agent')}
                user={user}
              />
            )}
            {page === 'flash' && (
              <FlashSales
                user={user}
                onRequireUser={requireUser}
                onOrders={() => navigate('orders')}
                onOpenProduct={openProduct}
              />
            )}
            {page === 'messages' && (
              <Messages user={user} selected={conversation} onSelect={setConversation} onUnread={setUnread} />
            )}
            {page === 'agent-admin' &&
              (user.is_admin ? (
                <AgentAdmin />
              ) : (
                <div className="auth-gate">
                  <h1>这里是管理员工作室。</h1>
                  <p>你的账号没有配置管理权限。</p>
                  <Button onClick={() => navigate('agent')}>返回 AI 助手</Button>
                </div>
              ))}
            {page === 'orders' && (
              <Orders
                user={user}
                config={config}
                onPay={(order) => setCheckout({ order })}
                revision={revision}
              />
            )}
            {page === 'profile' && (
              <Profile
                user={user}
                onUserUpdate={setUser}
                onNavigate={navigate}
                onPublish={publish}
                onLogout={async () => {
                  try {
                    await api('/auth/logout', { method: 'POST', body: {} })
                    authRevision.current += 1
                    setUser(null)
                    setAssistantOpen(false)
                    setLiked(new Set())
                    setUnread(0)
                    navigate('home')
                    message.success('已退出登录')
                  } catch (e) {
                    message.error(e.message)
                  }
                }}
              />
            )}
          </Suspense>
        )}
      </main>
      {!isWorkspace && (
        <footer className="site-footer">
          <div>
            <a className="brand footer-brand" href="#" onClick={() => navigate('home')}>
              校园集市<span className="lime">↗</span>
            </a>
            <p>物尽其用，心意继续。</p>
          </div>
          <div className="footer-note">
            <span>LESS WASTE. MORE POSSIBILITIES.</span>
            <small>© {new Date().getFullYear()} Campus Market</small>
          </div>
          <button
            className="icon-button"
            aria-label="回到顶部"
            onClick={() => window.scrollTo({ top: 0, behavior: 'smooth' })}
          >
            <ArrowUpOutlined />
          </button>
        </footer>
      )}
      <nav className="mobile-nav" aria-label="移动端导航">
        {NAV.map((n) => (
          <button
            key={n.id}
            onClick={() => (n.id === 'publish' ? publish() : navigate(n.id))}
            className={`${page === n.id ? 'active' : ''} ${n.id === 'publish' ? 'publish-tab' : ''}`}
          >
            <span>
              {n.icon}
              {n.id === 'messages' && unread > 0 && <i>{Math.min(unread, 99)}</i>}
            </span>
            <small>{n.label}</small>
          </button>
        ))}
      </nav>
      <FloatingAssistant
        user={user}
        avoidComposer={page === 'messages'}
        open={assistantOpen && !!user}
        onOpen={() => setAssistantIntent(true)}
        onClose={() => setAssistantOpen(false)}
      />
      <AuthModal
        open={authOpen}
        onClose={() => {
          setAuthOpen(false)
          afterAuth.current = null
        }}
        onSuccess={(u) => {
          authRevision.current += 1
          setUser(u)
          setAuthReady(true)
          setAuthOpen(false)
          const action = afterAuth.current
          afterAuth.current = null
          action?.()
          message.success(`欢迎，${u.name}`)
        }}
      />
      <PublishModal
        open={publishOpen}
        onClose={() => setPublishOpen(false)}
        onSuccess={() => {
          setPublishOpen(false)
          setRevision((n) => n + 1)
          navigate('home')
          message.success('发布成功，你的好物已上架')
        }}
      />
      <ProductDetail
        product={active}
        user={user}
        onClose={() => setActive(null)}
        liked={liked.has(active?.id)}
        onLike={toggleLike}
        onChat={startChat}
        onBuy={(p) =>
          requireUser(() => {
            setActive(null)
            setCheckout({ product: p })
          })
        }
        onWithdraw={async (p) => {
          try {
            await api(`/products/${p.id}`, { method: 'DELETE' })
            setActive(null)
            setRevision((n) => n + 1)
            message.success('商品已下架')
          } catch (e) {
            message.error(e.message)
          }
        }}
      />
      {checkout && (
        <Checkout
          key={checkout.order?.id || checkout.product?.id}
          {...checkout}
          config={config}
          onClose={() => {
            setCheckout(null)
            setRevision((n) => n + 1)
          }}
          onOrders={() => {
            setCheckout(null)
            setRevision((n) => n + 1)
            navigate('orders')
          }}
        />
      )}
    </div>
  )
}
