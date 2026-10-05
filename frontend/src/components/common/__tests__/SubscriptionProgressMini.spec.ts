import { enableAutoUnmount, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import SubscriptionProgressMini from '../SubscriptionProgressMini.vue'
const store = vi.hoisted(() => ({ activeSubscriptions: [] as unknown[], hasActiveSubscriptions: true, fetchActiveSubscriptions: vi.fn().mockResolvedValue(undefined) }))
// 金额口径依赖 app store；未配置充值倍率时按美元展示（旧行为），配置了倍率（codex 站）按人民币展示。
const appState = vi.hoisted(() => ({ cachedPublicSettings: null as Record<string, unknown> | null }))
vi.mock('@/stores/app', () => ({ useAppStore: () => appState }))
vi.mock('@/stores', () => ({ useSubscriptionStore: () => store }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)
beforeEach(() => { appState.cachedPublicSettings = null; vi.useFakeTimers(); vi.setSystemTime(new Date(2026, 8, 22, 12)) })
afterEach(() => vi.useRealTimers())
describe('subscription expiry calendar labels', () => {
  it.each([
    [new Date(2026, 8, 22, 18), 'expiresToday'],
    [new Date(2026, 8, 23, 18), 'expiresTomorrow'],
    [new Date(2026, 8, 22, 12), 'expired'],
    [new Date(2026, 8, 25, 12), 'daysRemaining'],
  ])('labels %s as %s', async (expires, label) => {
    store.activeSubscriptions = [{ id: 1, group_id: 1, expires_at: expires.toISOString(), group: { name: 'Plan' } }]
    const w = mount(SubscriptionProgressMini, { global: { stubs: { Icon: true, RouterLink: true } } })
    await w.get('button').trigger('click')
    expect(w.text()).toContain('subscriptionProgress.' + label)
  })
})

// 订阅卡不随分组删除：来源分组被删后卡仍在列表里，但读不到分组（group 缺失、group_id 仍指向旧分组）。
describe('subscription label when the source group no longer exists', () => {
  async function openTooltip(subscription: Record<string, unknown>) {
    store.activeSubscriptions = [subscription]
    const w = mount(SubscriptionProgressMini, { global: { stubs: { Icon: true, RouterLink: true } } })
    await w.get('button').trigger('click')
    return w
  }
  const expiresAt = new Date(2026, 8, 25, 12).toISOString()

  it('falls back to the daily amount title instead of exposing the group id', async () => {
    const w = await openTooltip({ id: 1, group_id: 777, daily_amount_usd: 30, expires_at: expiresAt })
    expect(w.text()).toContain('userSubscriptions.daily $30.00')
    expect(w.text()).not.toContain('Group #')
    expect(w.text()).not.toContain('777')
  })

  it('still shows the group name while the group exists', async () => {
    const w = await openTooltip({
      id: 2,
      group_id: 5,
      daily_amount_usd: 30,
      expires_at: expiresAt,
      group: { name: 'Plan' }
    })
    expect(w.text()).toContain('Plan')
    expect(w.text()).not.toContain('userSubscriptions.daily')
  })
})

// 人民币模式（充值倍率 ≠ 1）：没有分组的卡，回退标题和旁边的用量必须同一个币种。
describe('subscription label currency in fiat mode', () => {
  const expiresAt = new Date(2026, 8, 25, 12).toISOString()

  async function openTooltip(subscription: Record<string, unknown>) {
    store.activeSubscriptions = [subscription]
    const w = mount(SubscriptionProgressMini, { global: { stubs: { Icon: true, RouterLink: true } } })
    await w.get('button').trigger('click')
    return w
  }

  it('回退标题「每日 X」按卡的单价折成人民币，不再写死美元', async () => {
    appState.cachedPublicSettings = { balance_recharge_multiplier: 12 }
    const w = await openTooltip({
      id: 1,
      group_id: 777,
      daily_amount_usd: 30,
      daily_limit_usd: 30,
      daily_usage_usd: 10,
      fiat_per_credit: 0.05,
      expires_at: expiresAt
    })

    expect(w.text()).toContain('userSubscriptions.daily ¥1.50')
    // 旁边的用量同一个币种
    expect(w.text()).toContain('¥0.50/¥1.50')
    expect(w.text()).not.toContain('$')
  })

  it('卡没有单价时标题和用量一起回落到美元，不混排', async () => {
    appState.cachedPublicSettings = { balance_recharge_multiplier: 12 }
    const w = await openTooltip({
      id: 1,
      group_id: 777,
      daily_amount_usd: 30,
      daily_limit_usd: 30,
      daily_usage_usd: 10,
      expires_at: expiresAt
    })

    expect(w.text()).toContain('userSubscriptions.daily $30.00')
    expect(w.text()).toContain('$10.00/$30.00')
    expect(w.text()).not.toContain('¥')
  })
})
