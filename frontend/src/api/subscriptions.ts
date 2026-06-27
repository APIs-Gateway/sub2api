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

/** 续费结果（同档延长发放期，从余额扣续费价）。 */
export interface RenewResult {
  subscription_id: number
  added_days: number
  price: number
  new_expire_day: number
}

/** 转套餐结果（旧卡折剩余价值抵新套餐，多退少补）。 */
export interface ChangePlanResult {
  old_subscription_id: number
  new_subscription_id: number
  old_remaining_value: number
  new_plan_price: number
  /** P_新 − V：>0 已从余额扣的补差价；<0 已退进余额的差价；0 持平。 */
  diff: number
  new_card_today_balance: number
  new_expire_day: number
}

/** 续费当前生效卡：同套餐（相同每日额度）续 T' 天，从其他余额扣续费价。 */
export async function renewSubscription(planId: number): Promise<RenewResult> {
  const response = await apiClient.post<RenewResult>('/subscriptions/renew', {
    plan_id: planId,
  })
  return response.data
}

/** 转套餐：旧卡按剩余天数折价抵扣新套餐，多退少补；每自然日最多一次。 */
export async function changeSubscriptionPlan(planId: number): Promise<ChangePlanResult> {
  const response = await apiClient.post<ChangePlanResult>('/subscriptions/change-plan', {
    plan_id: planId,
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
  renewSubscription,
  changeSubscriptionPlan
}
