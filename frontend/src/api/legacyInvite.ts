/**
 * Legacy invite claim API endpoints
 *
 * 面向「在主站消费达标的老用户」：用主站邮箱验证身份后，领取一个本站的一次性邀请码。
 * 三个接口都不需要登录——来领码的人正是因为还没有本站账号。
 */

import { apiClient } from './client'

/**
 * 领码入口的开放状态，用于渲染页面文案。
 *
 * 达标有两条并列的口径：min_paid_amount 是主站累计付费（元），
 * min_usage_cost 是主站累计用量消费（美元），满足任意一条即可领码。
 * min_usage_cost 为 0 表示后一条口径没开，页面只展示付费门槛。
 */
export interface LegacyInviteStatus {
  enabled: boolean
  min_paid_amount: number
  min_usage_cost: number
}

export interface LegacyInviteSendCodeRequest {
  email: string
  turnstile_token?: string
}

export interface LegacyInviteSendCodeResponse {
  countdown: number
}

export interface LegacyInviteClaimRequest {
  email: string
  code: string
}

export interface LegacyInviteClaimResponse {
  invitation_code: string
  expires_at?: string
  /** true 表示这个码是之前就领过的同一个，不是新发的 */
  already_claimed: boolean
}

/**
 * 查询领码入口是否开放。关闭时页面直接渲染「暂未开放」，不再暴露后续表单。
 */
export async function getLegacyInviteStatus(): Promise<LegacyInviteStatus> {
  const { data } = await apiClient.get<LegacyInviteStatus>('/legacy-invite/status')
  return data
}

/**
 * 向主站邮箱发送验证码，用于证明邮箱归属。
 */
export async function sendLegacyInviteCode(
  request: LegacyInviteSendCodeRequest
): Promise<LegacyInviteSendCodeResponse> {
  const { data } = await apiClient.post<LegacyInviteSendCodeResponse>(
    '/legacy-invite/send-code',
    request
  )
  return data
}

/**
 * 校验验证码并领取邀请码。
 */
export async function claimLegacyInvite(
  request: LegacyInviteClaimRequest
): Promise<LegacyInviteClaimResponse> {
  const { data } = await apiClient.post<LegacyInviteClaimResponse>('/legacy-invite/claim', request)
  return data
}
