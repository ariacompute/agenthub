import { existsSync } from 'node:fs'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { createClient } from './client.js'
import { PreflightError } from './assertions.js'
import { readConfig } from '../e2e.config.js'

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

/** /healthz 200 or 503 both mean "the listener is up" (503 = database down). */
export async function waitForReady(config, client) {
  const deadline = Date.now() + config.readyTimeoutMs
  let last = ''
  while (Date.now() < deadline) {
    try {
      const res = await client.request('GET', '/healthz', { mode: 'anon', timeoutMs: 3000 })
      if (res.status === 200 || res.status === 503) return res
      last = `status ${res.status}`
    } catch (error) {
      last = String(error?.message || error)
    }
    await sleep(1000)
  }
  throw new PreflightError(
    `service never answered /healthz on ${config.baseURL} (${last}). ` +
      'Start it first: AGENTHUB_SEED_DEMO=true make up   or   make run',
  )
}

/**
 * One identity for the whole run. Token / API key are passed straight through;
 * otherwise we log in, and on a brand new instance register the first account
 * (which becomes the owner and therefore holds every permission).
 */
export async function acquireIdentity(config, client, reporter) {
  const mode = config.apiKey ? 'api-key' : config.sessionToken ? 'session-token' : 'login'
  if (mode === 'login') {
    const body = { email: config.email, password: config.password }
    let login = await client.request('POST', '/api/auth/login', { body, mode: 'anon' })
    if (login.status !== 200) {
      const registered = await client.request('POST', '/api/auth/register', {
        body: { ...body, name: 'e2e-smoke' }, mode: 'anon',
      })
      if (registered.status === 201) {
        reporter.note('registered a new account through self-registration')
        login = await client.request('POST', '/api/auth/login', { body, mode: 'anon' })
      }
    }
    if (login.status !== 200) {
      throw new PreflightError(`login failed (status ${login.status}, code ${login.code || 'none'}); check AGENTHUB_TEST_EMAIL / AGENTHUB_TEST_PASSWORD`)
    }
  } else {
    reporter.note(`using supplied ${mode}; login skipped`)
  }

  const me = await client.request('GET', '/api/auth/me')
  const permissions = Array.isArray(me.json?.permissions) ? me.json.permissions : []
  return { mode, user: me.json?.user || null, permissions, meStatus: me.status }
}

/** Playwright reads this file. Generated here so both suites share one login. */
export function buildStorageState(config, cookie, sessionToken, apiKey) {
  const origin = new URL(config.baseURL).origin
  const domain = new URL(config.baseURL).hostname
  const cookies = cookie
    ? [{
      name: 'agenthub_session',
      value: cookie.split('=', 2)[1] || '',
      domain, path: '/', expires: -1, httpOnly: true, secure: false, sameSite: 'Lax',
    }]
    : []
  const headers = {}
  if (apiKey) headers.Authorization = `Bearer ${apiKey}`
  else if (sessionToken) headers['X-AgentHub-Session'] = sessionToken
  return { origin, state: { cookies, origins: [] }, headers }
}

/**
 * globalSetup entry point: reuse a still-valid cookie if present, otherwise
 * log in once and write .auth/state.json + .auth/headers.json.
 */
export async function ensureStorageState(configOverride) {
  const config = configOverride || readConfig()
  const client = createClient(config)
  await waitForReady(config, client)

  if (existsSync(config.storageStatePath) && !config.apiKey && !config.sessionToken) {
    try {
      const saved = JSON.parse(await readFile(config.storageStatePath, 'utf8'))
      const value = saved?.cookies?.find((c) => c.name === 'agenthub_session')?.value
      if (value) {
        client.setCookie(`agenthub_session=${value}`)
        const me = await client.request('GET', '/api/auth/me')
        if (me.status === 200) return { reused: true }
      }
    } catch { /* fall through and log in again */ }
  }

  const { mode } = await acquireIdentity(config, client, { note() {} })
  const built = buildStorageState(config, client.getCookie(), config.sessionToken, config.apiKey)
  await mkdir(config.authDir, { recursive: true })
  await writeFile(config.storageStatePath, JSON.stringify(built.state, null, 2) + '\n', 'utf8')
  await writeFile(config.storageHeadersPath, JSON.stringify(built.headers, null, 2) + '\n', 'utf8')
  return { reused: false, mode }
}
