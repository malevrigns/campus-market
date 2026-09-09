import { test, expect } from '@playwright/test'

async function register(page, name) {
  await page.goto('/')
  await page.locator('.account-button').click()
  await page.getByRole('button', { name: '注册账号', exact: true }).click()
  await page.locator('input[name="name"]').fill(name)
  await page.locator('input[name="email"]').fill(`student-${Date.now()}@campus.test`)
  await page.locator('input[name="password"]').fill('E2e-strong-password-123')
  await page.getByRole('button', { name: '创建账号' }).click()
  await expect(page.locator('.account-button .avatar')).toBeVisible()
  await expect(page.locator('.ant-modal')).toBeHidden()
}
test('desktop and mobile layouts, search, demo boundary, reduced motion', async ({ page }) => {
  const errors = []
  page.on('pageerror', (e) => errors.push(e.message))
  for (const width of [1440, 768, 390, 360]) {
    await page.setViewportSize({ width, height: 900 })
    await page.goto('/')
    await expect(page.locator('.product-card')).toHaveCount(8)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy()
    await page.screenshot({ path: `test-results/home-${width}.png`, fullPage: true })
  }
  await page.getByRole('textbox', { name: '搜索好物' }).fill('相机')
  await expect(page.locator('.product-card')).toHaveCount(1)
  await page.locator('.product-open').click()
  await expect(page.getByRole('button', { name: '我想要', exact: true })).toBeDisabled()
  await expect(page.getByText('这是用于展示界面的示例商品', { exact: false })).toBeVisible()
  expect(errors).toEqual([])
  await page.emulateMedia({ reducedMotion: 'reduce' })
  expect(await page.locator('.hero-copy').evaluate((e) => getComputedStyle(e).animationName)).toBe('none')
})

test('real upload, two-account IM, server order, cancellation and persistence', async ({ browser, page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 })
  await register(page, '测试卖家')
  await page.locator('.header-publish').click()
  // Browser creates a genuine PNG; backend decodes and re-encodes it.
  const png = await page.evaluate(() => {
    const c = document.createElement('canvas')
    c.width = 300
    c.height = 300
    const ctx = c.getContext('2d')
    ctx.fillStyle = '#c2d298'
    ctx.fillRect(0, 0, 300, 300)
    ctx.fillStyle = '#344423'
    ctx.font = '28px sans-serif'
    ctx.fillText('Campus test', 50, 160)
    return c.toDataURL().split(',')[1]
  })
  await page
    .getByLabel('上传商品图片')
    .setInputFiles({ name: 'campus.png', mimeType: 'image/png', buffer: Buffer.from(png, 'base64') })
  await expect(page.getByAltText('待发布商品预览')).toBeVisible()
  await page.locator('input[name="title"]').fill('测试相机：双账号验收')
  await page.locator('input[name="price"]').fill('123.45')
  await page.locator('textarea[name="description"]').fill('这件商品由浏览器测试真实发布，可以当面验货。')
  await page.getByRole('button', { name: '发布好物', exact: true }).click()
  await expect(page.locator('.ant-modal')).toBeHidden()
  await expect(page.getByRole('heading', { name: '测试相机：双账号验收' })).toBeVisible()

  const buyerContext = await browser.newContext({
    viewport: { width: 390, height: 844 },
    baseURL: 'http://127.0.0.1:15173',
  })
  const buyer = await buyerContext.newPage()
  await register(buyer, '测试买家')
  await buyer.getByRole('textbox', { name: '搜索好物' }).fill('双账号验收')
  await expect(buyer.locator('.product-card')).toHaveCount(1)
  await buyer.locator('.save-button').click()
  await expect(buyer.locator('.save-button')).toHaveAttribute('aria-pressed', 'true')
  await buyer.locator('.product-open').click()
  await buyer.getByRole('button', { name: '聊一聊', exact: true }).click()
  await expect(buyer.getByRole('textbox', { name: '输入消息' })).toBeVisible()
  await buyer.getByRole('textbox', { name: '输入消息' }).fill('同学你好，可以在东区面交吗？')
  await buyer.getByRole('button', { name: '发送消息' }).click()
  await expect(buyer.locator('.message-bubble').getByText('同学你好，可以在东区面交吗？')).toBeVisible()
  await page.goto('/#messages')
  await page.locator('.conversation-list>button').click()
  await expect(page.locator('.message-bubble').getByText('同学你好，可以在东区面交吗？')).toBeVisible()
  await page.getByRole('textbox', { name: '输入消息' }).fill('可以，明天下午图书馆门口见。')
  await page.getByRole('button', { name: '发送消息' }).click()
  await expect(buyer.locator('.message-bubble').getByText('可以，明天下午图书馆门口见。')).toBeVisible({
    timeout: 20000,
  })
  await buyer.screenshot({ path: 'test-results/mobile-chat.png' })
  await buyer.reload()
  await buyer.locator('.conversation-list>button').click()
  await expect(buyer.locator('.message-bubble')).toHaveCount(2)

  await buyer.goto('/')
  await buyer.getByRole('textbox', { name: '搜索好物' }).fill('双账号验收')
  await expect(buyer.locator('.product-card')).toHaveCount(1)
  await buyer.locator('.product-open').click()
  await buyer.getByRole('button', { name: '我想要', exact: true }).click()
  await buyer.getByRole('button', { name: '确认订单', exact: true }).click()
  await expect(buyer.locator('.order-reference code')).toContainText('CM')
  await expect(buyer.locator('.fee-row strong')).toHaveText('¥123.45')
  await expect(buyer.getByRole('button', { name: '在线支付暂未开通', exact: true })).toBeDisabled()
  await buyer.screenshot({ path: 'test-results/mobile-checkout.png' })
  await buyer.getByRole('button', { name: '保存订单，稍后处理' }).click()
  await expect(buyer.locator('.order-status')).toHaveText('待付款')
  await buyer.getByRole('button', { name: '取消订单', exact: true }).click()
  await expect(buyer.locator('.order-status')).toHaveText('已取消')
  await buyer.reload()
  await expect(buyer.locator('.order-status')).toHaveText('已取消')
  await buyer.goto('/#agent')
  await expect(buyer.getByRole('button', { name: '发送问题' })).toBeDisabled()
  await buyer.screenshot({ path: 'test-results/mobile-agent.png' })
  expect(await buyer.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy()
  await buyerContext.close()
})
