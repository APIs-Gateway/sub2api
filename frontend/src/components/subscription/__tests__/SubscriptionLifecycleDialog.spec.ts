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

// 可变的假设置：默认没有充值倍率（按美元展示，旧行为）；人民币用例里改成 13。
const publicSettings = vi.hoisted(() => ({ value: {} as Record<string, unknown> }))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    get cachedPublicSettings() {
      return publicSettings.value
    },
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
    publicSettings.value = {}
    window.localStorage.clear()
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
  function mountDialog(mode: 'renew' | 'change') {
    return mount(SubscriptionLifecycleDialog, {
      props: {
        show: true,
        mode,
        subscription: { ...subscriptionFixture(), fiat_per_credit: 0.045 } as UserSubscription,
        paymentCurrency: 'CNY',
        subscriptionPaymentMultiplier: 1,
        locale: 'zh-CN',
      },
      global: {
        stubs: {
          BaseDialog: {
            template: '<section><slot /><footer><slot name="footer" /></footer></section>',
          },
          NumText: false,
        },
      },
    })
  }

  const changeQuote = {
    diff: 72.6,
    new_plan_price: 2700,
    old_remaining_value: 2627.4,
    weekly_cap_usd: 630,
    monthly_cap_usd: 2700,
    unit_price: 0.045,
  }

  it('人民币模式下转套餐的每日额度、封顶都写成人民币，不再出现美元和额度价值行', async () => {
    publicSettings.value = { balance_recharge_multiplier: 13 }
    changePlanQuote.mockResolvedValue(changeQuote)
    const wrapper = mountDialog('change')
    await flushPromises()
    await flushPromises()
    const text = wrapper.text().replace(/\s+/g, '')

    // 每日 90 × 0.045 = 4.05；封顶 630 × 0.045 = 28.35、2700 × 0.045 = 121.50。
    expect(text).toContain('¥4.05')
    expect(text).toContain('caps¥28.35¥121.50')
    expect(text).toContain('¥72.60')
    expect(text).not.toContain('$')
    expect(text).not.toContain('USD')
    expect(text).not.toContain('userSubscriptions.lifecycle.changeDiffValue')
    expect(wrapper.find('input[type="number"]').exists()).toBe(false)
  })

  it('人民币模式下续费的每日额度按当前卡单价折算，且不显示额度价值行', async () => {
    publicSettings.value = { balance_recharge_multiplier: 13 }
    renewQuote.mockResolvedValue({
      subscription_id: 376,
      daily_amount_usd: 90,
      added_days: 30,
      price: 121.5,
      unit_price: 0.045,
      group_id: 1,
    })
    const wrapper = mountDialog('renew')
    await flushPromises()
    await flushPromises()
    const text = wrapper.text().replace(/\s+/g, '')

    expect(text).toContain('¥4.05')
    expect(text).toContain('¥121.50')
    expect(text).not.toContain('$')
    expect(text).not.toContain('userSubscriptions.lifecycle.renewValue')
  })

  it('free 站（倍率 1）保持美元和额度输入框', async () => {
    publicSettings.value = { balance_recharge_multiplier: 1 }
    changePlanQuote.mockResolvedValue(changeQuote)
    const wrapper = mountDialog('change')
    await flushPromises()
    await flushPromises()
    const text = wrapper.text().replace(/\s+/g, '')

    expect(text).toContain('caps$630.00$2,700.00')
    expect(text).not.toContain('¥4.05')
    expect(wrapper.find('input[type="number"]').exists()).toBe(true)
  })

  async function confirmAndGetPayload(wrapper: ReturnType<typeof mountDialog>) {
    const goPay = wrapper.findAll('button').find((b) => b.text() === 'userSubscriptions.lifecycle.goPay')
    expect(goPay!.attributes('disabled')).toBeUndefined()
    await goPay!.trigger('click')
    return wrapper.emitted('purchase')![0][0] as Record<string, unknown>
  }

  it('转套餐去结账时把报价单价带给结账页，额度仍是额度单位', async () => {
    publicSettings.value = { balance_recharge_multiplier: 13 }
    changePlanQuote.mockResolvedValue(changeQuote)
    const wrapper = mountDialog('change')
    await flushPromises()
    await flushPromises()

    const payload = await confirmAndGetPayload(wrapper)
    expect(payload).toMatchObject({ intent: 'change_plan', dailyAmountUsd: 90, charge: 72.6, unitPrice: 0.045 })
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('续费去结账时把报价单价带给结账页', async () => {
    publicSettings.value = { balance_recharge_multiplier: 13 }
    renewQuote.mockResolvedValue({
      subscription_id: 376,
      daily_amount_usd: 90,
      added_days: 30,
      price: 121.5,
      unit_price: 0.045,
      group_id: 1,
    })
    const wrapper = mountDialog('renew')
    await flushPromises()
    await flushPromises()

    const payload = await confirmAndGetPayload(wrapper)
    expect(payload).toMatchObject({ intent: 'renew', dailyAmountUsd: 90, charge: 121.5, unitPrice: 0.045 })
  })

  it('报价没有单价且当前卡也没有单价时，每日额度先显示 ¥0.00 而不是美元', async () => {
    publicSettings.value = { balance_recharge_multiplier: 13 }
    changePlanQuote.mockResolvedValue({ ...changeQuote, unit_price: undefined })
    const wrapper = mount(SubscriptionLifecycleDialog, {
      props: {
        show: true,
        mode: 'change',
        subscription: subscriptionFixture(),
        paymentCurrency: 'CNY',
        subscriptionPaymentMultiplier: 1,
        locale: 'zh-CN',
      },
      global: {
        stubs: {
          BaseDialog: { template: '<section><slot /><footer><slot name="footer" /></footer></section>' },
          NumText: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()
    const text = wrapper.text().replace(/\s+/g, '')

    expect(text).toContain('¥0.00')
    expect(text).not.toContain('$')
    expect(text).not.toContain('¥4.05')
  })
})
