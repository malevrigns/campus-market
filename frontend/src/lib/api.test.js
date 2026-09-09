import { test } from 'node:test'
import assert from 'node:assert/strict'
import { consumeSSE, toCents } from './api.js'

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
