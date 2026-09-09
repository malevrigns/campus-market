import { useEffect, useState } from 'react'
import { Modal } from 'antd'
import {
  ArrowRightOutlined,
  HeartOutlined,
  LogoutOutlined,
  MessageOutlined,
  PlusOutlined,
  ShoppingOutlined,
  ThunderboltOutlined,
  UploadOutlined,
} from '@ant-design/icons'
import { api, CAMPUSES, CATEGORIES, CONDITIONS, toCents } from '../lib/api'
import { Avatar, Button, ErrorState, PageHeading, ProductImage } from './ui'

export function AuthModal({ open, onClose, onSuccess }) {
  const [register, setRegister] = useState(false),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(null)
  useEffect(() => {
    if (open) setError(null)
  }, [open, register])
  const submit = async (e) => {
    e.preventDefault()
    if (busy) return
    const values = Object.fromEntries(new FormData(e.currentTarget))
    setBusy(true)
    setError(null)
    try {
      onSuccess(await api(`/auth/${register ? 'register' : 'login'}`, { method: 'POST', body: values }))
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      open={open}
      onCancel={busy ? undefined : onClose}
      footer={null}
      width={440}
      destroyOnClose
      title={null}
      className="auth-modal"
    >
      <div className="auth-art">
        ✳<span>HELLO, CAMPUS.</span>
      </div>
      <h2>{register ? '下一程，一起出发。' : '好久不见，同学。'}</h2>
      <p className="modal-subtitle">
        {register ? '加入校园集市，让每一件好物继续发光。' : '登录校园集市，继续你的发现之旅。'}
      </p>
      <form className="form-stack" onSubmit={submit}>
        {register && (
          <label>
            昵称
            <input
              name="name"
              autoComplete="nickname"
              minLength={2}
              maxLength={24}
              required
              placeholder="同学们怎么称呼你"
            />
          </label>
        )}
        <label>
          邮箱
          <input
            name="email"
            type="email"
            autoComplete="email"
            maxLength={254}
            required
            placeholder="you@example.com"
          />
        </label>
        <label>
          密码
          <input
            name="password"
            type="password"
            autoComplete={register ? 'new-password' : 'current-password'}
            minLength={register ? 10 : undefined}
            maxLength={72}
            required
            placeholder={register ? '至少 10 位，建议包含字母和数字' : '输入你的密码'}
          />
        </label>
        {error && <ErrorState error={error} />}
        <Button type="submit" busy={busy}>
          {register ? '创建账号' : '登录'}
          <ArrowRightOutlined />
        </Button>
      </form>
      <p className="auth-switch">
        {register ? '已经有账号？' : '第一次来到这里？'}
        <button onClick={() => setRegister((v) => !v)} disabled={busy}>
          {register ? '去登录' : '注册账号'}
        </button>
      </p>
    </Modal>
  )
}
export function PublishModal({ open, onClose, onSuccess }) {
  const [busy, setBusy] = useState(false),
    [uploading, setUploading] = useState(false),
    [image, setImage] = useState(''),
    [error, setError] = useState(null)
  useEffect(() => {
    if (open) {
      setImage('')
      setError(null)
    }
  }, [open])
  const upload = async (e) => {
    const file = e.target.files?.[0]
    if (!file) return
    if (file.size > 5 * 1024 * 1024) {
      setError(new Error('图片不能超过 5 MB'))
      return
    }
    setUploading(true)
    setError(null)
    const form = new FormData()
    form.append('image', file)
    try {
      const d = await api('/uploads', { method: 'POST', body: form })
      setImage(d.url)
    } catch (e) {
      setError(e)
    } finally {
      setUploading(false)
    }
  }
  const submit = async (e) => {
    e.preventDefault()
    if (busy || uploading) return
    const data = Object.fromEntries(new FormData(e.currentTarget))
    setError(null)
    if (!image) {
      setError(new Error('请先上传一张实物照片'))
      return
    }
    setBusy(true)
    try {
      await api('/products', {
        method: 'POST',
        body: {
          title: data.title,
          description: data.description,
          category: data.category,
          campus: data.campus,
          condition: data.condition,
          price_ct: toCents(data.price),
          image,
        },
      })
      onSuccess()
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      open={open}
      onCancel={busy || uploading ? undefined : onClose}
      footer={null}
      width={560}
      title="给好物一个新的开始"
      destroyOnClose
    >
      <p className="modal-subtitle">真实照片、清楚描述，更容易遇到心动的同学。</p>
      <form className="form-stack" onSubmit={submit}>
        <label className="upload-field">
          {image ? (
            <ProductImage src={image} alt="待发布商品预览" />
          ) : (
            <>
              <UploadOutlined />
              <strong>{uploading ? '正在上传…' : '上传实物照片'}</strong>
              <small>JPG / PNG，5 MB 以内</small>
            </>
          )}
          <input
            type="file"
            accept="image/jpeg,image/png"
            onChange={upload}
            disabled={uploading || busy}
            aria-label="上传商品图片"
          />
        </label>
        <label>
          好物名称
          <input
            name="title"
            minLength={2}
            maxLength={80}
            placeholder="例如：自用 Sony 降噪耳机，奶油白"
            required
          />
        </label>
        <div className="form-grid">
          <label>
            出手价（元）
            <input
              name="price"
              inputMode="decimal"
              pattern="[0-9]+(\.[0-9]{1,2})?"
              placeholder="0.00"
              required
            />
          </label>
          <label>
            分类
            <select name="category" required>
              {CATEGORIES.map((v) => (
                <option key={v}>{v}</option>
              ))}
            </select>
          </label>
          <label>
            面交校区
            <select name="campus" required>
              {CAMPUSES.map((v) => (
                <option key={v}>{v}</option>
              ))}
            </select>
          </label>
          <label>
            成色
            <select name="condition" required>
              {CONDITIONS.map((v) => (
                <option key={v}>{v}</option>
              ))}
            </select>
          </label>
        </div>
        <label>
          说说它的故事
          <textarea
            name="description"
            rows={4}
            maxLength={3000}
            placeholder="使用多久、有无瑕疵、包含哪些配件？把细节说清楚吧。"
          />
        </label>
        {error && <ErrorState error={error} />}
        <Button type="submit" busy={busy} disabled={uploading}>
          <PlusOutlined />
          发布好物
        </Button>
      </form>
    </Modal>
  )
}
export function Profile({ user, onNavigate, onPublish, onLogout }) {
  const [busy, setBusy] = useState(false)
  const links = [
    { name: '我的订单', sub: '每一件好物的新旅程', page: 'orders', icon: <ShoppingOutlined /> },
    { name: '我的收藏', sub: '把喜欢留给下一次', page: 'favorites', icon: <HeartOutlined /> },
    { name: '我发布的', sub: '管理上架与下架', page: 'selling', icon: <PlusOutlined /> },
    { name: '我的消息', sub: '和同学聊聊交易细节', page: 'messages', icon: <MessageOutlined /> },
    { name: 'AI 小集', sub: '你的校园购物助手', page: 'agent', icon: <ThunderboltOutlined /> },
    ...(user.is_admin
      ? [
          {
            name: 'Agent 工作室',
            sub: '配置能力、发布助手与查看历史',
            page: 'agent-admin',
            icon: <ThunderboltOutlined />,
          },
        ]
      : []),
  ]
  return (
    <>
      <PageHeading eyebrow="YOUR CAMPUS SPACE" title="我的小天地" />
      <section className="profile-card">
        <Avatar name={user.name} size="large" />
        <div>
          <h2>{user.name}</h2>
          <p>{user.email}</p>
          <span className="member-badge">校园集市成员</span>
        </div>
        <Button onClick={onPublish}>
          <PlusOutlined />
          发布闲置
        </Button>
      </section>
      <div className="profile-links">
        {links.map((l) => (
          <button key={l.page} onClick={() => onNavigate(l.page)}>
            <span>{l.icon}</span>
            <div>
              <strong>{l.name}</strong>
              <small>{l.sub}</small>
            </div>
            <ArrowRightOutlined />
          </button>
        ))}
      </div>
      <Button
        variant="ghost"
        busy={busy}
        onClick={async () => {
          setBusy(true)
          await onLogout()
          setBusy(false)
        }}
      >
        <LogoutOutlined />
        退出登录
      </Button>
    </>
  )
}
