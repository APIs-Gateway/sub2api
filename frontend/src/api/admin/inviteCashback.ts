import { apiClient } from '../client'
import type { PaginatedResponse } from '@/types'
import type { ListAffiliateRecordsParams } from './affiliates'

// 兑换码返现台账记录（后台 cashback 记录表）。
// 旧版用户端「邀请返现」页已退役（issue #11 方案 C），此类型迁至本后台域。
export interface InviteCashbackRecord {
  ledger_id: number
  inviter_id: number
  inviter_email: string
  inviter_username: string
  invitee_id: number
  invitee_email: string
  invitee_username: string
  redeem_code_id?: number | null
  redeem_code?: string
  redeem_code_type: string
  redeem_value: number
  subscription_group_id?: number | null
  subscription_group?: string
  validity_days?: number | null
  cashback_base_amount: number
  cashback_rate_percent: number
  cashback_amount: number
  inviter_balance_after?: number | null
  created_at: string
}

export interface CashbackFaceValue {
  group_id: number
  group_name: string
  group_description?: string
  platform: string
  validity_days: number
  display_name: string
  cashback_base_amount: number
  created_at?: string
  updated_at?: string
}

export interface CashbackSettings {
  enabled: boolean
  rate_percent: number
  subscription_mappings: CashbackFaceValue[]
}

export async function getCashbackSettings(): Promise<CashbackSettings> {
  const { data } = await apiClient.get<CashbackSettings>('/admin/affiliates/cashback/settings')
  return data
}

export async function updateCashbackSettings(payload: CashbackSettings): Promise<CashbackSettings> {
  const { data } = await apiClient.put<CashbackSettings>('/admin/affiliates/cashback/settings', payload)
  return data
}

function recordParams(params: ListAffiliateRecordsParams = {}) {
  return {
    page: params.page ?? 1,
    page_size: params.page_size ?? 20,
    search: params.search ?? '',
    start_at: params.start_at || undefined,
    end_at: params.end_at || undefined,
    sort_by: params.sort_by || undefined,
    sort_order: params.sort_order || undefined,
    timezone: params.timezone || undefined,
  }
}

export async function listCashbackRecords(
  params: ListAffiliateRecordsParams = {},
): Promise<PaginatedResponse<InviteCashbackRecord>> {
  const { data } = await apiClient.get<PaginatedResponse<InviteCashbackRecord>>(
    '/admin/affiliates/cashback/records',
    { params: recordParams(params) },
  )
  return data
}

export default {
  getCashbackSettings,
  updateCashbackSettings,
  listCashbackRecords,
}
