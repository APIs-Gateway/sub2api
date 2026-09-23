import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zhCN from '../locales/zh-CN'
import zhHK from '../locales/zh-HK'

type LocaleValue = Record<string, unknown>

const locales: Record<string, LocaleValue> = {
  en: en as LocaleValue,
  'zh-CN': zhCN as LocaleValue,
  'zh-HK': zhHK as LocaleValue
}

// Keys that are referenced statically in source but intentionally resolved at
// runtime (e.g. placeholder strings built from a dynamic suffix). Keep this list
// short and justify every entry.
const ALLOWED_MISSING_KEYS = new Set<string>([])

// Leaves that are intentionally blank in every locale so the UI renders nothing
// (the purchase page header description and the subscription cap hint were
// blanked on purpose by the billing rework).
const ALLOWED_EMPTY_KEYS = new Set<string>(['purchase.description', 'subscriptionPurchase.capHint'])

function flattenLeafKeys(value: unknown, prefix = ''): string[] {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return prefix ? [prefix] : []
  }

  return Object.entries(value as LocaleValue).flatMap(([key, child]) => {
    const path = prefix ? `${prefix}.${key}` : key
    return flattenLeafKeys(child, path)
  })
}

function collectStaticSourceKeys(source: string): string[] {
  const keys = new Set<string>()

  // Covers useI18n().t(), the global $t(), and direct i18n.t() calls in both
  // TypeScript and Vue script/template source. Dynamic suffixes are checked by
  // the runtime type of the value and cannot be proven from source text alone.
  const translationCalls = /(?:\bi18n\.t|\$t|\bt)\s*\(\s*(['"])([^'"\r\n]+)\1/g
  for (const match of source.matchAll(translationCalls)) {
    const key = match[2]
    if (!key.endsWith('.')) {
      keys.add(key)
    }
  }

  // Router metadata and i18n-t are key references without a t() call.
  const keyReferences = /(?:keypath|titleKey|descriptionKey)\s*[:=]\s*(['"])([^'"\r\n]+)\1/g
  for (const match of source.matchAll(keyReferences)) {
    keys.add(match[2])
  }

  const i18nTKeypaths = /<i18n-t\b[^>]*\bkeypath\s*=\s*(['"])([^'"\r\n]+)\1/gi
  for (const match of source.matchAll(i18nTKeypaths)) {
    keys.add(match[2])
  }

  return [...keys]
}

function sourceKeys(): string[] {
  const sourceFiles = import.meta.glob('../../**/*.{ts,vue}', {
    query: '?raw',
    import: 'default',
    eager: true
  }) as Record<string, string>

  return Object.entries(sourceFiles)
    .filter(([path]) => !path.includes('/__tests__/') && !/\.(spec|test)\.ts$/.test(path))
    .flatMap(([, source]) => collectStaticSourceKeys(source))
}

function missingKeys(usedKeys: string[], availableKeys: Set<string>): string[] {
  return usedKeys.filter((key) => !availableKeys.has(key) && !ALLOWED_MISSING_KEYS.has(key)).sort()
}

describe('locale key completeness', () => {
  const usedKeys = [...new Set(sourceKeys())].sort()

  it('finds statically referenced keys in production source', () => {
    expect(usedKeys.length).toBeGreaterThan(100)
  })

  it('contains a non-empty message for every locale leaf', () => {
    for (const [locale, messages] of Object.entries(locales)) {
      const emptyKeys = flattenLeafKeys(messages).filter((key) => {
        let current: unknown = messages
        for (const segment of key.split('.')) {
          current = (current as LocaleValue)[segment]
        }
        if (typeof current !== 'string') {
          return true
        }
        return current.trim() === '' && !ALLOWED_EMPTY_KEYS.has(key)
      })
      expect(emptyKeys, `${locale} has empty or non-string messages`).toEqual([])
    }
  })

  for (const [locale, messages] of Object.entries(locales)) {
    it(`${locale} contains every statically referenced production key`, () => {
      const available = new Set(flattenLeafKeys(messages))
      expect(missingKeys(usedKeys, available), `${locale} locale is missing referenced keys`).toEqual([])
    })
  }
})
