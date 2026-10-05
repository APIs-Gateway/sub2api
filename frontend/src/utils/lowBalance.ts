import type { PublicSettings, User } from '@/types'

/**
 * 低余额横幅的阈值与判断。
 *
 * 阈值的单位和余额一致：站内额度（美元口径，即 users.balance 的单位）。后端的余额不足邮件
 * 直接拿余额和阈值比（backend/internal/service/balance_notify_service.go），这里照同一套规则：
 *
 * 1. 用户自己设过阈值（balance_notify_threshold > 0）就用用户的，没设就用站点默认
 *    （公开设置 balance_low_notify_threshold，后台「余额不足提醒」里配的那个值）；
 * 2. 阈值类型为 percentage 时，阈值是「累计充值额的百分之几」，折算成额度再比较
 *    （累计充值为 0 时按原值，与后端一致）；用户设置页只能设固定值，这一支只为和后端保持一致。
 *
 * 返回 0 表示没有可用阈值（用户没设、站点也没配），此时不提醒。
 * 人民币模式下的展示由调用方走 formatWallet（额度 ÷ 充值倍率），这里一律是额度。
 */
export type LowBalanceUser = Pick<User, 'balance_notify_threshold'> &
  Partial<Pick<User, 'balance_notify_threshold_type' | 'total_recharged'>>

function positive(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : 0
}

export function resolveLowBalanceThreshold(
  user: LowBalanceUser | null | undefined,
  settings: Pick<PublicSettings, 'balance_low_notify_threshold'> | null | undefined
): number {
  if (!user) return 0
  const base = positive(user.balance_notify_threshold) || positive(settings?.balance_low_notify_threshold)
  if (base <= 0) return 0
  const totalRecharged = positive(user.total_recharged)
  if (user.balance_notify_threshold_type === 'percentage' && totalRecharged > 0) {
    return (totalRecharged * base) / 100
  }
  return base
}

/** 余额低于阈值。阈值为 0（没有可用阈值）永远不算低。 */
export function isLowBalance(balance: number | null | undefined, threshold: number): boolean {
  return threshold > 0 && typeof balance === 'number' && Number.isFinite(balance) && balance < threshold
}

/** YYYY-MM-DD（本地时区），与使用记录页的日期口径一致。 */
function formatLocalDate(date: Date): string {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}

/** 含今天在内的最近 N 天（与使用记录页默认的「近 7 天」同一口径）。 */
export function recentDaysRange(days: number, now: Date = new Date()): { start_date: string; end_date: string } {
  const start = new Date(now)
  start.setDate(start.getDate() - (days - 1))
  return { start_date: formatLocalDate(start), end_date: formatLocalDate(now) }
}
