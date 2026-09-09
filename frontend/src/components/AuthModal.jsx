import { useEffect, useRef, useState } from 'react'
import { Modal } from 'antd'
import {
  ArrowRightOutlined,
  EyeInvisibleOutlined,
  EyeOutlined,
  MailOutlined,
  MobileOutlined,
} from '@ant-design/icons'
import { api } from '../lib/api'
import { Button, ErrorState } from './ui'
import './auth.css'

function checkTarget(channel, account) {
  const target = account.trim()
  if (channel === 'email' && !/^[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$/.test(target))
    return '请输入有效的邮箱，例如 name@qq.com'
  const phone = target.replace(/[ -]/g, '').replace(/^(\+86|86)(?=1\d{10}$)/, '')
  if (channel === 'sms' && !/^1[3-9]\d{9}$/.test(phone)) return '请输入中国大陆 11 位手机号'
  return ''
}
function checkPassword(password) {
  return Array.from(password).length < 8 || new TextEncoder().encode(password).length > 72 || !password.trim()
    ? '密码至少 8 个字符，且不能超过 72 字节'
    : ''
}

export default function AuthModal({
  open,
  onClose,
  onSuccess,
  initialMode = 'password',
  initialAccount = '',
  bindChannel,
  onBound,
}) {
  const [mode, setMode] = useState(initialMode),
    [channel, setChannel] = useState(bindChannel || 'email'),
    [account, setAccount] = useState(initialAccount),
    [name, setName] = useState(''),
    [password, setPassword] = useState(''),
    [code, setCode] = useState('')
  const [visible, setVisible] = useState(false),
    [busy, setBusy] = useState(false),
    [sending, setSending] = useState(false),
    [error, setError] = useState(null),
    [notice, setNotice] = useState(''),
    [challenge, setChallenge] = useState(null),
    [localCode, setLocalCode] = useState(''),
    [authConfig, setAuthConfig] = useState(null),
    [retryAt, setRetryAt] = useState(0),
    [tick, setTick] = useState(0)
  const controller = useRef(null),
    epoch = useRef(0)
  const purpose =
    mode === 'register'
      ? 'register'
      : mode === 'reset'
        ? 'reset_password'
        : mode === 'bind'
          ? 'bind'
          : 'login'
  const needsCode = mode !== 'password'
  const ready = channel === 'email' ? authConfig?.email_code : authConfig?.sms_code
  const countdown = Math.max(0, Math.ceil((retryAt - Date.now()) / 1000))
  useEffect(() => {
    if (!open) return
    epoch.current++
    controller.current = new AbortController()
    const signal = controller.current.signal
    setMode(initialMode)
    setChannel(bindChannel || 'email')
    setAccount(initialAccount)
    setName('')
    setPassword('')
    setCode('')
    setVisible(false)
    setError(null)
    setNotice('')
    setChallenge(null)
    setLocalCode('')
    setRetryAt(0)
    setBusy(false)
    setSending(false)
    api('/config', { signal })
      .then((d) => setAuthConfig(d.auth || {}))
      .catch((e) => {
        if (!signal.aborted) setError(e)
      })
    return () => {
      epoch.current++
      controller.current?.abort()
    }
  }, [open, initialMode, initialAccount, bindChannel])
  useEffect(() => {
    if (!open || retryAt <= Date.now()) return
    const timer = setInterval(() => setTick((n) => n + 1), 1000)
    return () => clearInterval(timer)
  }, [open, retryAt])
  const switchMode = (value) => {
    setMode(value)
    setError(null)
    setNotice('')
    setChallenge(null)
    setCode('')
    setLocalCode('')
    setRetryAt(0)
    if (value !== 'password' && account.trim()) setChannel(account.includes('@') ? 'email' : 'sms')
  }
  const changeAccount = (value) => {
    setAccount(value)
    setChallenge(null)
    setCode('')
    setLocalCode('')
    setError(null)
  }
  const send = async () => {
    if (sending || busy || countdown) return
    const validation = checkTarget(channel, account)
    if (validation) {
      setError(new Error(validation))
      return
    }
    if (!ready) {
      setError(new Error('该发送服务尚未开通，可先使用已有账号密码登录'))
      return
    }
    const current = epoch.current
    setSending(true)
    setError(null)
    setLocalCode('')
    setNotice('')
    setChallenge(null)
    setCode('')
    try {
      const data = await api('/auth/codes', {
        method: 'POST',
        body: { channel, target: account.trim(), purpose },
        signal: controller.current.signal,
      })
      if (current !== epoch.current) return
      setChallenge(data)
      setRetryAt(Date.now() + data.retry_after * 1000)
      setNotice(
        data.delivery === 'local'
          ? '验证码已放入本地测试收件箱，没有发送真实邮件或短信。'
          : `验证码已发送至 ${data.target}，5 分钟内有效。`,
      )
    } catch (e) {
      if (current === epoch.current) {
        setError(e)
        if (e.retryAfter) setRetryAt(Date.now() + e.retryAfter * 1000)
      }
    } finally {
      if (current === epoch.current) setSending(false)
    }
  }
  const showInbox = async () => {
    setError(null)
    try {
      const data = await api('/auth/dev-inbox', {
        method: 'POST',
        body: { challenge_id: challenge.challenge_id, delivery_token: challenge.delivery_token },
        signal: controller.current.signal,
      })
      setLocalCode(data.code)
      setCode(data.code)
    } catch (e) {
      if (!controller.current.signal.aborted) setError(e)
    }
  }
  const submit = async (e) => {
    e.preventDefault()
    if (busy || sending) return
    setError(null)
    const selectedChannel = mode === 'password' ? (account.includes('@') ? 'email' : 'sms') : channel
    const validation = checkTarget(selectedChannel, account)
    if (validation) {
      setError(new Error(validation))
      return
    }
    if (mode === 'register' && (!name.trim() || Array.from(name.trim()).length > 24)) {
      setError(new Error('昵称需要 1–24 个字'))
      return
    }
    if (
      ['password', 'register', 'reset'].includes(mode) &&
      (!password || (mode !== 'password' && checkPassword(password)))
    ) {
      setError(new Error(mode === 'password' ? '请输入密码' : checkPassword(password)))
      return
    }
    if (needsCode && (!challenge || !/^\d{6}$/.test(code))) {
      setError(new Error('请先获取验证码，并输入收到的 6 位数字'))
      return
    }
    const current = epoch.current
    setBusy(true)
    const proof = needsCode
      ? { channel, target: account.trim(), challenge_id: challenge.challenge_id, code }
      : {}
    try {
      if (mode === 'reset') {
        await api('/auth/password/reset', {
          method: 'POST',
          body: { ...proof, password },
          signal: controller.current.signal,
        })
        if (current !== epoch.current) return
        switchMode('password')
        setPassword('')
        setNotice('密码已更新，旧设备已退出登录。请使用新密码登录。')
      } else if (mode === 'bind') {
        const user = await api('/auth/bind', {
          method: 'POST',
          body: proof,
          signal: controller.current.signal,
        })
        if (current === epoch.current) onBound?.(user)
      } else {
        const path =
          mode === 'password' ? '/auth/login' : mode === 'register' ? '/auth/register' : '/auth/code-login'
        const body =
          mode === 'password'
            ? { account: account.trim(), password }
            : mode === 'register'
              ? { ...proof, name: name.trim(), password }
              : proof
        const user = await api(path, { method: 'POST', body, signal: controller.current.signal })
        if (current === epoch.current) onSuccess(user)
      }
    } catch (e) {
      if (current === epoch.current) setError(e)
    } finally {
      if (current === epoch.current) setBusy(false)
    }
  }
  const title =
    mode === 'register'
      ? '下一程，一起出发。'
      : mode === 'reset'
        ? '找回你的账号。'
        : mode === 'bind'
          ? '验证你的联系方式。'
          : '好久不见，同学。'
  return (
    <Modal
      open={open}
      onCancel={busy || sending ? undefined : onClose}
      footer={null}
      width={460}
      destroyOnClose
      title={null}
      className="auth-modal"
    >
      <div className="auth-art">
        ✳<span>HELLO, CAMPUS.</span>
      </div>
      <h2>{title}</h2>
      <p className="modal-subtitle">
        {mode === 'register'
          ? '验证邮箱或手机号后，创建你的校园集市账号。'
          : mode === 'reset'
            ? '验证你绑定的邮箱或手机号，重新设置密码。'
            : mode === 'bind'
              ? '完成验证后，可以用这个账号登录和找回密码。'
              : '支持邮箱或手机号，选择你方便的登录方式。'}
      </p>
      {(mode === 'password' || mode === 'code') && (
        <div className="auth-method-tabs">
          <button
            className={mode === 'password' ? 'active' : ''}
            disabled={busy || sending}
            onClick={() => switchMode('password')}
          >
            密码登录
          </button>
          <button
            className={mode === 'code' ? 'active' : ''}
            disabled={busy || sending}
            onClick={() => switchMode('code')}
          >
            验证码登录
          </button>
        </div>
      )}
      {needsCode && mode !== 'bind' && (
        <div className="auth-channel-tabs">
          <button
            aria-label="邮箱验证"
            className={channel === 'email' ? 'active' : ''}
            disabled={busy || sending}
            onClick={() => {
              setChannel('email')
              setAccount('')
              setChallenge(null)
              setCode('')
              setError(null)
              setNotice('')
              setRetryAt(0)
            }}
          >
            <MailOutlined />
            邮箱验证
          </button>
          <button
            aria-label="手机验证"
            className={channel === 'sms' ? 'active' : ''}
            disabled={busy || sending}
            onClick={() => {
              setChannel('sms')
              setAccount('')
              setChallenge(null)
              setCode('')
              setError(null)
              setNotice('')
              setRetryAt(0)
            }}
          >
            <MobileOutlined />
            手机验证
          </button>
        </div>
      )}
      <form className="form-stack" noValidate onSubmit={submit}>
        {mode === 'register' && (
          <label>
            昵称
            <input
              name="name"
              autoComplete="nickname"
              value={name}
              onChange={(e) => setName(e.target.value)}
              maxLength={24}
              disabled={busy || sending}
              placeholder="同学们怎么称呼你"
            />
          </label>
        )}
        <label>
          {mode === 'password' ? '邮箱或手机号' : channel === 'email' ? '邮箱' : '手机号'}
          <div className="auth-account-input">
            {needsCode && channel === 'sms' && <span>+86</span>}
            <input
              name={needsCode && channel === 'sms' ? 'phone' : 'email'}
              type="text"
              inputMode={needsCode && channel === 'sms' ? 'tel' : mode === 'password' ? 'text' : 'email'}
              autoComplete={needsCode ? (channel === 'sms' ? 'tel-national' : 'email') : 'username'}
              autoCapitalize="none"
              spellCheck={false}
              value={account}
              onChange={(e) => changeAccount(e.target.value)}
              maxLength={254}
              disabled={busy || sending}
              placeholder={
                mode === 'password'
                  ? '输入邮箱或手机号'
                  : channel === 'email'
                    ? 'name@qq.com'
                    : '请输入 11 位手机号'
              }
            />
          </div>
        </label>
        {needsCode && (
          <>
            <label>
              验证码
              <div className="auth-code-row">
                <input
                  name="code"
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  value={code}
                  onChange={(e) => setCode(e.target.value.replace(/\D/g, '').slice(0, 6))}
                  maxLength={6}
                  disabled={busy}
                  placeholder="6 位数字验证码"
                />
                <Button
                  type="button"
                  variant="secondary"
                  busy={sending}
                  disabled={busy || countdown > 0 || !ready}
                  onClick={send}
                >
                  {countdown > 0 ? `${countdown} 秒后重发` : challenge ? '重新发送' : '获取验证码'}
                </Button>
              </div>
            </label>
            {authConfig && !ready && (
              <div className="inline-notice">
                {channel === 'email' ? '邮件' : '短信'}发送服务尚未开通，暂时无法使用此方式。
              </div>
            )}
            {challenge?.delivery === 'local' && (
              <div className="auth-inbox">
                <span>本地测试收件箱</span>
                <button type="button" onClick={showInbox} disabled={busy}>
                  查看测试验证码
                </button>
                {localCode && <output data-testid="verification-code">{localCode}</output>}
              </div>
            )}
          </>
        )}
        {['password', 'register', 'reset'].includes(mode) && (
          <label>
            {mode === 'reset' ? '新密码' : '密码'}
            <div className="password-input">
              <input
                name="password"
                type={visible ? 'text' : 'password'}
                autoComplete={mode === 'password' ? 'current-password' : 'new-password'}
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                maxLength={72}
                disabled={busy}
                placeholder={mode === 'password' ? '输入你的密码' : '至少 8 个字符，建议包含字母和数字'}
              />
              <button
                type="button"
                onClick={() => setVisible((v) => !v)}
                aria-label={visible ? '隐藏密码' : '显示密码'}
              >
                {visible ? <EyeInvisibleOutlined /> : <EyeOutlined />}
              </button>
            </div>
          </label>
        )}
        {mode === 'password' && (
          <button type="button" className="forgot-password" onClick={() => switchMode('reset')}>
            忘记密码？
          </button>
        )}
        {notice && (
          <p className="auth-notice" role="status">
            {notice}
          </p>
        )}
        {error && <ErrorState error={error} />}
        <Button type="submit" busy={busy} disabled={sending}>
          {mode === 'register'
            ? '创建账号'
            : mode === 'reset'
              ? '重设密码'
              : mode === 'bind'
                ? '确认验证'
                : '登录'}
          <ArrowRightOutlined />
        </Button>
      </form>
      {mode !== 'bind' && (
        <p className="auth-switch">
          {mode === 'register' ? '已经有账号？' : mode === 'reset' ? '想起密码了？' : '第一次来到这里？'}
          <button
            onClick={() => switchMode(mode === 'register' || mode === 'reset' ? 'password' : 'register')}
            disabled={busy || sending}
          >
            {mode === 'register' || mode === 'reset' ? '去登录' : '注册账号'}
          </button>
        </p>
      )}
    </Modal>
  )
}
