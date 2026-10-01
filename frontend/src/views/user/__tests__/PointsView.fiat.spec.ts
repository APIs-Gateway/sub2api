import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import PointsView from '../PointsView.vue'
import { pointsViewMountOptions } from '@/views/__tests__/pointsTestStubs'
import { resetFiatDataMissingForTest, useCurrencyDisplay } from '@/composables/useCurrencyDisplay'

// 积分页按人民币展示：兑换余额的预估、套餐档位和封顶；实际兑换逻辑（积分数、下单参数）不变。

const { getPointsOverview, listPointsLedger, listPointsPlans, redeemPointsToBalance, redeemPointsToPlan } = vi.hoisted(() => ({
  getPointsOverview: vi.fn(),
  listPointsLedger: vi.fn(),
  listPointsPlans: vi.fn(),
  redeemPointsToBalance: vi.fn(),
  redeemPointsToPlan: vi.fn(),
}))
vi.mock('@/api/points', () => ({
  getPointsOverview,
  listPointsLedger,
  listPointsPlans,
  redeemPointsToBalance,
  redeemPointsToPlan,
  createWithdrawal: vi.fn(),
}))
vi.mock('@/api/subscriptions', () => ({ renewQuote: vi.fn(), changePlanQuote: vi.fn() }))
vi.mock('@/stores/subscriptions', () => ({
  useSubscriptionStore: () => ({
    activeSubscriptions: [],
    fetchActiveSubscriptions: vi.fn().mockResolvedValue([]),
    invalidateCache: vi.fn(),
  }),
}))

// 可变的假设置：改它就能模拟 codex 站（倍率 13）和 free 站（倍率 1）。
const publicSettings = vi.hoisted(() => ({ value: {} as Record<string, unknown> }))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    get cachedPublicSettings() {
      return publicSettings.value
    },
  }),
}))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: vi.fn() }) }))
// 带上参数，才能断言文案里的金额。
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}${JSON.stringify(params)}` : key) }),
  }
})

const overview = {
  account: { user_id: 7, available: 300000, frozen: 10, lifetime_earned: 120, created_at: '', updated_at: '' },
  affiliate: { aff_code: 'CODE7', aff_count: 3 },
  effective_rate: 20,
  config: {
    enabled: true,
    peg: 0.01,
    balance_redeem_rate: 13,
    withdraw_enabled: true,
    withdraw_min_points: 0,
    withdraw_fee_percent: 10,
    withdraw_usd_cny_rate: 7.2,
    redeem_balance_on: true,
    redeem_plan_on: true,
  },
}
const plans = [
  { validity_days: 30, daily_amount_usd: 30, unit_price: 0.045, price: 40.5, points_price: 4050, weekly_cap_usd: 210, monthly_cap_usd: 900 },
]

async function mountPoints() {
  const wrapper = mount(PointsView, pointsViewMountOptions())
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  window.localStorage.clear()
  resetFiatDataMissingForTest()
  useCurrencyDisplay().setMode('fiat')
  getPointsOverview.mockReset().mockResolvedValue(overview)
  listPointsLedger.mockReset().mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
  listPointsPlans.mockReset().mockResolvedValue(plans)
  redeemPointsToBalance.mockReset().mockResolvedValue({ balance: 5 })
  redeemPointsToPlan.mockReset().mockResolvedValue({})
})

describe('积分页按人民币展示', () => {
  it('兑换余额的预估是人民币，兑换的积分数不变', async () => {
    publicSettings.value = { balance_recharge_multiplier: 13 }
    const wrapper = await mountPoints()
    await wrapper.findAll('input[type="number"]')[0].setValue('100')
    const text = wrapper.text()

    // 100 积分 × ¥0.01 = ¥1.00（到账 13 个额度，折回就是 ¥1.00）。
    expect(text).toContain('"amount":"¥1.00"')
    expect(text).toContain('points.redeemBalance.rateHintFiat')
    expect(text).toContain('points.redeemBalance.descFiat')
    expect(text).not.toContain('points.redeemBalance.rateHint{')

    await wrapper.findAll('button.btn-primary.w-full')[0].trigger('click')
    await flushPromises()
    expect(redeemPointsToBalance).toHaveBeenCalledWith(100)
  })

  it('套餐档位和封顶写成人民币，不出现 USD 和 $', async () => {
    publicSettings.value = { balance_recharge_multiplier: 13 }
    const wrapper = await mountPoints()
    const text = wrapper.text().replace(/\s+/g, '')

    // 每日 30 × 0.045 = ¥1.35；封顶 210 × 0.045 = ¥9.45、900 × 0.045 = ¥40.50。
    expect(text).toContain('¥1.35')
    expect(text).toContain('¥9.45/¥40.50')
    expect(text).not.toContain('USD')
    expect(text).not.toContain('$')
  })

  it('兑换套餐仍按原来的每日额度和有效期下单', async () => {
    publicSettings.value = { balance_recharge_multiplier: 13 }
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = await mountPoints()
    await wrapper.findAll('button.btn-primary.w-full')[2].trigger('click')
    await flushPromises()

    expect(redeemPointsToPlan).toHaveBeenCalledWith(30, 30, expect.any(String))
    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining('points.redeemPlan.planTitleFiat'))
  })

  it('free 站（倍率 1）保持美元额度', async () => {
    publicSettings.value = { balance_recharge_multiplier: 1 }
    const wrapper = await mountPoints()
    const text = wrapper.text().replace(/\s+/g, '')

    expect(text).toContain('points.redeemPlan.dailyOption')
    expect(text).toContain('$210.00/$900.00')
    expect(text).not.toContain('¥9.45')
  })
})
