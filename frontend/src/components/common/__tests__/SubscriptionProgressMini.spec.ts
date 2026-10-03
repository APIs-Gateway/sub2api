import { enableAutoUnmount, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import SubscriptionProgressMini from '../SubscriptionProgressMini.vue'
const store = vi.hoisted(() => ({ activeSubscriptions: [] as unknown[], hasActiveSubscriptions: true, fetchActiveSubscriptions: vi.fn().mockResolvedValue(undefined) }))
// 金额口径依赖 app store；未配置充值倍率时按美元展示（旧行为）。
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ cachedPublicSettings: null }) }))
vi.mock('@/stores', () => ({ useSubscriptionStore: () => store }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)
beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date(2026, 8, 22, 12)) })
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
