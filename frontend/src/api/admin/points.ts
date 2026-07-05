import { apiClient } from '../client'
import type { PaginatedResponse } from '@/types'
import type { PointsLedgerEntry, PointsWithdrawal } from '@/api/points'

// 邀请返利积分制（issue #11）—— 后台 API。

export interface PointsSettings {
  enabled: boolean
  peg: number
  cashback_rate_percent: number
  freeze_hours: number
  withdraw_enabled: boolean
  withdraw_min_points: number
  withdraw_fee_percent: number
  withdraw_usd_cny_rate: number
  redeem_balance_on: boolean
  redeem_plan_on: boolean
}

export interface ListWithdrawalsParams {
  status?: string
  search?: string
  page?: number
  page_size?: number
}

export interface ReviewWithdrawalPayload {
  note?: string
  payout_proof?: string
}

export interface ListLedgerParams {
  kind?: string
  search?: string
  page?: number
  page_size?: number
}

export async function getPointsSettings(): Promise<PointsSettings> {
  const { data } = await apiClient.get<PointsSettings>('/admin/points/settings')
  return data
}

export async function updatePointsSettings(payload: PointsSettings): Promise<PointsSettings> {
  const { data } = await apiClient.put<PointsSettings>('/admin/points/settings', payload)
  return data
}

export async function listPointsWithdrawals(
  params: ListWithdrawalsParams = {},
): Promise<PaginatedResponse<PointsWithdrawal>> {
  const { data } = await apiClient.get<PaginatedResponse<PointsWithdrawal>>('/admin/points/withdrawals', {
    params: {
      status: params.status || undefined,
      search: params.search || undefined,
      page: params.page ?? 1,
      page_size: params.page_size ?? 20,
    },
  })
  return data
}

export async function approveWithdrawal(id: number, payload: ReviewWithdrawalPayload = {}): Promise<PointsWithdrawal> {
  const { data } = await apiClient.post<PointsWithdrawal>(`/admin/points/withdrawals/${id}/approve`, payload)
  return data
}

export async function rejectWithdrawal(id: number, payload: ReviewWithdrawalPayload = {}): Promise<PointsWithdrawal> {
  const { data } = await apiClient.post<PointsWithdrawal>(`/admin/points/withdrawals/${id}/reject`, payload)
  return data
}

export async function listPointsLedgerAdmin(
  params: ListLedgerParams = {},
): Promise<PaginatedResponse<PointsLedgerEntry>> {
  const { data } = await apiClient.get<PaginatedResponse<PointsLedgerEntry>>('/admin/points/ledger', {
    params: {
      kind: params.kind || undefined,
      search: params.search || undefined,
      page: params.page ?? 1,
      page_size: params.page_size ?? 20,
    },
  })
  return data
}

export default {
  getPointsSettings,
  updatePointsSettings,
  listPointsWithdrawals,
  approveWithdrawal,
  rejectWithdrawal,
  listPointsLedgerAdmin,
}
