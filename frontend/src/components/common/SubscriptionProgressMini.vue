<template>
  <div v-if="hasActiveSubscriptions" class="relative" ref="containerRef">
    <!-- Mini Progress Display -->
    <button
      @click="toggleTooltip"
      class="flex cursor-pointer items-center gap-2 rounded-md bg-gray-100 px-3 py-1.5 transition-colors hover:bg-gray-200 dark:bg-dark-800 dark:hover:bg-dark-700"
      :title="t('subscriptionProgress.viewDetails')"
    >
      <Icon name="creditCard" size="sm" class="text-gray-500 dark:text-gray-400" />
      <div class="flex items-center gap-1.5">
        <!-- Combined progress indicator -->
        <div class="flex items-center gap-0.5">
          <div
            v-for="(sub, index) in displaySubscriptions.slice(0, 3)"
            :key="index"
            class="h-2 w-2 rounded-full"
            :class="getProgressDotClass(sub)"
          ></div>
        </div>
        <span class="font-mono text-xs font-medium tabular-nums text-gray-900 dark:text-white">
          {{ activeSubscriptions.length }}
        </span>
      </div>
    </button>

    <!-- Hover/Click Tooltip -->
    <transition name="dropdown">
      <div
        v-if="tooltipOpen"
        class="absolute right-0 z-50 mt-2 w-[340px] overflow-hidden rounded-md border border-gray-200 bg-white shadow-overlay dark:border-dark-700 dark:bg-dark-800"
      >
        <div class="border-b border-gray-100 p-3 dark:border-dark-700">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ t('subscriptionProgress.title') }}
          </h3>
          <p class="mt-0.5 text-xs text-gray-600 dark:text-gray-400">
            {{ t('subscriptionProgress.activeCount', { count: activeSubscriptions.length }) }}
          </p>
        </div>

        <div class="max-h-64 overflow-y-auto">
          <div
            v-for="subscription in displaySubscriptions"
            :key="subscription.id"
            class="border-b border-gray-50 p-3 last:border-b-0 dark:border-dark-700/50"
          >
            <div class="mb-2 flex items-center justify-between">
              <span class="text-sm font-medium text-gray-900 dark:text-white">
                {{ subscription.group?.name || `Group #${subscription.group_id}` }}
              </span>
              <span
                v-if="subscription.expires_at"
                class="font-mono text-xs tabular-nums"
                :class="getDaysRemainingClass(subscription.expires_at)"
              >
                {{ formatDaysRemaining(subscription.expires_at) }}
              </span>
            </div>

            <!-- Progress bars or Unlimited badge -->
            <div class="space-y-1.5">
              <!-- Unlimited subscription badge -->
              <div
                v-if="isUnlimited(subscription)"
                class="flex items-center gap-2 rounded-lg border border-gray-200 bg-gray-50 px-2.5 py-1.5 dark:border-dark-700 dark:bg-dark-800/40"
              >
                <span class="font-mono text-lg text-gray-700 dark:text-gray-300">∞</span>
                <span class="text-xs font-medium text-gray-600 dark:text-gray-400">
                  {{ t('subscriptionProgress.unlimited') }}
                </span>
              </div>

              <!-- Progress bars for limited subscriptions（限额挂卡，逐窗口展示已配置的 日/周/月） -->
              <template v-else>
                <!-- Burn-down 余额进度（新模型） -->
                <div v-if="subscription.daily_amount_usd" class="flex items-center gap-2">
                  <span class="w-8 flex-shrink-0 text-[10px] text-gray-600 dark:text-gray-400">余额</span>
                  <div class="h-1.5 min-w-0 flex-1 rounded-full bg-gray-200 dark:bg-dark-700">
                    <div
                      class="h-1.5 rounded-full bg-gray-900 transition-all dark:bg-gray-100"
                      :style="{ width: burndownRemainingWidth(subscription) }"
                    ></div>
                  </div>
                  <span class="w-24 flex-shrink-0 text-right font-mono text-[10px] tabular-nums text-gray-700 dark:text-gray-300">
                    第{{ burndownCalendarDay(subscription) }}天·剩${{
                      (subscription.remaining_usd || 0).toFixed(0)
                    }}
                  </span>
                </div>

                <div
                  v-for="w in windowsOf(subscription)"
                  :key="w.key"
                  class="flex items-center gap-2"
                >
                  <span class="w-8 flex-shrink-0 text-[10px] text-gray-600 dark:text-gray-400">{{
                    w.label
                  }}</span>
                  <div class="h-1.5 min-w-0 flex-1 rounded-full bg-gray-200 dark:bg-dark-700">
                    <div
                      class="h-1.5 rounded-full transition-all"
                      :class="getProgressBarClass(w.used, w.limit)"
                      :style="{ width: getProgressWidth(w.used, w.limit) }"
                    ></div>
                  </div>
                  <span class="w-24 flex-shrink-0 text-right font-mono text-[10px] tabular-nums text-gray-700 dark:text-gray-300">
                    {{ formatUsage(w.used, w.limit) }}
                  </span>
                </div>
              </template>
            </div>
          </div>
        </div>

        <div class="border-t border-gray-100 p-2 dark:border-dark-700">
          <router-link
            to="/subscriptions"
            @click="closeTooltip"
            class="block w-full py-1 text-center text-xs text-gray-700 hover:underline dark:text-gray-300"
          >
            {{ t('subscriptionProgress.viewAll') }}
          </router-link>
        </div>
      </div>
    </transition>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { useSubscriptionStore } from '@/stores'
import type { UserSubscription } from '@/types'

const { t } = useI18n()

const subscriptionStore = useSubscriptionStore()

const containerRef = ref<HTMLElement | null>(null)
const tooltipOpen = ref(false)

// Use store data instead of local state
const activeSubscriptions = computed(() => subscriptionStore.activeSubscriptions)
const hasActiveSubscriptions = computed(() => subscriptionStore.hasActiveSubscriptions)

const displaySubscriptions = computed(() => {
  // Sort by most usage (highest percentage first)
  return [...activeSubscriptions.value].sort((a, b) => {
    const aMax = getMaxUsagePercentage(a)
    const bMax = getMaxUsagePercentage(b)
    return bMax - aMax
  })
})

// windowsOf 返回该订阅已配置（limit>0）的三窗口（限额挂卡）。limit 为 null/0 = 该窗口不限，不展示进度条。
function windowsOf(sub: UserSubscription) {
  return [
    {
      key: 'daily',
      label: t('subscriptionProgress.daily'),
      used: sub.daily_usage_usd,
      limit: sub.daily_limit_usd ?? null
    },
    {
      key: 'weekly',
      label: t('subscriptionProgress.weekly'),
      used: sub.weekly_usage_usd,
      limit: sub.weekly_limit_usd ?? null
    },
    {
      key: 'monthly',
      label: t('subscriptionProgress.monthly'),
      used: sub.monthly_usage_usd,
      limit: sub.monthly_limit_usd ?? null
    }
  ].filter((w) => w.limit != null && w.limit > 0)
}

function getMaxUsagePercentage(sub: UserSubscription): number {
  const percentages: number[] = []
  for (const w of windowsOf(sub)) {
    percentages.push(((w.used || 0) / (w.limit as number)) * 100)
  }
  return percentages.length > 0 ? Math.max(...percentages) : 0
}

function isUnlimited(sub: UserSubscription): boolean {
  return windowsOf(sub).length === 0
}

function getProgressDotClass(sub: UserSubscription): string {
  // Unlimited subscriptions carry no color — neutral ink dot.
  if (isUnlimited(sub)) {
    return 'bg-gray-900 dark:bg-gray-100'
  }
  const maxPercentage = getMaxUsagePercentage(sub)
  // Clay Signal only when over/near limit; otherwise neutral ink.
  if (maxPercentage >= 90) return 'bg-primary-600'
  return 'bg-gray-900 dark:bg-gray-100'
}

function getProgressBarClass(used: number | undefined, limit: number | null | undefined): string {
  if (!limit || limit === 0) return 'bg-gray-300 dark:bg-dark-600'
  const percentage = ((used || 0) / limit) * 100
  // Clay Signal only when over/near limit; otherwise neutral ink fill.
  if (percentage >= 90) return 'bg-primary-600'
  return 'bg-gray-900 dark:bg-gray-100'
}

function getProgressWidth(used: number | undefined, limit: number | null | undefined): string {
  if (!limit || limit === 0) return '0%'
  const percentage = Math.min(((used || 0) / limit) * 100, 100)
  return `${percentage}%`
}

// burndownRemainingWidth 返回 burn-down 订阅剩余余额占发放总额的百分比宽度。
function burndownRemainingWidth(sub: UserSubscription): string {
  const granted = sub.granted_total_usd || 0
  if (granted <= 0) return '0%'
  const pct = Math.max(0, Math.min((sub.remaining_usd || 0) / granted, 1)) * 100
  return `${pct}%`
}

function burndownCalendarDay(sub: UserSubscription): number {
  const day = sub.calendar_day
  return typeof day === 'number' && Number.isFinite(day) ? Math.max(0, Math.floor(day)) : 0
}

function formatUsage(used: number | undefined, limit: number | null | undefined): string {
  const usedValue = (used || 0).toFixed(2)
  const limitValue = limit?.toFixed(2) || '∞'
  return `$${usedValue}/$${limitValue}`
}

function formatDaysRemaining(expiresAt: string): string {
  const now = new Date()
  const expires = new Date(expiresAt)
  const diff = expires.getTime() - now.getTime()
  if (diff < 0) return t('subscriptionProgress.expired')
  const days = Math.ceil(diff / (1000 * 60 * 60 * 24))
  if (days === 0) return t('subscriptionProgress.expiresToday')
  if (days === 1) return t('subscriptionProgress.expiresTomorrow')
  return t('subscriptionProgress.daysRemaining', { days })
}

function getDaysRemainingClass(expiresAt: string): string {
  const now = new Date()
  const expires = new Date(expiresAt)
  const diff = expires.getTime() - now.getTime()
  const days = Math.ceil(diff / (1000 * 60 * 60 * 24))
  // Clay Signal only at true near-expiry; otherwise neutral ink figure.
  if (days <= 3) return 'text-primary-700 dark:text-primary-400'
  return 'text-gray-700 dark:text-gray-300'
}

function toggleTooltip() {
  tooltipOpen.value = !tooltipOpen.value
}

function closeTooltip() {
  tooltipOpen.value = false
}

function handleClickOutside(event: MouseEvent) {
  if (containerRef.value && !containerRef.value.contains(event.target as Node)) {
    closeTooltip()
  }
}

onMounted(() => {
  document.addEventListener('click', handleClickOutside)
  // Trigger initial fetch if not already loaded
  // The actual data loading is handled by App.vue globally
  subscriptionStore.fetchActiveSubscriptions().catch((error) => {
    console.error('Failed to load subscriptions in SubscriptionProgressMini:', error)
  })
})

onBeforeUnmount(() => {
  document.removeEventListener('click', handleClickOutside)
})
</script>

<style scoped>
.dropdown-enter-active,
.dropdown-leave-active {
  transition: all 0.2s ease;
}

.dropdown-enter-from,
.dropdown-leave-to {
  opacity: 0;
  transform: scale(0.95) translateY(-4px);
}
</style>
