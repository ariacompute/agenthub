import path from 'node:path'
import { fileURLToPath } from 'node:url'

export const E2E_ROOT = path.dirname(fileURLToPath(import.meta.url))
export const REPO_ROOT = path.resolve(E2E_ROOT, '..')

const num = (value, fallback) => {
  const n = Number(value)
  return Number.isFinite(n) && n > 0 ? n : fallback
}

const flag = (value, fallback = false) => {
  if (value === undefined || value === null || value === '') return fallback
  return /^(1|true|yes|on)$/i.test(String(value))
}

/**
 * Everything the suites know about the outside world lives here. No service is
 * started: BASE_URL must already point at a running agenthub.
 */
export function readConfig() {
  const baseURL = (process.env.BASE_URL ||
    `http://127.0.0.1:${num(process.env.AGENTHUB_HTTP_PORT, 3500)}`).replace(/\/+$/, '')

  return {
    baseURL,
    email: process.env.AGENTHUB_TEST_EMAIL ||
      process.env.AGENTHUB_BOOTSTRAP_ADMIN_EMAIL || 'admin@example.com',
    password: process.env.AGENTHUB_TEST_PASSWORD ||
      process.env.AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD || 'agenthub-admin-pass',
    sessionToken: process.env.AGENTHUB_SESSION_TOKEN || '',
    apiKey: process.env.AGENTHUB_API_KEY || '',
    maxTimeMs: num(process.env.MAX_TIME, 20) * 1000,
    readyTimeoutMs: num(process.env.READY_TIMEOUT, 30) * 1000,
    lang: process.env.AGENTHUB_E2E_LANG || 'en',
    strictCoverage: flag(process.env.AGENTHUB_E2E_STRICT_COVERAGE, true),
    reportsDir: path.join(E2E_ROOT, 'reports'),
    authDir: path.join(E2E_ROOT, '.auth'),
    storageStatePath: path.join(E2E_ROOT, '.auth', 'state.json'),
    storageHeadersPath: path.join(E2E_ROOT, '.auth', 'headers.json'),
    docsPaths: [
      path.join(REPO_ROOT, 'docs', 'docs.md'),
      path.join(REPO_ROOT, 'docs', 'docs_cn.md'),
    ],
    // Opt-in switches. All off by default: anything that needs a third party,
    // a real filesystem restore, an outbound network call or real execution
    // stays out of a normal run.
    optin: {
      oauth: flag(process.env.AGENTHUB_E2E_OAUTH),
      backupRestore: flag(process.env.AGENTHUB_E2E_BACKUP_RESTORE) ||
        flag(process.env.AGENTHUB_TEST_BACKUP_RESTORE),
      s3: flag(process.env.AGENTHUB_E2E_S3),
      pushCallback: flag(process.env.AGENTHUB_E2E_PUSH_CALLBACK),
      webhook: flag(process.env.AGENTHUB_E2E_WEBHOOK),
      delivery: flag(process.env.AGENTHUB_E2E_DELIVERY),
      sandboxExec: flag(process.env.AGENTHUB_E2E_SANDBOX_EXEC),
      sessionLogout: flag(process.env.AGENTHUB_E2E_SESSION_LOGOUT),
    },
  }
}

/** The subset of the config that is safe to echo into a report. */
export function publicConfig(config) {
  return {
    baseURL: config.baseURL,
    email: config.email,
    authMode: config.apiKey ? 'api-key' : config.sessionToken ? 'session-token' : 'login',
    maxTimeMs: config.maxTimeMs,
    readyTimeoutMs: config.readyTimeoutMs,
    lang: config.lang,
    strictCoverage: config.strictCoverage,
    optin: { ...config.optin },
  }
}

/** Opt-in reason helper: returns false (run) or a skip reason (string). */
export function optinReason(config, key, description) {
  if (config.optin[key]) return false
  return `${description}: set AGENTHUB_E2E_${key.replace(/[A-Z]/g, (m) => '_' + m.toLowerCase()).toUpperCase()} to enable`
}
