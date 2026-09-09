import { test, expect } from '@playwright/test'

const student = { id: 'widget-student', name: '浮窗同学', is_admin: false }
const reply = (route, data) => route.fulfill({ json: { code: 0, data } })

async function mockSession(page, signedIn = true) {
  await page.route('**/api/v1/auth/me', (route) =>
    signedIn ? reply(route, student) : route.fulfill({ status: 401, json: { code: 401, msg: '请登录' } }),
  )
  await page.route('**/api/v1/favorites', (route) => reply(route, { ids: [] }))
  await page.route('**/api/v1/conversations', (route) => reply(route, { items: [] }))
  await page.route('**/api/v1/products?**', (route) =>
    new URL(route.request().url()).searchParams.has('saved')
      ? reply(route, { items: [], total: 0 })
      : route.continue(),
  )
  await page.route('**/api/v1/agents', (route) =>
    reply(route, {
      items: [
        {
          id: 'campus-helper',
          name: '校园助手',
          version: 1,
          available: true,
          welcome: '一起发现校园好物',
          description: '帮你找好物',
          suggestions: ['寻找书籍'],
          tools: [],
        },
      ],
    }),
  )
  await page.route('**/api/v1/agent/threads', (route) =>
    reply(route, { items: [{ id: 'previous', title: '上次的选书建议' }] }),
  )
  await page.route('**/api/v1/agent/threads/previous', (route) =>
    reply(route, {
      agent_id: 'campus-helper',
      items: [{ id: 'saved', prompt: '推荐书籍', answer: '可以按课程寻找教材。', status: 'completed' }],
    }),
  )
}

test('theme switches, survives reload and keeps narrow layouts inside the viewport', async ({ page }) => {
  await mockSession(page)
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await page.getByRole('button', { name: '切换到浅色主题' }).click()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
  await page.reload()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
  for (const width of [1440, 768, 390, 320]) {
    await page.setViewportSize({ width, height: 844 })
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy()
    await expect(page.getByRole('button', { name: '切换到深色主题' })).toBeVisible()
    if (width === 1440 || width === 320)
      await page.screenshot({ path: `test-results/theme-light-${width}.png`, fullPage: true })
  }
  await page.getByRole('button', { name: '切换到深色主题' }).click()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  await page.screenshot({ path: 'test-results/theme-dark-320.png', fullPage: true })
})

test('floating assistant drags without opening, persists position, opens on the current page and supports history', async ({
  page,
}) => {
  await mockSession(page)
  await page.goto('/#favorites', { waitUntil: 'domcontentloaded' })
  const launcher = page.getByRole('button', { name: '打开 AI 助手' })
  const initial = await launcher.boundingBox()
  await page.mouse.move(initial.x + 28, initial.y + 28)
  await page.mouse.down()
  await page.mouse.move(180, 260, { steps: 12 })
  await page.mouse.up()
  await expect(page.getByRole('dialog', { name: 'AI 助手' })).toBeHidden()
  const moved = await launcher.boundingBox()
  expect(moved.x).toBeLessThan(initial.x)
  await page.reload()
  const restored = await launcher.boundingBox()
  expect(Math.abs(restored.x - moved.x)).toBeLessThan(2)
  await launcher.focus()
  await page.keyboard.press('Enter')
  const panel = page.getByRole('dialog', { name: 'AI 助手' })
  await expect(panel).toBeVisible()
  await expect(page).toHaveURL(/#favorites$/)
  await panel.getByRole('button', { name: '查看对话历史' }).click()
  await panel.getByRole('button', { name: '上次的选书建议' }).click()
  await expect(panel.getByText('可以按课程寻找教材。')).toBeVisible()
  await panel.getByRole('button', { name: '查看对话历史' }).click()
  await panel.getByRole('button', { name: '返回聊天' }).click()
  await panel.getByRole('textbox', { name: '向助手提问' }).fill('保留输入内容')
  await page.keyboard.press('Escape')
  await expect(panel).toBeHidden()
  await launcher.click()
  await expect(panel.getByRole('textbox', { name: '向助手提问' })).toHaveValue('保留输入内容')
  await page.screenshot({ path: 'test-results/widget-dark-desktop.png' })
  await page.getByRole('button', { name: '切换到浅色主题' }).click()
  await page.setViewportSize({ width: 320, height: 650 })
  const bounds = await panel.boundingBox()
  expect(bounds.x).toBeGreaterThanOrEqual(0)
  expect(bounds.x + bounds.width).toBeLessThanOrEqual(320)
  expect(bounds.y + bounds.height).toBeLessThanOrEqual(585)
  await page.screenshot({ path: 'test-results/widget-light-mobile.png' })
  await panel.getByRole('button', { name: '收起 AI 助手' }).click()
  const icon = await launcher.boundingBox()
  expect(icon.x + icon.width).toBeLessThanOrEqual(320)
  expect(icon.y + icon.height).toBeLessThanOrEqual(585)
})

test('floating chat sends streamed answers and stops a pending response', async ({ page }) => {
  await mockSession(page)
  let requests = 0
  await page.route('**/api/v1/agent/runs', async (route) => {
    requests += 1
    if (requests === 2) {
      // Keep the request in progress long enough to exercise the actual AbortController.
      await new Promise((resolve) => setTimeout(resolve, 1200))
      await route.abort().catch(() => {})
      return
    }
    expect(route.request().postDataJSON()).toMatchObject({ prompt: '寻找教材', agent_id: 'campus-helper' })
    await route.fulfill({
      contentType: 'text/event-stream',
      body: [
        'event: start\ndata: {"thread_id":"new-thread","agent_version":1,"agent_name":"校园助手"}\n\n',
        'event: delta\ndata: {"text":"可以看看教材书籍分类。"}\n\n',
        'event: done\ndata: {"id":"answer-1","prompt":"寻找教材","answer":"可以看看教材书籍分类。","status":"completed"}\n\n',
      ].join(''),
    })
  })
  await page.goto('/#agent', { waitUntil: 'domcontentloaded' })
  const panel = page.getByRole('dialog', { name: 'AI 助手' })
  await panel.getByRole('textbox', { name: '向助手提问' }).fill('寻找教材')
  await panel.getByRole('button', { name: '发送问题' }).click()
  await expect(panel.getByText('可以看看教材书籍分类。')).toBeVisible()
  await panel.getByRole('textbox', { name: '向助手提问' }).fill('继续找一找')
  await panel.getByRole('button', { name: '发送问题' }).click()
  await panel.getByRole('button', { name: '停止生成' }).click()
  await expect(panel.getByText('已停止生成')).toBeVisible()
  await expect(panel.getByRole('button', { name: '发送问题' })).toBeVisible()
})

test('legacy AI links open a chat over the market and guest clicks open login', async ({ page }) => {
  await mockSession(page)
  await page.goto('/#agent', { waitUntil: 'domcontentloaded' })
  await expect(page.getByRole('dialog', { name: 'AI 助手' })).toBeVisible()
  await expect(page.locator('.hero')).toBeVisible()
  await expect(page).not.toHaveURL(/#agent$/)
  await mockSession(page, false)
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await page.getByRole('button', { name: '打开 AI 助手' }).click()
  await expect(page.locator('.auth-modal')).toBeVisible()
  await expect(page.getByRole('dialog', { name: 'AI 助手' })).toBeHidden()
})

async function signInThroughModal(page) {
  const modal = page.locator('.auth-modal')
  await modal.locator('input[name="email"]').fill('widget@campus.test')
  await modal.locator('input[name="password"]').fill('Widget-test-password-123')
  await modal.locator('button[type="submit"]').click()
  await expect(modal).toBeHidden()
  await expect(page.locator('.account-button .avatar')).toHaveText('浮')
}

test('reopening refreshes removed assistants while preserving drafts and historical threads', async ({
  page,
}) => {
  await mockSession(page)
  const original = { id: 'campus-helper', name: '校园助手', available: true, tools: [], suggestions: [] }
  const replacement = { ...original, id: 'replacement', name: '新的助手' }
  let catalog = [original, replacement]
  await page.route('**/api/v1/agents', (route) => reply(route, { items: catalog }))
  await page.goto('/#agent', { waitUntil: 'domcontentloaded' })
  const panel = page.getByRole('dialog', { name: 'AI 助手' })
  const picker = panel.getByRole('combobox', { name: '选择助手' })
  await expect(picker).toHaveValue(original.id)
  await panel.getByRole('textbox', { name: '向助手提问' }).fill('切换前的草稿')
  await panel.getByRole('button', { name: '收起 AI 助手' }).click()
  catalog = [replacement]
  await page.getByRole('button', { name: '打开 AI 助手' }).click()
  await expect(picker).toHaveValue(replacement.id)
  await expect(picker.locator('option')).toHaveCount(1)
  await expect(panel.getByRole('textbox', { name: '向助手提问' })).toHaveValue('切换前的草稿')

  await panel.getByRole('button', { name: '查看对话历史' }).click()
  await panel.getByRole('button', { name: '上次的选书建议' }).click()
  await expect(picker).toHaveValue(original.id)
  await expect(picker.locator('option:checked')).toHaveText('助手已停用')
  await panel.getByRole('button', { name: '收起 AI 助手' }).click()
  await page.getByRole('button', { name: '打开 AI 助手' }).click()
  await expect(picker).toHaveValue(original.id)
  await expect(panel.getByText('可以按课程寻找教材。')).toBeVisible()
  await expect(panel.getByRole('button', { name: '发送问题' })).toBeDisabled()
})

test('entering mobile messages moves a saved bottom launcher away from the send button', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await mockSession(page)
  await page.addInitScript(() =>
    localStorage.setItem('campus-assistant-position', JSON.stringify({ x: 320, y: 700 })),
  )
  await page.routeWebSocket('**/api/v1/ws', () => {})
  await page.route('**/api/v1/conversations', (route) =>
    reply(route, {
      items: [
        {
          id: 'widget-chat',
          peer_name: '测试同学',
          product_title: '课本',
          unread: 0,
          updated_at: '2026-09-09T10:00:00Z',
        },
      ],
    }),
  )
  await page.route('**/api/v1/conversations/widget-chat/messages?**', (route) =>
    reply(route, { items: [], next_seq: 0, has_more: false }),
  )
  let sent = 0
  await page.route('**/api/v1/conversations/widget-chat/messages', (route) => {
    sent += 1
    return reply(route, {
      id: 'sent-message',
      seq: 1,
      sender_id: student.id,
      content: '可以直接发送',
      created_at: '2026-09-09T10:00:00Z',
    })
  })
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  const launcher = page.getByRole('button', { name: '打开 AI 助手' })
  const original = await launcher.boundingBox()
  await page.locator('.mobile-nav').getByRole('button', { name: '消息' }).click()
  await page.locator('.conversation-list > button').click()
  const composer = await page.locator('.message-composer').boundingBox()
  const moved = await launcher.boundingBox()
  expect(moved.y).toBeLessThan(original.y)
  expect(moved.y + moved.height).toBeLessThan(composer.y)
  await page.mouse.move(moved.x + 28, moved.y + 28)
  await page.mouse.down()
  await page.mouse.move(350, 740, { steps: 8 })
  await page.mouse.up()
  const clamped = await launcher.boundingBox()
  expect(clamped.y + clamped.height).toBeLessThan(composer.y)
  await page.getByRole('textbox', { name: '输入消息' }).fill('可以直接发送')
  await page.getByRole('button', { name: '发送消息' }).click()
  await expect.poll(() => sent).toBe(1)
})

test('logging in from the floating launcher opens chat and preserves its first draft', async ({ page }) => {
  await mockSession(page, false)
  await page.route('**/api/v1/auth/login', (route) => reply(route, student))
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await page.getByRole('button', { name: '打开 AI 助手' }).click()
  await signInThroughModal(page)
  const panel = page.getByRole('dialog', { name: 'AI 助手' })
  await expect(panel).toBeVisible()
  await panel.getByRole('textbox', { name: '向助手提问' }).fill('登录后的第一条草稿')
  await panel.getByRole('button', { name: '收起 AI 助手' }).click()
  await page.getByRole('button', { name: '打开 AI 助手' }).click()
  await expect(panel.getByRole('textbox', { name: '向助手提问' })).toHaveValue('登录后的第一条草稿')
})

test('a late initial identity response cannot overwrite a newly signed in user', async ({ page }) => {
  await mockSession(page, false)
  let resolveIdentity
  const identityReady = new Promise((resolve) => {
    resolveIdentity = resolve
  })
  let receivedIdentity = false
  await page.route('**/api/v1/auth/me', async (route) => {
    receivedIdentity = true
    await identityReady
    await reply(route, { ...student, id: 'old-account', name: '旧账号' }).catch(() => {})
  })
  await page.route('**/api/v1/auth/login', (route) => reply(route, student))
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await expect.poll(() => receivedIdentity).toBeTruthy()
  await page.locator('.account-button').click()
  await signInThroughModal(page)
  const response = page.waitForResponse('**/api/v1/auth/me')
  resolveIdentity()
  await response
  await page.getByRole('button', { name: '打开 AI 助手' }).click()
  await expect(page.getByRole('dialog', { name: 'AI 助手' })).toBeVisible()
  await expect(page.locator('.account-button .avatar')).toHaveText('浮')
})
