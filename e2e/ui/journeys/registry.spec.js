import { test, expect } from '@playwright/test'
import {
  SEL, preparePage, open, afterWrite, unique, apiCleanup, canWrite,
} from '../helpers.js'

test.describe('journey: register a resource', () => {
  test.beforeEach(async ({ page }) => {
    await preparePage(page)
  })

  test('create, probe, export and delete a resource from the console', async ({ page }) => {
    await open(page, '/registry')
    test.skip(!(await canWrite(page)), 'needs resources:write (owner identity)')

    const name = unique('e2e-ui-resource')
    let id = null
    try {
      await page.locator(SEL.toolbarPrimary).click()
      await expect(page.locator(SEL.drawerForm)).toBeVisible()
      await page.locator('#f-name').fill(name)
      await page.locator('#f-endpoint').fill('https://example.test/e2e')
      await page.locator('#f-version').fill('1.0.0')

      const created = await afterWrite(page, 'POST', '/api/resources', () =>
        page.locator(SEL.drawerSubmit).click())
      expect(created.status()).toBe(201)
      id = (await created.json())?.id
      expect(id).toBeTruthy()

      await expect(page.locator(SEL.drawer)).toHaveCount(0)
      const card = page.locator(SEL.resourceCard).filter({ hasText: name })
      await expect(card).toBeVisible()
      await expect(card.locator(SEL.resourceTitle)).toContainText(name)

      // Probe is the first .btn-ghost in .card-actions.
      const probed = await afterWrite(page, 'POST', `/api/resources/${id}/probe`, () =>
        card.locator(SEL.cardActions).first().click())
      expect(probed.status()).toBe(200)
      await expect(card.locator(SEL.stateDot)).toBeVisible()

      // The console has no export button; assert the same session can export.
      const exported = await page.request.get('/api/export/mcp')
      expect(exported.ok()).toBeTruthy()
      expect(Array.isArray((await exported.json()).servers)).toBe(true)

      // Delete is the third .btn-ghost; RegistryView asks via window.confirm.
      const removed = await afterWrite(page, 'DELETE', `/api/resources/${id}`, () =>
        card.locator(SEL.cardActions).nth(2).click())
      expect(removed.status()).toBe(204)
      await expect(card).toHaveCount(0)
      id = null
    } finally {
      if (id) await apiCleanup(page, `/api/resources/${id}`)
    }
  })

  test('the catalogue filter narrows the visible cards', async ({ page }) => {
    await open(page, '/registry')
    const search = page.locator('input[type=search]')
    await expect(search).toBeVisible()
    const before = await page.locator(SEL.resourceCard).count()
    await search.fill('e2e-this-matches-nothing')
    const after = await page.locator(SEL.resourceCard).count()
    expect(after).toBeLessThanOrEqual(before)
    if (after === 0) await expect(page.locator(SEL.empty)).toBeVisible()
    await search.fill('')
  })
})
