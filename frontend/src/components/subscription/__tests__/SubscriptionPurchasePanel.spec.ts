import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import SubscriptionPurchasePanel from '../SubscriptionPurchasePanel.vue'
import zhLocale from '@/i18n/locales/zh'
import subscriptionsAPI from '@/api/subscriptions'

vi.mock('@/api/subscriptions', () => ({
  default: {
    getSubscriptionPricing: vi.fn(),
    quoteSubscription: vi.fn(),
  },
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

describe('SubscriptionPurchasePanel', () => {
  beforeEach(() => {
    vi.mocked(subscriptionsAPI.getSubscriptionPricing).mockReset().mockResolvedValue({
      d_min: 30,
      d_max: 3000,
      d_step: 30,
      t_min: 30,
      t_max: 360,
      t_step: 30,
    })
    vi.mocked(subscriptionsAPI.quoteSubscription).mockReset().mockResolvedValue({
      price: 123456789.12,
      unit_price: 0.1,
      weekly_cap_usd: 210,
      monthly_cap_usd: 900,
    })
  })

  it('uses a concise Chinese price label', () => {
    expect(zhLocale.subscriptionPurchase.priceWithCurrency).toBe('\u4ef7\u683c')
  })

  it('constrains the formatted price so long mobile amounts can wrap inside the card', async () => {
    const wrapper = mount(SubscriptionPurchasePanel, {
      props: {
        paymentCurrency: 'HKD',
        locale: 'zh-CN',
      },
    })

    await flushPromises()

    const priceValue = wrapper.get('[data-testid="subscription-purchase-price-value"]')
    expect(priceValue.text()).toContain('123,456,789.12')
    expect(priceValue.classes()).toEqual(
      expect.arrayContaining(['block', 'w-full', 'max-w-full', 'break-all', 'text-right'])
    )
  })
})
