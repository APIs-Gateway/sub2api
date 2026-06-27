/**
 * User Subscription API
 * API for regular users to view their own subscriptions and progress
 */

import { apiClient } from './client'
import type { UserSubscription, SubscriptionProgress } from '@/types'

/**
 * Subscription summary for user dashboard
 */
export interface SubscriptionSummary {
  active_count: number
  subscriptions: Array<{
    id: number
    group_name: string
    status: string
    daily_progress: number | null
    weekly_progress: number | null
    monthly_progress: number | null
    expires_at: string | null
    days_remaining: number | null
  }>
}

/**
 * Get list of current user's subscriptions
 */
export async function getMySubscriptions(): Promise<UserSubscription[]> {
  const response = await apiClient.get<UserSubscription[]>('/subscriptions')
  return response.data
}

/**
 * Get current user's active subscriptions
 */
export async function getActiveSubscriptions(): Promise<UserSubscription[]> {
  const response = await apiClient.get<UserSubscription[]>('/subscriptions/active')
  return response.data
}

/**
 * Get progress for all user's active subscriptions
 */
export async function getSubscriptionsProgress(): Promise<SubscriptionProgress[]> {
  const response = await apiClient.get<SubscriptionProgress[]>('/subscriptions/progress')
  return response.data
}

/**
 * Get subscription summary for dashboard display
 */
export async function getSubscriptionSummary(): Promise<SubscriptionSummary> {
  const response = await apiClient.get<SubscriptionSummary>('/subscriptions/summary')
  return response.data
}

/**
 * Get progress for a specific subscription
 */
export async function getSubscriptionProgress(
  subscriptionId: number
): Promise<SubscriptionProgress> {
  const response = await apiClient.get<SubscriptionProgress>(
    `/subscriptions/${subscriptionId}/progress`
  )
  return response.data
}

/** 手动透支「借一天」结果（对齐后端 service.ManualOverdraftResult）。 */
export interface ManualOverdraftResult {
  subscription_id: number
  new_expires_at: string
  new_expire_day: number
  monthly_overdraft_remaining: number
}

/**
 * 手动透支「借一天」（三窗口模型，用户级、仅解日上限）。
 * 服务端在锁内：校验「有生效卡 + 当日额度撞满 + 本月<5 + 有未来天」→ daily_usage 清零 + expires_at −1 天 + 月度计数++。
 * 周/月封顶仍生效。idempotencyKey 通过 Idempotency-Key 头传给后端做持久化去重：同键重放返回首次结果、
 * 不重复借天/计数（POST /subscriptions/overdraft）。
 */
export async function borrowOverdraftDay(idempotencyKey: string): Promise<ManualOverdraftResult> {
  const response = await apiClient.post<ManualOverdraftResult>(
    '/subscriptions/overdraft',
    {},
    { headers: { 'Idempotency-Key': idempotencyKey } }
  )
  return response.data
}

/**
 * 续费报价（只读预览）。续费**走法币支付网关**：拿到报价后下单走 POST /payment/orders
 * （order_type=subscription, subscription_intent=renew, validity_days），支付成功回调履约延长有效期。
 */
export interface RenewOrderQuote {
  subscription_id: number
  daily_amount_usd: number
  added_days: number
  /** 续费价 = cfg.Price(card.D, T')（恒 >0，走网关收全价）。 */
  price: number
  unit_price: number
  group_id: number
}

/**
 * 转套餐报价（只读预览）。补差价**走法币支付网关**（仅 diff>0 可下单；diff≤0 报价即拒：降档赔钱/持平）。
 * 拿到 diff>0 后下单走 POST /payment/orders（order_type=subscription, subscription_intent=change_plan,
 * daily_amount_usd, validity_days），支付成功回调履约关旧开新。
 */
export interface ChangePlanOrderQuote {
  old_subscription_id: number
  daily_amount_usd: number
  validity_days: number
  weekly_cap_usd: number
  monthly_cap_usd: number
  /** P_新 = D_新×T_新×u(D_新)。 */
  new_plan_price: number
  /** V = 旧卡按剩余服务天数折出的剩余价值。 */
  old_remaining_value: number
  /** P_新 − V：>0 走网关补差价；≤0 后端报价即拒（降档赔钱/持平无差价）。 */
  diff: number
  unit_price: number
}

/** 续费报价（统一 D+T）：同 D 续 T' 天（整月），返回续费价等供前端预览。POST /subscriptions/renew/quote */
export async function renewQuote(validityDays: number): Promise<RenewOrderQuote> {
  const response = await apiClient.post<RenewOrderQuote>('/subscriptions/renew/quote', {
    validity_days: validityDays,
  })
  return response.data
}

/** 转套餐报价（统一 D+T）：算新档价 P_新、旧卡剩余价值 V、差价 diff。POST /subscriptions/change-plan/quote */
export async function changePlanQuote(
  dailyAmountUsd: number,
  validityDays: number
): Promise<ChangePlanOrderQuote> {
  const response = await apiClient.post<ChangePlanOrderQuote>('/subscriptions/change-plan/quote', {
    daily_amount_usd: dailyAmountUsd,
    validity_days: validityDays,
  })
  return response.data
}

/** 自定义购买区间（每日额度 D / 有效天数 T / 单价 u 的允许范围）；购买页据此设滑块/校验。 */
export interface SubscriptionPricingBounds {
  d_min: number
  d_max: number
  u_min: number
  u_max: number
  t_min: number
  t_max: number
  /** 有效天数步长：T 必须为该值整数倍（默认 30，即按整月购买 30/60/90…） */
  t_step: number
}

/** 自定义购买报价：按 D+T 算售价 + 派生周/月封顶（金额由后端公式决定，前端只展示）。 */
export interface SubscriptionQuote {
  daily_amount_usd: number
  validity_days: number
  unit_price: number
  price: number
  weekly_cap_usd: number
  monthly_cap_usd: number
  formula_version: number
}

/** 取自定义购买区间（滑块范围）。 */
export async function getSubscriptionPricing(): Promise<SubscriptionPricingBounds> {
  const response = await apiClient.get<SubscriptionPricingBounds>('/subscriptions/pricing')
  return response.data
}

/** 实时报价：D + T → 售价 P=D×T×u(D) + 派生周/月封顶。校验失败抛 INVALID_SUBSCRIPTION_PARAMS。 */
export async function quoteSubscription(
  dailyAmountUsd: number,
  validityDays: number
): Promise<SubscriptionQuote> {
  const response = await apiClient.post<SubscriptionQuote>('/subscriptions/quote', {
    daily_amount_usd: dailyAmountUsd,
    validity_days: validityDays,
  })
  return response.data
}

export default {
  getMySubscriptions,
  getActiveSubscriptions,
  getSubscriptionsProgress,
  getSubscriptionSummary,
  getSubscriptionProgress,
  borrowOverdraftDay,
  getSubscriptionPricing,
  quoteSubscription,
  renewQuote,
  changePlanQuote
}
