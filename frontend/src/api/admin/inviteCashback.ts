import { apiClient } from '../client'
import type { PaginatedResponse } from '@/types'
import type { InviteCashbackRecord } from '@/api/inviteCashback'
import type { ListAffiliateRecordsParams } from './affiliates'

export interface CashbackFaceValue {
  redeem_value: number
  cashback_base_amount: number
  created_at?: string
  updated_at?: string
}

export interface CashbackSettings {
  enabled: boolean
  rate_percent: number
  face_values: CashbackFaceValue[]
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
