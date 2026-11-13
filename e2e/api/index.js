import { readConfig } from '../e2e.config.js'
import { createClient } from '../lib/client.js'
import { acquireIdentity, waitForReady } from '../lib/auth.js'
import { createCleanup } from '../lib/cleanup.js'
import { createReporter } from '../lib/report.js'
import { checkCoverage, parseDocsEndpoints, printCoverage } from '../lib/coverage.js'
import { PreflightError, SkipError } from '../lib/assertions.js'
import { runCases } from '../lib/runner.js'
import { ALL_CASES } from './cases/index.js'

const coverageOnly = process.argv.includes('--coverage-only')

async function main() {
  const config = readConfig()
  const reporter = createReporter(config, 'api')
  process.stdout.write(`agenthub E2E (API) against ${config.baseURL}\n`)

  // 1. Static guard: every documented route must have a declared case.
  const docs = parseDocsEndpoints(config.docsPaths)
  const coverage = checkCoverage(ALL_CASES, docs)
  printCoverage(coverage)
  if (coverageOnly) {
    return coverage.missing.length || coverage.extra.length ? 1 : 0
  }
  if (!docs.size) {
    process.stdout.write('  !!    no endpoint table found under docs/; coverage guard is blind\n')
  }

  const client = createClient(config)
  const cleanup = createCleanup()
  let summary
  try {
    // 2. Ready check, one identity for the whole run.
    const health = await waitForReady(config, client)
    const identity = await acquireIdentity(config, client, reporter)

    // A full run writes to every domain. Self-registration on an instance that
    // already has users only yields role-viewer, which would produce dozens of
    // misleading 403 failures instead of one actionable message.
    if (identity.user?.isOwner !== true) {
      const required = [
        'resources:write', 'tasks:write', 'users:manage', 'registries:manage',
        'backups:manage', 'audit:read',
      ]
      const missing = required.filter((code) => !identity.permissions.includes(code))
      if (missing.length) {
        throw new PreflightError(
          `the run identity is read-only (roles: ${(identity.user?.roles || []).join(', ') || 'none'}); ` +
          `missing ${missing.join(', ')}. A full E2E run needs owner-equivalent scopes.\n` +
          '  set AGENTHUB_TEST_EMAIL / AGENTHUB_TEST_PASSWORD to the bootstrap administrator,\n' +
          '  or AGENTHUB_API_KEY=ahk_… with all scopes. On an instance that already has\n' +
          '  users, self-registration only grants role-viewer.',
        )
      }
    }

    if (config.apiKey && identity.user?.isOwner !== true) {
      // Scopes open a route; resource grants are a second gate. A key with every
      // scope is still not the owner, so prove that a record it just created can
      // be written back before 200 cases all report the same 404.
      const body = {
        kind: 'model', name: 'e2e preflight resource', version: '1.0.0',
        health: 'healthy', status: 'approved', visibility: 'public',
        namespace: 'default', kind_config: { model: { provider_mode: 'mock' } },
      }
      const probe = await client.request('POST', '/api/resources', { mode: 'session', body })
      const id = probe.json?.id
      if (id) {
        cleanup.add(`preflight resource ${id}`, () => client.request('DELETE', `/api/resources/${id}`, { mode: 'session' }))
      }
      const write = id
        ? await client.request('PUT', `/api/resources/${id}`, { mode: 'session', body: { ...body, name: 'e2e preflight resource (write)' } })
        : { status: probe.status }
      if (!id || write.status !== 200) {
        throw new PreflightError(
          `the run identity can register a resource but cannot write it back ` +
          `(create=${probe.status}, update=${write.status}).\n` +
          '  A non-owner principal needs a write grant on the records it creates;\n' +
          '  check resource_grants, or run the suite with the bootstrap owner session.',
        )
      }
    }

    const ctx = {
      config,
      client,
      reporter,
      health,
      identity,
      state: { demoResources: [] },
      api: (method, path, opts = {}) => client.request(method, path, { mode: 'session', ...opts }),
      anon: (method, path, opts = {}) => client.request(method, path, { mode: 'anon', ...opts }),
      cookie: (method, path, opts = {}) => client.request(method, path, { mode: 'cookie', ...opts }),
      key: (method, path, opts = {}) => {
        if (!config.apiKey) return Promise.reject(new SkipError('AGENTHUB_API_KEY was not supplied'))
        return client.request(method, path, { ...opts, mode: 'key' })
      },
      rpc: (id, method, params, opts = {}) => client.request('POST', '/a2a', {
        ...opts,
        mode: opts.mode || 'session',
        body: { jsonrpc: '2.0', id, method, params },
      }),
      stream: (method, path, opts = {}) => client.collectStream(method, path, { ...opts, mode: opts.mode || 'session' }),
      cleanup: (label, fn) => cleanup.add(label, fn),
    }

    // Cache the catalogue once: cases pick demo-* resources or create their own.
    const catalogue = await ctx.api('GET', '/api/resources')
    ctx.state.demoResources = Array.isArray(catalogue.json?.resources) ? catalogue.json.resources : []
    if (!ctx.state.demoResources.some((e) => String(e.id).startsWith('demo-'))) {
      reporter.note('no demo-* resources on this instance; cases create their own equivalents')
    }

    // 3. Sequential execution, cleanup in finally.
    await runCases({ cases: ALL_CASES, ctx, reporter })
  } catch (error) {
    if (error instanceof PreflightError) {
      process.stdout.write(`\n${error.message}\n`)
      return 2
    }
    reporter.group('harness')
    reporter.fail('harness', error, 0)
  } finally {
    const failures = await cleanup.runAll((label, error) => reporter.cleanupFailure(label, error))
    if (failures.length) reporter.note(`${failures.length} cleanup warning(s); see report`)
  }

  summary = reporter.printSummary()
  const written = await reporter.write()
  process.stdout.write(`  report: ${written.json}\n  report: ${written.markdown}\n`)

  const coverageFailed = config.strictCoverage && (coverage.missing.length > 0 || coverage.extra.length > 0)
  if (coverageFailed) {
    process.stdout.write('  endpoint coverage guard FAILED (set AGENTHUB_E2E_STRICT_COVERAGE=0 to bypass)\n')
  }
  process.stdout.write(summary.fail > 0 || coverageFailed || summary.cleanupFailures.length > 0
    ? 'agenthub E2E (API) FAILED\n'
    : 'agenthub E2E (API) passed\n')
  return summary.fail > 0 || coverageFailed || summary.cleanupFailures.length > 0 ? 1 : 0
}

main().then((code) => { process.exitCode = code }).catch((error) => {
  process.stderr.write(`${error?.stack || error}\n`)
  process.exitCode = 1
})
