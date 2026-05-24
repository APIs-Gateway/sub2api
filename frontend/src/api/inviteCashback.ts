import { apiClient } from './client'

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

export interface InviteCashbackInvitee {
  user_id: number
  email: string
  username: string
  created_at?: string
  total_rebate: number
}

export interface InviteCashbackDetail {
  user_id: number
  aff_code: string
  inviter_id?: number | null
  invited_count: number
  total_cashback: number
  cashback_enabled: boolean
  cashback_rate_percent: number
  invitees: InviteCashbackInvitee[]
  records: InviteCashbackRecord[]
}

export async function getInviteCashbackDetail(): Promise<InviteCashbackDetail> {
  const { data } = await apiClient.get<InviteCashbackDetail>('/user/invite-cashback')
  return data
}

export default {
  getInviteCashbackDetail,
}
