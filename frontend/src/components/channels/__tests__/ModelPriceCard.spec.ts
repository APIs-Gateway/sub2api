import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ModelPriceCard from '../ModelPriceCard.vue'
import { useCurrencyDisplay } from '@/composables/useCurrencyDisplay'

// 可变的假设置：默认倍率缺失（= 1，按美元展示），与历史断言保持一致。
const publicSettings: { value: Record<string, unknown> | null } = { value: null }

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    get cachedPublicSettings() {
      return publicSettings.value
    },
  }),
}))

vi.mock('@/i18n', () => ({
  getLocale: () => 'zh-CN',
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: { rate?: string }) => ({
      'availableChannels.pricing.inputPrice': 'Input',
      'availableChannels.pricing.outputPrice': 'Output',
      'availableChannels.pricing.cacheReadPrice': 'Cache read',
      'availableChannels.pricing.cacheWritePrice': 'Cache write',
      'availableChannels.pricing.intervals': 'Context pricing',
      'availableChannels.pricing.unitPerMillion': '/ 1M tokens',
      'availableChannels.pricing.unitPerRequest': '/ request',
      'availableChannels.pricing.billingModeToken': 'Token',
      'availableChannels.effectiveTitle': `Effective ${params?.rate}x`,
      'availableChannels.fiat.balancePrice': 'Balance price',
      'availableChannels.fiat.subscriptionPrice': 'Plan price',
      'availableChannels.fiat.yourSubscriptionPrice': 'Your plan price',
      'availableChannels.fiat.officialPrice': 'Official',
    }[key] ?? key),
  }),
}))

describe('ModelPriceCard', () => {
  beforeEach(() => {
    publicSettings.value = null
    window.localStorage.clear()
    useCurrencyDisplay().setMode('fiat')
  })

  it('renders all four official GPT-5.6 long-context price dimensions', () => {
    const wrapper = mount(ModelPriceCard, {
      props: {
        rateMultiplier: 2,
        model: {
          name: 'gpt-5.6-terra',
          platform: 'openai',
          pricing: {
            billing_mode: 'token',
            input_price: 2.5e-6,
            output_price: 15e-6,
            cache_read_price: 0.25e-6,
            cache_write_price: 3.125e-6,
            image_output_price: null,
            per_request_price: null,
            intervals: [
              {
                min_tokens: 0,
                max_tokens: 272000,
                input_price: 2.5e-6,
                output_price: 15e-6,
                cache_read_price: 0.25e-6,
                cache_write_price: 3.125e-6,
                per_request_price: null,
              },
              {
                min_tokens: 272000,
                max_tokens: null,
                input_price: 5e-6,
                output_price: 22.5e-6,
                cache_read_price: 0.5e-6,
                cache_write_price: 6.25e-6,
                per_request_price: null,
              },
            ],
          },
        },
      },
      global: {
        stubs: {
          PlatformIcon: true,
          PricingRow: {
            props: ['label', 'value', 'unit'],
            template: '<div>{{ label }} {{ value }} {{ unit }}</div>',
          },
        },
      },
    })

    const content = wrapper.text()
    expect(content).toContain('Context pricing')
    expect(content).toContain('Cache read $0.5')
    expect(content).toContain('Cache write $6.25')
    expect(content).toContain('Cache read $1')
    expect(content).toContain('Cache write $12.5')
  })

  describe('人民币口径', () => {
    const tokenModel = {
      name: 'gpt-test',
      platform: 'openai',
      pricing: {
        billing_mode: 'token',
        input_price: 2e-6,
        output_price: 10e-6,
        cache_read_price: null,
        cache_write_price: null,
        image_output_price: null,
        per_request_price: null,
        intervals: [],
      },
    }

    function mountCard(props: Record<string, unknown>) {
      return mount(ModelPriceCard, {
        props: { model: tokenModel, ...props } as any,
        global: { stubs: { PlatformIcon: true } },
      })
    }

    const plain = (text: string) => text.replace(/[\u00a0\u202f]/g, ' ')

    it('余额价 = 官方价 × 分组倍率 ÷ 充值倍率，官方美元价只作参考', () => {
      publicSettings.value = { balance_recharge_multiplier: 10 }
      const wrapper = mountCard({ rateMultiplier: 2 })

      // 输入 $2/M × 2 ÷ 10 = ¥0.40；输出 $10/M × 2 ÷ 10 = ¥2.00
      const balance = plain(wrapper.get('[data-test="balance-prices"]').text())
      expect(balance).toContain('0.400')
      expect(balance).toContain('2.00')
      expect(balance).not.toContain('$')
      expect(plain(wrapper.get('[data-test="official-prices"]').text())).toContain('$2')
      expect(wrapper.find('[data-test="subscription-prices"]').exists()).toBe(false)
    })

    it('套餐价按卡单价区间展示；有生效卡时是精确值', () => {
      publicSettings.value = { balance_recharge_multiplier: 10 }
      const range = mountCard({ rateMultiplier: 2, subscriptionUnit: { min: 0.04, max: 0.05, exact: false } })
      // 输入 $2/M × 2 × 0.04–0.05 = ¥0.16–¥0.20
      const rangeText = plain(range.get('[data-test="subscription-prices"]').text())
      expect(rangeText).toContain('Plan price')
      expect(rangeText).toContain('0.160')
      expect(rangeText).toContain('0.200')

      const exact = mountCard({ rateMultiplier: 2, subscriptionUnit: { min: 0.05, max: 0.05, exact: true } })
      const exactText = plain(exact.get('[data-test="subscription-prices"]').text())
      expect(exactText).toContain('Your plan price')
      expect(exactText).not.toContain('–')
    })

    it('倍率为 1 时保持美元展示', () => {
      publicSettings.value = { balance_recharge_multiplier: 1 }
      const wrapper = mountCard({ rateMultiplier: 2, subscriptionUnit: { min: 0.04, max: 0.05, exact: false } })

      expect(wrapper.find('[data-test="balance-prices"]').exists()).toBe(false)
      expect(wrapper.text()).toContain('$')
    })
  })
})
