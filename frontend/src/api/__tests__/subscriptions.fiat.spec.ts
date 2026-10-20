import { beforeEach, describe, expect, it, vi } from 'vitest'

const apiGet = vi.hoisted(() => vi.fn())

vi.mock('../client', () => ({ apiClient: { get: apiGet, post: vi.fn() } }))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ cachedPublicSettings: { balance_recharge_multiplier: 13 } })
}))

import { getActiveSubscriptions, getMySubscriptions } from '../subscriptions'
import { resetFiatDataMissingForTest, useCurrencyDisplay } from '@/composables/useCurrencyDisplay'

function card(overrides: Record<string, unknown> = {}) {
  return { id: 1, status: 'active', daily_limit_usd: 60, weekly_limit_usd: null, monthly_limit_usd: null, ...overrides }
}

describe('订阅接口的人民币数据保护', () => {
  beforeEach(() => {
    apiGet.mockReset()
    resetFiatDataMissingForTest()
    window.localStorage.clear()
  })

  it('有限额的生效卡全都没有单价，说明后端不支持折算，整站退回美元', async () => {
    apiGet.mockResolvedValue({ data: [card()] })
    await getActiveSubscriptions()

    expect(useCurrencyDisplay().canSwitch.value).toBe(false)
    expect(useCurrencyDisplay().isFiat.value).toBe(false)
  })

  it('只有个别卡缺单价（旧数据）不影响其他卡，仍按人民币展示', async () => {
    apiGet.mockResolvedValue({ data: [card({ fiat_per_credit: 0.045 }), card({ id: 2 })] })
    await getActiveSubscriptions()

    expect(useCurrencyDisplay().canSwitch.value).toBe(true)
    expect(useCurrencyDisplay().isFiat.value).toBe(true)
  })

  it('没有限额的卡不带单价属于正常情况', async () => {
    apiGet.mockResolvedValue({ data: [card({ daily_limit_usd: null })] })
    await getActiveSubscriptions()

    expect(useCurrencyDisplay().isFiat.value).toBe(true)
  })

  it('我的订阅列表同样做保护：有限额的生效卡都没有单价时退回美元', async () => {
    apiGet.mockResolvedValue({ data: [card()] })
    const cards = await getMySubscriptions()

    expect(cards).toHaveLength(1)
    expect(apiGet).toHaveBeenCalledWith('/subscriptions')
    expect(useCurrencyDisplay().isFiat.value).toBe(false)
  })

  it('我的订阅列表里非生效的卡缺单价不触发退回', async () => {
    apiGet.mockResolvedValue({ data: [card({ status: 'expired' }), card({ id: 2, fiat_per_credit: 0.045 })] })
    await getMySubscriptions()

    expect(useCurrencyDisplay().isFiat.value).toBe(true)
  })
})
