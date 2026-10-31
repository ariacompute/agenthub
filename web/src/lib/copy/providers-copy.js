// Display names for external identity providers on the login screen.
//
// These are brand identifiers, not translatable prose, so they live in their own
// namespace outside the prose-equivalence test that applies to the *-copy.js
// files in tests/copy-fallback.spec.js. Each value is the provider's name as it
// should appear on the button; brands that have no localized form keep the
// international "WeChat".
export const providerCopy = {
  en: { wechat: 'WeChat' },
  zh: { wechat: '微信' },
  'zh-TW': { wechat: '微信' },
  es: { wechat: 'WeChat' },
  fr: { wechat: 'WeChat' },
  de: { wechat: 'WeChat' },
  it: { wechat: 'WeChat' },
  ru: { wechat: 'Вичат' },
  ja: { wechat: '微信' },
  ko: { wechat: '위챗' },
  pt: { wechat: 'WeChat' },
  ar: { wechat: 'ويشات' },
  hi: { wechat: 'वीचैट' },
}
