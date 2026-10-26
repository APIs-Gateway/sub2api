import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import SubscriptionPurchasePanel from '../SubscriptionPurchasePanel.vue'
import subscriptionsAPI from '@/api/subscriptions'
import { userGroupsAPI } from '@/api/groups'
import { resetPlanPricingForTest } from '@/composables/useRateDisplay'
import { resetFiatDataMissingForTest, useCurrencyDisplay } from '@/composables/useCurrencyDisplay'

// 可变的假设置：改它就能模拟 codex 站（倍率 13）和 free 站（倍率 1）。
const publicSettings: { value: Record<string, unknown> } = { value: {} }

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    get cachedPublicSettings() {
      return publicSettings.value
    }
  })
}))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      // 倍率值带参数，其余键原样返回；这样能断言页面上的 `0.056x`。
      t: (key: string, params?: Record<string, unknown>) =>
        key === 'subscriptionPurchase.rateValue' ? `${params?.rate}x` : key
    })
  }
})
vi.mock('@/api/groups', () => ({
  userGroupsAPI: {
    getAvailable: vi.fn(),
    getUserGroupRates: vi.fn()
  }
}))
vi.mock('@/api/subscriptions', () => ({
  default: {
    getSubscriptionPricing: vi.fn(),
    quoteSubscription: vi.fn()
  }
}))

// 去掉空白，方便断言金额。
function plain(text: string): string {
  return text.replace(/\s+/g, '')
}

beforeEach(() => {
  setActivePinia(createPinia())
  resetPlanPricingForTest()
  window.localStorage.clear()
  resetFiatDataMissingForTest()
  // 展示口径是模块级单例，逐个用例复位。
  useCurrencyDisplay().setMode('fiat')
  publicSettings.value = { balance_recharge_multiplier: 13 }
})

describe('购买面板按人民币展示', () => {
  beforeEach(() => {
    vi.mocked(subscriptionsAPI.getSubscriptionPricing).mockReset().mockResolvedValue({
      d_min: 30,
      d_max: 3000,
      u_min: 0.04,
      u_max: 0.05,
      t_min: 30,
      t_max: 360,
      t_step: 30
    })
    vi.mocked(subscriptionsAPI.quoteSubscription).mockReset().mockResolvedValue({
      daily_amount_usd: 30,
      validity_days: 30,
      price: 40.5,
      unit_price: 0.045,
      weekly_cap_usd: 210,
      monthly_cap_usd: 900,
      formula_version: 1
    })
  })

  async function mountPanel() {
    const wrapper = mount(SubscriptionPurchasePanel, { props: { paymentCurrency: 'CNY', locale: 'zh-CN' } })
    await flushPromises()
    return wrapper
  }

  it('每日额度和周/月封顶写成人民币，不再出现 USD 或额度数字输入框', async () => {
    const wrapper = await mountPanel()
    const text = plain(wrapper.text())

    // 30 × 0.045 = 1.35；210 × 0.045 = 9.45；900 × 0.045 = 40.50。
    expect(plain(wrapper.get('[data-testid="subscription-purchase-daily-fiat"]').text())).toContain('¥1.35')
    expect(text).toContain('¥9.45')
    expect(text).toContain('¥40.50')
    expect(text).not.toContain('USD')
    expect(wrapper.find('input[type="number"]').exists()).toBe(false)
  })

  it('free 站保持美元：封顶写 $，保留额度输入框', async () => {
    publicSettings.value = { balance_recharge_multiplier: 1 }
    const wrapper = await mountPanel()
    const text = plain(wrapper.text())

    expect(text).toContain('$210.00')
    expect(text).not.toMatch(/USD\d/)
    expect(wrapper.find('[data-testid="subscription-purchase-daily-fiat"]').exists()).toBe(false)
    expect(wrapper.find('input[type="number"]').exists()).toBe(true)
    expect(text).not.toContain('subscriptionPurchase.unitPrice')
    expect(text).not.toContain('×0.0450')
  })

  it('美元模式写每日额度和封顶，不写每刀单价', async () => {
    useCurrencyDisplay().setMode('usd')
    const wrapper = await mountPanel()
    const text = plain(wrapper.text())

    // 报价区第一格是每日额度（$30.00），和周/月封顶（$210.00、$900.00）同一口径。
    expect(text).toContain('subscriptionPurchase.dailyAmount')
    expect(text).toContain('$30.00')
    expect(text).toContain('$210.00')
    expect(text).toContain('$900.00')
    expect(text).not.toMatch(/USD\d/)
    expect(text).not.toContain('subscriptionPurchase.unitPrice')
    expect(text).not.toContain('×0.0450')
    expect(wrapper.find('input[type="number"]').exists()).toBe(true)
  })
})

describe('购买面板：开通后各分组的倍率', () => {
  const group = (id: number, name: string, rate: number, extra: Record<string, unknown> = {}) => ({
    id,
    name,
    rate_multiplier: rate,
    platform: 'openai',
    status: 'active',
    subscription_type: 'standard',
    ...extra
  })
  const quoteWith = (d: number, unit: number) => ({
    daily_amount_usd: d,
    validity_days: 30,
    price: d * unit * 30,
    unit_price: unit,
    weekly_cap_usd: d * 7,
    monthly_cap_usd: d * 30,
    formula_version: 1
  })

  beforeEach(() => {
    vi.useRealTimers()
    vi.mocked(subscriptionsAPI.getSubscriptionPricing).mockReset().mockResolvedValue({
      d_min: 30,
      d_max: 3000,
      u_min: 0.04,
      u_max: 0.05,
      t_min: 30,
      t_max: 360,
      t_step: 30
    })
    // 默认档位 D=30，单价 0.05；滑到 210 档后单价 0.04。
    vi.mocked(subscriptionsAPI.quoteSubscription)
      .mockReset()
      .mockImplementation(async (d: number) => quoteWith(d, d >= 210 ? 0.04 : 0.05))
    vi.mocked(userGroupsAPI.getAvailable).mockReset().mockResolvedValue([
      group(1, 'codex特惠分组', 1.4),
      group(2, 'codex pro+plus', 3),
      group(3, '订阅专用', 1, { subscription_type: 'subscription' })
    ] as never)
    vi.mocked(userGroupsAPI.getUserGroupRates).mockReset().mockResolvedValue({})
  })

  async function mountPanel() {
    const wrapper = mount(SubscriptionPurchasePanel, { props: { paymentCurrency: 'CNY', locale: 'zh-CN' } })
    await flushPromises()
    return wrapper
  }

  async function setDaily(wrapper: Awaited<ReturnType<typeof mountPanel>>, d: number) {
    vi.useFakeTimers()
    await wrapper.get('input[type="range"]').setValue(d)
    await vi.advanceTimersByTimeAsync(400)
    vi.useRealTimers()
    await flushPromises()
  }

  function cells(wrapper: Awaited<ReturnType<typeof mountPanel>>, kind: 'balance' | 'plan') {
    return wrapper.findAll(`[data-testid="subscription-purchase-rate-${kind}"]`).map((c) => plain(c.text()))
  }

  it('D=30（u=0.05）：套餐倍率 0.07x，余额倍率 0.108x；只列标准分组并按倍率升序', async () => {
    const wrapper = await mountPanel()

    expect(wrapper.find('[data-testid="subscription-purchase-rates"]').exists()).toBe(true)
    expect(wrapper.findAll('[data-testid="subscription-purchase-rate-row"]')).toHaveLength(2)
    expect(cells(wrapper, 'balance')).toEqual(['0.108x', '0.231x'])
    expect(cells(wrapper, 'plan')).toEqual(['0.07x', '0.15x'])
    expect(wrapper.text()).not.toContain('订阅专用')
  })

  it('D=210（u=0.04）：套餐倍率 0.056x；档位变化时套餐倍率跟着变，余额倍率不变', async () => {
    const wrapper = await mountPanel()
    const balanceBefore = cells(wrapper, 'balance')
    expect(cells(wrapper, 'plan')[0]).toBe('0.07x')

    await setDaily(wrapper, 210)

    expect(cells(wrapper, 'plan')).toEqual(['0.056x', '0.12x'])
    expect(cells(wrapper, 'balance')).toEqual(balanceBefore)
  })

  it('专属倍率分组用专属 r 计算', async () => {
    vi.mocked(userGroupsAPI.getUserGroupRates).mockResolvedValue({ 1: 0.5 })
    const wrapper = await mountPanel()

    // 0.5 ÷ 13 = 0.0385；0.5 × 0.05 = 0.025。专属倍率更低，排在第一行。
    expect(cells(wrapper, 'balance')[0]).toBe('0.0385x')
    expect(cells(wrapper, 'plan')[0]).toBe('0.025x')
  })

  it('超过 5 个分组时只显示前 5 行，点「查看全部分组」展开', async () => {
    vi.mocked(userGroupsAPI.getAvailable).mockResolvedValue(
      [1, 2, 3, 4, 5, 6, 7].map((i) => group(i, `分组${i}`, i)) as never
    )
    const wrapper = await mountPanel()
    expect(wrapper.findAll('[data-testid="subscription-purchase-rate-row"]')).toHaveLength(5)

    await wrapper.get('[data-testid="subscription-purchase-rates-toggle"]').trigger('click')
    expect(wrapper.findAll('[data-testid="subscription-purchase-rate-row"]')).toHaveLength(7)
  })

  it('面板里没有「套餐价」、每刀字样，也没有「≈」', async () => {
    const wrapper = await mountPanel()
    const text = wrapper.text()

    expect(text).not.toMatch(/套餐[价價]|plan price/i)
    expect(text).not.toContain('每刀')
    expect(text).not.toContain('≈')
  })

  it('美元模式不显示', async () => {
    useCurrencyDisplay().setMode('usd')
    const wrapper = await mountPanel()
    expect(wrapper.find('[data-testid="subscription-purchase-rates"]').exists()).toBe(false)
  })

  it('free 站（m=1）不显示', async () => {
    publicSettings.value = { balance_recharge_multiplier: 1 }
    const wrapper = await mountPanel()
    expect(wrapper.find('[data-testid="subscription-purchase-rates"]').exists()).toBe(false)
  })

  it('支付关闭不显示', async () => {
    publicSettings.value = { balance_recharge_multiplier: 13, payment_enabled: false }
    const wrapper = await mountPanel()
    expect(wrapper.find('[data-testid="subscription-purchase-rates"]').exists()).toBe(false)
  })

  it('报价失败不显示', async () => {
    vi.mocked(subscriptionsAPI.quoteSubscription).mockReset().mockRejectedValue(new Error('boom'))
    const wrapper = await mountPanel()
    expect(wrapper.find('[data-testid="subscription-purchase-rates"]').exists()).toBe(false)
  })

  it('报价请求中不显示', async () => {
    vi.mocked(subscriptionsAPI.quoteSubscription).mockReset().mockReturnValue(new Promise(() => {}))
    const wrapper = await mountPanel()
    expect(wrapper.find('[data-testid="subscription-purchase-rates"]').exists()).toBe(false)
  })

  it('m × u ≥ 1（套餐并不更省）不显示', async () => {
    // 13 × 0.08 = 1.04 ≥ 1。
    vi.mocked(subscriptionsAPI.getSubscriptionPricing).mockResolvedValue({
      d_min: 30, d_max: 3000, u_min: 0.08, u_max: 0.1, t_min: 30, t_max: 360, t_step: 30
    })
    vi.mocked(subscriptionsAPI.quoteSubscription).mockReset().mockResolvedValue(quoteWith(30, 0.08))
    const wrapper = await mountPanel()
    expect(wrapper.find('[data-testid="subscription-purchase-rates"]').exists()).toBe(false)
  })

  it('分组接口失败不显示，也不影响购买面板其余部分', async () => {
    vi.mocked(userGroupsAPI.getAvailable).mockReset().mockRejectedValue(new Error('boom'))
    const wrapper = await mountPanel()
    expect(wrapper.find('[data-testid="subscription-purchase-rates"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="subscription-purchase-price-value"]').exists()).toBe(true)
  })

  // SUBSCRIPTION_PAYMENT_MULTIPLIER 不在公开设置里；以后若不为 1，需要 /subscriptions/pricing 带 payment_multiplier。
  it.todo('SUBSCRIPTION_PAYMENT_MULTIPLIER ≠ 1 时套餐倍率按 unit_price ÷ 倍数计算')
})
