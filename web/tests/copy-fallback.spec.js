import { describe, expect, it } from 'vitest'
import { LANGUAGES } from '../src/lib/languages.js'
import { resolve } from '../src/lib/use-copy.js'
import { shellCopy } from '../src/lib/copy/shell-copy.js'
import { registryCopy } from '../src/lib/copy/registry-copy.js'
import { traceCopy } from '../src/lib/copy/trace-copy.js'
import { topologyCopy } from '../src/lib/copy/topology-copy.js'
import { errorsCopy } from '../src/lib/copy/errors-copy.js'
import { authCopy } from '../src/lib/copy/auth-copy.js'
import { adminCopy } from '../src/lib/copy/admin-copy.js'
import { backupCopy } from '../src/lib/copy/backup-copy.js'

const PROSE = {
  shell: shellCopy,
  registry: registryCopy,
  trace: traceCopy,
  topology: topologyCopy,
  auth: authCopy,
  admin: adminCopy,
  backup: backupCopy,
  errors: errorsCopy,
}

describe('copy fallback', () => {
  it('keeps English complete for every key translated elsewhere', () => {
    for (const [name, copy] of Object.entries(PROSE)) {
      for (const [code] of LANGUAGES) {
        for (const key of Object.keys(copy[code] || {})) {
          expect(copy.en[key], `${name}.${code}.${key} lacks an English fallback`).toBeDefined()
        }
      }
    }
  })

  it('gives every language its own translation', () => {
    for (const [name, copy] of Object.entries(PROSE)) {
      for (const [code] of LANGUAGES) {
        if (code === 'en') continue
        const t = resolve(copy, code)
        for (const key of Object.keys(copy.en)) {
          expect(t[key], `${name}.${code}.${key} is not translated`).toBeDefined()
          expect(t[key], `${name}.${code}.${key} should not equal English`).not.toBe(copy.en[key])
        }
      }
    }
  })

  it('falls back to English for an unsupported language', () => {
    for (const [name, copy] of Object.entries(PROSE)) {
      const t = resolve(copy, 'nl')
      for (const key of Object.keys(copy.en)) {
        expect(t[key], `${name}.nl.${key}`).toBe(copy.en[key])
      }
    }
  })

  it('returns undefined for a missing key instead of throwing', () => {
    for (const [code] of LANGUAGES) {
      expect(resolve(shellCopy, code).thisKeyDoesNotExist).toBeUndefined()
    }
  })
})
