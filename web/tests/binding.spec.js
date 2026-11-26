import { describe, expect, it } from 'vitest'
import { filterOptions, toggleRef, unresolvedRefs } from '../src/lib/binding.js'

const options = [
  { id: 'mcp-1', name: 'hr-mcp', display_name: 'HR server', namespace: 'team-a' },
  { id: 'mcp-2', name: 'ledger', namespace: 'default' },
]

describe('unresolvedRefs', () => {
  it('keeps what the catalogue cannot offer', () => {
    // A stale id, a record outside the caller's grants and a legacy free-text
    // skill label all look the same from here: selected but not offered, and the
    // drawer must not silently drop them.
    expect(unresolvedRefs(['mcp-1', 'hand written label', 'mcp-gone'], options)).toEqual([
      'hand written label',
      'mcp-gone',
    ])
  })

  it('is empty when everything resolves', () => {
    expect(unresolvedRefs(['mcp-2'], options)).toEqual([])
  })

  it('tolerates missing inputs', () => {
    expect(unresolvedRefs(undefined, undefined)).toEqual([])
    expect(unresolvedRefs(['a'], undefined)).toEqual(['a'])
  })
})

describe('filterOptions', () => {
  it('matches name, display name, namespace and id', () => {
    expect(filterOptions(options, 'ledger').map((entry) => entry.id)).toEqual(['mcp-2'])
    expect(filterOptions(options, 'hr server').map((entry) => entry.id)).toEqual(['mcp-1'])
    expect(filterOptions(options, 'team-a').map((entry) => entry.id)).toEqual(['mcp-1'])
    expect(filterOptions(options, 'mcp-2').map((entry) => entry.id)).toEqual(['mcp-2'])
  })

  it('returns the whole catalogue for an empty query', () => {
    expect(filterOptions(options, '  ')).toHaveLength(2)
    expect(filterOptions(undefined, 'x')).toEqual([])
  })
})

describe('toggleRef', () => {
  it('appends in selection order and removes by id', () => {
    expect(toggleRef([], 'a')).toEqual(['a'])
    expect(toggleRef(['a'], 'b')).toEqual(['a', 'b'])
    expect(toggleRef(['a', 'b'], 'a')).toEqual(['b'])
  })

  it('removes an unresolved reference the same way', () => {
    // Removing a stale reference is how an operator cleans one up; it needs no
    // special case in the picker.
    expect(toggleRef(['gone', 'mcp-1'], 'gone')).toEqual(['mcp-1'])
  })

  it('tolerates a missing value', () => {
    expect(toggleRef(undefined, 'a')).toEqual(['a'])
  })
})
