import { computed, onMounted, watch } from 'vue'

import { useCurrencyDisplay } from '@/composables/useCurrencyDisplay'
import { useTopUpEntry } from '@/composables/useTopUpEntry'
import { useAppStore, useAuthStore, useSubscriptionStore } from '@/stores'
import { useLowBalanceStore } from '@/stores/lowBalance'
import { isLowBalance, resolveLowBalanceThreshold } from '@/utils/lowBalance'

/**
 * 低余额站内横幅的显示条件（rivo 第 1 批 PR-3）。
 *
 * 余额低于阈值，并且满足下面任一条才显示：
 *   ① 用户没有生效中的订阅；
 *   ② 用户有订阅，但近 7 天从钱包余额扣过费（钱包扣费 = 使用记录里 billing_type 为钱包）。
 * 只用订阅的用户钱包余额常年是 0，不能当成告警。
 *
 * 另外：
 * - 阈值：用户自己设的优先，没设用站点默认（见 utils/lowBalance）。
 * - 没有支付（free 站、后台关了支付）或简易模式时不显示：横幅的出口是「充值」，没有出口就不催。
 * - 用户在「余额不足提醒」里关掉了提醒就不显示；管理员不显示。
 * - 订阅信息没回来之前不显示，避免订阅用户先闪一下再消失。
 * - 关掉后同一浏览器会话内不再弹，余额回到阈值以上后再跌破才重新提醒。
 */
export function useLowBalanceBanner() {
  const appStore = useAppStore()
  const authStore = useAuthStore()
  const subscriptionStore = useSubscriptionStore()
  const lowBalanceStore = useLowBalanceStore()
  const { formatWallet } = useCurrencyDisplay()
  const { canTopUp, topUpLocation } = useTopUpEntry()

  const user = computed(() => authStore.user)
  const userId = computed(() => user.value?.id ?? null)

  const applicable = computed(
    () =>
      !!user.value &&
      !authStore.isAdmin &&
      !authStore.isSimpleMode &&
      !appStore.backendModeEnabled &&
      canTopUp.value &&
      user.value.balance_notify_enabled !== false
  )

  const threshold = computed(() => resolveLowBalanceThreshold(user.value, appStore.cachedPublicSettings))
  const isLow = computed(() => applicable.value && isLowBalance(user.value?.balance, threshold.value))

  const subscriptionsLoaded = computed(() => subscriptionStore.loaded)
  const hasSubscription = computed(() => subscriptionStore.hasActiveSubscriptions)

  const needsWalletDebitCheck = computed(() => isLow.value && subscriptionsLoaded.value && hasSubscription.value)

  const eligible = computed(() => {
    if (!isLow.value || !subscriptionsLoaded.value || userId.value === null) return false
    if (!hasSubscription.value) return true
    return lowBalanceStore.recentWalletDebit(userId.value) === true
  })

  const visible = computed(
    () => eligible.value && userId.value !== null && !lowBalanceStore.isDismissed(userId.value)
  )

  const depleted = computed(() => (user.value?.balance ?? 0) <= 0)
  const balanceText = computed(() => formatWallet(user.value?.balance))

  function dismiss() {
    if (userId.value !== null) lowBalanceStore.dismiss(userId.value)
  }

  // 订阅用户才需要查近 7 天的扣费，而且只在余额真的低于阈值时查。
  watch(
    [needsWalletDebitCheck, userId],
    ([needed, id]) => {
      if (needed && id !== null) void lowBalanceStore.refreshWalletDebit(id)
    },
    { immediate: true }
  )

  // 余额回到阈值以上：清掉「已关闭」，下次再跌破时重新提醒。
  watch(
    [isLow, userId],
    ([low, id]) => {
      if (!low && id !== null && user.value) lowBalanceStore.clearDismissed(id)
    },
    { immediate: true }
  )

  onMounted(() => {
    // 带缓存且去重；App.vue 登录后已预载并轮询，这里只是兜底。
    if (!applicable.value) return
    subscriptionStore.fetchActiveSubscriptions().catch((error) => {
      console.error('Failed to load subscriptions for the low-balance banner:', error)
    })
  })

  return { visible, depleted, balanceText, topUpLocation, dismiss }
}
