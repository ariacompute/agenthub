import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

// Both stores have to survive an answer that is thinner than the documented one:
// a resolver that returns nothing at all, or a rejection that carries a plain
// Error instead of the machine readable `code`. Neither may leave the screen
// holding yesterday's data under a success banner.

const apiMock = vi.hoisted(() => {
  const names = [
    'listResources',
    'listTasks',
    'healthz',
    'createResource',
    'updateResource',
    'deleteResource',
    'probeResource',
    'listRegistries',
    'listApprovals',
    'createRegistry',
    'deleteRegistry',
    'setResourceStatus',
    'listSources',
    'createSource',
    'deleteSource',
    'syncSource',
    'authConfig',
    'me',
    'login',
    'register',
    'logout',
    'listSessions',
    'revokeSession',
    'listPermissions',
    'listAPIKeys',
    'createAPIKey',
  ]
  const out = {}
  for (const name of names) out[name] = vi.fn().mockResolvedValue({})
  return out
})

vi.mock('../src/lib/api.js', () => apiMock)

const { useHubStore } = await import('../src/lib/store.js')
const { useAuthStore } = await import('../src/lib/auth-store.js')

beforeEach(() => {
  setActivePinia(createPinia())
  for (const fn of Object.values(apiMock)) fn.mockReset().mockResolvedValue({})
})

describe('hub store with an empty answer', () => {
  it('keeps empty buckets when the resolver returns nothing', async () => {
    apiMock.listResources.mockResolvedValue(undefined)
    const store = useHubStore()
    expect(await store.refreshResources()).toBe(true)
    expect(store.resources).toEqual([])
    expect(store.counts).toEqual({})
    expect(store.healthCounts).toEqual({})
  })

  it('leaves loading false after a failure', async () => {
    apiMock.listResources.mockRejectedValue(new Error('network'))
    const store = useHubStore()
    expect(await store.refreshResources()).toBe(false)
    expect(store.loading).toBe(false)
    expect(store.errorCode).toBe('internal_error')
  })

  it('names the fallback code when a rejection carries none', async () => {
    for (const action of ['refreshTasks', 'refreshHealth', 'refreshRegistries', 'refreshApprovals']) {
      apiMock.listTasks.mockRejectedValue(new Error('network'))
      apiMock.healthz.mockRejectedValue(new Error('network'))
      apiMock.listRegistries.mockRejectedValue(new Error('network'))
      apiMock.listApprovals.mockRejectedValue(new Error('network'))
      const store = useHubStore()
      expect(await store[action]()).toBe(false)
      expect(store.errorCode).toBe('internal_error')
    }
  })

  it('clears the health reading when the probe fails', async () => {
    const store = useHubStore()
    store.health = { status: 'ok' }
    apiMock.healthz.mockRejectedValue(new Error('network'))
    await store.refreshHealth()
    expect(store.health).toBe(null)
  })

  it('names the fallback code when a write rejects without one', async () => {
    const store = useHubStore()
    apiMock.deleteResource.mockRejectedValue(new Error('network'))
    expect(await store.remove('r1')).toBe(false)
    expect(store.errorCode).toBe('internal_error')
  })

  it('refuses to claim success when only the read fails', async () => {
    apiMock.createResource.mockResolvedValue({ resource: { id: 'r1' } })
    apiMock.listResources.mockRejectedValue(Object.assign(new Error('read'), { code: 'store_failed' }))
    const store = useHubStore()
    expect(await store.register({ name: 'r1' })).toBe(false)
    expect(store.errorCode).toBe('store_failed')
  })
})

describe('auth store with an empty answer', () => {
  it('falls back to empty provider and permission lists', async () => {
    const store = useAuthStore()
    apiMock.authConfig.mockResolvedValue(undefined)
    await store.loadConfig()
    expect(store.providers).toEqual([])
    expect(store.configured).toBe(false)

    apiMock.me.mockResolvedValue(undefined)
    expect(await store.loadMe()).toBe(false)
    expect(store.user).toBe(null)
    expect(store.permissions).toEqual([])
    expect(store.roles).toEqual([])
    expect(store.groups).toEqual([])

    apiMock.listSessions.mockResolvedValue(undefined)
    await store.loadSessions()
    expect(store.sessions).toEqual([])

    apiMock.listPermissions.mockResolvedValue(undefined)
    await store.loadPermissions()
    expect(store.permissionsCatalogue).toEqual([])

    apiMock.listAPIKeys.mockResolvedValue(undefined)
    await store.loadAPIKeys()
    expect(store.apiKeys).toEqual([])
  })

  it('returns an empty object when a minted key comes back without one', async () => {
    const store = useAuthStore()
    apiMock.createAPIKey.mockResolvedValue(undefined)
    expect(await store.createAPIKey({ name: 'ci' })).toEqual({})
    expect(store.apiKeys).toEqual([])
    expect(store.errorCode).toBe('')
  })

  it('clears the loading flag on both endings of login and register', async () => {
    const store = useAuthStore()

    apiMock.login.mockResolvedValue({})
    apiMock.me.mockResolvedValue({ user: { id: 'u1' } })
    expect(await store.login('a@example.com', 'secret')).toBe(true)
    expect(store.loading).toBe(false)

    apiMock.login.mockRejectedValue(Object.assign(new Error('bad'), { code: 'invalid_credentials' }))
    expect(await store.login('a@example.com', 'wrong')).toBe(false)
    expect(store.loading).toBe(false)
    expect(store.errorCode).toBe('invalid_credentials')

    apiMock.register.mockResolvedValue({})
    expect(await store.register({ email: 'b@example.com' })).toBe(true)
    expect(store.loading).toBe(false)

    apiMock.register.mockRejectedValue(new Error('bad'))
    expect(await store.register({ email: 'b@example.com' })).toBe(false)
    expect(store.loading).toBe(false)
    expect(store.errorCode).toBe('internal_error')
  })

  it('drops the session even when logging out fails', async () => {
    const store = useAuthStore()
    store.user = { id: 'u1' }
    store.permissions = ['tasks:read']
    apiMock.logout.mockRejectedValue(new Error('gone'))
    await store.logout()
    expect(store.user).toBe(null)
    expect(store.permissions).toEqual([])
    expect(store.errorCode).toBe('')
  })
})
