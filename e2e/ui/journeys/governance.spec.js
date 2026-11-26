import { test, expect } from '@playwright/test'
import {
  preparePage, open, afterWrite, unique, apiCleanup, canWrite, canPermission,
} from '../helpers.js'

test.describe('journey: governance approvals', () => {
  test.beforeEach(async ({ page }) => {
    await preparePage(page)
  })

  test('create a curated namespace and approve a pending record', async ({ page }) => {
    await open(page, '/registries')
    test.skip(!(await canWrite(page)), 'needs registry:curate (owner identity)')

    const nsId = unique('e2e-ui-ns')
    let resourceId = null
    try {
      await page.locator('.toolbar button.btn-primary').click()
      await expect(page.locator('#n-id')).toBeVisible()
      await page.locator('#n-id').fill(nsId)
      await page.locator('#n-name').fill('E2E UI Namespace')

      // Turn auto-approve off so the queue is meaningful.
      const autoApprove = page.locator('.drawer-backdrop label.check input[type=checkbox]')
      if (await autoApprove.isChecked()) await autoApprove.uncheck()

      const created = await afterWrite(page, 'POST', '/api/registries', () =>
        page.locator('.drawer-backdrop .drawer-actions button[type=submit]').click())
      expect(created.status()).toBe(201)
      await expect(page.locator('.grid article.card').filter({ hasText: 'E2E UI Namespace' })).toBeVisible()

      // Seed a pending record through the same session so the queue has a row.
      const record = await page.request.post('/api/resources', {
        data: {
          kind: 'skill', name: unique('e2e-ui-pending'), health: 'healthy',
          status: 'draft', visibility: 'public', namespace: nsId,
        },
      })
      expect(record.status()).toBe(201)
      resourceId = (await record.json())?.id
      const pending = await page.request.post(`/api/resources/${resourceId}/status`, {
        data: { status: 'pending' },
      })
      expect(pending.ok()).toBeTruthy()

      await page.reload()
      await expect(page.locator('.queue article.card.queue-row').first()).toBeVisible({ timeout: 20000 })
      const approved = await afterWrite(page, 'POST', '/api/resources/', () =>
        page.locator('.queue-row .card-actions button.btn-primary').first().click())
      expect(approved.status()).toBe(200)
    } finally {
      if (resourceId) await apiCleanup(page, `/api/resources/${resourceId}`)
      await apiCleanup(page, `/api/registries/${nsId}`)
    }
  })

  test('register an upstream registry from the console and remove it', async ({ page }) => {
    await page.goto('/sources')
    await expect(page.locator('.shell')).toBeVisible()
    if (!(await canPermission(page, 'sources:manage'))) {
      // A viewer lacks sources:manage, so the router sends it back to the
      // registry. Assert the guard rather than failing on a missing panel: the
      // write half below is skipped, not faked.
      test.skip(true, 'the sources route needs sources:manage')
    }
    await expect(page).toHaveURL(/\/sources/)

    const name = unique('e2e-ui-source')
    let sourceId = null
    try {
      await page.locator('.toolbar button.btn-primary').click()
      await expect(page.locator('#s-name')).toBeVisible()
      await page.locator('#s-name').fill(name)
      // Fixture mode is the default and is never synced here: a sync imports
      // records into the catalogue, which the console cannot remove again.
      await page.locator('#s-url').fill('https://registry.example.com/v1')

      const created = await afterWrite(page, 'POST', '/api/registry-sources', () =>
        page.locator('.drawer-backdrop .drawer-actions button[type=submit]').click())
      expect(created.status()).toBe(201)
      sourceId = (await created.json())?.id ?? null

      // The drawer closes on success and the saved source is a card of its own.
      await expect(page.locator('.drawer-backdrop')).toHaveCount(0)
      const card = page.locator('.grid article.card').filter({ hasText: name })
      await expect(card).toBeVisible()
      await expect(card.locator('.resource-endpoint.mono')).toHaveText('https://registry.example.com/v1')
      await expect(card.locator('.card-actions button.btn')).toHaveCount(2)

      // Deleting goes through the console's own confirm (accepted by
      // preparePage) and has to take the card with it.
      const removed = await afterWrite(page, 'DELETE', '/api/registry-sources/', () =>
        card.locator('.card-actions button.btn-ghost').click())
      expect(removed.status()).toBe(204)
      await expect(card).toHaveCount(0)
      sourceId = null
    } finally {
      if (sourceId) await apiCleanup(page, `/api/registry-sources/${sourceId}`)
    }
  })
})
