import { test, expect } from '@playwright/test'
import { preparePage, open, canWrite } from '../helpers.js'

test.describe('journey: playground trial run', () => {
  test.beforeEach(async ({ page }) => {
    await preparePage(page)
  })

  test('run a task against a resource and read the result', async ({ page }) => {
    await open(page, '/playground')
    test.skip(!(await canWrite(page)), 'needs tasks:write (owner identity)')

    const resource = page.locator('#pg-resource')
    await expect(resource).toBeVisible()
    const first = resource.locator('option').first()
    await expect(first).toBeAttached()
    const value = await first.getAttribute('value')
    if (!value) test.skip(true, 'no discoverable resource to run against')

    await resource.selectOption(value)
    await page.locator('#pg-text').fill('e2e playground trial run')
    await expect(page.locator('#pg-text')).toHaveValue('e2e playground trial run')

    const run = page.locator('.drawer-actions button.btn-primary[type=submit]')
    await expect(run).toBeEnabled()
    await run.click()

    const result = page.locator('.card.result[aria-live="polite"]')
    await expect(result).toBeVisible({ timeout: 30000 })
    await expect(result.locator('.result-head .chip')).toBeVisible()
    await expect(result.locator('.result-text.mono')).toBeVisible()

    // When an adapter parks on an approval prompt, the resume form appears.
    const resume = result.locator('.resume')
    if (await resume.count()) {
      await resume.locator('textarea.mono').fill('approved')
      await afterResume(page, resume)
    }
  })
})

async function afterResume(page, resume) {
  const pending = page.waitForResponse(
    (response) => response.request().method() === 'POST' && response.url().includes('/a2a'),
    { timeout: 20000 },
  )
  await resume.locator('button.btn').click()
  const response = await pending
  expect(response.status()).toBe(200)
}
