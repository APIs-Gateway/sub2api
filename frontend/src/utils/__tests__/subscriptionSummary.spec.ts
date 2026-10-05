import { describe, expect, it } from 'vitest'

import type { UserSubscription } from '@/types'
import { summarizeActiveSubscriptions } from '../subscriptionSummary'

function card(overrides: Partial<UserSubscription> = {}): UserSubscription {
  return {
    id: 1,
    user_id: 1,
    group_id: null,
    status: 'active',
    starts_at: '2026-10-01T00:00:00Z',
    daily_usage_usd: 0,
    weekly_usage_usd: 0,
    monthly_usage_usd: 0,
    daily_window_start: null,
    weekly_window_start: null,
    monthly_window_start: null,
    daily_limit_usd: 10,
    weekly_limit_usd: 70,
    monthly_limit_usd: 300,
    fiat_per_credit: 0.1,
    created_at: '2026-10-01T00:00:00Z',
    updated_at: '2026-10-01T00:00:00Z',
    expires_at: '2026-11-01T00:00:00Z',
    ...overrides
  }
}

describe('summarizeActiveSubscriptions', () => {
  it('没有订阅时给空摘要', () => {
    expect(summarizeActiveSubscriptions([])).toEqual({
      count: 0,
      unlimited: false,
      window: null,
      remainingCredits: 0,
      parts: [],
      nextExpiresAt: null
    })
  })

  it('取最窄的已配置窗口（日）剩余多少，并带上这张卡自己的单价（不做换算）', () => {
    const s = summarizeActiveSubscriptions([card({ daily_usage_usd: 4 })])

    expect(s.window).toBe('daily')
    expect(s.remainingCredits).toBe(6)
    expect(s.parts).toEqual([{ credits: 6, fiatPerCredit: 0.1 }])
    expect(s.unlimited).toBe(false)
  })

  it('已用超的卡按 0 计，不出现负数', () => {
    const s = summarizeActiveSubscriptions([card({ daily_usage_usd: 25 })])

    expect(s.remainingCredits).toBe(0)
    expect(s.parts).toEqual([{ credits: 0, fiatPerCredit: 0.1 }])
  })

  it('没有日限额时退到周，再退到月', () => {
    const weekly = summarizeActiveSubscriptions([
      card({ daily_limit_usd: null, weekly_usage_usd: 20 })
    ])
    expect(weekly.window).toBe('weekly')
    expect(weekly.remainingCredits).toBe(50)

    const monthly = summarizeActiveSubscriptions([
      card({ daily_limit_usd: 0, weekly_limit_usd: null, monthly_usage_usd: 100 })
    ])
    expect(monthly.window).toBe('monthly')
    expect(monthly.remainingCredits).toBe(200)
  })

  it('多张卡相加，每张卡各带各的单价', () => {
    const s = summarizeActiveSubscriptions([
      card({ id: 1, daily_usage_usd: 0, fiat_per_credit: 0.1 }),
      card({ id: 2, daily_limit_usd: 20, daily_usage_usd: 5, fiat_per_credit: 0.05 })
    ])

    expect(s.count).toBe(2)
    expect(s.remainingCredits).toBe(25)
    // 每张卡的额度和单价原样给出，由调用方走共享函数折算
    expect(s.parts).toEqual([
      { credits: 10, fiatPerCredit: 0.1 },
      { credits: 15, fiatPerCredit: 0.05 }
    ])
  })

  it('有一张卡拿不到单价时，那张卡的单价为 null，交给调用方回落', () => {
    const s = summarizeActiveSubscriptions([
      card({ id: 1 }),
      card({ id: 2, fiat_per_credit: undefined })
    ])

    expect(s.remainingCredits).toBe(20)
    expect(s.parts.map((p) => p.fiatPerCredit)).toEqual([0.1, null])
  })

  it('三个窗口都没配限额的卡视为不限额', () => {
    const s = summarizeActiveSubscriptions([
      card({ daily_limit_usd: null, weekly_limit_usd: null, monthly_limit_usd: 0 })
    ])

    expect(s.unlimited).toBe(true)
    expect(s.window).toBeNull()
    expect(s.parts).toEqual([])
  })

  it('只要有一张不限额的卡，整体就是不限额', () => {
    const s = summarizeActiveSubscriptions([
      card({ id: 1 }),
      card({ id: 2, daily_limit_usd: null, weekly_limit_usd: null, monthly_limit_usd: null })
    ])

    expect(s.unlimited).toBe(true)
  })

  it('配了更宽窗口的卡不并入最窄窗口的合计', () => {
    const s = summarizeActiveSubscriptions([
      card({ id: 1, daily_usage_usd: 2 }),
      card({ id: 2, daily_limit_usd: null, weekly_limit_usd: 70, weekly_usage_usd: 0 })
    ])

    expect(s.window).toBe('daily')
    expect(s.remainingCredits).toBe(8)
  })

  it('到期时间取最早的一张，不到期的卡不参与', () => {
    const s = summarizeActiveSubscriptions([
      card({ id: 1, expires_at: '2026-12-01T00:00:00Z' }),
      card({ id: 2, expires_at: '2026-11-03T00:00:00Z' }),
      card({ id: 3, expires_at: null })
    ])

    expect(s.nextExpiresAt).toBe('2026-11-03T00:00:00Z')
  })

  it('都不到期时没有到期时间', () => {
    expect(summarizeActiveSubscriptions([card({ expires_at: null })]).nextExpiresAt).toBeNull()
  })

  it('用量字段缺失按 0 计', () => {
    const s = summarizeActiveSubscriptions([
      card({ daily_usage_usd: undefined as unknown as number })
    ])

    expect(s.remainingCredits).toBe(10)
  })
})
