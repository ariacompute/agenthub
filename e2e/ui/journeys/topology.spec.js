import { test, expect } from '@playwright/test'
import { preparePage, open } from '../helpers.js'

test.describe('journey: topology', () => {
  test.beforeEach(async ({ page }) => {
    await preparePage(page)
  })

  test('every kind gets a lane, a node opens its detail and the edge window widens', async ({ page }) => {
    await open(page, '/topology')

    // One lane per resource kind, in the order the console knows them. The lanes
    // are drawn from KINDS, not from the catalogue, so they exist even when one
    // kind has nothing registered.
    const lanes = page.locator('.lanes .card.lane')
    await expect(lanes).toHaveCount(6)
    await expect(lanes.first().locator('.lane-title')).toBeVisible()

    // The view loads the whole catalogue on mount: a node or a lane's empty line
    // is what says the load finished.
    await expect(page.locator('.lanes .node, .lanes .lane .muted').first()).toBeVisible({ timeout: 20000 })
    const nodes = page.locator('.lanes .node')
    if (!(await nodes.count())) test.skip(true, 'the catalogue is empty: nothing to draw')

    // The selection is shared with the registry and the playground, so the node
    // and the detail card have to agree on it.
    await nodes.first().click()
    await expect(nodes.first()).toHaveClass(/selected/)
    await expect(page.locator('.card.panel .tag-row')).toBeVisible()
    await expect(page.locator('.card.panel .tag-row .chip').first()).not.toBeEmpty()

    // Edges are derived from task spans; the legend explains all three states
    // whether or not any task has run yet.
    await expect(page.locator('.card.panel.edges')).toBeVisible()
    await expect(page.locator('.card.panel.edges .legend .legend-item')).toHaveCount(4)

    // The window switch is display only: "all" stops the ten edge cut without
    // touching the lanes or the selection.
    const tabs = page.locator('.toolbar .tabs button.tab')
    await expect(tabs).toHaveCount(2)
    await tabs.nth(1).click()
    await expect(tabs.nth(1)).toHaveClass(/active/)
    await expect(lanes).toHaveCount(6)
    await expect(nodes.first()).toHaveClass(/selected/)
  })
})
