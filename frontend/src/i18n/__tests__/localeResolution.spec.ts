import { describe, expect, it } from 'vitest'

import {
  DEFAULT_LOCALE,
  LOCALE_KEY,
  normalizeLocaleCode,
  resolveInitialLocale
} from '../index'

describe('i18n locale resolution', () => {
  it('uses simplified Chinese as the built-in fallback', () => {
    expect(DEFAULT_LOCALE).toBe('zh-CN')
    expect(LOCALE_KEY).toBe('sub2api_locale')
    expect(resolveInitialLocale({ storedLocale: null, publicDefaultLocale: null }).locale).toBe('zh-CN')
  })

  it('uses the admin-configured public default when no local preference exists', () => {
    expect(resolveInitialLocale({ storedLocale: null, publicDefaultLocale: 'zh-HK' }).locale).toBe('zh-HK')
    expect(resolveInitialLocale({ storedLocale: null, publicDefaultLocale: 'en' }).locale).toBe('en')
  })

  it('keeps a local user preference ahead of the admin default', () => {
    expect(resolveInitialLocale({ storedLocale: 'en', publicDefaultLocale: 'zh-HK' }).locale).toBe('en')
  })

  it('migrates the legacy zh locale to zh-CN', () => {
    const resolved = resolveInitialLocale({ storedLocale: 'zh', publicDefaultLocale: 'zh-HK' })

    expect(resolved.locale).toBe('zh-CN')
    expect(resolved.persistLocale).toBe('zh-CN')
    expect(normalizeLocaleCode('zh')).toBe('zh-CN')
  })

  it('ignores invalid stored and public locale values', () => {
    const resolved = resolveInitialLocale({ storedLocale: 'fr', publicDefaultLocale: 'ja' })

    expect(resolved.locale).toBe('zh-CN')
    expect(resolved.clearStoredLocale).toBe(true)
  })
})
