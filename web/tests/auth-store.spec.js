import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

// Mock the API module with stable, per-test configurable fns.
const apiMock = vi.hoisted(() => {
  const make = () => vi.fn().mockResolvedValue({})
  return {
    authConfig: make(),
    me: make(),
    login: make(),
    register: make(),
    logout: make(),
    listSessions: make(),
    revokeSession: make(),
    listUsers: make(),
    createUser: make(),
    updateUser: make(),
    deleteUser: make(),
    revokeUserSessions: make(),
    listRoles: make(),
    createRole: make(),
    updateRole: make(),
    deleteRole: make(),
    listGroups: make(),
    createGroup: make(),
    updateGroup: make(),
    deleteGroup: make(),
    listPermissions: make(),
    listGrants: make(),
    createGrant: make(),
    deleteGrant: make(),
    listAPIKeys: make(),
    createAPIKey: make(),
    updateAPIKey: make(),
    revokeAPIKey: make(),
  }
})

vi.mock('../src/lib/api.js', () => apiMock)

import * as api from '../src/lib/api.js'
import { PERMISSIONS, useAuthStore } from '../src/lib/auth-store.js'

beforeEach(() => {
  setActivePinia(createPinia())
  // Reset call history and restore the default no-op resolution for every fn.
  for (const fn of Object.values(apiMock)) fn.mockReset().mockResolvedValue({})
})

describe('auth-store permission vocabulary', () => {
  it('exposes the 14 permissions the matrix renders', () => {
    expect(PERMISSIONS).toHaveLength(14)
    for (const p of [
      'resources:read',
      'tasks:read',
      'tasks:write',
      'users:manage',
      'backups:manage',
      'registry:curate',
      // Added with the audit table: the admin matrix renders this list, so a
      // permission missing here cannot be granted from the console at all.
      'audit:read',
    ]) {
      expect(PERMISSIONS).toContain(p)
    }
  })

  it('matches the backend vocabulary exactly', async () => {
    // The console and authz.AllPermissions drift silently: a code the backend
    // knows but the console omits becomes un-grantable from the matrix, and one
    // the console invents is never honoured. This pins the set, not the count.
    const { readFileSync } = await import('node:fs')
    const source = readFileSync(new URL('../../internal/authz/authz.go', import.meta.url), 'utf8')

    const values = new Map()
    for (const m of source.matchAll(/(Perm[A-Za-z]+)\s*=\s*"([^"]+)"/g)) values.set(m[1], m[2])

    const block = /AllPermissions = \[\]string\{([\s\S]*?)\n\}/.exec(source)
    expect(block, 'AllPermissions moved; update this guard').toBeTruthy()
    const names = [...block[1].matchAll(/\b(Perm[A-Za-z]+)\b/g)].map((m) => m[1])
    expect(names.length).toBeGreaterThan(0)

    const backend = names.map((name) => {
      expect(values.has(name), `${name} has no literal in authz.go`).toBe(true)
      return values.get(name)
    })
    expect([...PERMISSIONS].sort()).toEqual([...backend].sort())
  })
})

describe('identity loading', () => {
  it('loads the user and permissions on success', async () => {
    api.me.mockResolvedValue({
      user: { id: 'u1', isOwner: true, roles: ['admin'], groups: ['eng'] },
      permissions: ['tasks:read'],
    })
    const store = useAuthStore()
    const ok = await store.loadMe()
    expect(ok).toBe(true)
    expect(store.user.id).toBe('u1')
    expect(store.isOwner).toBe(true)
    expect(store.roles).toEqual(['admin'])
    expect(store.signedIn).toBe(true)
  })

  it('clears identity and records the code on failure', async () => {
    api.me.mockRejectedValue({ code: 'unauthorized' })
    const store = useAuthStore()
    const ok = await store.loadMe()
    expect(ok).toBe(false)
    expect(store.user).toBeNull()
    expect(store.permissions).toEqual([])
    expect(store.errorCode).toBe('unauthorized')
  })
})

describe('can() permission checks', () => {
  it('grants the owner every known permission', async () => {
    api.me.mockResolvedValue({ user: { id: 'u1', isOwner: true }, permissions: [] })
    const store = useAuthStore()
    await store.loadMe()
    expect(store.can('tasks:read')).toBe(true)
    expect(store.can('users:manage')).toBe(true)
    expect(store.can('bogus:perm')).toBe(false)
  })

  it('limits a non-owner to its granted permissions', async () => {
    api.me.mockResolvedValue({ user: { id: 'u1', isOwner: false }, permissions: ['tasks:read'] })
    const store = useAuthStore()
    await store.loadMe()
    expect(store.can('tasks:read')).toBe(true)
    expect(store.can('users:manage')).toBe(false)
  })
})

describe('login and logout', () => {
  it('logs in then reloads the user', async () => {
    api.login.mockResolvedValue({})
    api.me.mockResolvedValue({ user: { id: 'u2', isOwner: false }, permissions: ['tasks:read'] })
    const store = useAuthStore()
    expect(await store.login('a@b.c', 'pw')).toBe(true)
    expect(store.signedIn).toBe(true)
    expect(store.errorCode).toBe('')
  })

  it('records the code and returns false on login failure', async () => {
    api.login.mockRejectedValue({ code: 'invalid_credentials' })
    const store = useAuthStore()
    expect(await store.login('a@b.c', 'bad')).toBe(false)
    expect(store.errorCode).toBe('invalid_credentials')
    expect(store.signedIn).toBe(false)
  })

  it('clears identity on logout', async () => {
    api.me.mockResolvedValue({ user: { id: 'u1', isOwner: false }, permissions: ['tasks:read'] })
    const store = useAuthStore()
    await store.loadMe()
    api.logout.mockResolvedValue({})
    await store.logout()
    expect(store.user).toBeNull()
    expect(store.permissions).toEqual([])
    expect(store.sessions).toEqual([])
    expect(store.apiKeys).toEqual([])
  })
})

describe('API key lifecycle', () => {
  it('prepends a created key', async () => {
    api.createAPIKey.mockResolvedValue({ apiKey: { id: 'k1', name: 'ci' } })
    const store = useAuthStore()
    const res = await store.createAPIKey({ name: 'ci' })
    expect(res.apiKey.id).toBe('k1')
    expect(store.apiKeys[0].id).toBe('k1')
  })

  it('filters a revoked key out of the list', async () => {
    const store = useAuthStore()
    store.apiKeys = [{ id: 'k1' }, { id: 'k2' }]
    api.revokeAPIKey.mockResolvedValue({})
    expect(await store.revokeAPIKey('k1')).toBe(true)
    expect(store.apiKeys.map((k) => k.id)).toEqual(['k2'])
  })

  it('returns null and records the code when key creation fails', async () => {
    api.createAPIKey.mockRejectedValue({ code: 'api_key_name_required' })
    const store = useAuthStore()
    expect(await store.createAPIKey({})).toBeNull()
    expect(store.errorCode).toBe('api_key_name_required')
  })
})

describe('admin catalogue actions', () => {
  it('loads users and records errors', async () => {
    api.listUsers.mockResolvedValue({ users: [{ id: 'u1' }] })
    const store = useAuthStore()
    await store.loadUsers('q')
    expect(store.users).toEqual([{ id: 'u1' }])

    api.listUsers.mockRejectedValue({ code: 'boom' })
    await store.loadUsers()
    expect(store.errorCode).toBe('boom')
  })

  it('creates a user then refreshes the list', async () => {
    const store = useAuthStore()
    api.createUser.mockResolvedValue({})
    api.listUsers.mockResolvedValue({ users: [{ id: 'u9' }] })
    expect(await store.createUser({ email: 'x@y.z' })).toBe(true)
    expect(store.users).toEqual([{ id: 'u9' }])
  })

  it('deletes a user and refreshes', async () => {
    const store = useAuthStore()
    api.deleteUser.mockResolvedValue({})
    api.listUsers.mockResolvedValue({ users: [] })
    expect(await store.deleteUser('u9')).toBe(true)
    expect(api.deleteUser).toHaveBeenCalledWith('u9')
  })

  it('loads roles, groups, permissions and grants', async () => {
    const store = useAuthStore()
    api.listRoles.mockResolvedValue({ roles: [{ id: 'r1' }] })
    api.listGroups.mockResolvedValue({ groups: [{ id: 'g1' }] })
    api.listPermissions.mockResolvedValue({ permissions: [{ code: 'tasks:read' }] })
    api.listGrants.mockResolvedValue({ grants: [{ id: 'gr1' }] })
    await store.loadRoles()
    await store.loadGroups()
    await store.loadPermissions()
    await store.loadGrants()
    expect(store.rolesCatalogue).toEqual([{ id: 'r1' }])
    expect(store.groupsCatalogue).toEqual([{ id: 'g1' }])
    expect(store.permissionsCatalogue).toEqual([{ code: 'tasks:read' }])
    expect(store.grants).toEqual([{ id: 'gr1' }])
  })

  it('clears the error code', async () => {
    const store = useAuthStore()
    store.errorCode = 'boom'
    store.clearError()
    expect(store.errorCode).toBe('')
  })
})

// The administration actions are the same shape repeated: call the endpoint,
// reload the list, report success or keep the machine code. Covering them as a
// table keeps the shapes provably identical instead of twelve hand-written
// cases that drift apart.
const MUTATIONS = [
  ['createUser', [], 'createUser', 'listUsers'],
  ['updateUser', [], 'updateUser', 'listUsers'],
  ['deleteUser', [], 'deleteUser', 'listUsers'],
  ['createRole', [], 'createRole', 'listRoles'],
  ['updateRole', [], 'updateRole', 'listRoles'],
  ['deleteRole', [], 'deleteRole', 'listRoles'],
  ['createGroup', [], 'createGroup', 'listGroups'],
  ['updateGroup', [], 'updateGroup', 'listGroups'],
  ['deleteGroup', [], 'deleteGroup', 'listGroups'],
  ['createGrant', [], 'createGrant', 'listGrants'],
  ['deleteGrant', [], 'deleteGrant', 'listGrants'],
]

describe('administration mutations', () => {
  for (const [action, , endpoint, reload] of MUTATIONS) {
    it(`${action} succeeds and reloads through ${reload}`, async () => {
      const store = useAuthStore()
      const args = action === 'deleteUser' || action === 'deleteRole' || action === 'deleteGroup'
        ? ['x1']
        : action === 'deleteGrant'
          ? ['gr1']
          : [{ name: 'x' }]
      if (action.startsWith('update')) args.unshift('x1')

      expect(await store[action](...args)).toBe(true)
      expect(api[endpoint]).toHaveBeenCalledWith(...args)
      expect(api[reload]).toHaveBeenCalled()
      expect(store.errorCode).toBe('')
    })

    it(`${action} keeps the code when the server refuses`, async () => {
      const store = useAuthStore()
      api[endpoint].mockRejectedValue({ code: 'forbidden' })
      expect(await store[action]('x1')).toBe(false)
      expect(store.errorCode).toBe('forbidden')
      expect(api[reload]).not.toHaveBeenCalled()
    })
  }
})

describe('list loaders', () => {
  const LOADERS = [
    ['loadSessions', 'listSessions', 'sessions', 'sessions'],
    ['loadUsers', 'listUsers', 'users', 'users'],
    ['loadRoles', 'listRoles', 'roles', 'rolesCatalogue'],
    ['loadGroups', 'listGroups', 'groups', 'groupsCatalogue'],
    ['loadPermissions', 'listPermissions', 'permissions', 'permissionsCatalogue'],
    ['loadGrants', 'listGrants', 'grants', 'grants'],
    ['loadAPIKeys', 'listAPIKeys', 'apiKeys', 'apiKeys'],
  ]

  for (const [action, endpoint, responseKey, stateKey] of LOADERS) {
    it(`${action} stores what the endpoint returned`, async () => {
      const store = useAuthStore()
      api[endpoint].mockResolvedValue({ [responseKey]: [{ id: 'x1' }] })
      await store[action]()
      expect(store[stateKey]).toEqual([{ id: 'x1' }])
      expect(store.errorCode).toBe('')
    })

    it(`${action} keeps the code and leaves the list alone`, async () => {
      const store = useAuthStore()
      store[stateKey] = [{ id: 'old' }]
      api[endpoint].mockRejectedValue({ code: 'unavailable' })
      await store[action]()
      expect(store.errorCode).toBe('unavailable')
      expect(store[stateKey]).toEqual([{ id: 'old' }])
    })
  }

  it('reads a missing collection as empty rather than undefined', async () => {
    const store = useAuthStore()
    api.listUsers.mockResolvedValue({})
    await store.loadUsers()
    expect(store.users).toEqual([])
  })
})

describe('API key actions', () => {
  it('puts a freshly minted key at the top of the list', async () => {
    const store = useAuthStore()
    store.apiKeys = [{ id: 'k1' }]
    api.createAPIKey.mockResolvedValue({ apiKey: { id: 'k2' }, token: 'secret' })
    const data = await store.createAPIKey({ name: 'ci' })
    expect(store.apiKeys.map((k) => k.id)).toEqual(['k2', 'k1'])
    expect(data.token).toBe('secret')
  })

  it('ignores a create that returned no key', async () => {
    const store = useAuthStore()
    store.apiKeys = [{ id: 'k1' }]
    api.createAPIKey.mockResolvedValue({})
    await store.createAPIKey({ name: 'ci' })
    expect(store.apiKeys.map((k) => k.id)).toEqual(['k1'])
  })

  it('returns null and keeps the code when minting fails', async () => {
    const store = useAuthStore()
    api.createAPIKey.mockRejectedValue({ code: 'forbidden' })
    expect(await store.createAPIKey({ name: 'ci' })).toBeNull()
    expect(store.errorCode).toBe('forbidden')
  })

  it('replaces the edited key in place', async () => {
    const store = useAuthStore()
    store.apiKeys = [{ id: 'k1', name: 'old' }, { id: 'k2', name: 'other' }]
    api.updateAPIKey.mockResolvedValue({ apiKey: { id: 'k1', name: 'new' } })
    expect(await store.updateAPIKey('k1', { name: 'new' })).toBe(true)
    expect(store.apiKeys[0].name).toBe('new')
  })

  it('keeps the code when an edit fails', async () => {
    const store = useAuthStore()
    api.updateAPIKey.mockRejectedValue({ code: 'not_found' })
    expect(await store.updateAPIKey('k1', { name: 'new' })).toBe(false)
    expect(store.errorCode).toBe('not_found')
  })

  it('drops a revoked key from the list', async () => {
    const store = useAuthStore()
    store.apiKeys = [{ id: 'k1' }, { id: 'k2' }]
    expect(await store.revokeAPIKey('k1')).toBe(true)
    expect(store.apiKeys.map((k) => k.id)).toEqual(['k2'])
  })

  it('keeps the code and the list when revocation fails', async () => {
    const store = useAuthStore()
    store.apiKeys = [{ id: 'k1' }]
    api.revokeAPIKey.mockRejectedValue({ code: 'forbidden' })
    expect(await store.revokeAPIKey('k1')).toBe(false)
    expect(store.errorCode).toBe('forbidden')
    expect(store.apiKeys.map((k) => k.id)).toEqual(['k1'])
  })
})

describe('sign-out', () => {
  it('clears the session even when the endpoint fails', async () => {
    // A cookie that already expired makes /api/auth/logout answer 401; the
    // console still has to forget the identity it was holding.
    const store = useAuthStore()
    store.user = { id: 'u1' }
    store.permissions = ['tasks:read']
    store.sessions = [{ id: 's1' }]
    store.apiKeys = [{ id: 'k1' }]
    api.logout.mockRejectedValue({ code: 'unauthenticated' })
    await store.logout()
    expect(store.user).toBeNull()
    expect(store.permissions).toEqual([])
    expect(store.sessions).toEqual([])
    expect(store.apiKeys).toEqual([])
    expect(store.errorCode).toBe('')
  })

  it('does not remember the identity after a successful sign-out', async () => {
    const store = useAuthStore()
    store.user = { id: 'u1' }
    await store.logout()
    expect(store.signedIn).toBe(false)
  })
})
