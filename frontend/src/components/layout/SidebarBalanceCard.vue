<template>
  <section
    v-if="visible"
    class="balance-card flex-shrink-0 border-t border-gray-200 dark:border-dark-800"
    :class="collapsed ? 'px-3 py-3' : 'px-7 pb-4 pt-4'"
    :aria-label="t('dashboard.balance')"
    data-testid="sidebar-balance-card"
  >
    <!-- 图标态：只留一个入口，余额放进悬停提示和读屏文字 -->
    <router-link
      v-if="collapsed"
      :to="collapsedTarget"
      class="sidebar-link balance-card-icon-link"
      :title="collapsedTitle"
      data-testid="balance-card-collapsed"
      @click="onNavigate"
    >
      <Icon name="wallet" size="md" class="flex-shrink-0" />
      <span class="sr-only">{{ collapsedTitle }}</span>
    </router-link>

    <template v-else>
      <p class="metric-label">{{ t('dashboard.balance') }}</p>

      <!-- 用户资料还没回来：占位，不闪 ¥0.00 -->
      <div v-if="!user" class="mt-2 space-y-2" data-testid="balance-card-skeleton" aria-hidden="true">
        <div class="h-7 w-28 animate-pulse rounded bg-gray-200 dark:bg-dark-700"></div>
      </div>
      <NumText
        v-else
        tier="primary"
        class="mt-1 block truncate !text-[1.5rem]"
        :text="balanceText"
        :title="balanceExactText"
        data-testid="balance-card-amount"
      />

      <!-- 有生效订阅：订阅还剩多少。余额为 0 对订阅用户是常态，所以这里不做任何告警样式。
           屏幕高度不到 700px 时收起这一行，把空间还给导航（顶栏的订阅进度仍在） -->
      <router-link
        v-if="user && hasSubscription"
        to="/subscriptions"
        class="group mt-3 block rounded-sm border-t [@media(max-height:700px)]:hidden border-gray-200 pt-3 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-gray-900/15 dark:border-dark-700 dark:focus-visible:ring-white/20"
        data-testid="balance-card-subscription"
        @click="onNavigate"
      >
        <span class="flex items-baseline justify-between gap-2 text-xs text-gray-600 dark:text-dark-400">
          <span class="truncate">{{ subscriptionLabel }}</span>
          <span v-if="expiryText" class="flex-shrink-0 num" :class="expiryClass">{{ expiryText }}</span>
        </span>
        <span class="mt-1 block truncate text-sm text-gray-900 group-hover:text-black dark:text-white">
          <template v-if="summary.unlimited || !summary.window">{{ t('subscriptionProgress.unlimited') }}</template>
          <template v-else>
            {{ windowLeftLabel }}
            <NumText tier="secondary" :text="subscriptionAmountText" />
          </template>
        </span>
      </router-link>

      <div class="mt-4 flex items-center justify-between gap-3">
        <router-link
          v-if="canTopUp"
          :to="topUpLocation"
          class="btn btn-primary btn-sm"
          data-testid="balance-card-topup"
          @click="onNavigate"
        >
          {{ t('nav.topUp') }}
        </router-link>
        <router-link
          to="/usage"
          class="rounded-sm text-xs text-gray-600 underline-offset-2 transition-colors hover:text-gray-900 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-gray-900/15 dark:text-dark-300 dark:hover:text-white dark:focus-visible:ring-white/20"
          data-testid="balance-card-usage"
          @click="onNavigate"
        >
          {{ t('dashboard.viewUsage') }}
        </router-link>
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
/**
 * 侧栏余额卡：每个页面都看得到余额，并能一步去充值、去看费用。
 *
 * 挂在 AppSidebar 的 `balance-card` 插槽里，只在普通用户的标准模式下渲染。
 *
 * - 余额是钱包额度：人民币模式按充值倍率折成 ¥，美元模式（含倍率为 1 的 free 站）直接按 $，
 *   一律走 useCurrencyDisplay.formatWallet。
 * - 「充值」只在支付开启时出现，指向购买页的充值标签。
 * - 订阅用户的钱包余额常年为 0，所以卡上没有告警样式；订阅剩余另起一行。
 */
import { computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'

import Icon from '@/components/icons/Icon.vue'
import NumText from '@/components/common/NumText.vue'
import { EXACT_DIGITS, useCurrencyDisplay } from '@/composables/useCurrencyDisplay'
import { useTopUpEntry } from '@/composables/useTopUpEntry'
import { useAppStore, useAuthStore, useSubscriptionStore } from '@/stores'
import { getExpirationDateRelation } from '@/utils/subscriptionQuota'
import { summarizeActiveSubscriptions } from '@/utils/subscriptionSummary'

defineProps<{
  /** 侧栏收成图标态。 */
  collapsed?: boolean
}>()

const { t } = useI18n()
const appStore = useAppStore()
const authStore = useAuthStore()
const subscriptionStore = useSubscriptionStore()
const { isFiat, formatWallet, formatFiat, formatSubscription, fiatFromCredits } = useCurrencyDisplay()
const { canTopUp, topUpLocation } = useTopUpEntry()

const user = computed(() => authStore.user)
const visible = computed(() => !authStore.isSimpleMode)

const balanceText = computed(() => formatWallet(user.value?.balance))
const balanceExactText = computed(() => formatWallet(user.value?.balance, EXACT_DIGITS))

// ─── 订阅摘要 ────────────────────────────────────────────────
const hasSubscription = computed(() => subscriptionStore.hasActiveSubscriptions)
const summary = computed(() => summarizeActiveSubscriptions(subscriptionStore.activeSubscriptions))

const subscriptionLabel = computed(() =>
  summary.value.count > 1
    ? t('subscriptionProgress.activeCount', { count: summary.value.count })
    : t('nav.balanceCard.subscription')
)

const windowLeftLabel = computed(() => {
  switch (summary.value.window) {
    case 'weekly':
      return t('nav.balanceCard.leftWeekly')
    case 'monthly':
      return t('nav.balanceCard.leftMonthly')
    default:
      return t('nav.balanceCard.leftDaily')
  }
})

// 订阅额度的单价每张卡各不相同：折算和格式化都走 useCurrencyDisplay 的共享函数，这里不做换算。
// 人民币模式下每张卡都有单价才合计成人民币；有卡拿不到单价时整体回落（formatSubscription 不猜单价），不混排。
const subscriptionAmountText = computed(() => {
  const { parts, remainingCredits } = summary.value
  const priced = parts.length > 0 && parts.every((p) => p.fiatPerCredit !== null)
  if (isFiat.value && priced) {
    return formatFiat(parts.reduce((sum, p) => sum + fiatFromCredits(p.credits, p.fiatPerCredit), 0))
  }
  return formatSubscription(remainingCredits, null)
})

const daysToExpiry = computed(() => {
  const at = summary.value.nextExpiresAt
  if (!at) return null
  const diff = new Date(at).getTime() - Date.now()
  return Number.isFinite(diff) ? Math.ceil(diff / (24 * 60 * 60 * 1000)) : null
})

const expiryText = computed(() => {
  const at = summary.value.nextExpiresAt
  if (!at) return ''
  const relation = getExpirationDateRelation(at)
  if (relation === 'expired') return t('subscriptionProgress.expired')
  if (relation === 'today') return t('subscriptionProgress.expiresToday')
  if (relation === 'tomorrow') return t('subscriptionProgress.expiresTomorrow')
  const days = daysToExpiry.value
  return days === null ? '' : t('subscriptionProgress.daysRemaining', { days })
})

// 临近到期才用陶土色提示，其余保持中性墨色（与顶栏订阅进度同一口径）。
const expiryClass = computed(() => {
  const days = daysToExpiry.value
  return days !== null && days <= 3
    ? 'text-primary-700 dark:text-primary-400'
    : 'text-gray-600 dark:text-dark-400'
})

// ─── 图标态 ──────────────────────────────────────────────────
const collapsedTarget = computed(() => (canTopUp.value ? topUpLocation : '/usage'))
const collapsedTitle = computed(() =>
  user.value ? `${t('dashboard.balance')} ${balanceText.value}` : t('dashboard.balance')
)

// 手机抽屉里点了入口就收起抽屉，与侧栏其他入口一致。
function onNavigate() {
  if (appStore.mobileOpen) {
    setTimeout(() => appStore.setMobileOpen(false), 150)
  }
}

onMounted(() => {
  // 带缓存且去重；App.vue 登录后已预载并轮询，这里只是兜底。
  subscriptionStore.fetchActiveSubscriptions().catch((error) => {
    console.error('Failed to load subscriptions for the balance card:', error)
  })
})
</script>

<style scoped>
/* 与侧栏图标态下其他入口同一条竖线：72px 宽、图标居中（AppSidebar 的同名样式是 scoped，插槽里用不到） */
.balance-card-icon-link {
  gap: 0;
  padding-left: 0.875rem;
  padding-right: 0.875rem;
}
</style>
