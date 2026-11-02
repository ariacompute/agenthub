import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

const apiMock = vi.hoisted(() => {
  const make = () => vi.fn().mockResolvedValue({})
  return {
    listResources: make(),
    listTasks: make(),
    healthz: make(),
    createResource: make(),
    updateResource: make(),
    deleteResource: make(),
    probeResource: make(),
    listRegistries: make(),
    listApprovals: make(),
    createRegistry: make(),
    deleteRegistry: make(),
    setResourceStatus: make(),
    listSources: make(),
    createSource: make(),
    deleteSource: make(),
    syncSource: make(),
  }
})

vi.mock('../src/lib/api.js', () => apiMock)

import * as api from '../src/lib/api.js'
import { KINDS, STATUSES, useHubStore } from '../src/lib/store.js'

beforeEach(() => {
  setActivePinia(createPinia())
  for (const fn of Object.values(apiMock)) fn.mockReset().mockResolvedValue({})
})

describe('catalogue constants', () => {
  it('exposes the five protocol kinds plus custom', () => {
    expect(KINDS).toEqual(['model', 'agent', 'mcp', 'skill', 'cli', 'custom'])
  })
  it('exposes the curation statuses', () => {
    expect(STATUSES).toEqual(['draft', 'pending', 'approved', 'rejected', 'deprecated'])
  })
})

describe('refreshResources', () => {
  it('stores resources, counts and health splits', async () => {
    api.listResources.mockResolvedValue({
      resources: [{ id: 'r1' }],
      counts: { mcp: 1 },
      health: { healthy: 1 },
    })
    const store = useHubStore()
    await store.refreshResources()
    expect(store.resources).toEqual([{ id: 'r1' }])
    expect(store.counts).toEqual({ mcp: 1 })
    expect(store.healthCounts).toEqual({ healthy: 1 })
    expect(store.errorCode).toBe('')
  })

  it('records the code on failure', async () => {
    api.listResources.mockRejectedValue({ code: 'resource_not_found' })
    const store = useHubStore()
    await store.refreshResources()
    expect(store.errorCode).toBe('resource_not_found')
    expect(store.resources).toEqual([])
  })
})

describe('refreshTasks', () => {
  it('shows the newest task first', async () => {
    api.listTasks.mockResolvedValue({ tasks: [{ id: 'a' }, { id: 'b' }] })
    const store = useHubStore()
    await store.refreshTasks()
    expect(store.tasks.map((t) => t.id)).toEqual(['b', 'a'])
  })
})

describe('resource mutations', () => {
  it('registers and refreshes on success', async () => {
    api.createResource.mockResolvedValue({})
    api.listResources.mockResolvedValue({ resources: [{ id: 'r1' }] })
    const store = useHubStore()
    expect(await store.register({ name: 'x' })).toBe(true)
    expect(store.errorCode).toBe('')
    expect(store.resources).toEqual([{ id: 'r1' }])
  })

  it('returns false and records the code on register failure', async () => {
    api.createResource.mockRejectedValue({ code: 'invalid_entry' })
    const store = useHubStore()
    expect(await store.register({ name: 'x' })).toBe(false)
    expect(store.errorCode).toBe('invalid_entry')
  })

  it('removes and refreshes', async () => {
    api.deleteResource.mockResolvedValue({})
    const store = useHubStore()
    expect(await store.remove('r1')).toBe(true)
    expect(api.deleteResource).toHaveBeenCalledWith('r1')
  })

  it('probes a single resource', async () => {
    api.probeResource.mockResolvedValue({})
    const store = useHubStore()
    expect(await store.probe('r1')).toBe(true)
    expect(api.probeResource).toHaveBeenCalledWith('r1')
  })
})

describe('registry governance', () => {
  it('loads registries and the approval queue', async () => {
    api.listRegistries.mockResolvedValue({ registries: [{ id: 'ns1' }] })
    api.listApprovals.mockResolvedValue({ records: [{ id: 'rec1' }] })
    const store = useHubStore()
    await store.refreshRegistries()
    await store.refreshApprovals()
    expect(store.registries).toEqual([{ id: 'ns1' }])
    expect(store.approvals).toEqual([{ id: 'rec1' }])
  })

  it('saves and removes a registry', async () => {
    api.createRegistry.mockResolvedValue({})
    api.deleteRegistry.mockResolvedValue({})
    const store = useHubStore()
    expect(await store.saveRegistry({ id: 'ns1' })).toBe(true)
    expect(await store.removeRegistry('ns1')).toBe(true)
    expect(api.deleteRegistry).toHaveBeenCalledWith('ns1')
  })

  it('curates a record then refreshes both views', async () => {
    api.setResourceStatus.mockResolvedValue({})
    api.listResources.mockResolvedValue({ resources: [] })
    api.listApprovals.mockResolvedValue({ records: [] })
    const store = useHubStore()
    expect(await store.curate('r1', 'approved', 'looks good')).toBe(true)
    expect(api.setResourceStatus).toHaveBeenCalledWith('r1', 'approved', 'looks good')
  })
})

describe('upstream sources', () => {
  it('loads sources', async () => {
    api.listSources.mockResolvedValue({ sources: [{ id: 's1' }] })
    const store = useHubStore()
    await store.refreshSources()
    expect(store.sources).toEqual([{ id: 's1' }])
  })

  it('saves and removes a source', async () => {
    api.createSource.mockResolvedValue({})
    api.deleteSource.mockResolvedValue({})
    const store = useHubStore()
    expect(await store.saveSource({ name: 'up' })).toBe(true)
    expect(await store.removeSource('s1')).toBe(true)
  })

  it('returns the sync result to the caller', async () => {
    api.syncSource.mockResolvedValue({ status: 'ok', imported: 3 })
    api.listSources.mockResolvedValue({ sources: [] })
    api.listResources.mockResolvedValue({ resources: [] })
    const store = useHubStore()
    const result = await store.syncSource('s1')
    expect(result).toEqual({ status: 'ok', imported: 3 })
  })
})

describe('selection and health', () => {
  it('tracks the selected task and resource', () => {
    const store = useHubStore()
    store.selectTask('t1')
    store.selectResource('r1')
    expect(store.selectedTaskID).toBe('t1')
    expect(store.selectedResourceID).toBe('r1')
  })

  it('stores health on success and null on failure', async () => {
    api.healthz.mockResolvedValue({ status: 'ok' })
    const store = useHubStore()
    await store.refreshHealth()
    expect(store.health).toEqual({ status: 'ok' })

    api.healthz.mockRejectedValue({ code: 'internal_error' })
    await store.refreshHealth()
    expect(store.health).toBeNull()
  })

  it('clears the error code', async () => {
    const store = useHubStore()
    store.errorCode = 'boom'
    store.clearError()
    expect(store.errorCode).toBe('')
  })
})

// ---------------------------------------------------------------------------
// An older server may answer 200 with no body, and a proxy may reject with a
// plain Error. Neither may leave a slice undefined or blank the last code.
// ---------------------------------------------------------------------------

describe('partial payloads', () => {
  it('falls back to empty slices when the payload omits them', async () => {
    const store = useHubStore()
    await store.refreshResources()
    expect(store.resources).toEqual([])
    expect(store.counts).toEqual({})
    expect(store.healthCounts).toEqual({})
  })

  it('keeps empty defaults for the governance slices', async () => {
    const store = useHubStore()
    await store.refreshTasks()
    await store.refreshRegistries()
    await store.refreshApprovals()
    await store.refreshSources()
    expect(store.tasks).toEqual([])
    expect(store.registries).toEqual([])
    expect(store.approvals).toEqual([])
    expect(store.sources).toEqual([])
  })

  it('names an internal error when the rejection carries no code', async () => {
    api.listResources.mockRejectedValue(new Error('network down'))
    const store = useHubStore()
    await store.refreshResources()
    expect(store.errorCode).toBe('internal_error')
  })
})

describe('every action records a failure the same way', () => {
  // Each case runs one action against an api call that fails, and asserts the
  // return value the caller switches on plus the code the console localises.
  const cases = [
    ['refreshTasks', (s) => s.refreshTasks(), 'listTasks', false],
    ['update', (s) => s.update('r1', { name: 'x' }), 'updateResource', false],
    ['remove', (s) => s.remove('r1'), 'deleteResource', false],
    ['probe', (s) => s.probe('r1'), 'probeResource', false],
    ['refreshRegistries', (s) => s.refreshRegistries(), 'listRegistries', false],
    ['refreshApprovals', (s) => s.refreshApprovals(), 'listApprovals', false],
    ['saveRegistry', (s) => s.saveRegistry({ id: 'ns1' }), 'createRegistry', false],
    ['removeRegistry', (s) => s.removeRegistry('ns1'), 'deleteRegistry', false],
    ['curate', (s) => s.curate('r1', 'approved'), 'setResourceStatus', false],
    ['refreshSources', (s) => s.refreshSources(), 'listSources', false],
    ['saveSource', (s) => s.saveSource({ name: 'up' }), 'createSource', false],
    ['removeSource', (s) => s.removeSource('s1'), 'deleteSource', false],
    ['syncSource', (s) => s.syncSource('s1'), 'syncSource', null],
  ]

  for (const [name, run, endpoint, expected] of cases) {
    it(`${name} keeps the machine code and reports the failure`, async () => {
      api[endpoint].mockRejectedValue({ code: 'forbidden' })
      const store = useHubStore()
      const result = await run(store)
      expect(result).toBe(expected)
      expect(store.errorCode).toBe('forbidden')
    })
  }
})

describe('update', () => {
  it('saves the entry, re-reads the catalogue and returns true', async () => {
    api.listResources.mockResolvedValue({ resources: [{ id: 'r1', name: 'renamed' }] })
    const store = useHubStore()
    expect(await store.update('r1', { name: 'renamed' })).toBe(true)
    expect(api.updateResource).toHaveBeenCalledWith('r1', { name: 'renamed' })
    expect(store.resources).toEqual([{ id: 'r1', name: 'renamed' }])
    expect(store.errorCode).toBe('')
  })
})

// ---------------------------------------------------------------------------
// The re-read after a write is part of the action. If it fails the caller must
// not be told "done", and the reason must survive: clearing the code here is
// what used to turn a stale list into a success toast.
// ---------------------------------------------------------------------------

describe('a write that the re-read cannot follow up', () => {
  const cases = [
    ['register', (s) => s.register({ name: 'x' }), 'createResource'],
    ['update', (s) => s.update('r1', { name: 'x' }), 'updateResource'],
    ['remove', (s) => s.remove('r1'), 'deleteResource'],
    ['probe', (s) => s.probe('r1'), 'probeResource'],
    ['saveRegistry', (s) => s.saveRegistry({ id: 'ns1' }), 'createRegistry'],
    ['removeRegistry', (s) => s.removeRegistry('ns1'), 'deleteRegistry'],
    ['saveSource', (s) => s.saveSource({ name: 'up' }), 'createSource'],
    ['removeSource', (s) => s.removeSource('s1'), 'deleteSource'],
  ]

  for (const [name, run, endpoint] of cases) {
    it(`${name} reports failure and keeps the read's code`, async () => {
      api.listResources.mockRejectedValue({ code: 'unavailable' })
      api.listRegistries.mockRejectedValue({ code: 'unavailable' })
      api.listSources.mockRejectedValue({ code: 'unavailable' })

      const store = useHubStore()
      expect(await run(store)).toBe(false)
      // The write itself went through exactly once: the action must not retry it.
      expect(api[endpoint]).toHaveBeenCalledTimes(1)
      expect(store.errorCode).toBe('unavailable')
    })
  }
})

describe('curate', () => {
  it('refreshes the catalogue and the queue, and needs both', async () => {
    const store = useHubStore()
    expect(await store.curate('r1', 'approved')).toBe(true)
    expect(api.setResourceStatus).toHaveBeenCalledWith('r1', 'approved', '')
    expect(api.listResources).toHaveBeenCalled()
    expect(api.listApprovals).toHaveBeenCalled()
    expect(store.errorCode).toBe('')
  })

  it('still re-reads the queue when the catalogue read fails', async () => {
    api.listResources.mockRejectedValue({ code: 'unavailable' })
    api.listApprovals.mockResolvedValue({ records: [{ id: 'r1' }] })

    const store = useHubStore()
    expect(await store.curate('r1', 'approved')).toBe(false)
    // The two slices are independent: a failed catalogue must not skip the queue.
    expect(api.listApprovals).toHaveBeenCalled()
    expect(store.approvals).toEqual([{ id: 'r1' }])
    expect(store.errorCode).toBe('unavailable')
  })

  it('reports failure when only the queue read fails', async () => {
    api.listApprovals.mockRejectedValue({ code: 'unavailable' })
    const store = useHubStore()
    expect(await store.curate('r1', 'rejected')).toBe(false)
    expect(store.errorCode).toBe('unavailable')
  })
})

describe('syncSource', () => {
  it('returns the run outcome and re-reads both slices', async () => {
    api.syncSource.mockResolvedValue({ imported: 2, updated: 1, skipped: 0 })
    const store = useHubStore()
    expect(await store.syncSource('s1')).toEqual({ imported: 2, updated: 1, skipped: 0 })
    expect(api.listSources).toHaveBeenCalled()
    expect(api.listResources).toHaveBeenCalled()
    expect(store.errorCode).toBe('')
  })

  it('keeps the outcome when the re-read fails, and says why', async () => {
    api.syncSource.mockResolvedValue({ imported: 2 })
    api.listSources.mockRejectedValue({ code: 'unavailable' })

    const store = useHubStore()
    // Hiding the counts the server did report would be a second, worse lie.
    expect(await store.syncSource('s1')).toEqual({ imported: 2 })
    expect(store.errorCode).toBe('unavailable')
  })

  it('returns null when the run itself fails', async () => {
    api.syncSource.mockRejectedValue({ code: 'source_disabled' })
    const store = useHubStore()
    expect(await store.syncSource('s1')).toBeNull()
    expect(store.errorCode).toBe('source_disabled')
  })
})

describe('the refresh actions report whether the read landed', () => {
  const refreshers = [
    ['refreshResources', 'listResources'],
    ['refreshTasks', 'listTasks'],
    ['refreshHealth', 'healthz'],
    ['refreshRegistries', 'listRegistries'],
    ['refreshApprovals', 'listApprovals'],
    ['refreshSources', 'listSources'],
  ]

  for (const [action, endpoint] of refreshers) {
    it(`${action} answers true, then false with the code`, async () => {
      const store = useHubStore()
      expect(await store[action]()).toBe(true)
      expect(store.errorCode).toBe('')

      api[endpoint].mockRejectedValue({ code: 'unavailable' })
      expect(await store[action]()).toBe(false)
      expect(store.errorCode).toBe('unavailable')
    })
  }
})
