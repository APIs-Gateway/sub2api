import type { LocaleCode } from '@/types'

export function normalizeLocaleCode(value: unknown): LocaleCode | null {
  if (typeof value !== 'string') {
    return null
  }

  switch (value.trim()) {
    case 'zh-CN':
      return 'zh-CN'
    case 'zh-HK':
      return 'zh-HK'
    case 'en':
      return 'en'
    case 'zh':
      return 'zh-CN'
    default:
      return null
  }
}

export function isChineseLocale(locale: unknown): boolean {
  const normalizedLocale = normalizeLocaleCode(locale)
  return normalizedLocale === 'zh-CN' || normalizedLocale === 'zh-HK'
}
