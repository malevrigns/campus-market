import { defineConfig } from '@playwright/test'

// Uses the already running development server; never starts or stops a service.
export default defineConfig({
  testDir: '.',
  testMatch: 'theme-widget.spec.js',
  workers: 1,
  timeout: 30000,
  use: {
    baseURL: 'http://127.0.0.1:15173',
    channel: process.env.PLAYWRIGHT_CHANNEL || 'chrome',
    headless: true,
    reducedMotion: 'reduce',
    screenshot: 'only-on-failure',
  },
})
