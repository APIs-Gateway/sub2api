import { beforeEach, describe, expect, it, vi } from 'vitest'

// 邀请返利积分制（issue #11）—— 后台 points API 单测：端点/入参（含空 status/search 转 undefined）/分页默认值。

const { get, post, put } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: { get, post, put },
}))

import {
  getPointsSettings,
  updatePointsSettings,
  listPointsWithdrawals,
  approveWithdrawal,
  rejectWithdrawal,
  listPointsLedgerAdmin,
} from '@/api/admin/points'
import adminPointsDefault from '@/api/admin/points'

const settings = {
  enabled: true,
  peg: 0.01,
  cashback_rate_percent: 20,
  freeze_hours: 0,
  withdraw_enabled: true,
  withdraw_min_points: 0,
  withdraw_fee_percent: 10,
  withdraw_usd_cny_rate: 7.2,
  redeem_balance_on: true,
  redeem_plan_on: true,
}

describe('admin points api', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
    put.mockReset()
    get.mockResolvedValue({ data: {} })
    post.mockResolvedValue({ data: {} })
    put.mockResolvedValue({ data: {} })
  })

  it('getPointsSettings hits settings endpoint', async () => {
    get.mockResolvedValueOnce({ data: settings })
    const res = await getPointsSettings()
    expect(get).toHaveBeenCalledWith('/admin/points/settings')
    expect(res.peg).toBe(0.01)
  })

  it('updatePointsSettings puts payload', async () => {
    put.mockResolvedValueOnce({ data: settings })
    await updatePointsSettings(settings)
    expect(put).toHaveBeenCalledWith('/admin/points/settings', settings)
  })

  it('listPointsWithdrawals uses defaults + maps empty filters to undefined', async () => {
    await listPointsWithdrawals()
    expect(get).toHaveBeenCalledWith('/admin/points/withdrawals', {
      params: { status: undefined, search: undefined, page: 1, page_size: 20 },
    })
  })

  it('listPointsWithdrawals forwards provided filters/pagination', async () => {
    await listPointsWithdrawals({ status: 'pending', search: 'foo', page: 2, page_size: 50 })
    expect(get).toHaveBeenCalledWith('/admin/points/withdrawals', {
      params: { status: 'pending', search: 'foo', page: 2, page_size: 50 },
    })
  })

  it('approveWithdrawal posts to approve endpoint', async () => {
    await approveWithdrawal(5, { payout_proof: 'tx' })
    expect(post).toHaveBeenCalledWith('/admin/points/withdrawals/5/approve', { payout_proof: 'tx' })
  })

  it('approveWithdrawal defaults payload to {}', async () => {
    await approveWithdrawal(6)
    expect(post).toHaveBeenCalledWith('/admin/points/withdrawals/6/approve', {})
  })

  it('rejectWithdrawal posts to reject endpoint', async () => {
    await rejectWithdrawal(8, { note: 'bad account' })
    expect(post).toHaveBeenCalledWith('/admin/points/withdrawals/8/reject', { note: 'bad account' })
  })

  it('listPointsLedgerAdmin uses defaults + maps empty filters to undefined', async () => {
    await listPointsLedgerAdmin()
    expect(get).toHaveBeenCalledWith('/admin/points/ledger', {
      params: { kind: undefined, search: undefined, page: 1, page_size: 20 },
    })
  })

  it('listPointsLedgerAdmin forwards filters', async () => {
    await listPointsLedgerAdmin({ kind: 'earn', search: 'x', page: 3, page_size: 10 })
    expect(get).toHaveBeenCalledWith('/admin/points/ledger', {
      params: { kind: 'earn', search: 'x', page: 3, page_size: 10 },
    })
  })

  it('default export exposes all functions', () => {
    expect(typeof adminPointsDefault.getPointsSettings).toBe('function')
    expect(typeof adminPointsDefault.approveWithdrawal).toBe('function')
  })
})
