import { apiClient } from './client'
import type { PaginatedResponse, UserAffiliateDetail } from '@/types'

// 邀请返利积分制（issue #11）—— 用户端 API。

export interface PointsAccount {
  user_id: number
  available: number
  frozen: number
  lifetime_earned: number
  created_at: string
  updated_at: string
}

export interface PointsConfig {
  enabled: boolean
  peg: number
  balance_redeem_rate: number
  withdraw_enabled: boolean
  withdraw_min_points: number
  withdraw_fee_percent: number
  withdraw_usd_cny_rate: number
  redeem_balance_on: boolean
  redeem_plan_on: boolean
}

export interface PointsOverview {
  account: PointsAccount
  affiliate: UserAffiliateDetail
  effective_rate: number
  config: PointsConfig
}

export interface PointsLedgerEntry {
  id: number
  user_id: number
  user_email?: string
  username?: string
  kind: string
  points: number
  peg_at?: number | null
  source_user_id?: number | null
  source_order_id?: number | null
  withdrawal_id?: number | null
  frozen_until?: string | null
  available_after?: number | null
  frozen_after?: number | null
  note?: string
  created_at: string
}

export type PointsPayoutMethod = 'alipay' | 'usdt'
export type PointsUSDTChain = 'TRC20' | 'ERC20' | 'BEP20'

export interface PointsWithdrawal {
  id: number
  user_id: number
  user_email?: string
  username?: string
  points: number
  gross_amount: number
  fee_amount: number
  net_amount: number
  payout_currency?: 'CNY' | 'USD'
  usd_cny_rate_at?: number | null
  peg_at?: number | null
  fee_percent_at?: number | null
  payout_method: PointsPayoutMethod
  payout_alipay_account?: string
  payout_alipay_name?: string
  payout_usdt_chain?: PointsUSDTChain
  payout_usdt_address?: string
  status: 'pending' | 'paid' | 'rejected'
  review_note?: string
  reviewed_by?: number | null
  payout_proof?: string
  created_at: string
  updated_at: string
  reviewed_at?: string | null
}

export interface PointsPlanOption {
  validity_days: number
  daily_amount_usd: number
  unit_price: number
  price: number
  points_price: number
  weekly_cap_usd: number
  monthly_cap_usd: number
}

export interface CreateWithdrawalPayload {
  points: number
  payout_method: PointsPayoutMethod
  payout_alipay_account?: string
  payout_alipay_name?: string
  payout_usdt_chain?: PointsUSDTChain
  payout_usdt_address?: string
}

export async function getPointsOverview(): Promise<PointsOverview> {
  const { data } = await apiClient.get<PointsOverview>('/user/points/overview')
  return data
}

export async function listPointsLedger(page = 1, pageSize = 20): Promise<PaginatedResponse<PointsLedgerEntry>> {
  const { data } = await apiClient.get<PaginatedResponse<PointsLedgerEntry>>('/user/points/ledger', {
    params: { page, page_size: pageSize },
  })
  return data
}

export async function listPointsPlans(): Promise<PointsPlanOption[]> {
  const { data } = await apiClient.get<{ plans: PointsPlanOption[] }>('/user/points/plans')
  return data.plans ?? []
}

export async function redeemPointsToBalance(points: number): Promise<{ balance: number }> {
  const { data } = await apiClient.post<{ balance: number }>('/user/points/redeem-balance', { points })
  return data
}

export async function redeemPointsToPlan(dailyAmountUsd: number, validityDays: number, idempotencyKey = ''): Promise<unknown> {
  const { data } = await apiClient.post('/user/points/redeem-plan', {
    daily_amount_usd: dailyAmountUsd,
    validity_days: validityDays,
    idempotency_key: idempotencyKey,
  })
  return data
}

export async function listMyWithdrawals(): Promise<PointsWithdrawal[]> {
  const { data } = await apiClient.get<{ withdrawals: PointsWithdrawal[] }>('/user/points/withdrawals')
  return data.withdrawals ?? []
}

export async function createWithdrawal(payload: CreateWithdrawalPayload): Promise<PointsWithdrawal> {
  const { data } = await apiClient.post<PointsWithdrawal>('/user/points/withdrawals', payload)
  return data
}

export default {
  getPointsOverview,
  listPointsLedger,
  listPointsPlans,
  redeemPointsToBalance,
  redeemPointsToPlan,
  listMyWithdrawals,
  createWithdrawal,
}
