import { describe, expect, it } from 'vitest'

import { isLowBalance, recentDaysRange, resolveLowBalanceThreshold } from '../lowBalance'

const system = { balance_low_notify_threshold: 50 }

describe('resolveLowBalanceThreshold', () => {
  it('用户自己设过就用用户的', () => {
    expect(resolveLowBalanceThreshold({ balance_notify_threshold: 20 }, system)).toBe(20)
  })

  it('用户没设（null / 0 / 缺省）就用站点默认', () => {
    expect(resolveLowBalanceThreshold({ balance_notify_threshold: null }, system)).toBe(50)
    expect(resolveLowBalanceThreshold({ balance_notify_threshold: 0 }, system)).toBe(50)
    expect(resolveLowBalanceThreshold({} as { balance_notify_threshold: null }, system)).toBe(50)
  })

  it('用户设得比默认还小也尊重用户的', () => {
    expect(resolveLowBalanceThreshold({ balance_notify_threshold: 1 }, system)).toBe(1)
  })

  it('用户没设、站点也没配时没有阈值', () => {
    expect(resolveLowBalanceThreshold({ balance_notify_threshold: null }, { balance_low_notify_threshold: 0 })).toBe(0)
    expect(resolveLowBalanceThreshold({ balance_notify_threshold: null }, null)).toBe(0)
    expect(resolveLowBalanceThreshold({ balance_notify_threshold: null }, undefined)).toBe(0)
  })

  it('没有用户时没有阈值', () => {
    expect(resolveLowBalanceThreshold(null, system)).toBe(0)
    expect(resolveLowBalanceThreshold(undefined, system)).toBe(0)
  })

  it('损坏的数值按没设处理', () => {
    expect(resolveLowBalanceThreshold({ balance_notify_threshold: Number.NaN }, system)).toBe(50)
    expect(resolveLowBalanceThreshold({ balance_notify_threshold: -5 }, { balance_low_notify_threshold: -1 })).toBe(0)
  })

  it('百分比类型：阈值是累计充值额的百分之几，与后端一致', () => {
    expect(
      resolveLowBalanceThreshold(
        { balance_notify_threshold: 20, balance_notify_threshold_type: 'percentage', total_recharged: 500 },
        system
      )
    ).toBe(100)
  })

  it('百分比类型对站点默认值同样适用', () => {
    expect(
      resolveLowBalanceThreshold(
        { balance_notify_threshold: null, balance_notify_threshold_type: 'percentage', total_recharged: 1000 },
        system
      )
    ).toBe(500)
  })

  it('百分比类型但没有累计充值时按原值（后端同样如此）', () => {
    expect(
      resolveLowBalanceThreshold(
        { balance_notify_threshold: 20, balance_notify_threshold_type: 'percentage', total_recharged: 0 },
        system
      )
    ).toBe(20)
  })

  it('固定类型忽略累计充值', () => {
    expect(
      resolveLowBalanceThreshold(
        { balance_notify_threshold: 20, balance_notify_threshold_type: 'fixed', total_recharged: 500 },
        system
      )
    ).toBe(20)
  })
})

describe('isLowBalance', () => {
  it('余额低于阈值才算低，等于不算', () => {
    expect(isLowBalance(4.99, 5)).toBe(true)
    expect(isLowBalance(5, 5)).toBe(false)
    expect(isLowBalance(100, 5)).toBe(false)
  })

  it('余额为 0 或负数（透支）也算低', () => {
    expect(isLowBalance(0, 5)).toBe(true)
    expect(isLowBalance(-1, 5)).toBe(true)
  })

  it('没有阈值永远不算低', () => {
    expect(isLowBalance(0, 0)).toBe(false)
  })

  it('余额不是有效数字时不算低', () => {
    expect(isLowBalance(undefined, 5)).toBe(false)
    expect(isLowBalance(null, 5)).toBe(false)
    expect(isLowBalance(Number.NaN, 5)).toBe(false)
  })
})

describe('recentDaysRange', () => {
  it('含今天在内的最近 7 天', () => {
    expect(recentDaysRange(7, new Date(2026, 9, 5, 12, 0, 0))).toEqual({
      start_date: '2026-09-29',
      end_date: '2026-10-05',
    })
  })

  it('跨月、跨年', () => {
    expect(recentDaysRange(7, new Date(2027, 0, 3, 8, 0, 0))).toEqual({
      start_date: '2026-12-28',
      end_date: '2027-01-03',
    })
  })
})
