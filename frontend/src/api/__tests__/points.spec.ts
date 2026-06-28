import { beforeEach, describe, expect, it, vi } from 'vitest'

// 邀请返利积分制（issue #11）—— 用户端 points API 单测：断言端点/入参/返回与空值回退。

const { get, post } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: { get, post },
}))

import {
  getPointsOverview,
  listPointsLedger,
  listPointsPlans,
  redeemPointsToBalance,
  redeemPointsToPlan,
  listMyWithdrawals,
  createWithdrawal,
} from '@/api/points'
import pointsApiDefault from '@/api/points'

describe('points api (user)', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
    get.mockResolvedValue({ data: {} })
    post.mockResolvedValue({ data: {} })
  })

  it('getPointsOverview hits overview endpoint', async () => {
    get.mockResolvedValueOnce({ data: { account: { available: 10 } } })
    const res = await getPointsOverview()
    expect(get).toHaveBeenCalledWith('/user/points/overview')
    expect(res.account.available).toBe(10)
  })

  it('listPointsLedger passes pagination params (defaults)', async () => {
    get.mockResolvedValueOnce({ data: { items: [], total: 0 } })
    await listPointsLedger()
    expect(get).toHaveBeenCalledWith('/user/points/ledger', { params: { page: 1, page_size: 20 } })
  })

  it('listPointsLedger passes explicit pagination params', async () => {
    await listPointsLedger(3, 50)
    expect(get).toHaveBeenCalledWith('/user/points/ledger', { params: { page: 3, page_size: 50 } })
  })

  it('listPointsPlans returns plans array', async () => {
    get.mockResolvedValueOnce({ data: { plans: [{ group_id: 1 }] } })
    const plans = await listPointsPlans()
    expect(get).toHaveBeenCalledWith('/user/points/plans')
    expect(plans).toHaveLength(1)
  })

  it('listPointsPlans falls back to [] when plans missing', async () => {
    get.mockResolvedValueOnce({ data: {} })
    expect(await listPointsPlans()).toEqual([])
  })

  it('redeemPointsToBalance posts points', async () => {
    post.mockResolvedValueOnce({ data: { balance: 12.5 } })
    const res = await redeemPointsToBalance(100)
    expect(post).toHaveBeenCalledWith('/user/points/redeem-balance', { points: 100 })
    expect(res.balance).toBe(12.5)
  })

  it('redeemPointsToPlan posts group/days/idempotency (defaults)', async () => {
    await redeemPointsToPlan(7)
    expect(post).toHaveBeenCalledWith('/user/points/redeem-plan', {
      group_id: 7,
      validity_days: 0,
      idempotency_key: '',
    })
  })

  it('redeemPointsToPlan forwards explicit validity/idempotency', async () => {
    await redeemPointsToPlan(7, 30, 'xid-abc')
    expect(post).toHaveBeenCalledWith('/user/points/redeem-plan', {
      group_id: 7,
      validity_days: 30,
      idempotency_key: 'xid-abc',
    })
  })

  it('listMyWithdrawals returns withdrawals array', async () => {
    get.mockResolvedValueOnce({ data: { withdrawals: [{ id: 1 }] } })
    const ws = await listMyWithdrawals()
    expect(get).toHaveBeenCalledWith('/user/points/withdrawals')
    expect(ws).toHaveLength(1)
  })

  it('listMyWithdrawals falls back to [] when missing', async () => {
    get.mockResolvedValueOnce({ data: {} })
    expect(await listMyWithdrawals()).toEqual([])
  })

  it('createWithdrawal posts payload', async () => {
    const payload = { points: 1000, payout_method: 'alipay' as const, payout_alipay_account: 'a', payout_alipay_name: 'b' }
    post.mockResolvedValueOnce({ data: { id: 9 } })
    const res = await createWithdrawal(payload)
    expect(post).toHaveBeenCalledWith('/user/points/withdrawals', payload)
    expect(res.id).toBe(9)
  })

  it('default export exposes all functions', () => {
    expect(typeof pointsApiDefault.getPointsOverview).toBe('function')
    expect(typeof pointsApiDefault.createWithdrawal).toBe('function')
    expect(typeof pointsApiDefault.redeemPointsToPlan).toBe('function')
  })
})
