import { test, expect } from '@playwright/test'
import {
  SEL, preparePage, open, afterWrite, unique, apiCleanup, canWrite, canPermission,
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
      // The server rejects a key with an empty scope set, and the drawer starts
      // with none ticked: pick one through the real scope matrix before saving.
      const scope = page.locator('.scope-matrix input[type=checkbox]').first()
      await expect(scope).toBeVisible()
      await scope.check()
      const createdKey = await afterWrite(page, 'POST', '/api/api-keys', () =>
        page.locator('form.drawer-form button.btn-primary[type=submit]').click())
      expect(createdKey.status()).toBe(201)
      const key = (await createdKey.json())?.apiKey
      keyId = key?.id
      expect(key?.scopes?.length).toBeGreaterThan(0)
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
    test.skip(!(await canPermission(page, 'audit:read')), 'the audit route needs audit:read')
    await open(page, '/audit')
    await expect(page).toHaveURL(/\/audit/)
    // AuditView has its own filter rail (.audit-filters), not the shared
    // .toolbar used by the other screens: time presets, selects, search, chain.
    const filters = page.locator('.audit-filters')
    await expect(filters).toBeVisible()
    await expect(filters.locator('select').first()).toBeVisible()
    await expect(filters.locator('#audit-search')).toBeVisible()
  })
})
