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
import IdentityModal from './AuthModal'
import { api, CAMPUSES, CATEGORIES, CONDITIONS, toCents } from '../lib/api'
import { Avatar, Button, ErrorState, PageHeading, ProductImage } from './ui'

export { default as AuthModal } from './AuthModal'

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
export function Profile({ user, onNavigate, onPublish, onLogout, onUserUpdate }) {
  const [busy, setBusy] = useState(false)
  const [contact, setContact] = useState(null)
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
          <p>{user.email || user.phone}</p>
          <span className="member-badge">校园集市成员</span>
        </div>
        <Button onClick={onPublish}>
          <PlusOutlined />
          发布闲置
        </Button>
      </section>
      <section className="account-security">
        <h3>账号安全</h3>
        <div>
          <div>
            <strong>邮箱</strong>
            <small>{user.email || '尚未绑定邮箱'}</small>
          </div>
          {user.email_verified ? (
            <span className="verified">已验证</span>
          ) : (
            <Button
              variant="secondary"
              onClick={() => setContact({ channel: 'email', target: user.email || '' })}
            >
              {user.email ? '验证邮箱' : '绑定邮箱'}
            </Button>
          )}
        </div>
        <div>
          <div>
            <strong>手机号</strong>
            <small>{user.phone || '尚未绑定手机号'}</small>
          </div>
          {user.phone_verified ? (
            <span className="verified">已验证</span>
          ) : (
            <Button
              variant="secondary"
              onClick={() => setContact({ channel: 'sms', target: user.phone || '' })}
            >
              {user.phone ? '验证手机' : '绑定手机'}
            </Button>
          )}
        </div>
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
      {contact && (
        <IdentityModal
          open
          initialMode="bind"
          initialAccount={contact.target}
          bindChannel={contact.channel}
          onClose={() => setContact(null)}
          onBound={(fresh) => {
            onUserUpdate?.(fresh)
            setContact(null)
          }}
        />
      )}
    </>
  )
}
