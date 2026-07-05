import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import SubscriptionLifecycleDialog from '../SubscriptionLifecycleDialog.vue'
import type { UserSubscription } from '@/types'

const getSubscriptionPricing = vi.hoisted(() => vi.fn())
const changePlanQuote = vi.hoisted(() => vi.fn())
const renewQuote = vi.hoisted(() => vi.fn())
const showError = vi.hoisted(() => vi.fn())

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        if (key === 'userSubscriptions.lifecycle.caps') return `caps ${params?.weekly} ${params?.monthly}`
        return key
      },
    }),
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
  }),
}))

vi.mock('@/api/subscriptions', () => ({
  default: {
    getSubscriptionPricing,
    changePlanQuote,
    renewQuote,
  },
}))

function subscriptionFixture(): UserSubscription {
  return {
    id: 376,
    user_id: 345,
    status: 'active',
    daily_amount_usd: 90,
    daily_limit_usd: 90,
    weekly_limit_usd: 630,
    monthly_limit_usd: 2700,
    starts_at: '2026-07-04T00:00:00.000Z',
    expires_at: '2026-08-02T00:00:00.000Z',
  } as unknown as UserSubscription
}

describe('SubscriptionLifecycleDialog', () => {
  beforeEach(() => {
    getSubscriptionPricing.mockReset().mockResolvedValue({
      d_min: 30,
      d_max: 300,
      t_min: 30,
      t_max: 360,
      t_step: 30,
    })
    changePlanQuote.mockReset().mockResolvedValue({
      diff: 72.60000000000001,
      new_plan_price: 2700,
      old_remaining_value: 2627.4,
      weekly_cap_usd: 630,
      monthly_cap_usd: 2700,
    })
    renewQuote.mockReset()
    showError.mockReset()
  })

  it('loads bounds and quote on the first open when mounted with show=true', async () => {
    const wrapper = mount(SubscriptionLifecycleDialog, {
      props: {
        show: true,
        mode: 'change',
        subscription: subscriptionFixture(),
        paymentCurrency: 'CNY',
        subscriptionPaymentMultiplier: 10,
        locale: 'zh-CN',
      },
      global: {
        stubs: {
          BaseDialog: {
            template: '<section><slot /><footer><slot name="footer" /></footer></section>',
          },
        },
      },
    })

    await flushPromises()
    await flushPromises()

    expect(getSubscriptionPricing).toHaveBeenCalledTimes(1)
    expect(changePlanQuote).toHaveBeenCalledWith(90, 30)
    expect(wrapper.text()).toContain('userSubscriptions.lifecycle.dailyAmount')
    expect(wrapper.text()).toContain('¥7.26')
    expect(wrapper.text()).toContain('$72.60')
    expect(wrapper.text()).not.toContain('USD 72.60')
  })
})
