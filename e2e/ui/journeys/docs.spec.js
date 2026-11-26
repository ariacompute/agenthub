import { test, expect } from '@playwright/test'
import { preparePage, open } from '../helpers.js'

test.describe('journey: documentation', () => {
  test.beforeEach(async ({ page }) => {
    await preparePage(page)
  })

  test('the index jumps between sections and an endpoint row discloses its samples', async ({ page }) => {
    // The screen is deliberately ungated: anyone with a session may read what the
    // console does, so no permission check stands between the journey and it.
    await open(page, '/docs')

    const index = page.locator('.docs-index .index-item')
    await expect(index).toHaveCount(6)
    await expect(index.first()).toHaveClass(/active/)

    // "version@commit" is the same stamp the sidebar foot shows: the reader has
    // to be able to tell which build these endpoints belong to.
    await expect(page.locator('.docs-title .chip.mono')).not.toBeEmpty()

    // Jumping from the index lands on the section and the scroll spy follows, so
    // the reader's place is marked without a second click.
    await index.nth(2).click()
    await expect(page.locator('#sec-http')).toBeVisible()
    await expect(index.nth(2)).toHaveClass(/active/)

    // The endpoint table is the reason this screen exists. Each row is a
    // disclosure: the permission column is derived, and the request/response pair
    // is the machine data the API coverage guard also reads.
    const rows = page.locator('.docs-table.endpoints tr.endpoint-row')
    await expect(rows.first()).toBeVisible()
    await expect(rows.first().locator('.verb.mono')).not.toBeEmpty()

    await rows.first().click()
    const detail = page.locator('tr.endpoint-detail')
    await expect(detail).toHaveCount(1)
    await expect(detail.locator('pre.code').first()).toBeVisible()

    // The same row collapses again; nothing else on the page moves with it.
    await rows.first().click()
    await expect(detail).toHaveCount(0)
  })
})
