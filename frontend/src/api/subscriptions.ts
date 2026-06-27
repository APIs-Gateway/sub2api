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

/**
 * Set the max overdraft days on one of the current user's own subscription cards.
 * days = null → turn off overdraft; >=0 → turn on (0 = only today's accrual).
 */
export async function setOverdraftDays(
  subscriptionId: number,
  days: number | null
): Promise<void> {
  await apiClient.put(`/subscriptions/${subscriptionId}/overdraft`, {
    max_overdraft_days: days,
  })
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

export default {
  getMySubscriptions,
  getActiveSubscriptions,
  getSubscriptionsProgress,
  getSubscriptionSummary,
  getSubscriptionProgress,
  setOverdraftDays,
  renewSubscription,
  changeSubscriptionPlan
}
