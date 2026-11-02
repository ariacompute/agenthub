import { afterEach, describe, expect, it, vi } from 'vitest'

// use-copy.js decides the language once, at import time, from whatever the
// environment offers. These cases run the module against a stubbed browser so
// the two environment branches — "there is a window" and "there is not" — are
// both exercised. Each case imports the module after stubbing, because the
// choice happens when the module is evaluated.

afterEach(() => {
  vi.unstubAllGlobals()
  vi.resetModules()
})

describe('detectLang without a browser', () => {
  it('falls back to English when there is no navigator', async () => {
    vi.stubGlobal('navigator', undefined)
    const { detectLang } = await import('../src/lib/use-copy.js')

    expect(detectLang('', null)).toBe('en')
    // The query still wins: a link can pin the language without storage.
    expect(detectLang('?lang=fr', null)).toBe('fr')
    expect(detectLang('?lang=klingon', null)).toBe('en')
  })

  it('prefers the URL over storage', async () => {
    vi.stubGlobal('navigator', undefined)
    const { detectLang } = await import('../src/lib/use-copy.js')
    const storage = { getItem: () => 'de' }

    expect(detectLang('?lang=ja', storage)).toBe('ja')
    expect(detectLang('', storage)).toBe('de')
    expect(detectLang('', { getItem: () => 'klingon' })).toBe('en')
  })
})

describe('setLang with a browser', () => {
  function stubBrowser(language = 'en') {
    const setItem = vi.fn()
    const documentElement = {}
    vi.stubGlobal('window', {
      location: { search: '' },
      localStorage: { setItem, getItem: () => null },
      navigator: { language },
    })
    vi.stubGlobal('navigator', { language })
    vi.stubGlobal('document', { documentElement })
    return { setItem, documentElement }
  }

  it('persists the choice and mirrors it onto <html>', async () => {
    const { setItem, documentElement } = stubBrowser()
    const { setLang, STORAGE_KEY } = await import('../src/lib/use-copy.js')

    expect(setLang('ar')).toBe('ar')
    expect(setItem).toHaveBeenCalledWith(STORAGE_KEY, 'ar')
    // Arabic is the right-to-left case the layout mirrors on.
    expect(documentElement.lang).toBe('ar')
    expect(documentElement.dir).toBe('rtl')

    expect(setLang('fr')).toBe('fr')
    expect(documentElement.lang).toBe('fr')
    expect(documentElement.dir).toBe('ltr')
  })

  it('falls back to English for a language it does not ship', async () => {
    const { setItem, documentElement } = stubBrowser()
    const { setLang } = await import('../src/lib/use-copy.js')

    expect(setLang('klingon')).toBe('en')
    expect(setItem).toHaveBeenLastCalledWith('agenthub.lang', 'en')
    expect(documentElement.lang).toBe('en')
    expect(documentElement.dir).toBe('ltr')
  })

  it('still returns the language when there is no window to persist into', async () => {
    vi.stubGlobal('navigator', undefined)
    const { setLang } = await import('../src/lib/use-copy.js')

    // No window means no storage and no <html> to touch, but the caller still
    // needs the normalised code back.
    expect(setLang('de')).toBe('de')
    expect(setLang('klingon')).toBe('en')
  })
})
