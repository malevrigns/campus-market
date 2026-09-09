import { useEffect, useState } from 'react'
import { Input, Badge, Dropdown, Avatar, Tooltip } from 'antd'
import {
  SearchOutlined, HeartFilled, PlusOutlined, UserOutlined,
  LogoutOutlined, DownOutlined,
} from '@ant-design/icons'
import { ripple } from '../utils/ripple'

export default function Navbar({ search, onSearch, favCount, onPublish, user, onLoginToggle, onShowFavs }) {
  const [scrolled, setScrolled] = useState(false)

  useEffect(() => {
    const fn = () => setScrolled(window.scrollY > 24)
    fn()
    window.addEventListener('scroll', fn, { passive: true })
    return () => window.removeEventListener('scroll', fn)
  }, [])

  const menuItems = user
    ? [{ key: 'logout', icon: <LogoutOutlined />, label: '退出登录' }]
    : [{ key: 'login', icon: <UserOutlined />, label: '模拟登录' }]

  return (
    <header
      className={`fixed top-0 inset-x-0 z-50 transition-all duration-300 ${
        scrolled ? 'glass-strong shadow-[0_8px_40px_rgba(0,0,0,.45)]' : 'bg-transparent'
      }`}
      style={{ borderBottom: '1px solid rgba(255,255,255,.06)' }}
    >
      <div className="max-w-7xl mx-auto px-5 sm:px-8 h-16 flex items-center gap-6">
        {/* Logo */}
        <a href="#" className="flex items-center gap-2.5 shrink-0 group">
          <div
            className="w-9 h-9 rounded-xl grid place-items-center font-display font-bold text-lg text-white transition-transform duration-300 group-hover:rotate-6 group-hover:scale-105"
            style={{ background: '#1a40ff', boxShadow: '0 0 20px rgba(168,85,247,.5)' }}
          >
            集
          </div>
          <div className="font-display font-bold text-lg tracking-tight text-white">
            校园集市<span className="text-gradient">.</span>
          </div>
        </a>

        {/* 居中搜索 */}
        <div className="hidden md:flex flex-1 justify-center">
          <Input
            allowClear
            prefix={<SearchOutlined className="text-zinc-500" />}
            placeholder="搜索闲置好物、教材、数码…"
            value={search}
            onChange={(e) => onSearch(e.target.value)}
            className="max-w-md transition-all duration-300 hover:border-violet-400/60 focus:border-violet-400"
            style={{
              height: 40,
              borderRadius: 999,
              background: 'rgba(255,255,255,.05)',
              border: '1px solid rgba(255,255,255,.1)',
              color: '#fafafa',
            }}
          />
        </div>

        {/* 右侧操作 */}
        <div className="flex items-center gap-3 ml-auto shrink-0">
          <Tooltip title="我的收藏">
            <button
              onClick={(e) => { ripple(e); onShowFavs() }}
              className="ripple relative w-10 h-10 rounded-full glass grid place-items-center text-zinc-300 hover:text-pink-400 hover:border-pink-400/50 transition-all duration-300 hover:scale-105"
            >
              <Badge count={favCount} size="small" color="#1a40ff" offset={[-2, 2]}>
                <HeartFilled style={{ fontSize: 16 }} />
              </Badge>
            </button>
          </Tooltip>

          <button
            onClick={(e) => { ripple(e); onPublish() }}
            className="ripple btn-primary !py-2 !px-4 text-sm hidden sm:inline-flex"
          >
            <PlusOutlined /> 发布闲置
          </button>

          <Dropdown
            menu={{
              items: menuItems,
              onClick: ({ key }) => onLoginToggle(key === 'logout'),
            }}
            placement="bottomRight"
            trigger={['click']}
          >
            <button className="flex items-center gap-2 glass rounded-full pl-1.5 pr-3 py-1.5 hover:border-white/25 transition-all duration-300">
              {user ? (
                <>
                  <Avatar size={28} src={user.avatar} />
                  <span className="text-sm text-zinc-200 hidden lg:inline">{user.name}</span>
                </>
              ) : (
                <>
                  <Avatar size={28} icon={<UserOutlined />} style={{ background: '#1a40ff' }} />
                  <span className="text-sm text-zinc-400 hidden lg:inline">未登录</span>
                </>
              )}
              <DownOutlined className="text-[10px] text-zinc-500" />
            </button>
          </Dropdown>
        </div>
      </div>

      {/* 移动端搜索 */}
      <div className="md:hidden px-5 pb-3">
        <Input
          allowClear
          prefix={<SearchOutlined className="text-zinc-500" />}
          placeholder="搜索闲置好物…"
          value={search}
          onChange={(e) => onSearch(e.target.value)}
          style={{ height: 38, borderRadius: 999, background: 'rgba(255,255,255,.05)', border: '1px solid rgba(255,255,255,.1)', color: '#fafafa' }}
        />
      </div>
    </header>
  )
}