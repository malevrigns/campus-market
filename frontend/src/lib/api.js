export class ApiError extends Error {
  constructor(message, status) {
    super(message)
    this.status = status
  }
}

export async function api(path, { body, ...options } = {}) {
  const timeout = AbortSignal.timeout(20000)
  const signal = options.signal ? AbortSignal.any([options.signal, timeout]) : timeout
  const response = await fetch(`/api/v1${path}`, {
    credentials: 'same-origin',
    ...options,
    signal,
    headers: body instanceof FormData ? {} : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : body instanceof FormData ? body : JSON.stringify(body),
  })
  const result = await response.json().catch(() => null)
  if (!response.ok || !result || result.code !== 0) {
    if (response.status === 401) window.dispatchEvent(new Event('session-expired'))
    throw new ApiError(result?.msg || '连接暂时不可用，请稍后重试', response.status)
  }
  return result.data
}

export async function consumeSSE(body, onEvent) {
  const reader = body.getReader(),
    decoder = new TextDecoder()
  let buffer = ''
  const dispatch = (frame) => {
    let event = 'message'
    const data = []
    for (const line of frame.split('\n')) {
      if (line.startsWith('event:')) event = line.slice(6).trim()
      if (line.startsWith('data:')) data.push(line.slice(5).trimStart())
    }
    if (data.length) onEvent(event, JSON.parse(data.join('\n')))
  }
  try {
    for (;;) {
      const { value, done } = await reader.read()
      buffer += decoder.decode(value, { stream: !done })
      buffer = buffer.replace(/\r\n/g, '\n')
      let boundary
      while ((boundary = buffer.indexOf('\n\n')) !== -1) {
        dispatch(buffer.slice(0, boundary))
        buffer = buffer.slice(boundary + 2)
      }
      if (buffer.length > 1000000) throw new Error('流式响应过长')
      if (done) {
        if (buffer.trim()) dispatch(buffer)
        break
      }
    }
  } finally {
    reader.releaseLock()
  }
}

export async function runAgent(body, signal, onEvent) {
  const response = await fetch('/api/v1/agent/runs', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
    signal,
  })
  if (!response.ok) {
    const data = await response.json().catch(() => ({}))
    throw new ApiError(data.msg || 'AI 暂时无法连接', response.status)
  }
  let completed = false
  await consumeSSE(response.body, (type, value) => {
    if (type === 'done') completed = true
    onEvent(type, value)
  })
  if (!completed) throw new Error('连接中断，请在对话历史中查看保存的回答')
}
export const money = (ct) =>
  new Intl.NumberFormat('zh-CN', {
    style: 'currency',
    currency: 'CNY',
    maximumFractionDigits: ct % 100 === 0 ? 0 : 2,
  }).format(ct / 100)
export const dateTime = (ts) =>
  new Date(ts).toLocaleString('zh-CN', {
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
export function toCents(value) {
  const text = String(value)
  if (!/^\d{1,6}(\.\d{1,2})?$/.test(text)) throw new Error('请输入最多两位小数的金额')
  const [whole, fraction = ''] = text.split('.')
  return Number(whole) * 100 + Number(fraction.padEnd(2, '0'))
}
export const newKey = () => crypto.randomUUID()
export const CATEGORIES = ['数码电子', '教材书籍', '生活日用', '服饰鞋包', '运动户外', '美妆个护']
export const CAMPUSES = ['东区', '西区', '南湖校区', '未来城']
export const CONDITIONS = ['全新', '九九新', '九成新', '八成新', '七成新']
export const STATUS = {
  pending: '待付款',
  paid: '待面交',
  handover: '待确认收货',
  completed: '已完成',
  cancelled: '已取消',
  refund_requested: '退款待处理',
  refunded: '已退款',
}
