/**
 * Low-balance banner state
 *
 * - 「近 7 天有没有从钱包余额扣过费」：订阅用户余额为 0 是常态，只有近期真的在扣余额时，
 *   余额低才值得提醒。取数用用户自己的使用记录接口，按扣费来源（billing_type = 0 即钱包）
 *   和近 7 天过滤，只要第一条，不需要后端改动。结果按用户缓存，避免每次换页都查一遍。
 * - 横幅被关掉的状态：按用户记在 sessionStorage，刷新页面不再弹；余额回升到阈值以上后清除，
 *   下次再跌破阈值会重新提醒。
 */

import { defineStore } from 'pinia'
import { ref } from 'vue'
import { usageAPI } from '@/api/usage'
import { recentDaysRange } from '@/utils/lowBalance'

/** 钱包余额的扣费来源（后端 BillingTypeBalance）；订阅套餐是 1。 */
export const BILLING_TYPE_WALLET = 0
export const WALLET_DEBIT_LOOKBACK_DAYS = 7

const WALLET_DEBIT_TTL_MS = 10 * 60 * 1000
/** 查询失败后多久再试：失败时按「没有近期扣费」处理（宁可不提醒，也不误报）。 */
const WALLET_DEBIT_RETRY_MS = 60 * 1000
const DISMISSED_KEY = 'low-balance-banner-dismissed'

interface WalletDebitEntry {
  userId: number
  /** true / false；null = 查询失败。 */
  value: boolean | null
  at: number
}

function readDismissed(): number[] {
  try {
    const raw = window.sessionStorage.getItem(DISMISSED_KEY)
    const parsed = raw ? JSON.parse(raw) : []
    return Array.isArray(parsed) ? parsed.filter((id): id is number => typeof id === 'number') : []
  } catch {
    return []
  }
}

function writeDismissed(ids: number[]) {
  try {
    window.sessionStorage.setItem(DISMISSED_KEY, JSON.stringify(ids))
  } catch {
    // 存不下就只在本次页面内生效，不影响使用
  }
}

export const useLowBalanceStore = defineStore('lowBalance', () => {
  const dismissedUserIds = ref<number[]>(readDismissed())
  const walletDebit = ref<WalletDebitEntry | null>(null)
  let inflight: { userId: number; promise: Promise<void> } | null = null

  function isDismissed(userId: number): boolean {
    return dismissedUserIds.value.includes(userId)
  }

  function dismiss(userId: number) {
    if (isDismissed(userId)) return
    dismissedUserIds.value = [...dismissedUserIds.value, userId]
    writeDismissed(dismissedUserIds.value)
  }

  function clearDismissed(userId: number) {
    if (!isDismissed(userId)) return
    dismissedUserIds.value = dismissedUserIds.value.filter((id) => id !== userId)
    writeDismissed(dismissedUserIds.value)
  }

  /** 该用户近 7 天是否从钱包余额扣过费；还没查到（或查询失败）时为 null。 */
  function recentWalletDebit(userId: number): boolean | null {
    const entry = walletDebit.value
    return entry && entry.userId === userId ? entry.value : null
  }

  function isFresh(entry: WalletDebitEntry | null, userId: number): boolean {
    if (!entry || entry.userId !== userId) return false
    const ttl = entry.value === null ? WALLET_DEBIT_RETRY_MS : WALLET_DEBIT_TTL_MS
    return Date.now() - entry.at < ttl
  }

  /** 查近 7 天的钱包扣费；有缓存且未过期、或同一个用户已有请求在途时直接复用。 */
  function refreshWalletDebit(userId: number): Promise<void> {
    if (isFresh(walletDebit.value, userId)) return Promise.resolve()
    if (inflight && inflight.userId === userId) return inflight.promise

    const range = recentDaysRange(WALLET_DEBIT_LOOKBACK_DAYS)
    const promise = usageAPI
      .query({ page: 1, page_size: 1, billing_type: BILLING_TYPE_WALLET, ...range })
      .then((page) => {
        walletDebit.value = { userId, value: (page.items?.length ?? 0) > 0, at: Date.now() }
      })
      .catch((error) => {
        console.error('Failed to check recent wallet usage for the low-balance banner:', error)
        walletDebit.value = { userId, value: null, at: Date.now() }
      })
      .finally(() => {
        if (inflight?.promise === promise) inflight = null
      })
    inflight = { userId, promise }
    return promise
  }

  return { dismissedUserIds, walletDebit, isDismissed, dismiss, clearDismissed, recentWalletDebit, refreshWalletDebit }
})
