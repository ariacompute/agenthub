import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  createBackup,
  deleteBackup,
  getBackupSchedule,
  listBackups,
  restoreBackup,
  saveBackupSchedule,
  downloadBackup,
} from '../src/lib/api.js'

// The checkpoint endpoints are the ones that can rewrite every table, so the
// client has to be boringly exact about them: the confirm travels in the body,
// the id is escaped into the path, and a failed download surfaces the code the
// console localises rather than a generic "request failed".

afterEach(() => {
  vi.unstubAllGlobals()
})

function stubFetch(response) {
  const fetchMock = vi.fn(async () => response)
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function jsonResponse(payload, status = 200) {
  return { ok: status >= 200 && status < 300, status, text: async () => JSON.stringify(payload) }
}

describe('checkpoint api', () => {
  it('asks for the overview', async () => {
    const fetchMock = stubFetch(jsonResponse({ enabled: true, checkpoints: [] }))
    await listBackups()
    expect(fetchMock.mock.calls[0][0]).toContain('/api/backups')
  })

  it('sends the label of a manual checkpoint', async () => {
    const fetchMock = stubFetch(jsonResponse({ checkpoint: { id: 'bkp-1' } }, 201))
    await createBackup('before the upgrade')
    const [, init] = fetchMock.mock.calls[0]
    expect(init.method).toBe('POST')
    expect(JSON.parse(init.body)).toEqual({ label: 'before the upgrade' })
  })

  it('sends no label when none was given', async () => {
    const fetchMock = stubFetch(jsonResponse({ checkpoint: { id: 'bkp-2' } }, 201))
    await createBackup()
    expect(JSON.parse(fetchMock.mock.calls[0][1].body)).toEqual({ label: '' })
  })

  it('echoes the checkpoint id back when restoring', async () => {
    const fetchMock = stubFetch(jsonResponse({ checkpoint: { id: 'bkp-1' } }))
    await restoreBackup('bkp-1', 'bkp-1')
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toContain('/api/backups/bkp-1/restore')
    expect(JSON.parse(init.body)).toEqual({ confirm: 'bkp-1' })
  })

  it('escapes the id it puts in the path', async () => {
    const fetchMock = stubFetch(jsonResponse({}))
    await deleteBackup('bkp/../etc')
    expect(fetchMock.mock.calls[0][0]).toContain(encodeURIComponent('bkp/../etc'))
  })

  it('puts the whole schedule in the body', async () => {
    const fetchMock = stubFetch(jsonResponse({ schedule: { mode: 'daily' } }))
    await saveBackupSchedule({ mode: 'daily', dailyTime: '03:00', keepLast: 7 })
    const [, init] = fetchMock.mock.calls[0]
    expect(init.method).toBe('PUT')
    expect(JSON.parse(init.body)).toEqual({ mode: 'daily', dailyTime: '03:00', keepLast: 7 })
  })

  it('reads the schedule back', async () => {
    const fetchMock = stubFetch(jsonResponse({ schedule: { mode: 'off' } }))
    await getBackupSchedule()
    expect(fetchMock.mock.calls[0][0]).toContain('/api/backups/schedule')
  })

  it('takes the file name from the header the server sends', async () => {
    stubFetch({
      ok: true,
      status: 200,
      headers: { get: () => 'attachment; filename="checkpoint-20261018T063745Z-manual.tar.gz"' },
      blob: async () => 'archive-bytes',
    })
    const { blob, filename } = await downloadBackup('bkp-1')
    expect(blob).toBe('archive-bytes')
    expect(filename).toBe('checkpoint-20261018T063745Z-manual.tar.gz')
  })

  it('falls back to a usable name when the header is missing', async () => {
    stubFetch({
      ok: true,
      status: 200,
      headers: { get: () => null },
      blob: async () => 'archive-bytes',
    })
    expect((await downloadBackup('bkp-1')).filename).toBe('checkpoint.tar.gz')
  })

  it('surfaces the code of a refused download', async () => {
    stubFetch({
      ok: false,
      status: 409,
      json: async () => ({ code: 'backup_checksum_mismatch' }),
    })
    await expect(downloadBackup('bkp-1')).rejects.toMatchObject({ code: 'backup_checksum_mismatch' })
  })

  it('survives an error body that is not JSON', async () => {
    stubFetch({
      ok: false,
      status: 500,
      json: async () => {
        throw new Error('not json')
      },
    })
    await expect(downloadBackup('bkp-1')).rejects.toMatchObject({ code: 'internal_error' })
  })
})
