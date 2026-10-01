import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import SubscriptionPurchasePanel from '../SubscriptionPurchasePanel.vue'
import subscriptionsAPI from '@/api/subscriptions'
import { resetFiatDataMissingForTest } from '@/composables/useCurrencyDisplay'

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
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})
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
  window.localStorage.clear()
  resetFiatDataMissingForTest()
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

  it('free 站保持美元：封顶带 USD，保留额度输入框', async () => {
    publicSettings.value = { balance_recharge_multiplier: 1 }
    const wrapper = await mountPanel()
    const text = plain(wrapper.text())

    expect(text).toContain('USD210.00')
    expect(wrapper.find('[data-testid="subscription-purchase-daily-fiat"]').exists()).toBe(false)
    expect(wrapper.find('input[type="number"]').exists()).toBe(true)
  })
})
