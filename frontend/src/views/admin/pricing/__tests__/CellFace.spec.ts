import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { ref } from 'vue'
import { resetFiatDataMissingForTest } from '@/composables/useCurrencyDisplay'
import { useAppStore } from '@/stores/app'
import type { CellView } from '../pricingModel'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const { default: zhCN } = await import('@/i18n/locales/zh-CN')
  const translate = (key: string, params: Record<string, unknown> = {}) => {
    const hit = key.split('.').reduce<unknown>((o, k) => (o as Record<string, unknown> | undefined)?.[k], zhCN)
    if (typeof hit !== 'string') return key
    return hit.replace(/\{(\w+)\}/g, (_, name) => String(params[name] ?? `{${name}}`))
  }
  return { ...actual, useI18n: () => ({ locale: ref('zh-CN'), t: translate }) }
})

import CellFace from '../components/CellFace.vue'

const base: CellView = {
  kind: 'custom',
  unswitched: false,
  usd: null,
  perRequestUsd: null,
  perRequestRange: null,
  extra: null,
  reason: null
}

function mountCell(view: CellView) {
  const pinia = createPinia()
  setActivePinia(pinia)
  resetFiatDataMissingForTest()
  window.localStorage.removeItem('currency-display-mode')
  useAppStore().cachedPublicSettings = { balance_recharge_multiplier: 1, official_price_cny_rate: 7 } as never
  return mount(CellFace, { props: { view }, global: { plugins: [pinia] } })
}

describe('CellFace 按次区间价', () => {
  it('只有区间价：显示 a–b，不出现 0', () => {
    const w = mountCell({ ...base, perRequestRange: { min: 1.8, max: 2.5 } })
    const text = w.find('.cell-price').text()
    expect(text).toContain('$1.80')
    expect(text).toContain('$2.50')
    expect(text).toContain('–')
    expect(text).not.toContain('$0.00')
  })

  it('区间两端相同：只显示一个价', () => {
    const w = mountCell({ ...base, perRequestRange: { min: 1.8, max: 1.8 } })
    const text = w.find('.cell-price').text()
    expect(text).toContain('$1.80')
    expect(text).not.toContain('–')
  })
})
