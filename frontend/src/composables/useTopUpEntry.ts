import { computed } from 'vue'

import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { FeatureFlags, resolveFeatureFlag } from '@/utils/featureFlags'

/**
 * 「充值」入口是否可用，以及它指向哪里。
 *
 * 入口指向购买页，所以门控与侧栏、账号菜单里的「充值/订阅」完全一致：
 * 后台把支付关掉（含 free 站这类没有支付的站点）或处于简易模式时不出现。
 * 设置未加载完时按开启处理，避免侧栏一闪而过。
 *
 * 余额卡和低余额横幅共用这一处，两个地方不会各判各的。
 */
export const TOP_UP_LOCATION = { path: '/purchase', query: { tab: 'recharge' } } as const

export function useTopUpEntry() {
  const appStore = useAppStore()
  const authStore = useAuthStore()

  const canTopUp = computed(
    () => resolveFeatureFlag(FeatureFlags.payment, appStore.cachedPublicSettings) && !authStore.isSimpleMode
  )

  return { canTopUp, topUpLocation: TOP_UP_LOCATION }
}
