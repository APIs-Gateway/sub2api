import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import PointsView from '../PointsView.vue'
import { pointsViewMountOptions } from '@/views/__tests__/pointsTestStubs'

// 邀请返利积分制（issue #11）—— 用户端积分中心视图测试:
// 加载/停用态、复制、换余额、提现(alipay/usdt)、换套餐(confirm)、错误分支。

const {
  getPointsOverview,
  listPointsLedger,
  listPointsPlans,
  redeemPointsToBalance,
  createWithdrawal,
  redeemPointsToPlan,
} = vi.hoisted(() => ({
  getPointsOverview: vi.fn(),
  listPointsLedger: vi.fn(),
  listPointsPlans: vi.fn(),
  redeemPointsToBalance: vi.fn(),
  createWithdrawal: vi.fn(),
  redeemPointsToPlan: vi.fn(),
}))
vi.mock('@/api/points', () => ({
  getPointsOverview,
  listPointsLedger,
  listPointsPlans,
  redeemPointsToBalance,
  createWithdrawal,
  redeemPointsToPlan,
}))

const { renewQuote, changePlanQuote } = vi.hoisted(() => ({
  renewQuote: vi.fn(),
  changePlanQuote: vi.fn(),
}))
vi.mock('@/api/subscriptions', () => ({
  renewQuote,
  changePlanQuote,
}))

const { activeSubscriptions, fetchActiveSubscriptions, invalidateSubscriptionCache } = vi.hoisted(() => ({
  activeSubscriptions: [] as any[],
  fetchActiveSubscriptions: vi.fn(),
  invalidateSubscriptionCache: vi.fn(),
}))
vi.mock('@/stores/subscriptions', () => ({
  useSubscriptionStore: () => ({
    get activeSubscriptions() {
      return activeSubscriptions
    },
    fetchActiveSubscriptions,
    invalidateCache: invalidateSubscriptionCache,
  }),
}))

const showError = vi.fn()
const showSuccess = vi.fn()
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess }) }))

const copyToClipboard = vi.fn()
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard }) }))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

function makeOverview(overrides: Record<string, unknown> = {}) {
  return {
    account: { user_id: 7, available: 300000, frozen: 10, lifetime_earned: 120, created_at: '', updated_at: '' },
    affiliate: { aff_code: 'CODE7', aff_count: 3 },
    effective_rate: 20,
    config: {
      enabled: true,
      peg: 0.01,
      withdraw_enabled: true,
      withdraw_min_points: 0,
      withdraw_fee_percent: 10,
      withdraw_usd_cny_rate: 7.2,
      redeem_balance_on: true,
      redeem_plan_on: true,
      ...(overrides as any).config,
    },
    ...overrides,
  }
}

const ledgerRows = [
  { id: 1, user_id: 7, kind: 'earn', points: 50, available_after: 50, created_at: '2026-06-01T00:00:00Z' },
  { id: 2, user_id: 7, kind: 'to_balance', points: -20, available_after: null, created_at: '2026-06-02T00:00:00Z' },
]
const planList = [
  { validity_days: 30, daily_amount_usd: 30, unit_price: 2, price: 1800, points_price: 180000, weekly_cap_usd: 210, monthly_cap_usd: 900 },
]

describe('user PointsView', () => {
  beforeEach(() => {
    getPointsOverview.mockReset()
    listPointsLedger.mockReset()
    listPointsPlans.mockReset()
    redeemPointsToBalance.mockReset()
    createWithdrawal.mockReset()
    redeemPointsToPlan.mockReset()
    renewQuote.mockReset()
    changePlanQuote.mockReset()
    fetchActiveSubscriptions.mockReset()
    invalidateSubscriptionCache.mockReset()
    activeSubscriptions.length = 0
    showError.mockReset()
    showSuccess.mockReset()
    copyToClipboard.mockReset()

    getPointsOverview.mockResolvedValue(makeOverview())
    listPointsLedger.mockResolvedValue({ items: ledgerRows, total: 2, page: 1, page_size: 20, pages: 1 })
    listPointsPlans.mockResolvedValue(planList)
    redeemPointsToBalance.mockResolvedValue({ balance: 5 })
    createWithdrawal.mockResolvedValue({ id: 1 })
    redeemPointsToPlan.mockResolvedValue({})
    renewQuote.mockResolvedValue({ subscription_id: 1, daily_amount_usd: 30, added_days: 30, price: 900, unit_price: 1, group_id: 0 })
    changePlanQuote.mockResolvedValue({ old_subscription_id: 1, daily_amount_usd: 30, validity_days: 30, weekly_cap_usd: 210, monthly_cap_usd: 900, new_plan_price: 1800, old_remaining_value: 1000, diff: 800, unit_price: 2 })
    fetchActiveSubscriptions.mockResolvedValue(activeSubscriptions)
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('renders disabled message when points feature off', async () => {
    getPointsOverview.mockResolvedValue(makeOverview({ config: { enabled: false } }))
    const wrapper = mount(PointsView, pointsViewMountOptions())
    await flushPromises()
    expect(wrapper.text()).toContain('points.disabled')
    expect(listPointsLedger).not.toHaveBeenCalled()
  })

  it('loads overview, stats, invite, plans and ledger', async () => {
    const wrapper = mount(PointsView, pointsViewMountOptions())
    await flushPromises()
    expect(getPointsOverview).toHaveBeenCalled()
    expect(listPointsLedger).toHaveBeenCalledWith(1, 20)
    expect(listPointsPlans).toHaveBeenCalled()
    expect(fetchActiveSubscriptions).toHaveBeenCalledWith(true)
    const text = wrapper.text()
    expect(text).toContain('CODE7') // aff code
    expect(text).toContain('points.invite.rewardExample') // invite reward explanation
    expect(text).toContain('points.invite.rewardFormula')
    expect(text).toContain('points.stats.firstPaymentRate')
    expect(text).toContain('points.stats.repeatPaymentRate')
    expect(text).toContain('points.redeemPlan.dailyOption') // compact plan selector
    expect(text).toContain('points.redeemPlan.actionLabels.purchase')
    expect(text).toContain('+50') // ledger positive
    expect(text).toContain('—') // ledger null balance
  })

  it('copy buttons call clipboard', async () => {
    const wrapper = mount(PointsView, pointsViewMountOptions())
    await flushPromises()
    const copyBtn = wrapper.findAll('button.btn-secondary.btn-sm')[0]
    await copyBtn.trigger('click')
    expect(copyToClipboard).toHaveBeenCalled()
  })

  it('redeem to balance success + refresh', async () => {
    const wrapper = mount(PointsView, pointsViewMountOptions())
    await flushPromises()
    await wrapper.findAll('input[type="number"]')[0].setValue('100')
    await wrapper.findAll('button.btn-primary.w-full')[0].trigger('click')
    await flushPromises()
    expect(redeemPointsToBalance).toHaveBeenCalledWith(100)
    expect(showSuccess).toHaveBeenCalled()
    expect(getPointsOverview).toHaveBeenCalledTimes(2) // initial + refresh
  })

  it('redeem to balance error shows error', async () => {
    redeemPointsToBalance.mockRejectedValueOnce(new Error('boom'))
    const wrapper = mount(PointsView, pointsViewMountOptions())
    await flushPromises()
    await wrapper.findAll('input[type="number"]')[0].setValue('100')
    await wrapper.findAll('button.btn-primary.w-full')[0].trigger('click')
    await flushPromises()
    expect(showError).toHaveBeenCalled()
  })

  it('withdraw via alipay sends alipay fields', async () => {
    const wrapper = mount(PointsView, pointsViewMountOptions())
    await flushPromises()
    await wrapper.findAll('input[type="number"]')[1].setValue('1000') // withdraw points
    const textInputs = wrapper.findAll('input[type="text"]')
    await textInputs[0].setValue('alipay-acc') // alipay account
    await textInputs[1].setValue('alipay-name')
    expect(wrapper.text()).toMatch(/(?:CN)?¥9\.00/)
    await wrapper.findAll('button.btn-primary.w-full')[1].trigger('click')
    await flushPromises()
    expect(createWithdrawal).toHaveBeenCalledWith({
      points: 1000,
      payout_method: 'alipay',
      payout_alipay_account: 'alipay-acc',
      payout_alipay_name: 'alipay-name',
      payout_usdt_chain: undefined,
      payout_usdt_address: undefined,
    })
    expect(showSuccess).toHaveBeenCalled()
  })

  it('redeem plan with same active daily amount shows renew quote', async () => {
    activeSubscriptions.push({ id: 1, status: 'active', daily_amount_usd: 30 })
    renewQuote.mockResolvedValueOnce({ subscription_id: 1, daily_amount_usd: 30, added_days: 30, price: 900, unit_price: 1, group_id: 0 })
    const wrapper = mount(PointsView, pointsViewMountOptions())
    await flushPromises()
    expect(renewQuote).toHaveBeenCalledWith(30)
    expect(changePlanQuote).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('points.redeemPlan.actionLabels.renew')
    expect(wrapper.text()).toContain('90,000')
  })

  it('redeem plan with different active daily amount shows change-plan quote', async () => {
    activeSubscriptions.push({ id: 1, status: 'active', daily_amount_usd: 10 })
    changePlanQuote.mockResolvedValueOnce({ old_subscription_id: 1, daily_amount_usd: 30, validity_days: 30, weekly_cap_usd: 210, monthly_cap_usd: 900, new_plan_price: 1800, old_remaining_value: 1000, diff: 800, unit_price: 2 })
    const wrapper = mount(PointsView, pointsViewMountOptions())
    await flushPromises()
    expect(changePlanQuote).toHaveBeenCalledWith(30, 30)
    expect(wrapper.text()).toContain('points.redeemPlan.actionLabels.change_plan')
    expect(wrapper.text()).toContain('80,000')
  })

  it('keeps visible pressed feedback on selected redeem plan options', async () => {
    listPointsPlans.mockResolvedValue([
      ...planList,
      { validity_days: 90, daily_amount_usd: 30, unit_price: 1.8, price: 4860, points_price: 486000, weekly_cap_usd: 210, monthly_cap_usd: 900 },
      { validity_days: 30, daily_amount_usd: 60, unit_price: 1.6, price: 2880, points_price: 288000, weekly_cap_usd: 420, monthly_cap_usd: 1800 },
    ])
    const wrapper = mount(PointsView, pointsViewMountOptions())
    await flushPromises()

    const pressed = () => wrapper.findAll('button[aria-pressed="true"]').map(button => button.text())
    expect(pressed()).toEqual(expect.arrayContaining([
      'points.redeemPlan.dailyOption',
      'points.redeemPlan.validity',
    ]))
    expect(wrapper.findAll('button[aria-pressed="true"]').every(button => button.classes().includes('btn-primary'))).toBe(true)

    const dailyButtons = wrapper.findAll('button').filter(button => button.text() === 'points.redeemPlan.dailyOption')
    await dailyButtons[1].trigger('click')
    await flushPromises()

    const selectedButtons = wrapper.findAll('button[aria-pressed="true"]')
    expect(selectedButtons).toHaveLength(2)
    expect(selectedButtons.every(button => button.classes().includes('btn-primary'))).toBe(true)
  })

  it('redeem plan keeps button feedback when selected plan would downgrade daily amount', async () => {
    activeSubscriptions.push({ id: 1, status: 'active', daily_amount_usd: 60 })
    const wrapper = mount(PointsView, pointsViewMountOptions())
    await flushPromises()

    expect(renewQuote).not.toHaveBeenCalled()
    expect(changePlanQuote).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('points.redeemPlan.actionLabels.downgrade')
    expect(wrapper.text()).toContain('points.redeemPlan.downgradeBlocked')

    await wrapper.findAll('button.btn-primary.w-full')[2].trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('points.redeemPlan.downgradeBlocked')
    expect(redeemPointsToPlan).not.toHaveBeenCalled()
  })

  it('withdraw via usdt sends usdt address', async () => {
    const wrapper = mount(PointsView, pointsViewMountOptions())
    await flushPromises()
    await wrapper.findAll('input[type="number"]')[1].setValue('1000')
    await wrapper.findAll('select')[0].setValue('usdt')
    expect(wrapper.text()).toContain('7.30')
    expect(wrapper.text()).toMatch(/(?:US)?\$1\.23/)
    await wrapper.find('input[maxlength="128"]').setValue('TXaddr')
    await wrapper.findAll('button.btn-primary.w-full')[1].trigger('click')
    await flushPromises()
    expect(createWithdrawal).toHaveBeenCalledWith({
      points: 1000,
      payout_method: 'usdt',
      payout_alipay_account: undefined,
      payout_alipay_name: undefined,
      payout_usdt_chain: 'TRC20',
      payout_usdt_address: 'TXaddr',
    })
  })

  it('withdraw error shows error', async () => {
    createWithdrawal.mockRejectedValueOnce(new Error('boom'))
    const wrapper = mount(PointsView, pointsViewMountOptions())
    await flushPromises()
    await wrapper.findAll('input[type="number"]')[1].setValue('1000')
    await wrapper.findAll('button.btn-primary.w-full')[1].trigger('click')
    await flushPromises()
    expect(showError).toHaveBeenCalled()
  })

  it('redeem plan: confirm yes calls redeemPointsToPlan with idempotency key', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = mount(PointsView, pointsViewMountOptions())
    await flushPromises()
    await wrapper.findAll('button.btn-primary.w-full')[2].trigger('click')
    await flushPromises()
    expect(redeemPointsToPlan).toHaveBeenCalledTimes(1)
    const args = redeemPointsToPlan.mock.calls[0]
    expect(args[0]).toBe(30) // daily_amount_usd
    expect(args[1]).toBe(30) // validity_days
    expect(typeof args[2]).toBe('string') // idempotency key
    expect(args[2].length).toBeGreaterThan(0)
    expect(showSuccess).toHaveBeenCalled()
  })

  it('redeem plan: confirm no does not call api', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    const wrapper = mount(PointsView, pointsViewMountOptions())
    await flushPromises()
    await wrapper.findAll('button.btn-primary.w-full')[2].trigger('click')
    await flushPromises()
    expect(redeemPointsToPlan).not.toHaveBeenCalled()
  })

  it('redeem plan error shows error', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    redeemPointsToPlan.mockRejectedValueOnce(new Error('boom'))
    const wrapper = mount(PointsView, pointsViewMountOptions())
    await flushPromises()
    await wrapper.findAll('button.btn-primary.w-full')[2].trigger('click')
    await flushPromises()
    expect(showError).toHaveBeenCalled()
  })

  it('shows error when overview load fails', async () => {
    getPointsOverview.mockRejectedValueOnce(new Error('boom'))
    mount(PointsView, pointsViewMountOptions())
    await flushPromises()
    expect(showError).toHaveBeenCalled()
  })

  it('renders empty plans and empty ledger states', async () => {
    listPointsPlans.mockResolvedValue([])
    listPointsLedger.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
    const wrapper = mount(PointsView, pointsViewMountOptions())
    await flushPromises()
    expect(wrapper.text()).toContain('points.redeemPlan.empty')
    expect(wrapper.text()).toContain('points.ledger.empty')
  })
})
