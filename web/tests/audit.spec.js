import { describe, expect, it } from 'vitest'
import {
  AUDIT_PAGE,
  AUDIT_RANGES,
  actorLabel,
  auditFilters,
  chainState,
  nextCursor,
  outcomeOf,
  shortId,
  statsOf,
  windowRange,
} from '../src/lib/audit.js'

const NOW = new Date('2026-03-04T12:00:00.000Z')

describe('windowRange', () => {
  it('turns a preset into a window that ends now', () => {
    const { from, to } = windowRange('range1h', NOW)
    expect(to).toBe('2026-03-04T12:00:00.000Z')
    expect(from).toBe('2026-03-04T11:00:00.000Z')
  })

  it('supports every preset the rail offers', () => {
    expect(AUDIT_RANGES.map((range) => range.key)).toEqual([
      'range1h',
      'range24h',
      'range7d',
      'rangeAll',
    ])
    const week = windowRange('range7d', NOW)
    expect(week.from).toBe('2026-02-25T12:00:00.000Z')
    const day = windowRange('range24h', NOW)
    expect(day.from).toBe('2026-03-03T12:00:00.000Z')
  })

  it('sends no bounds at all for "everything"', () => {
    expect(windowRange('rangeAll', NOW)).toEqual({ from: '', to: '' })
  })

  it('falls back to 24 hours for an unknown preset', () => {
    // Widening an unknown preset to the whole trail would be a slow query on a
    // real deployment; a bounded default is the safer failure.
    expect(windowRange('nonsense', NOW)).toEqual(windowRange('range24h', NOW))
  })
})

describe('auditFilters', () => {
  it('asks only for the window and a page size when nothing is filtered', () => {
    expect(auditFilters({ rangeKey: 'range24h' }, 0, NOW)).toEqual({
      from: '2026-03-03T12:00:00.000Z',
      to: '2026-03-04T12:00:00.000Z',
      limit: AUDIT_PAGE,
    })
  })

  it('carries the result, the source and the trimmed search', () => {
    const query = auditFilters(
      { rangeKey: 'range7d', outcome: 'denied', source: 'a2a', search: '  tasks/get  ' },
      0,
      NOW,
    )
    expect(query.outcome).toBe('denied')
    expect(query.source).toBe('a2a')
    expect(query.q).toBe('tasks/get')
  })

  it('omits a padded-empty search rather than sending whitespace', () => {
    expect(auditFilters({ rangeKey: 'range7d', search: '   ' }, 0, NOW).q).toBeUndefined()
  })

  it('uses the cursor only when there is one', () => {
    expect(auditFilters({}, 0, NOW).before).toBeUndefined()
    expect(auditFilters({}, 42, NOW).before).toBe(42)
  })
})

describe('outcomeOf', () => {
  it('separates a refusal from a failure', () => {
    // A refusal is the authorization layer working; a 5xx is breakage. An
    // operator reads the two differently, so the table colours them apart.
    expect(outcomeOf({ status: 200 })).toBe('ok')
    expect(outcomeOf({ status: 302 })).toBe('ok')
    expect(outcomeOf({ status: 401 })).toBe('denied')
    expect(outcomeOf({ status: 403 })).toBe('denied')
    expect(outcomeOf({ status: 429 })).toBe('denied')
    expect(outcomeOf({ status: 404 })).toBe('failed')
    expect(outcomeOf({ status: 500 })).toBe('failed')
  })

  it('treats a row without a status as a failure, not a success', () => {
    expect(outcomeOf({})).toBe('failed')
    expect(outcomeOf(null)).toBe('failed')
  })
})

describe('actorLabel', () => {
  const t = (key) => `t:${key}`

  it('names an anonymous caller in the user’s language', () => {
    expect(actorLabel({}, t)).toBe('t:anonymous')
    expect(actorLabel({ actorId: '' }, t)).toBe('t:anonymous')
  })

  it('shows the human label and the machine id together', () => {
    expect(actorLabel({ actorId: 'u1', actorLabel: 'owner@example.com' }, t)).toBe(
      'owner@example.com · u1',
    )
  })

  it('falls back to the bare id when no label was resolved', () => {
    expect(actorLabel({ actorId: 'u1' }, t)).toBe('u1')
  })
})

describe('shortId', () => {
  it('keeps the first eight characters', () => {
    expect(shortId('0123456789abcdef')).toBe('01234567')
    expect(shortId('abc')).toBe('abc')
    expect(shortId('')).toBe('')
    expect(shortId(undefined)).toBe('')
  })
})

describe('chainState', () => {
  it('reports a verified chain with the number of rows', () => {
    expect(chainState({ ok: true, checked: 128 })).toEqual({
      state: 'ok',
      checked: 128,
      brokenAt: '',
    })
  })

  it('reports the first broken row', () => {
    expect(chainState({ ok: false, checked: 12, brokenAt: 'evt-9' })).toEqual({
      state: 'broken',
      checked: 12,
      brokenAt: 'evt-9',
    })
  })

  it('treats a missing answer as broken', () => {
    // Only an affirmative answer is reassuring; anything else must not render
    // as a green light.
    expect(chainState(undefined)).toEqual({ state: 'broken', checked: 0, brokenAt: '' })
  })
})

describe('nextCursor', () => {
  it('is zero when the page carried no cursor', () => {
    expect(nextCursor(undefined)).toBe(0)
    expect(nextCursor({ events: [] })).toBe(0)
    expect(nextCursor({ next: 0 })).toBe(0)
  })

  it('returns the cursor for the next page', () => {
    expect(nextCursor({ next: 987 })).toBe(987)
  })
})

describe('statsOf', () => {
  it('zeroes every counter for a failed first load', () => {
    expect(statsOf(undefined)).toEqual({ total: 0, failed: 0, denied: 0, written: 0 })
  })

  it('reads the counters the endpoint returned', () => {
    expect(statsOf({ stats: { total: 10, failed: 2, denied: 3, written: 9 } })).toEqual({
      total: 10,
      failed: 2,
      denied: 3,
      written: 9,
    })
  })

  it('fills a missing counter with zero instead of undefined', () => {
    expect(statsOf({ stats: { total: 5 } })).toEqual({
      total: 5,
      failed: 0,
      denied: 0,
      written: 0,
    })
  })
})

describe('windowRange inputs', () => {
  it('accepts an ISO string as "now"', () => {
    // The screen passes a Date; the tests and any future deep link pass a string
    // restored from the URL. Both have to produce a window.
    const { from, to } = windowRange('range1h', '2026-11-02T12:00:00.000Z')
    expect(to).toBe('2026-11-02T12:00:00.000Z')
    expect(from).toBe('2026-11-02T11:00:00.000Z')
  })
})

describe('chainState without counters', () => {
  it('reports a clean chain even when the response omits checked', () => {
    // An older server answers `{ ok: true }` and nothing else; the badge must
    // still say verified rather than NaN rows checked.
    expect(chainState({ ok: true })).toEqual({ state: 'ok', checked: 0, brokenAt: '' })
  })
})
