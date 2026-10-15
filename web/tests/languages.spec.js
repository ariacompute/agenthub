import { describe, expect, it } from 'vitest'
import {
  LANGUAGES,
  LANG_CODES,
  hreflangFor,
  isRTL,
  isValidLang,
  matchLang,
  ogLocaleFor,
} from '../src/lib/languages.js'

describe('language catalogue', () => {
  it('exposes exactly the 13 languages of ariacompute.cn', () => {
    expect(LANGUAGES).toHaveLength(13)
    expect(LANG_CODES).toEqual([
      'en',
      'zh',
      'zh-TW',
      'es',
      'fr',
      'de',
      'it',
      'ru',
      'ja',
      'ko',
      'pt',
      'ar',
      'hi',
    ])
  })

  it('marks only Arabic as right-to-left', () => {
    expect(isRTL('ar')).toBe(true)
    for (const code of LANG_CODES.filter((code) => code !== 'ar')) {
      expect(isRTL(code)).toBe(false)
    }
  })

  it('validates language codes', () => {
    expect(isValidLang('zh-TW')).toBe(true)
    expect(isValidLang('nl')).toBe(false)
    expect(isValidLang('')).toBe(false)
  })

  it('maps raw browser locales onto supported codes', () => {
    expect(matchLang('')).toBe('en')
    expect(matchLang('en-US')).toBe('en')
    expect(matchLang('zh-CN')).toBe('zh')
    expect(matchLang('zh-TW')).toBe('zh-TW')
    expect(matchLang('zh-Hant-HK')).toBe('zh-TW')
    expect(matchLang('fr-CA')).toBe('fr')
    expect(matchLang('nl-NL')).toBe('en')
  })

  it('exposes hreflang and Open Graph locales', () => {
    expect(hreflangFor('zh')).toBe('zh-Hans')
    expect(hreflangFor('zh-TW')).toBe('zh-Hant')
    expect(ogLocaleFor('pt')).toBe('pt_BR')
    expect(ogLocaleFor('xx')).toBe('en_US')
  })
})
