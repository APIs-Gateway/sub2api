import type { UserSubscription } from '@/types'

/**
 * 侧栏余额卡里的订阅剩余摘要。
 *
 * 订阅卡的额度按日/周/月三个窗口限额（限额挂卡，某个窗口为空或 0 表示该窗口不限）。
 * 摘要回答用户最关心的一件事：「订阅现在还能用多少」，所以取已配置的最窄窗口
 * （日 < 周 < 月）剩余多少，多张卡相加。
 *
 * 金额口径：钱包和订阅卡的额度单价不同，折算成人民币必须用每张卡自己的单价
 * （fiat_per_credit）。只要有一张卡拿不到单价，就不给人民币值（remainingFiat 为 null），
 * 由调用方回落到美元展示，与 formatSubscription 的「宁可展示美元也不猜单价」一致。
 */

export type SubscriptionWindow = 'daily' | 'weekly' | 'monthly'

export interface ActiveSubscriptionSummary {
  /** 生效中的订阅张数。 */
  count: number
  /** 至少有一张卡三个窗口都没配限额：额度不设上限，没有「剩余多少」可言。 */
  unlimited: boolean
  /** 参与统计的窗口；没有卡或全部不限时为 null。 */
  window: SubscriptionWindow | null
  /** 该窗口内剩余额度（美元口径的额度值，已用超的卡按 0 计）。 */
  remainingCredits: number
  /** 同一笔剩余额度折成人民币；有卡拿不到单价时为 null。 */
  remainingFiat: number | null
  /** 最早到期的那张卡的到期时间；都不到期时为 null。 */
  nextExpiresAt: string | null
}

const WINDOW_ORDER: readonly SubscriptionWindow[] = ['daily', 'weekly', 'monthly']

function limitOf(sub: UserSubscription, window: SubscriptionWindow): number | null {
  const limit =
    window === 'daily' ? sub.daily_limit_usd : window === 'weekly' ? sub.weekly_limit_usd : sub.monthly_limit_usd
  return typeof limit === 'number' && Number.isFinite(limit) && limit > 0 ? limit : null
}

function usageOf(sub: UserSubscription, window: SubscriptionWindow): number {
  const used =
    window === 'daily' ? sub.daily_usage_usd : window === 'weekly' ? sub.weekly_usage_usd : sub.monthly_usage_usd
  return typeof used === 'number' && Number.isFinite(used) && used > 0 ? used : 0
}

export function summarizeActiveSubscriptions(subs: readonly UserSubscription[]): ActiveSubscriptionSummary {
  const summary: ActiveSubscriptionSummary = {
    count: subs.length,
    unlimited: false,
    window: null,
    remainingCredits: 0,
    remainingFiat: 0,
    nextExpiresAt: null
  }
  if (subs.length === 0) return summary

  let soonest = Number.POSITIVE_INFINITY
  for (const sub of subs) {
    if (!sub.expires_at) continue
    const at = new Date(sub.expires_at).getTime()
    if (Number.isFinite(at) && at < soonest) {
      soonest = at
      summary.nextExpiresAt = sub.expires_at
    }
  }

  if (subs.some((sub) => WINDOW_ORDER.every((w) => limitOf(sub, w) === null))) {
    summary.unlimited = true
    summary.remainingFiat = null
    return summary
  }

  const window = WINDOW_ORDER.find((w) => subs.some((sub) => limitOf(sub, w) !== null)) ?? null
  summary.window = window
  if (!window) {
    summary.remainingFiat = null
    return summary
  }

  let fiat: number | null = 0
  for (const sub of subs) {
    const limit = limitOf(sub, window)
    // 这张卡没有配这个窗口：它在更宽的窗口里限额，不并入「最窄窗口」的合计。
    if (limit === null) continue
    const remaining = Math.max(0, limit - usageOf(sub, window))
    summary.remainingCredits += remaining
    const unit = sub.fiat_per_credit
    if (fiat !== null && typeof unit === 'number' && Number.isFinite(unit) && unit > 0) {
      fiat += remaining * unit
    } else {
      fiat = null
    }
  }
  summary.remainingFiat = fiat
  return summary
}
