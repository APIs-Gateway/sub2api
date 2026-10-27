import type { SubscriptionPlan } from '@/types/payment'

/** 套餐有效期折成天数：validity_unit 可以是 day / week / month（月按 30 天）。 */
export function planValidityDays(plan: Pick<SubscriptionPlan, 'validity_days' | 'validity_unit'>): number {
  const unit = String(plan.validity_unit || 'day').trim().toLowerCase()
  const base = unit.endsWith('s') ? unit.slice(0, -1) : unit
  const n = Number(plan.validity_days) || 0
  if (base === 'month') return n * 30
  if (base === 'week') return n * 7
  return n
}

/**
 * 固定套餐 1 个额度值多少人民币：实付金额 ÷（每日额度 × 天数）。
 * 套餐没有每日额度或有效期时无法折算，返回 null，调用方应隐藏人民币额度而不是混入美元。
 *
 * @param paymentAmount 这个套餐实付的法币金额（已按订阅付款倍率折算）。
 */
export function planFiatPerCredit(
  plan: Pick<SubscriptionPlan, 'validity_days' | 'validity_unit' | 'daily_amount_usd' | 'daily_limit_usd'>,
  paymentAmount: number
): number | null {
  const daily = plan.daily_amount_usd ?? plan.daily_limit_usd ?? 0
  const days = planValidityDays(plan)
  if (!(daily > 0) || !(days > 0) || !(paymentAmount > 0)) return null
  return paymentAmount / (daily * days)
}
