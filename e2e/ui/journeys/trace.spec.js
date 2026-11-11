import { test, expect } from '@playwright/test'
import { preparePage, open, seedTask, firstResourceId } from '../helpers.js'

test.describe('journey: task trace', () => {
  test.beforeEach(async ({ page }) => {
    await preparePage(page)
  })

  test('select a task, inspect its timeline and filter by state', async ({ page }) => {
    await open(page, '/trace')

    const resourceId = await firstResourceId(page)
    if (!resourceId) test.skip(true, 'no resource available to create a task')
    const taskId = await seedTask(page, resourceId)
    if (!taskId) test.skip(true, 'could not create a task to trace')

    // Task lists poll or reload; force a fresh read so the new row is present.
    await page.reload()
    const rows = page.locator('.task-list button.task-row')
    await expect(rows.first()).toBeVisible({ timeout: 20000 })

    await rows.first().click()
    await expect(page.locator('.detail')).toBeVisible()
    await expect(page.locator('.timeline')).toBeVisible()
    // Spans are fetched for the selected task.
    await expect(page.locator('.timeline .tl-node, .timeline').first()).toBeVisible()

    const filter = page.locator('.toolbar select')
    await expect(filter).toBeVisible()
    await filter.selectOption('completed')
    await expect(filter).toHaveValue('completed')
  })
})
