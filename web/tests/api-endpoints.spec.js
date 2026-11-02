import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import * as api from '../src/lib/api.js'

// One case per endpoint wrapper. These functions are a map from a console action
// to a URL, so a typo is invisible until an operator clicks that one button on
// that one screen — and then it looks like the server broke. The table is the
// contract with handlers/listen.go, and a 404 in a spec means a route moved.

let calls = []

function ok(payload = {}) {
  return {
    ok: true,
    status: 200,
    headers: { get: () => null },
    text: async () => JSON.stringify(payload),
  }
}

beforeEach(() => {
  calls = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url, options = {}) => {
      calls.push({ url, options })
      return ok({ ok: true })
    }),
  )
  // `request` logs a parse failure to the console; a deliberate 500 body in a
  // test should not print a stack trace over the results.
  vi.spyOn(console, 'error').mockImplementation(() => {})
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function lastCall() {
  if (calls.length === 0) throw new Error('nothing was requested')
  return calls[calls.length - 1]
}

function jsonBody() {
  return JSON.parse(lastCall().options.body)
}

describe('resource and registry endpoints', () => {
  const cases = [
    ['healthz', () => api.healthz(), '/healthz', undefined],
    ['listResources', () => api.listResources(), '/api/resources', undefined],
    ['listResources filters', () => api.listResources({ kind: 'agent', namespace: 'team-a' }),
      '/api/resources?kind=agent&namespace=team-a', undefined],
    ['createResource', () => api.createResource({ name: 'a' }), '/api/resources', 'POST'],
    ['updateResource', () => api.updateResource('r 1', { name: 'a' }), '/api/resources/r%201', 'PUT'],
    ['deleteResource', () => api.deleteResource('r1'), '/api/resources/r1', 'DELETE'],
    ['setResourceHealth', () => api.setResourceHealth('r1', 'healthy'), '/api/resources/r1/health', 'POST'],
    ['probeResource', () => api.probeResource('r1'), '/api/resources/r1/probe', 'POST'],
    ['setResourceStatus', () => api.setResourceStatus('r1', 'approved', 'looks fine'),
      '/api/resources/r1/status', 'POST'],
    ['listApprovals', () => api.listApprovals(), '/api/registries/approvals', undefined],
    ['listRegistries', () => api.listRegistries(), '/api/registries', undefined],
    ['createRegistry', () => api.createRegistry({ namespace: 'team-a' }), '/api/registries', 'POST'],
    ['updateRegistry', () => api.updateRegistry('n1', { namespace: 'team-b' }), '/api/registries/n1', 'PUT'],
    ['deleteRegistry', () => api.deleteRegistry('n1'), '/api/registries/n1', 'DELETE'],
    ['listSources', () => api.listSources(), '/api/registry-sources', undefined],
    ['listSources scoped', () => api.listSources('team-a'), '/api/registry-sources?namespace=team-a', undefined],
    ['createSource', () => api.createSource({ url: 'u' }), '/api/registry-sources', 'POST'],
    ['updateSource', () => api.updateSource('s1', { url: 'u' }), '/api/registry-sources/s1', 'PUT'],
    ['deleteSource', () => api.deleteSource('s1'), '/api/registry-sources/s1', 'DELETE'],
    ['syncSource', () => api.syncSource('s1'), '/api/registry-sources/s1/sync', 'POST'],
    ['mcpExport', () => api.mcpExport(), '/api/export/mcp', undefined],
  ]

  for (const [name, call, url, method] of cases) {
    it(name, async () => {
      await call()
      expect(lastCall().url).toBe(url)
      expect(lastCall().options.method).toBe(method)
    })
  }

  it('sends the status without a reason when the operator typed none', async () => {
    await api.setResourceStatus('r1', 'approved')
    expect(jsonBody()).toEqual({ status: 'approved' })
  })
})

describe('task endpoints', () => {
  const cases = [
    ['listTasks', () => api.listTasks(), '/api/tasks', undefined],
    ['getTask', () => api.getTask('t1'), '/api/tasks/t1', undefined],
    ['getTaskSpans', () => api.getTaskSpans('t1'), '/api/tasks/t1/spans', undefined],
    ['cancelTask', () => api.cancelTask('t1'), '/api/tasks/t1/cancel', 'POST'],
    ['resumeTask', () => api.resumeTask('t1', 'go on'), '/api/tasks/t1/resume', 'POST'],
  ]

  for (const [name, call, url, method] of cases) {
    it(name, async () => {
      await call()
      expect(lastCall().url).toBe(url)
      expect(lastCall().options.method).toBe(method)
    })
  }

  it('resumes with a user message part', async () => {
    await api.resumeTask('t1', 'go on')
    expect(jsonBody()).toEqual({ message: { role: 'user', parts: [{ type: 'text', text: 'go on' }] } })
  })
})

describe('identity endpoints', () => {
  const cases = [
    ['authConfig', () => api.authConfig(), '/api/auth/config', undefined],
    ['register', () => api.register({ email: 'a@b.c' }), '/api/auth/register', 'POST'],
    ['login', () => api.login('a@b.c', 'pw'), '/api/auth/login', 'POST'],
    ['logout', () => api.logout(), '/api/auth/logout', 'POST'],
    ['me', () => api.me(), '/api/auth/me', undefined],
    ['listSessions', () => api.listSessions(), '/api/auth/sessions', undefined],
    ['revokeSession', () => api.revokeSession('s1'), '/api/auth/sessions/s1', 'DELETE'],
  ]

  for (const [name, call, url, method] of cases) {
    it(name, async () => {
      await call()
      expect(lastCall().url).toBe(url)
      expect(lastCall().options.method).toBe(method)
    })
  }

  it('logs in with the credentials in the body', async () => {
    await api.login('a@b.c', 'pw')
    expect(jsonBody()).toEqual({ email: 'a@b.c', password: 'pw' })
  })
})

describe('administration endpoints', () => {
  const cases = [
    ['listUsers', () => api.listUsers(), '/api/users', undefined],
    ['listUsers searched', () => api.listUsers('ada'), '/api/users?q=ada', undefined],
    ['getUser', () => api.getUser('u1'), '/api/users/u1', undefined],
    ['createUser', () => api.createUser({ email: 'a@b.c' }), '/api/users', 'POST'],
    ['updateUser', () => api.updateUser('u1', { name: 'A' }), '/api/users/u1', 'PUT'],
    ['deleteUser', () => api.deleteUser('u1'), '/api/users/u1', 'DELETE'],
    ['revokeUserSessions', () => api.revokeUserSessions('u1'), '/api/users/u1/sessions', 'DELETE'],
    ['listPermissions', () => api.listPermissions(), '/api/permissions', undefined],
    ['listRoles', () => api.listRoles(), '/api/roles', undefined],
    ['createRole', () => api.createRole({ name: 'r' }), '/api/roles', 'POST'],
    ['updateRole', () => api.updateRole('r1', { name: 'r' }), '/api/roles/r1', 'PUT'],
    ['deleteRole', () => api.deleteRole('r1'), '/api/roles/r1', 'DELETE'],
    ['listGroups', () => api.listGroups(), '/api/groups', undefined],
    ['createGroup', () => api.createGroup({ name: 'g' }), '/api/groups', 'POST'],
    ['updateGroup', () => api.updateGroup('g1', { name: 'g' }), '/api/groups/g1', 'PUT'],
    ['deleteGroup', () => api.deleteGroup('g1'), '/api/groups/g1', 'DELETE'],
    ['listGrants', () => api.listGrants(), '/api/grants', undefined],
    ['listGrants scoped', () => api.listGrants('res-1'), '/api/grants?resource=res-1', undefined],
    ['createGrant', () => api.createGrant({ resourceId: 'r' }), '/api/grants', 'POST'],
    ['deleteGrant', () => api.deleteGrant('gr1'), '/api/grants/gr1', 'DELETE'],
  ]

  for (const [name, call, url, method] of cases) {
    it(name, async () => {
      await call()
      expect(lastCall().url).toBe(url)
      expect(lastCall().options.method).toBe(method)
    })
  }
})

describe('checkpoint and api key endpoints', () => {
  const cases = [
    ['listBackups', () => api.listBackups(), '/api/backups', undefined],
    ['createBackup', () => api.createBackup('nightly'), '/api/backups', 'POST'],
    ['getBackupSchedule', () => api.getBackupSchedule(), '/api/backups/schedule', undefined],
    ['saveBackupSchedule', () => api.saveBackupSchedule({ mode: 'off' }), '/api/backups/schedule', 'PUT'],
    ['deleteBackup', () => api.deleteBackup('b1'), '/api/backups/b1', 'DELETE'],
    ['restoreBackup', () => api.restoreBackup('b1', 'b1'), '/api/backups/b1/restore', 'POST'],
    ['listAPIKeys', () => api.listAPIKeys(), '/api/api-keys', undefined],
    ['createAPIKey', () => api.createAPIKey({ name: 'ci' }), '/api/api-keys', 'POST'],
    ['updateAPIKey', () => api.updateAPIKey('k1', { name: 'ci' }), '/api/api-keys/k1', 'PUT'],
    ['revokeAPIKey', () => api.revokeAPIKey('k1'), '/api/api-keys/k1', 'DELETE'],
  ]

  for (const [name, call, url, method] of cases) {
    it(name, async () => {
      await call()
      expect(lastCall().url).toBe(url)
      expect(lastCall().options.method).toBe(method)
    })
  }

  it('echoes the checkpoint id back on restore', async () => {
    await api.restoreBackup('b1', 'b1')
    expect(jsonBody()).toEqual({ confirm: 'b1' })
  })
})

describe('audit endpoints', () => {
  it('sends no query string when no filter is set', async () => {
    await api.listAudit({ limit: 0 })
    expect(lastCall().url).toBe('/api/audit')
  })

  it('sends every filter the operator set', async () => {
    await api.listAudit({
      from: '2026-11-01T00:00:00Z',
      to: '2026-11-02T00:00:00Z',
      actor: 'u-1',
      action: 'tasks/get',
      resource: 'res-1',
      source: 'a2a',
      outcome: 'denied',
      q: 'nightly',
      before: 42,
      limit: 25,
    })
    const url = new URL(lastCall().url, 'http://x')
    expect(url.pathname).toBe('/api/audit')
    expect(url.searchParams.get('from')).toBe('2026-11-01T00:00:00Z')
    expect(url.searchParams.get('actor')).toBe('u-1')
    expect(url.searchParams.get('action')).toBe('tasks/get')
    expect(url.searchParams.get('before')).toBe('42')
    expect(url.searchParams.get('limit')).toBe('25')
    expect(url.searchParams.get('q')).toBe('nightly')
  })

  it('fetches one event by id, encoded', async () => {
    await api.getAudit('ev/1')
    expect(lastCall().url).toBe('/api/audit/ev%2F1')
  })

  it('verifies the whole trail or one window', async () => {
    await api.verifyAudit()
    expect(lastCall().url).toBe('/api/audit/verify')
    await api.verifyAudit({ from: '2026-11-01T00:00:00Z', to: '2026-11-02T00:00:00Z' })
    expect(lastCall().url).toBe('/api/audit/verify?from=2026-11-01T00%3A00%3A00Z&to=2026-11-02T00%3A00%3A00Z')
  })
})
