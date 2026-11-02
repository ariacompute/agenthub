import { describe, expect, it } from 'vitest'
import {
  ARGS_LABELS,
  ARGS_POLICIES,
  NETWORK_LABELS,
  NETWORK_MODES,
  applyBool,
  applyChoice,
  applyList,
  applyNumber,
  applyText,
  mergeSandbox,
  sandboxEmpty,
  sandboxOf,
  showsSandbox,
} from '../src/lib/sandbox-form.js'
import { sandboxCopy } from '../src/lib/copy/sandbox-copy.js'

// The rules here mirror internal/registry.SandboxConfig. Two of them are easy to
// get wrong in a way nobody notices until production: an empty value has to
// delete the key (otherwise the server keeps a zero instead of inheriting the
// instance default), and readonly_root has to keep an explicit false.

describe('showsSandbox', () => {
  it('offers the block only to the kinds that execute something', () => {
    for (const kind of ['agent', 'mcp', 'cli']) expect(showsSandbox(kind), kind).toBe(true)
    for (const kind of ['model', 'skill', 'custom', '', undefined]) {
      expect(showsSandbox(kind), String(kind)).toBe(false)
    }
  })
})

describe('sandboxOf', () => {
  it('answers an empty object for a record with no policy', () => {
    expect(sandboxOf({})).toEqual({})
    expect(sandboxOf(undefined)).toEqual({})
    expect(sandboxOf({ sandbox: { network: 'none' } })).toEqual({ network: 'none' })
  })
})

describe('applyText', () => {
  it('trims, because a padded path fails the server check for no visible reason', () => {
    expect(applyText({}, 'work_dir', '  /work  ').work_dir).toBe('/work')
  })

  it('deletes the key when the field is cleared', () => {
    expect(applyText({ work_dir: '/work' }, 'work_dir', '')).toEqual({})
    expect(applyText({ work_dir: '/work' }, 'work_dir', '   ')).toEqual({})
  })

  it('keeps a non-string value as given', () => {
    // applyText is also called with numbers from the advanced controls; they
    // must not become "4" or be trimmed away.
    expect(applyText({}, 'timeout_ms', 4)).toEqual({ timeout_ms: 4 })
  })

  it('leaves the other fields alone', () => {
    expect(applyText({ image: 'debian' }, 'provider', 'docker')).toEqual({
      image: 'debian',
      provider: 'docker',
    })
  })
})

describe('applyNumber', () => {
  it('stores integers for the integer fields', () => {
    expect(applyNumber({}, 'memory_mb', '512')).toEqual({ memory_mb: 512 })
    expect(applyNumber({}, 'timeout_ms', '1500')).toEqual({ timeout_ms: 1500 })
  })

  it('stores a fraction for the CPU field', () => {
    expect(applyNumber({}, 'cpu', '1.5', { float: true })).toEqual({ cpu: 1.5 })
  })

  it('drops values the server would reject instead of sending NaN', () => {
    expect(applyNumber({}, 'memory_mb', 'abc')).toEqual({})
    expect(applyNumber({}, 'cpu', '', { float: true })).toEqual({})
  })

  it('deletes the key when the field is cleared', () => {
    expect(applyNumber({ memory_mb: 512 }, 'memory_mb', '')).toEqual({})
  })
})

describe('applyBool', () => {
  it('keeps an explicit false, which is the only way to widen the default', () => {
    expect(applyBool({}, 'readonly_root', 'false')).toEqual({ readonly_root: false })
  })

  it('keeps an explicit true', () => {
    expect(applyBool({}, 'readonly_root', 'true')).toEqual({ readonly_root: true })
  })

  it('treats the inherit option as unset', () => {
    expect(applyBool({ readonly_root: false }, 'readonly_root', '')).toEqual({})
  })
})

describe('applyList', () => {
  it('splits, trims and drops blank entries', () => {
    expect(applyList({}, 'egress_allowlist', ' a.example , ,b.example ')).toEqual({
      egress_allowlist: ['a.example', 'b.example'],
    })
  })

  it('deletes the key when every entry is removed', () => {
    expect(applyList({ egress_allowlist: ['a'] }, 'egress_allowlist', ' , ')).toEqual({})
  })

  it('reads a missing value as an empty list', () => {
    // The form hands over undefined before the field is touched; that is a
    // cleared list, not the string "undefined".
    expect(applyList({ egress_allowlist: ['a'] }, 'egress_allowlist', undefined)).toEqual({})
    expect(applyList({}, 'egress_allowlist', null)).toEqual({})
  })
})

describe('applyChoice', () => {
  it('stores a value from the vocabulary', () => {
    for (const mode of NETWORK_MODES) {
      expect(applyChoice({}, 'network', mode, NETWORK_MODES)).toEqual({ network: mode })
    }
  })

  it('refuses a value the console cannot name', () => {
    // The server would reject "egress" (it is not a mode), so storing it would
    // turn an obvious mistake into a failed save.
    expect(applyChoice({ network: 'none' }, 'network', 'egress', NETWORK_MODES)).toEqual({})
  })

  it('treats the inherit option as unset', () => {
    expect(applyChoice({ args_policy: 'none' }, 'args_policy', '', ARGS_POLICIES)).toEqual({})
  })
})

describe('mergeSandbox', () => {
  it('removes the key rather than storing an empty object', () => {
    expect(mergeSandbox({ agent: { url: 'u' }, sandbox: { cpu: 1 } }, {})).toEqual({
      agent: { url: 'u' },
    })
  })

  it('replaces the policy wholesale', () => {
    expect(mergeSandbox({ sandbox: { cpu: 1, memory_mb: 256 } }, { cpu: 2 })).toEqual({
      sandbox: { cpu: 2 },
    })
  })

  it('leaves the rest of the descriptor untouched', () => {
    const config = { mcp: { transport: 'stdio' } }
    expect(mergeSandbox(config, { network: 'none' })).toEqual({
      mcp: { transport: 'stdio' },
      sandbox: { network: 'none' },
    })
    expect(config).toEqual({ mcp: { transport: 'stdio' } })
  })

  it('survives a missing descriptor', () => {
    expect(mergeSandbox(undefined, {})).toEqual({})
    expect(mergeSandbox(undefined, { cpu: 1 })).toEqual({ sandbox: { cpu: 1 } })
  })
})

describe('sandboxEmpty', () => {
  it('treats null, undefined and {} the same', () => {
    expect(sandboxEmpty({})).toBe(true)
    expect(sandboxEmpty(null)).toBe(true)
    expect(sandboxEmpty(undefined)).toBe(true)
    expect(sandboxEmpty({ cpu: 0 })).toBe(false)
  })
})

describe('label maps', () => {
  it('names every mode and policy in every language', () => {
    // A missing key renders the key itself in the select, which is how a mode
    // quietly becomes unreadable in one language only.
    for (const lang of Object.keys(sandboxCopy)) {
      for (const key of [...Object.values(NETWORK_LABELS), ...Object.values(ARGS_LABELS)]) {
        expect(sandboxCopy[lang][key], `${lang}.${key}`).toBeTruthy()
      }
    }
  })

  it('covers the whole vocabulary', () => {
    expect(Object.keys(NETWORK_LABELS).sort()).toEqual([...NETWORK_MODES].sort())
    expect(Object.keys(ARGS_LABELS).sort()).toEqual([...ARGS_POLICIES].sort())
  })
})
