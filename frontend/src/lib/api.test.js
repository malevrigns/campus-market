import { test } from 'node:test'
import assert from 'node:assert/strict'
import { api, consumeSSE, toCents } from './api.js'

test('SSE survives split UTF-8 and CRLF boundaries', async () => {
  const bytes = new TextEncoder().encode(
    'event: delta\r\ndata: {"text":"你好"}\r\n\r\nevent: done\ndata: {"status":"completed"}\n\n',
  )
  const body = new ReadableStream({
    start(c) {
      for (const b of bytes) c.enqueue(new Uint8Array([b]))
      c.close()
    },
  })
  const events = []
  await consumeSSE(body, (type, value) => events.push([type, value]))
  assert.deepEqual(events, [
    ['delta', { text: '你好' }],
    ['done', { status: 'completed' }],
  ])
})
test('money input preserves integer cents and rejects extra decimals', () => {
  assert.equal(toCents('123.45'), 12345)
  assert.equal(toCents('0.01'), 1)
  for (const text of ['1.001', '-1', 'NaN', '1e3']) assert.throws(() => toCents(text))
})

test('wrong credentials do not erase an existing session, protected 401 does', async () => {
  const previousFetch = globalThis.fetch,
    previousWindow = globalThis.window
  globalThis.window = new EventTarget()
  let expired = 0
  window.addEventListener('session-expired', () => expired++)
  globalThis.fetch = async () =>
    new Response(JSON.stringify({ code: 401, msg: 'invalid credentials' }), {
      status: 401,
      headers: { 'Content-Type': 'application/json' },
    })
  try {
    await assert.rejects(
      api('/auth/login', { method: 'POST', body: { account: 'test@example.test', password: 'wrong' } }),
    )
    assert.equal(expired, 0)
    await assert.rejects(api('/auth/me', { suppressAuthEvent: true }))
    assert.equal(expired, 0)
    await assert.rejects(api('/orders'))
    assert.equal(expired, 1)
  } finally {
    globalThis.fetch = previousFetch
    globalThis.window = previousWindow
  }
})
test('requests work without AbortSignal.any or AbortSignal.timeout', async () => {
  const fetchBefore = globalThis.fetch,
    any = AbortSignal.any,
    timeout = AbortSignal.timeout
  AbortSignal.any = undefined
  AbortSignal.timeout = undefined
  globalThis.fetch = async () =>
    new Response(JSON.stringify({ code: 0, data: { ok: true } }), {
      headers: { 'Content-Type': 'application/json' },
    })
  try {
    assert.deepEqual(await api('/config'), { ok: true })
  } finally {
    globalThis.fetch = fetchBefore
    AbortSignal.any = any
    AbortSignal.timeout = timeout
  }
})
test('late unauthorized response from an old session cannot clear a new login', async () => {
  const fetchBefore = globalThis.fetch,
    windowBefore = globalThis.window
  globalThis.window = new EventTarget()
  let expired = 0,
    release
  window.addEventListener('session-expired', () => expired++)
  globalThis.fetch = async (url) =>
    url.endsWith('/orders')
      ? new Promise((r) => {
          release = r
        })
      : new Response(JSON.stringify({ code: 0, data: { id: 'new-user' } }), {
          headers: { 'Content-Type': 'application/json' },
        })
  try {
    const old = api('/orders')
    await api('/auth/login', { method: 'POST', body: { account: 'new@example.test', password: 'password' } })
    release(
      new Response(JSON.stringify({ code: 401, msg: 'old session' }), {
        status: 401,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    await assert.rejects(old)
    assert.equal(expired, 0)
  } finally {
    globalThis.fetch = fetchBefore
    globalThis.window = windowBefore
  }
})
