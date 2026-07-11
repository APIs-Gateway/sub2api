import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ModelPriceCard from '../ModelPriceCard.vue'

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
    }[key] ?? key),
  }),
}))

describe('ModelPriceCard', () => {
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
})
