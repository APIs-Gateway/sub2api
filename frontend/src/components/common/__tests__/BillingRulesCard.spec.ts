import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import BillingRulesCard from '../BillingRulesCard.vue'
import { useCurrencyDisplay } from '@/composables/useCurrencyDisplay'

const publicSettings: { value: Record<string, unknown> | null } = { value: null }

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    get cachedPublicSettings() {
      return publicSettings.value
    },
  }),
}))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

function bullets(modelsBelow: boolean): string[] {
  const w = mount(BillingRulesCard, { props: { modelsBelow } })
  return w.findAll('li').map((li) => li.text().replace('•', '').trim())
}

describe('BillingRulesCard', () => {
  beforeEach(() => {
    window.localStorage.clear()
  })

  describe('fiat mode', () => {
    beforeEach(() => {
      publicSettings.value = { balance_recharge_multiplier: 13 }
      useCurrencyDisplay().setMode('fiat')
    })

    it('points to the pricing page and does not mention expanding models on pages without a model list', () => {
      expect(bullets(false)).toEqual([
        'billingRules.fiat.pricesLink',
        'billingRules.fiat.rate',
        'billingRules.fiat.plan',
        'billingRules.fiat.groupBrief',
      ])
    })

    it('tells users to expand a model on the pricing page itself', () => {
      expect(bullets(true)).toEqual([
        'billingRules.fiat.rate',
        'billingRules.fiat.plan',
        'billingRules.fiat.group',
      ])
    })
  })

  describe('usd mode', () => {
    beforeEach(() => {
      publicSettings.value = null
    })

    it('links to the pricing page, or points at the models below', () => {
      expect(bullets(false)).toEqual(['billingRules.modelPriceLink', 'billingRules.rate', 'billingRules.plan'])
      expect(bullets(true)).toEqual(['billingRules.modelPriceHere', 'billingRules.rate', 'billingRules.plan'])
    })
  })
})
