import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zhCN from '../locales/zh-CN'
import zhHK from '../locales/zh-HK'
import { completeLocaleMessages } from '../index'

type LocaleObject = Record<string, unknown>

function collectLeafKeys(value: unknown, prefix = ''): string[] {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return prefix ? [prefix] : []
  }

  return Object.entries(value as LocaleObject).flatMap(([key, child]) =>
    collectLeafKeys(child, prefix ? `${prefix}.${key}` : key)
  )
}

function expectSameKeys(baseName: string, base: LocaleObject, candidateName: string, candidate: LocaleObject) {
  const baseKeys = collectLeafKeys(base).sort()
  const candidateKeys = collectLeafKeys(candidate).sort()

  expect(candidateKeys, `${candidateName} locale keys should match ${baseName}`).toEqual(baseKeys)
}

describe('locale key coverage', () => {
  it('keeps zh-CN and zh-HK source leaf key sets aligned', () => {
    expectSameKeys('zh-CN', zhCN, 'zh-HK', zhHK)
  })

  it('keeps en, zh-CN, and zh-HK effective leaf key sets aligned after fallback completion', () => {
    const effectiveEn = completeLocaleMessages(en, [zhCN])
    const effectiveZhCN = completeLocaleMessages(zhCN, [en])
    const effectiveZhHK = completeLocaleMessages(zhHK, [en, zhCN])

    expectSameKeys('en', effectiveEn, 'zh-CN', effectiveZhCN)
    expectSameKeys('zh-CN', effectiveZhCN, 'zh-HK', effectiveZhHK)
  })
})
