import type { UserSubscription } from '@/types'
const ONE_DAY_MS = 24 * 60 * 60 * 1000

export type ExpirationDateRelation = 'expired' | 'today' | 'tomorrow' | 'later'

export type RemainingExpiryDuration =
  | { unit: 'days'; days: number }
  | { unit: 'hoursMinutes'; hours: number; minutes: number }

export interface RemainingDurationParts {
  days: number
  hours: number
  minutes: number
}

export function isOneTimeDailyQuota(
  subscription: Pick<UserSubscription, 'starts_at' | 'expires_at'>
): boolean {
  if (!subscription.starts_at || !subscription.expires_at) return false

  const startsAt = new Date(subscription.starts_at).getTime()
  const expiresAt = new Date(subscription.expires_at).getTime()

  if (!Number.isFinite(startsAt) || !Number.isFinite(expiresAt)) return false

  return expiresAt <= startsAt + ONE_DAY_MS
}

export function getRemainingDurationParts(
  targetAt: Date | string,
  now: Date = new Date()
): RemainingDurationParts | null {
  const targetTime = targetAt instanceof Date ? targetAt.getTime() : new Date(targetAt).getTime()
  const nowTime = now.getTime()

  if (!Number.isFinite(targetTime) || !Number.isFinite(nowTime)) return null

  const diffMs = targetTime - nowTime
  if (diffMs <= 0) return null

  const totalMinutes = Math.floor(diffMs / (1000 * 60))
  const days = Math.floor(totalMinutes / (24 * 60))
  const hours = Math.floor((totalMinutes % (24 * 60)) / 60)
  const minutes = totalMinutes % 60

  return { days, hours, minutes }
}

export function getExpirationDateRelation(
  targetAt: Date | string,
  now: Date = new Date()
): ExpirationDateRelation | null {
  const target = targetAt instanceof Date ? targetAt : new Date(targetAt)
  const targetTime = target.getTime()
  const nowTime = now.getTime()

  if (!Number.isFinite(targetTime) || !Number.isFinite(nowTime)) return null
  if (targetTime <= nowTime) return 'expired'

  const targetDay = Date.UTC(target.getFullYear(), target.getMonth(), target.getDate())
  const currentDay = Date.UTC(now.getFullYear(), now.getMonth(), now.getDate())
  const calendarDays = Math.round((targetDay - currentDay) / ONE_DAY_MS)

  if (calendarDays === 0) return 'today'
  if (calendarDays === 1) return 'tomorrow'
  return 'later'
}

export function getRemainingExpiryDuration(
  targetAt: Date | string,
  now: Date = new Date()
): RemainingExpiryDuration | null {
  const targetTime = targetAt instanceof Date ? targetAt.getTime() : new Date(targetAt).getTime()
  const nowTime = now.getTime()

  if (!Number.isFinite(targetTime) || !Number.isFinite(nowTime)) return null

  const diffMs = targetTime - nowTime
  if (diffMs <= 0) return null
  if (diffMs >= ONE_DAY_MS) {
    return { unit: 'days', days: Math.ceil(diffMs / ONE_DAY_MS) }
  }

  const totalMinutes = Math.ceil(diffMs / (60 * 1000))
  return {
    unit: 'hoursMinutes',
    hours: Math.floor(totalMinutes / 60),
    minutes: totalMinutes % 60
  }
}

type SubscriptionNameSource = Pick<UserSubscription, 'group' | 'daily_amount_usd' | 'daily_limit_usd'>

/**
 * 订阅卡的展示名。分组只是卡的历史来源：自定义卡没有分组，来源分组被后台删除后卡仍保留、
 * 但读不到分组。这两种情况都回退到与「我的订阅」卡片标题一致的「每日 X」
 * （没有日额度则为「无限制」），不要把分组 ID 展示出来。
 *
 * 金额的币种由调用方决定，必须传入：用户端传 useCurrencyDisplay 的 formatSubscription
 * （按卡的单价折算，人民币模式下是 ¥），管理后台传 formatUsdAmount（账本数据保持美元）。
 * 这里不替调用方选币种，否则用户端会出现标题是 $、旁边的用量却是 ¥。
 *
 * @param formatDailyAmount 把日额度（美元口径的额度值）格式化成展示文本。
 */
export function subscriptionDisplayName(
  subscription: SubscriptionNameSource,
  t: (key: string) => string,
  formatDailyAmount: (dailyAmount: number) => string
): string {
  const groupName = subscription.group?.name?.trim()
  if (groupName) return groupName

  const dailyAmount = subscription.daily_amount_usd ?? subscription.daily_limit_usd ?? 0
  if (dailyAmount > 0) return `${t('userSubscriptions.daily')} ${formatDailyAmount(dailyAmount)}`
  return t('userSubscriptions.unlimited')
}
