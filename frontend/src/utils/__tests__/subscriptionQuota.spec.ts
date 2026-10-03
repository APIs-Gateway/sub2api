import { describe, expect, it } from 'vitest'

import {
  getExpirationDateRelation,
  getRemainingExpiryDuration,
  subscriptionDisplayName
} from '../subscriptionQuota'

describe('subscription expiry timing', () => {
  it('uses local calendar dates for today and tomorrow', () => {
    const now = new Date(2026, 2, 7, 23, 30)

    expect(getExpirationDateRelation(new Date(2026, 2, 7, 23, 45), now)).toBe('today')
    expect(getExpirationDateRelation(new Date(2026, 2, 8, 3, 30), now)).toBe('tomorrow')
  })

  it('returns later for expiries two or more calendar days away', () => {
    const now = new Date(2026, 2, 7, 23, 30)

    expect(getExpirationDateRelation(new Date(2026, 2, 9, 0, 5), now)).toBe('later')
  })

  it('accepts ISO strings', () => {
    const now = new Date(2026, 6, 30, 9, 0)

    expect(getExpirationDateRelation(new Date(2026, 6, 30, 18, 0).toISOString(), now)).toBe('today')
    expect(getRemainingExpiryDuration(new Date(2026, 6, 30, 18, 0).toISOString(), now)).toEqual({
      unit: 'hoursMinutes',
      hours: 9,
      minutes: 0
    })
  })

  it('treats the exact expiry instant and elapsed expiries as expired', () => {
    const now = new Date(2026, 6, 30, 9, 0)

    expect(getExpirationDateRelation(now, now)).toBe('expired')
    expect(getRemainingExpiryDuration(now, now)).toBeNull()
    expect(getExpirationDateRelation(new Date(2026, 6, 30, 8, 59), now)).toBe('expired')
    expect(getRemainingExpiryDuration(new Date(2026, 6, 30, 8, 59), now)).toBeNull()
  })

  it('rejects invalid target and current dates', () => {
    const invalid = new Date('invalid')
    const valid = new Date(2026, 6, 30, 9, 0)

    expect(getExpirationDateRelation(invalid, valid)).toBeNull()
    expect(getExpirationDateRelation(valid, invalid)).toBeNull()
    expect(getRemainingExpiryDuration(invalid, valid)).toBeNull()
    expect(getRemainingExpiryDuration(valid, invalid)).toBeNull()
  })

  it('returns rounded-up hours and minutes for an expiry under 24 hours away', () => {
    const now = new Date(2026, 6, 30, 9, 0)

    expect(getRemainingExpiryDuration(new Date(2026, 6, 31, 8, 30), now)).toEqual({
      unit: 'hoursMinutes',
      hours: 23,
      minutes: 30
    })
    expect(getRemainingExpiryDuration(new Date(now.getTime() + 1), now)).toEqual({
      unit: 'hoursMinutes',
      hours: 0,
      minutes: 1
    })
    expect(getRemainingExpiryDuration(new Date(now.getTime() + 23 * 60 * 60 * 1000 + 1), now)).toEqual({
      unit: 'hoursMinutes',
      hours: 23,
      minutes: 1
    })
  })

  it('preserves rounded-up day display from 24 hours onward', () => {
    const now = new Date(2026, 6, 30, 9, 0)

    expect(getRemainingExpiryDuration(new Date(now.getTime() + 24 * 60 * 60 * 1000), now)).toEqual({
      unit: 'days',
      days: 1
    })
    expect(getRemainingExpiryDuration(new Date(now.getTime() + 24 * 60 * 60 * 1000 + 1), now)).toEqual({
      unit: 'days',
      days: 2
    })
  })
})

describe('subscriptionDisplayName', () => {
  const t = (key: string) => key

  it('prefers the group name while the source group still exists', () => {
    const group = { name: '  Pro  ' } as never
    expect(subscriptionDisplayName({ group, daily_amount_usd: 30 }, t)).toBe('Pro')
  })

  it('falls back to the daily amount title when the source group was deleted', () => {
    // 分组被删后卡保留，但读不到 group 边；不能展示 `Group #id`。
    expect(subscriptionDisplayName({ group: undefined, daily_amount_usd: 30 }, t)).toBe(
      'userSubscriptions.daily $30.00'
    )
    expect(subscriptionDisplayName({ group: undefined, daily_amount_usd: 12.5 }, t)).toBe(
      'userSubscriptions.daily $12.50'
    )
  })

  it('uses the card daily limit when the daily amount is missing', () => {
    expect(subscriptionDisplayName({ group: undefined, daily_limit_usd: 8 }, t)).toBe(
      'userSubscriptions.daily $8.00'
    )
  })

  it('treats a blank group name like a missing group', () => {
    const group = { name: '   ' } as never
    expect(subscriptionDisplayName({ group, daily_amount_usd: 5 }, t)).toBe(
      'userSubscriptions.daily $5.00'
    )
  })

  it('shows the unlimited label for a card with neither group nor daily amount', () => {
    expect(subscriptionDisplayName({ group: undefined }, t)).toBe('userSubscriptions.unlimited')
    expect(subscriptionDisplayName({ group: undefined, daily_amount_usd: 0 }, t)).toBe(
      'userSubscriptions.unlimited'
    )
  })
})
