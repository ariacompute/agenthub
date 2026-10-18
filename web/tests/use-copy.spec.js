import { afterEach, describe, expect, it } from 'vitest'
import { STORAGE_KEY, detectLang, enumLabel, healthLabel, resolve, setLang, useCopy, useLang } from '../src/lib/use-copy.js'
import { shellCopy } from '../src/lib/copy/shell-copy.js'

// Locale resolution is what decides what an operator reads, so it is asserted
// in preference order: the URL the console was opened with wins over storage,
// and anything unsupported falls back to English rather than to a blank.

afterEach(() => {
  setLang('en')
})

describe('resolve', () => {
  it('merges a language over English', () => {
    const table = resolve(shellCopy, 'zh')
    expect(table.navRegistry).toBe('注册中心')
    // A shallow merge over English: every key exists, the translated ones win.
    expect(Object.keys(table).sort()).toEqual(Object.keys(shellCopy.en).sort())
    expect(table.actionSave).toBe(shellCopy.zh.actionSave)
  })

  it('falls back to English for a language that does not exist', () => {
    expect(resolve(shellCopy, 'nl').navRegistry).toBe('Registry')
    expect(resolve(shellCopy, undefined).navRegistry).toBe('Registry')
  })
})

describe('detectLang', () => {
  it('prefers the query string over storage', () => {
    const storage = { getItem: () => 'de' }
    expect(detectLang('?lang=ja', storage)).toBe('ja')
  })

  it('falls back to storage when the query is absent or unsupported', () => {
    const storage = { getItem: () => 'ko' }
    expect(detectLang('', storage)).toBe('ko')
    expect(detectLang('?lang=klingon', storage)).toBe('ko')
  })

  it('answers English when nothing else is known', () => {
    expect(detectLang('', null)).toBe('en')
    expect(detectLang('', { getItem: () => null })).toBe('en')
    expect(detectLang('', { getItem: () => 'unsupported' })).toBe('en')
  })
})

describe('setLang and useCopy', () => {
  it('applies a supported language and refuses an unsupported one', () => {
    expect(setLang('fr')).toBe('fr')
    expect(useLang().value).toBe('fr')
    expect(setLang('klingon')).toBe('en')
    expect(useLang().value).toBe('en')

    const { t, te } = useCopy('shell')
    expect(t('actionSave')).toBe('Save')
    expect(te('actionSave')).toBe(true)
    expect(te('thisKeyDoesNotExist')).toBe(false)
  })

  it('switches the table a namespace hands out', () => {
    setLang('es')
    const { t } = useCopy('shell')
    expect(t('actionSave')).toBe('Guardar')
    setLang('en')
    expect(t('actionSave')).toBe('Save')
  })

  it('answers an unknown namespace without throwing', () => {
    expect(useCopy('nope').t('anything')).toBeUndefined()
  })
})

describe('enumLabel', () => {
  it('localises a backend enum value', () => {
    setLang('zh')
    expect(enumLabel('kind', 'mcp')).not.toBe('mcp')
    expect(healthLabel('healthy')).not.toBe('healthy')
    setLang('en')
  })

  it('hands back the raw value when there is nothing to translate', () => {
    expect(enumLabel('kind', '')).toBe('')
    expect(enumLabel('kind', 'something_new')).toBe('something_new')
    expect(enumLabel('not-a-group', 'x')).toBe('x')
  })
})

describe('storage key', () => {
  it('is the one the console and the tests share', () => {
    expect(STORAGE_KEY).toBe('agenthub.lang')
  })
})
