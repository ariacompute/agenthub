import { expect } from '@playwright/test'
import { uniqueId } from '../lib/cleanup.js'

/**
 * The console has no data-testid attributes and its copy is localised in 13
 * languages, so journeys use structural selectors (id / class / role / value)
 * and freeze the language to English. All of these were read out of the real
 * components under web/src/views and web/src/components.
 */
export const SEL = {
  loginCard: '.login-card.card',
  loginEmail: '#login-email',
  loginPassword: '#login-password',
  loginName: '#login-name',
  loginConfirm: '#login-confirm',
  loginSubmit: '.login-submit',
  loginSwitch: 'button.login-switch',
  loginTabs: '.tabs[role=tablist] button.tab[role=tab]',
  oauthEntry: '.login-oauth a.btn.btn-ghost[href^="/api/auth/oauth/"]',
  shell: '.shell',
  sidebar: '.sidebar',
  navItem: (href) => `.nav a.nav-item[href="${href}"]`,
  account: '.topbar .chip.account strong',
  logout: '.topbar button.btn.btn-ghost',
  toolbarPrimary: '.toolbar button.btn-primary',
  drawer: '.drawer-backdrop',
  drawerForm: '.drawer-backdrop form.card.drawer',
  drawerSubmit: '.drawer-actions button[type=submit].btn-primary',
  drawerCancel: '.drawer-actions button.btn-ghost',
  resourceCard: '.grid article.card.resource-card',
  resourceTitle: '.resource-title',
  cardActions: '.card-actions > button.btn-ghost',
  curateAction: '.card-actions.curate > button.btn.btn-ghost',
  stateDot: '.state-wrap .state-dot',
  adminTabs: '.toolbar .tabs[role=tablist] button.tab[role=tab]',
  adminRow: '.admin-table[role=table] > .admin-row[role=row]',
  errorLine: '.error-line',
  empty: '.empty',
}

/** Called in beforeEach, before any navigation. */
export async function preparePage(page, lang = 'en') {
  await page.addInitScript((value) => {
    try { localStorage.setItem('agenthub.lang', value) } catch { /* about:blank */ }
  }, lang)
  // RegistryView, RegistriesView and SourcesView use native window.confirm.
  page.on('dialog', (dialog) => dialog.accept())
}

export async function open(page, path) {
  await page.goto(path)
  await expect(page.locator(SEL.shell)).toBeVisible({ timeout: 20000 })
}

/** Pair a click with the write request it triggers (the app polls every 5s). */
export async function afterWrite(page, method, urlPart, action, timeout = 20000) {
  const pending = page.waitForResponse(
    (response) => response.request().method() === method && response.url().includes(urlPart),
    { timeout },
  )
  await action()
  return pending
}

export async function seedTask(page, resourceId, text = 'e2e ui task') {
  const response = await page.request.post('/a2a', {
    data: {
      jsonrpc: '2.0', id: Date.now(), method: 'message/send',
      params: {
        message: { role: 'user', parts: [{ type: 'text', text }] },
        metadata: { resourceId },
      },
    },
  })
  return (await response.json())?.result?.id || null
}

/** A read-only identity cannot drive the write journeys; skip instead of failing. */
export async function canWrite(page) {
  try {
    const response = await page.request.get('/api/auth/me')
    if (!response.ok()) return false
    const body = await response.json()
    return body.user?.isOwner === true || (body.permissions || []).includes('resources:write')
  } catch { return false }
}

export async function firstResourceId(page) {
  const response = await page.request.get('/api/resources')
  const body = await response.json()
  const entry = (body.resources || []).find((e) => e.status === 'approved' || !e.status) ||
    (body.resources || [])[0]
  return entry?.id || null
}

/** Idempotent: a 404 means the journey already removed the object. */
export async function apiCleanup(page, path) {
  try {
    await page.request.delete(path)
  } catch { /* never mask the test result */ }
}

export const unique = (prefix) => uniqueId(prefix)
