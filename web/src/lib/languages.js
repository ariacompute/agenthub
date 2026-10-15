// Language catalogue shared with the aria compute site (serve). Kept byte-for-byte
// compatible so agenthub can never drift from the 13 languages ariacompute ships.
export const LANGUAGES = [
  ['en', 'English'],
  ['zh', '简体中文'],
  ['zh-TW', '繁體中文'],
  ['es', 'Español'],
  ['fr', 'Français'],
  ['de', 'Deutsch'],
  ['it', 'Italiano'],
  ['ru', 'Русский'],
  ['ja', '日本語'],
  ['ko', '한국어'],
  ['pt', 'Português'],
  ['ar', 'العربية'],
  ['hi', 'हिन्दी'],
]

export const LANG_CODES = LANGUAGES.map(([code]) => code)

const HREFLANG_MAP = {
  en: 'en',
  zh: 'zh-Hans',
  'zh-TW': 'zh-Hant',
  es: 'es',
  fr: 'fr',
  de: 'de',
  it: 'it',
  ru: 'ru',
  ja: 'ja',
  ko: 'ko',
  pt: 'pt',
  ar: 'ar',
  hi: 'hi',
}

const OG_LOCALE_MAP = {
  en: 'en_US',
  zh: 'zh_CN',
  'zh-TW': 'zh_TW',
  es: 'es_ES',
  fr: 'fr_FR',
  de: 'de_DE',
  it: 'it_IT',
  ru: 'ru_RU',
  ja: 'ja_JP',
  ko: 'ko_KR',
  pt: 'pt_BR',
  ar: 'ar_SA',
  hi: 'hi_IN',
}

export function hreflangFor(code) {
  return HREFLANG_MAP[code] || code
}

export function ogLocaleFor(code) {
  return OG_LOCALE_MAP[code] || 'en_US'
}

export function isRTL(code) {
  return code === 'ar'
}

export function isValidLang(code) {
  return LANG_CODES.includes(code)
}

/** Maps a raw browser locale onto one of the supported codes. */
export function matchLang(raw) {
  if (!raw) return 'en'
  if (isValidLang(raw)) return raw
  const lower = raw.toLowerCase()
  if (lower.startsWith('zh')) {
    return lower.includes('tw') || lower.includes('hk') || lower.includes('hant') ? 'zh-TW' : 'zh'
  }
  const base = lower.split('-')[0]
  return isValidLang(base) ? base : 'en'
}
