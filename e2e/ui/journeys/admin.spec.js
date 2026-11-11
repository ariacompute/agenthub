import { test, expect } from '@playwright/test'
import {
  SEL, preparePage, open, afterWrite, unique, apiCleanup, canWrite,
} from '../helpers.js'

test.describe('journey: administration', () => {
  test.beforeEach(async ({ page }) => {
    await preparePage(page)
  })

  test('create a user, mint an API key and inspect the backup panel', async ({ page }) => {
    await open(page, '/admin')
    test.skip(!(await canWrite(page)), 'needs users:manage and apikeys:manage (owner identity)')

    const tabs = page.locator(SEL.adminTabs)
    await expect(tabs.first()).toBeVisible()

    let userId = null
    let keyId = null
    try {
      // --- users ---
      const email = `e2e-${unique('ui')}@example.test`
      await page.locator('.admin-head button.btn-primary').click()
      await expect(page.locator('#user-email')).toBeVisible()
      await page.locator('#user-email').fill(email)
      await page.locator('#user-name').fill('E2E UI User')
      await page.locator('#user-password').fill('e2e-password-123')
      const createdUser = await afterWrite(page, 'POST', '/api/users', () =>
        page.locator('.drawer-actions button[type=submit].btn-primary').click())
      expect(createdUser.status()).toBe(201)
      userId = (await createdUser.json())?.user?.id
      expect(userId).toBeTruthy()
      await expect(page.locator(SEL.adminRow).filter({ hasText: email })).toBeVisible()

      // --- api keys (last tab) ---
      await tabs.last().click()
      await page.locator('.admin-head button.btn-primary').click()
      await expect(page.locator('#key-name')).toBeVisible()
      await page.locator('#key-name').fill(unique('e2e-ui-key'))
      const createdKey = await afterWrite(page, 'POST', '/api/api-keys', () =>
        page.locator('form.drawer-form button.btn-primary[type=submit]').click())
      expect(createdKey.status()).toBe(201)
      keyId = (await createdKey.json())?.apiKey?.id
      // The one-time token is shown exactly once, in a read-only input.
      await expect(page.locator('input.token-input[readonly]')).toBeVisible()
      await page.locator('.token-block button.btn-primary').click()

      // --- backups ---
      await tabs.nth(4).click()
      const disabled = page.locator('.backup-disabled')
      if (await disabled.count()) {
        await expect(disabled).toBeVisible()
      } else {
        await expect(page.locator('.admin-table[role=table], button.btn-primary').first()).toBeVisible()
      }
    } finally {
      if (keyId) await apiCleanup(page, `/api/api-keys/${keyId}`)
      if (userId) await apiCleanup(page, `/api/users/${userId}`)
    }
  })

  test('the audit console renders its filter bar', async ({ page }) => {
    test.skip(!(await canWrite(page)), 'the audit route needs audit:read')
    await open(page, '/audit')
    await expect(page).toHaveURL(/\/audit/)
    await expect(page.locator('.toolbar').first()).toBeVisible()
    await expect(page.locator('.toolbar select, .toolbar input').first()).toBeVisible()
  })
})
