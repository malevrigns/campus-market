import { test, expect } from '@playwright/test'

async function code(page) {
  await page.getByRole('button', { name: '获取验证码', exact: true }).click()
  await page.getByRole('button', { name: '查看测试验证码', exact: true }).click()
  await expect(page.locator('input[name="code"]')).toHaveValue(/^\d{6}$/)
}
async function logout(page) {
  await page.goto('/#profile', { waitUntil: 'domcontentloaded' })
  await page.getByRole('button', { name: '退出登录', exact: true }).click()
  await expect(page.locator('.account-button .avatar')).toBeHidden()
}
async function loginPanel(page) {
  await page.locator('.account-button').click()
  await expect(page.locator('input[name="email"]')).toBeVisible()
}

test('verified email registration, password and code login, reset and phone binding', async ({ page }) => {
  test.setTimeout(90000)
  const errors = []
  page.on('pageerror', (e) => errors.push(e.message))
  const email = `identity-${Date.now()}@example.test`,
    phone = `1390000${String(Date.now() % 10000).padStart(4, '0')}`
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await loginPanel(page)
  await page.getByRole('button', { name: '注册账号', exact: true }).click()
  await page.locator('input[name="name"]').fill('王')
  await page.locator('input[name="email"]').fill(email)
  await page.locator('input[name="password"]').fill('short')
  await page.getByRole('button', { name: '创建账号', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('密码至少 8 个字符')
  await page.locator('input[name="password"]').fill('Password1')
  await code(page)
  await page.getByRole('button', { name: '创建账号', exact: true }).click()
  await expect(page.locator('.account-button .avatar')).toBeVisible()
  await page.goto('/#profile', { waitUntil: 'domcontentloaded' })
  await expect(page.locator('.account-security .verified')).toHaveCount(1)
  await page.reload()
  await expect(page.locator('.account-button .avatar')).toBeVisible()
  await logout(page)
  await loginPanel(page)
  await page.locator('input[name="email"]').fill(email)
  await page.locator('input[name="password"]').fill('WrongPass1')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('账号或密码不正确')
  await page.locator('input[name="password"]').fill('Password1')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.locator('.account-button .avatar')).toBeVisible()
  await logout(page)
  await loginPanel(page)
  await page.getByRole('button', { name: '验证码登录', exact: true }).click()
  await page.locator('input[name="email"]').fill(email)
  await code(page)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.locator('.account-button .avatar')).toBeVisible()
  await logout(page)
  await loginPanel(page)
  await page.getByRole('button', { name: '忘记密码？', exact: true }).click()
  await page.locator('input[name="email"]').fill(email)
  await page.locator('input[name="password"]').fill('NewPassword2')
  await code(page)
  await page.getByRole('button', { name: '重设密码', exact: true }).click()
  await expect(page.locator('.auth-notice')).toContainText('密码已更新')
  await page.locator('input[name="password"]').fill('NewPassword2')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.locator('.account-button .avatar')).toBeVisible()
  await page.goto('/#profile', { waitUntil: 'domcontentloaded' })
  await page.getByRole('button', { name: '绑定手机', exact: true }).click()
  await page.locator('input[name="phone"]').fill(phone)
  await code(page)
  await page.getByRole('button', { name: '确认验证', exact: true }).click()
  await expect(page.locator('.account-security .verified')).toHaveCount(2)
  await logout(page)
  await loginPanel(page)
  await page.locator('input[name="email"]').fill(phone)
  await page.locator('input[name="password"]').fill('NewPassword2')
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.locator('.account-button .avatar')).toBeVisible()
  await logout(page)
  await loginPanel(page)
  await page.getByRole('button', { name: '验证码登录', exact: true }).click()
  await page.getByRole('button', { name: '手机验证', exact: true }).click()
  await page.locator('input[name="phone"]').fill(phone)
  await code(page)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.locator('.account-button .avatar')).toBeVisible()
  expect(errors).toEqual([])
})

test('mobile SMS registration and themed verification panel are usable', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await page.getByRole('button', { name: '切换到浅色主题' }).click()
  await loginPanel(page)
  await page.getByRole('button', { name: '注册账号', exact: true }).click()
  await page.getByRole('button', { name: '手机验证', exact: true }).click()
  await page.locator('input[name="name"]').fill('手机同学')
  await page.locator('input[name="phone"]').fill(`1380000${String(Date.now() % 10000).padStart(4, '0')}`)
  await page.locator('input[name="password"]').fill('PhonePass123')
  await code(page)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy()
  await page.screenshot({ path: 'test-results/auth-sms-mobile-light.png', fullPage: true })
  await page.getByRole('button', { name: '创建账号', exact: true }).click()
  await expect(page.locator('.account-button .avatar')).toBeVisible()
})

test('failed resend clears the previous success state and old verification code', async ({ page }) => {
  await page.route('**/api/v1/config', (route) =>
    route.fulfill({
      json: { code: 0, data: { auth: { email_code: true, sms_code: true, local_inbox: false } } },
    }),
  )
  let sent = 0
  await page.route('**/api/v1/auth/codes', (route) =>
    ++sent === 1
      ? route.fulfill({
          json: {
            code: 0,
            data: {
              challenge_id: 'test-challenge',
              delivery: 'sent',
              target: 't***@example.test',
              expires_in: 300,
              retry_after: 0,
            },
          },
        })
      : route.fulfill({ status: 502, json: { code: 502, msg: '发送服务暂时不可用' } }),
  )
  await page.goto('/', { waitUntil: 'domcontentloaded' })
  await loginPanel(page)
  await page.getByRole('button', { name: '注册账号', exact: true }).click()
  await page.locator('input[name="email"]').fill('test@example.test')
  await page.getByRole('button', { name: '获取验证码', exact: true }).click()
  await expect(page.locator('.auth-notice')).toContainText('验证码已发送')
  await page.locator('input[name="code"]').fill('123456')
  await page.getByRole('button', { name: '重新发送', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('发送服务暂时不可用')
  await expect(page.locator('.auth-notice')).toHaveCount(0)
  await expect(page.locator('input[name="code"]')).toHaveValue('')
  await expect(page.getByRole('button', { name: '获取验证码', exact: true })).toBeEnabled()
})
