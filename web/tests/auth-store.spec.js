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
  it('exposes the 13 permissions the matrix renders', () => {
    expect(PERMISSIONS).toHaveLength(13)
    for (const p of [
      'resources:read',
      'tasks:read',
      'tasks:write',
      'users:manage',
      'backups:manage',
      'registry:curate',
    ]) {
      expect(PERMISSIONS).toContain(p)
    }
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
