import { describe, expect, it } from 'vitest'
import { LANGUAGES } from '../src/lib/languages.js'
import { enumsCopy } from '../src/lib/copy/enums-copy.js'
import { enumLabel } from '../src/lib/use-copy.js'

const GROUPS = ['kind', 'taskState', 'transport', 'health', 'flow', 'connection']

describe('enum copy', () => {
  it('covers every backend enum group', () => {
    expect(Object.keys(enumsCopy).sort()).toEqual([...GROUPS].sort())
  })

  it('labels every value in all 13 languages', () => {
    for (const group of GROUPS) {
      const values = Object.keys(enumsCopy[group].en)
      expect(values.length).toBeGreaterThan(0)
      for (const [code] of LANGUAGES) {
        const block = enumsCopy[group][code]
        expect(block, `${group}.${code} is missing`).toBeDefined()
        for (const value of values) {
          expect(typeof block[value], `${group}.${code}.${value}`).toBe('string')
          expect(block[value].length, `${group}.${code}.${value} is empty`).toBeGreaterThan(0)
        }
      }
    }
  })

  it('never translates protocol identifiers used as values', () => {
    expect(Object.keys(enumsCopy.kind.en)).toEqual(['model', 'agent', 'mcp', 'skill', 'cli'])
    expect(Object.keys(enumsCopy.transport.en)).toEqual(['stdio', 'streamable-http', 'sse-legacy'])
  })

  it('falls back to the raw value for an unknown label', () => {
    expect(enumLabel('kind', 'not-a-kind')).toBe('not-a-kind')
  })
})
