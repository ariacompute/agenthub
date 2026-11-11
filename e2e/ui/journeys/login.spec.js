import { test, expect } from '@playwright/test'
import { SEL, preparePage } from '../helpers.js'

// This file deliberately starts logged out so the guard and the form are real.
test.use({ storageState: { cookies: [], origins: [] }, extraHTTPHeaders: {} })

test.describe('journey: login', () => {
  test.beforeEach(async ({ page }) => {
    await preparePage(page)
  })

  test('an anonymous visitor is redirected to login with a return path', async ({ page }) => {
    await page.goto('/registry')
    await expect(page).toHaveURL(/\/login\?redirect=/)
    await expect(page.locator(SEL.loginCard)).toBeVisible()
    await expect(page.locator(SEL.loginEmail)).toBeVisible()
    await expect(page.locator(SEL.loginPassword)).toBeVisible()
  })

  test('the login screen offers sign-in and registration', async ({ page }) => {
    await page.goto('/login')
    await expect(page.locator(SEL.loginCard)).toBeVisible()
    await expect(page.locator(SEL.loginTabs)).toHaveCount(2)
    await expect(page.locator(SEL.loginSubmit)).toBeVisible()
    await expect(page.locator('form.login-form button[type=submit]')).toBeVisible()
    // OAuth buttons only render when a provider is configured on the server,
    // so their presence is asserted by the API suite rather than here.
  })

  test('switching to registration reveals the name and confirm fields', async ({ page }) => {
    await page.goto('/login')
    await page.locator(SEL.loginSwitch).click()
    await expect(page.locator(SEL.loginName)).toBeVisible()
    await expect(page.locator(SEL.loginConfirm)).toBeVisible()
  })

  test('a wrong password keeps the user on the login screen', async ({ page }) => {
    await page.goto('/login')
    await page.locator(SEL.loginEmail).fill('e2e-no-such-user@example.test')
    await page.locator(SEL.loginPassword).fill('definitely-not-a-password')
    await page.locator(SEL.loginSubmit).click()
    await expect(page.locator(SEL.errorLine)).toBeVisible()
    await expect(page).toHaveURL(/\/login/)
  })
})
