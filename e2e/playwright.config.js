import { existsSync, readFileSync } from 'node:fs'
import { defineConfig, devices } from '@playwright/test'
import { readConfig } from './e2e.config.js'

const config = readConfig()

// globalSetup writes this file; it carries the bearer/session-token header when
// the API-key mode is used (a cookie cannot be forged from a raw token).
function extraHeaders() {
  try {
    if (existsSync(config.storageHeadersPath)) {
      return JSON.parse(readFileSync(config.storageHeadersPath, 'utf8'))
    }
  } catch { /* an empty header set still lets the cookie jar work */ }
  return {}
}

export default defineConfig({
  testDir: './ui/journeys',
  testMatch: '**/*.spec.js',
  globalSetup: './ui/global-setup.js',
  timeout: 90_000,
  expect: { timeout: 12_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [
    ['list'],
    ['html', { outputFolder: 'playwright-report', open: 'never' }],
  ],
  use: {
    baseURL: config.baseURL,
    locale: 'en-US',
    storageState: config.storageStatePath,
    extraHTTPHeaders: extraHeaders(),
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    actionTimeout: 15_000,
    navigationTimeout: 30_000,
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  ],
})
