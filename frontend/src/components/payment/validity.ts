import type { SubscriptionPlan } from '@/types/payment'

type TranslateFn = (key: string) => string

export function planValiditySuffix(
  plan: Pick<SubscriptionPlan, 'validity_days' | 'validity_unit'>,
  t: TranslateFn,
): string {
  const unit = String(plan.validity_unit || 'day').trim().toLowerCase()
  const base = unit.endsWith('s') ? unit.slice(0, -1) : unit
  if (base === 'month') {
    return plan.validity_days === 1 ? t('payment.perMonth') : `${plan.validity_days}${t('payment.months')}`
  }
  if (base === 'week') {
    return `${plan.validity_days}${t('payment.weeks')}`
  }
  return `${plan.validity_days}${t('payment.days')}`
}
