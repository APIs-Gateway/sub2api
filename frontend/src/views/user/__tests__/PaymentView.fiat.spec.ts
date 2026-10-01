import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import PaymentView from '../PaymentView.vue'
import { resetFiatDataMissingForTest, useCurrencyDisplay } from '@/composables/useCurrencyDisplay'

const routeState = vi.hoisted(() => ({ path: '/purchase', query: {} as Record<string, unknown> }))
const createOrder = vi.hoisted(() => vi.fn())
const getCheckoutInfo = vi.hoisted(() => vi.fn())
// 可变的假设置：改它就能模拟 codex 站（倍率 13）和 free 站（倍率 1）。
const publicSettings = vi.hoisted(() => ({ value: {} as Record<string, unknown> }))
// 当前生效的订阅卡：续费缺单价时回落到它的 fiat_per_credit。
const activeSubs = vi.hoisted(() => ({ value: [] as Array<Record<string, unknown>> }))

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return {
    ...actual,
    useRoute: () => routeState,
    useRouter: () => ({ replace: vi.fn(), push: vi.fn(), resolve: vi.fn(() => ({ href: '/x' })) }),
  }
})
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: { username: 'demo-user', balance: 650 }, refreshUser: vi.fn() }),
}))
vi.mock('@/stores/payment', () => ({ usePaymentStore: () => ({ createOrder }) }))
vi.mock('@/stores/subscriptions', () => ({
  useSubscriptionStore: () => ({
    get activeSubscriptions() {
      return activeSubs.value
    },
    fetchActiveSubscriptions: vi.fn().mockResolvedValue(undefined),
  }),
}))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    get cachedPublicSettings() {
      return publicSettings.value
    },
  }),
}))
vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError: vi.fn(), showInfo: vi.fn(), showWarning: vi.fn() }),
}))
vi.mock('@/api/payment', () => ({ paymentAPI: { getCheckoutInfo } }))
vi.mock('@/utils/device', () => ({ isMobileDevice: () => false }))

function checkoutInfo(multiplier: number, feeRate = 0, plans: unknown[] = []) {
  return {
    data: {
      methods: {
        alipay: {
          daily_limit: 0,
          daily_used: 0,
          daily_remaining: 0,
          single_min: 0,
          single_max: 0,
          fee_rate: feeRate,
          available: true,
          currency: 'CNY',
        },
      },
      global_min: 0,
      global_max: 0,
      plans,
      balance_disabled: false,
      balance_recharge_multiplier: multiplier,
      subscription_payment_multiplier: 1,
      recharge_fee_rate: feeRate,
      refund_fee_rate: 0,
      help_text: '',
      help_image_url: '',
      stripe_publishable_key: '',
    },
  }
}

async function mountTopUp(multiplier: number, feeRate = 0) {
  getCheckoutInfo.mockResolvedValue(checkoutInfo(multiplier, feeRate))
  const wrapper = mount(PaymentView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        // 金额输入：点按钮等价于用户输入 10（人民币，和下单金额同一单位）。
        AmountInput: {
          props: ['modelValue', 'amounts', 'min', 'max', 'currencyLabel', 'prefix'],
          emits: ['update:modelValue'],
          template: '<button data-test="amount-10" @click="$emit(\'update:modelValue\', 10)" />',
        },
        PaymentMethodSelector: true,
        CryptoNetworkSelector: true,
        SubscriptionPurchasePanel: true,
        PaymentStatusPanel: true,
        BillingRulesCard: true,
        Teleport: true,
        Transition: false,
      },
    },
  })
  await flushPromises()
  await flushPromises()
  const topUpTab = wrapper.findAll('button').find((button) => button.text() === 'payment.tabTopUp')
  await topUpTab!.trigger('click')
  await wrapper.get('[data-test="amount-10"]').trigger('click')
  await flushPromises()
  return wrapper
}

function plain(text: string): string {
  return text.replace(/\s+/g, '')
}

beforeEach(() => {
  window.localStorage.clear()
  resetFiatDataMissingForTest()
  // 展示口径是模块级单例，逐个用例复位。
  useCurrencyDisplay().setMode('fiat')
  activeSubs.value = []
  routeState.query = {}
  createOrder.mockReset().mockRejectedValue(new Error('stop after payload'))
})

describe('充值页按人民币展示', () => {
  it('付 ¥10 到账 ¥10：余额和到账都是人民币，不出现美元和倍率说明', async () => {
    publicSettings.value = { balance_recharge_multiplier: 13 }
    const wrapper = await mountTopUp(13)
    const text = plain(wrapper.text())

    // 余额 650 个额度 ÷ 13 = ¥50.00；输入 10 到账 130 个额度 = ¥10.00。
    expect(text).toContain('¥50.00')
    expect(text).toContain('payment.creditedBalance¥10.00')
    expect(text).not.toContain('$')
    expect(text).not.toContain('USD')
    expect(text).not.toContain('payment.rechargeMultiplier')
    expect(text).not.toContain('payment.rechargeRatePreview')
  })

  it('下单金额就是用户输入的人民币数，不做倍率换算', async () => {
    publicSettings.value = { balance_recharge_multiplier: 13 }
    const wrapper = await mountTopUp(13)

    const submit = wrapper.findAll('button').find((button) => button.text().startsWith('payment.createOrder'))
    expect(submit).toBeTruthy()
    await submit!.trigger('click')
    await flushPromises()

    expect(createOrder).toHaveBeenCalledTimes(1)
    expect(createOrder.mock.calls[0][0]).toMatchObject({ amount: 10, order_type: 'balance' })
    // 页面上展示的支付金额也是 ¥10.00，和下单金额一致。
    expect(plain(wrapper.text())).toContain('¥10.00')
  })

  it('有手续费时到账仍是输入的金额，实付才包含手续费', async () => {
    publicSettings.value = { balance_recharge_multiplier: 13 }
    const wrapper = await mountTopUp(13, 5)
    const text = plain(wrapper.text())

    expect(text).toContain('payment.creditedBalance¥10.00')
    expect(text).toContain('¥10.50')
  })

  it('美元模式下保持原来的美元到账和倍率说明', async () => {
    publicSettings.value = { balance_recharge_multiplier: 13 }
    useCurrencyDisplay().setMode('usd')
    const wrapper = await mountTopUp(13)
    const text = plain(wrapper.text())

    expect(text).toContain('USD130.00')
    expect(text).toContain('payment.rechargeMultiplier')
  })

  it('free 站（倍率 1）保持美元，不显示到账换算行', async () => {
    publicSettings.value = { balance_recharge_multiplier: 1 }
    const wrapper = await mountTopUp(1)
    const text = plain(wrapper.text())

    expect(text).toContain('$650.00')
    expect(text).not.toContain('payment.creditedBalance')
    expect(text).not.toContain('payment.rechargeMultiplier')
  })
})

describe('续费/转套餐结账页按人民币展示', () => {
  async function mountCheckout(multiplier: number) {
    routeState.query = {
      tab: 'subscription',
      intent: 'change_plan',
      daily_amount_usd: '90',
      validity_days: '30',
      charge: '72.60',
      unit_price: '0.045',
    }
    getCheckoutInfo.mockResolvedValue(checkoutInfo(multiplier))
    const wrapper = mount(PaymentView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          PaymentMethodSelector: true,
          CryptoNetworkSelector: true,
          SubscriptionPurchasePanel: true,
          PaymentStatusPanel: true,
          BillingRulesCard: true,
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()
    return plain(wrapper.text())
  }

  it('每日额度按新卡单价写成人民币，不显示美元的额度价值', async () => {
    publicSettings.value = { balance_recharge_multiplier: 13 }
    const text = await mountCheckout(13)

    // 90 × 0.045 = ¥4.05；实付 ¥72.60。
    expect(text).toContain('¥4.05')
    expect(text).toContain('¥72.60')
    expect(text).not.toContain('USD')
    expect(text).not.toContain('userSubscriptions.lifecycle.changeDiffValue')
  })

  it('free 站（倍率 1）保持美元额度', async () => {
    publicSettings.value = { balance_recharge_multiplier: 1 }
    const text = await mountCheckout(1)

    expect(text).toContain('USD90.00')
    expect(text).not.toContain('¥4.05')
  })
})

describe('续费/转套餐结账页：每日额度的单价来源', () => {
  async function mountLifecycle(query: Record<string, string>) {
    publicSettings.value = { balance_recharge_multiplier: 13 }
    routeState.query = { tab: 'subscription', daily_amount_usd: '90', validity_days: '30', charge: '72.60', ...query }
    getCheckoutInfo.mockResolvedValue(checkoutInfo(13))
    const wrapper = mount(PaymentView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          PaymentMethodSelector: true,
          CryptoNetworkSelector: true,
          SubscriptionPurchasePanel: true,
          PaymentStatusPanel: true,
          BillingRulesCard: true,
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()
    return { wrapper, text: plain(wrapper.text()) }
  }

  it('续费缺单价时沿用当前生效卡的单价：90 × 0.05 = ¥4.50', async () => {
    activeSubs.value = [{ id: 1, status: 'active', fiat_per_credit: 0.05 }]
    const { text } = await mountLifecycle({ intent: 'renew' })

    expect(text).toContain('userSubscriptions.lifecycle.renewTitle')
    expect(text).toContain('userSubscriptions.lifecycle.dailyAmount')
    expect(text).toContain('¥4.50')
    expect(text).not.toContain('USD')
  })

  it('续费缺单价且当前卡也没有单价时不显示每日额度', async () => {
    activeSubs.value = [{ id: 1, status: 'active' }]
    const { text } = await mountLifecycle({ intent: 'renew' })

    expect(text).toContain('¥72.60')
    expect(text).not.toContain('userSubscriptions.lifecycle.dailyAmount')
    expect(text).not.toContain('USD')
  })

  it('转套餐缺单价时不显示每日额度，也不借用当前卡的单价', async () => {
    activeSubs.value = [{ id: 1, status: 'active', fiat_per_credit: 0.05 }]
    const { text } = await mountLifecycle({ intent: 'change_plan' })

    expect(text).toContain('userSubscriptions.lifecycle.changeTitle')
    expect(text).not.toContain('userSubscriptions.lifecycle.dailyAmount')
    expect(text).not.toContain('¥4.50')
  })

  it('无效的 unit_price 按缺失处理，续费回落到当前卡单价', async () => {
    activeSubs.value = [{ id: 1, status: 'active', fiat_per_credit: 0.05 }]
    const { text } = await mountLifecycle({ intent: 'renew', unit_price: 'abc' })

    expect(text).toContain('¥4.50')
  })

  it('提交续费订单时额度仍按额度单位（90）上报，不会变成人民币', async () => {
    activeSubs.value = [{ id: 1, status: 'active', fiat_per_credit: 0.05 }]
    const { wrapper } = await mountLifecycle({ intent: 'renew', unit_price: '0.045' })
    expect(plain(wrapper.text())).toContain('¥4.05')

    const submit = wrapper.findAll('button').find((button) => button.text().startsWith('payment.createOrder'))
    await submit!.trigger('click')
    await flushPromises()

    expect(createOrder).toHaveBeenCalledTimes(1)
    expect(createOrder.mock.calls[0][0]).toMatchObject({
      amount: 72.6,
      order_type: 'subscription',
      subscription_intent: 'renew',
      daily_amount_usd: 90,
      validity_days: 30,
    })
  })
})

describe('固定套餐详情按人民币展示', () => {
  function plan(overrides: Record<string, unknown> = {}) {
    return {
      id: 7,
      group_id: 3,
      name: 'Starter',
      description: '',
      price: 90,
      original_price: 0,
      validity_days: 30,
      validity_unit: 'day',
      rate_multiplier: 2,
      daily_amount_usd: 10,
      daily_limit_usd: 10,
      weekly_limit_usd: 70,
      monthly_limit_usd: 300,
      features: [],
      group_platform: 'openai',
      sort_order: 1,
      for_sale: true,
      group_name: 'OpenAI',
      ...overrides,
    }
  }

  async function mountPlan(multiplier: number, planOverrides: Record<string, unknown> = {}) {
    publicSettings.value = { balance_recharge_multiplier: multiplier }
    routeState.query = { tab: 'subscription', group: '3' }
    getCheckoutInfo.mockResolvedValue(checkoutInfo(multiplier, 0, [plan(planOverrides)]))
    const wrapper = mount(PaymentView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          PaymentMethodSelector: true,
          CryptoNetworkSelector: true,
          SubscriptionPurchasePanel: true,
          PaymentStatusPanel: true,
          BillingRulesCard: true,
          Teleport: true,
          Transition: false,
        },
      },
    })
    await flushPromises()
    await flushPromises()
    return plain(wrapper.text())
  }

  it('日/周/月限额按套餐实付折算成人民币并标注等效，不显示倍率和美元价值', async () => {
    const text = await mountPlan(13)

    // 实付 ¥90 ÷（每日 10 × 30 天）= ¥0.3 / 额度。
    expect(text).toContain('¥3.00')
    expect(text).toContain('¥21.00')
    expect(text).toContain('¥90.00')
    expect(text).toContain('payment.planCard.equivalentCny')
    expect(text).not.toContain('payment.planCard.rate')
    expect(text).not.toContain('payment.subscriptionValueWithCurrency')
    expect(text).not.toContain('USD')
  })

  it('套餐没有每日额度无法折算时隐藏各项限额，不混入美元', async () => {
    const text = await mountPlan(13, { daily_amount_usd: null, daily_limit_usd: null })

    expect(text).not.toContain('payment.planCard.weeklyLimit')
    expect(text).not.toContain('payment.planCard.monthlyLimit')
    expect(text).not.toContain('payment.planCard.equivalentCny')
    expect(text).not.toContain('USD')
  })

  it('free 站（倍率 1）保持美元额度和倍率', async () => {
    useCurrencyDisplay().setMode('usd')
    const text = await mountPlan(1)

    expect(text).toContain('USD10.00')
    expect(text).toContain('USD70.00')
    expect(text).toContain('payment.planCard.rate')
    expect(text).not.toContain('payment.planCard.equivalentCny')
  })
})
