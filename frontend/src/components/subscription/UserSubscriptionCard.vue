<template>
  <div
    class="overflow-hidden rounded-md border bg-white dark:bg-dark-800"
    :class="platformBorderClass(subscription.group?.platform || '')"
  >
    <!-- Header -->
    <div class="flex items-center justify-between border-b border-gray-100 p-4 dark:border-dark-700">
      <div class="flex items-center gap-3">
        <div
          :class="[
            'h-1.5 w-1.5 shrink-0 rounded-full',
            platformAccentDotClass(subscription.group?.platform || '')
          ]"
        />
        <div>
          <div class="flex items-center gap-2">
            <h3 class="font-semibold text-gray-900 dark:text-white">
              {{ subscription.group?.name || `Group #${subscription.group_id}` }}
            </h3>
            <span
              :class="[
                'rounded-md border px-2 py-0.5 text-[11px] font-medium',
                platformBadgeClass(subscription.group?.platform || '')
              ]"
            >
              {{ platformLabel(subscription.group?.platform || '') }}
            </span>
          </div>
          <p
            v-if="subscription.group?.description"
            class="mt-0.5 text-xs text-gray-600 dark:text-gray-400"
          >
            {{ subscription.group.description }}
          </p>
        </div>
      </div>
      <div class="flex items-center gap-2">
        <span
          :class="[
            'rounded-md border px-2 py-0.5 text-xs font-medium',
            subscription.status === 'active'
              ? 'border-gray-200 text-gray-700 dark:border-dark-700 dark:text-gray-300'
              : subscription.status === 'expired'
                ? 'border-gray-200 text-gray-600 dark:border-dark-700 dark:text-gray-400'
                : 'border-primary-200 text-primary-700 dark:border-primary-900/50 dark:text-primary-300'
          ]"
        >
          {{ t(`userSubscriptions.status.${subscription.status}`) }}
        </span>
        <div v-if="subscription.status === 'active'" class="flex items-center gap-2">
          <button
            type="button"
            class="rounded-md bg-gray-900 px-3 py-1.5 text-xs font-semibold text-white transition-colors hover:bg-gray-800 dark:bg-gray-100 dark:text-gray-900 dark:hover:bg-white"
            @click="openLifecycle('renew')"
          >
            {{ t('payment.renewNow') }}
          </button>
          <button
            type="button"
            class="rounded-md border border-gray-300 px-3 py-1.5 text-xs font-semibold text-gray-700 transition-colors hover:bg-gray-50 dark:border-dark-600 dark:text-gray-200 dark:hover:bg-dark-700"
            @click="openLifecycle('change')"
          >
            {{ t('userSubscriptions.lifecycle.changeTitle') }}
          </button>
        </div>
      </div>
    </div>

    <!-- Usage Progress -->
    <div class="space-y-4 p-4">
      <!-- Expiration Info -->
      <div v-if="subscription.expires_at" class="flex items-center justify-between text-sm">
        <span class="text-gray-600 dark:text-gray-400">{{ t('userSubscriptions.expires') }}</span>
        <span :class="getExpirationClass(subscription.expires_at)">
          {{ formatExpirationDate(subscription.expires_at) }}
        </span>
      </div>
      <div v-else class="flex items-center justify-between text-sm">
        <span class="text-gray-600 dark:text-gray-400">{{ t('userSubscriptions.expires') }}</span>
        <span class="text-gray-700 dark:text-gray-300">{{
          t('userSubscriptions.noExpiration')
        }}</span>
      </div>

      <!-- 三窗口用量 vs 限额（限额挂卡，不挂 group）：每窗口 usage / limit + 自然边界重置时间。 -->
      <template v-if="hasAnyLimit">
        <div v-for="w in windows" :key="w.key" class="space-y-2">
          <div class="flex items-center justify-between">
            <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ w.label }}</span>
            <span class="font-mono tabular-nums text-sm text-gray-900 dark:text-white">
              <template v-if="w.limit != null && w.limit > 0">
                ${{ (w.used || 0).toFixed(2) }} / ${{ w.limit.toFixed(2) }}
              </template>
              <template v-else>
                ${{ (w.used || 0).toFixed(2) }} / {{ t('userSubscriptions.unlimited') }}
              </template>
            </span>
          </div>
          <div
            v-if="w.limit != null && w.limit > 0"
            class="relative h-2 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-600"
          >
            <div
              class="absolute inset-y-0 left-0 rounded-full transition-all duration-300"
              :class="getProgressBarClass(w.used, w.limit)"
              :style="{ width: getProgressWidth(w.used, w.limit) }"
            ></div>
          </div>
          <p v-if="w.windowStart" class="text-xs text-gray-600 dark:text-gray-400">
            {{ t('userSubscriptions.resetIn', { time: formatResetTime(w.windowStart, w.hours) }) }}
          </p>
        </div>
      </template>

      <!-- 完全不限：卡上三窗口限额全空（未配置 / 不限额订阅）。 -->
      <div
        v-else
        class="flex items-center justify-center rounded-md border border-gray-200 bg-gray-50 py-6 dark:border-dark-700 dark:bg-dark-800/40"
      >
        <div class="flex items-center gap-3">
          <span class="font-mono text-4xl text-gray-900 dark:text-white">∞</span>
          <div>
            <p class="text-sm font-medium text-gray-900 dark:text-white">
              {{ t('userSubscriptions.unlimited') }}
            </p>
            <p class="text-xs text-gray-600 dark:text-gray-400">
              {{ t('userSubscriptions.unlimitedDesc') }}
            </p>
          </div>
        </div>
      </div>
    </div>

    <SubscriptionLifecycleDialog
      v-if="lifecycleMode"
      :show="showLifecycle"
      :mode="lifecycleMode"
      :subscription="subscription"
      @close="showLifecycle = false"
      @done="onLifecycleDone"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import SubscriptionLifecycleDialog from '@/components/subscription/SubscriptionLifecycleDialog.vue'
import type { UserSubscription } from '@/types'
import { formatDateOnly } from '@/utils/format'
import { platformBorderClass, platformBadgeClass, platformLabel } from '@/utils/platformColors'
import { getRemainingDurationParts, type RemainingDurationParts } from '@/utils/subscriptionQuota'

const props = defineProps<{
  subscription: UserSubscription
}>()

const emit = defineEmits<{
  (e: 'saved'): void
}>()

const { t } = useI18n()

// 续费 / 转套餐 生命周期对话框。
const showLifecycle = ref(false)
const lifecycleMode = ref<'renew' | 'change' | null>(null)
function openLifecycle(mode: 'renew' | 'change') {
  lifecycleMode.value = mode
  showLifecycle.value = true
}
function onLifecycleDone() {
  showLifecycle.value = false
  emit('saved')
}

// 三窗口用量 vs 限额（限额挂卡；limit 为 null/0 = 该窗口不限）。窗口重置时间从 window_start 起算：
// 日 24h / 周 168h / 月按近似 720h（自然边界由后端 timezone.StartOf* 决定，前端仅展示倒计时）。
const windows = computed(() => {
  const s = props.subscription
  return [
    {
      key: 'daily',
      label: t('userSubscriptions.daily'),
      used: s.daily_usage_usd,
      limit: s.daily_limit_usd ?? null,
      windowStart: s.daily_window_start,
      hours: 24
    },
    {
      key: 'weekly',
      label: t('userSubscriptions.weekly'),
      used: s.weekly_usage_usd,
      limit: s.weekly_limit_usd ?? null,
      windowStart: s.weekly_window_start,
      hours: 168
    },
    {
      key: 'monthly',
      label: t('userSubscriptions.monthly'),
      used: s.monthly_usage_usd,
      limit: s.monthly_limit_usd ?? null,
      windowStart: s.monthly_window_start,
      hours: 720
    }
  ]
})
const hasAnyLimit = computed(() => windows.value.some((w) => w.limit != null && w.limit > 0))

function platformAccentDotClass(_p: string): string {
  return 'bg-gray-400 dark:bg-dark-500'
}

function getProgressWidth(used: number | undefined, limit: number | null | undefined): string {
  if (!limit || limit === 0) return '0%'
  const percentage = Math.min(((used || 0) / limit) * 100, 100)
  return `${percentage}%`
}

function getProgressBarClass(used: number | undefined, limit: number | null | undefined): string {
  if (!limit || limit === 0) return 'bg-gray-300 dark:bg-dark-500'
  const percentage = ((used || 0) / limit) * 100
  if (percentage >= 90) return 'bg-primary-600'
  return 'bg-gray-900 dark:bg-gray-100'
}

function formatExpirationDate(expiresAt: string): string {
  const now = new Date()
  const expires = new Date(expiresAt)
  const diff = expires.getTime() - now.getTime()
  const days = Math.ceil(diff / (1000 * 60 * 60 * 24))

  if (days < 0) {
    return t('userSubscriptions.status.expired')
  }

  const dateStr = formatDateOnly(expires)

  if (days === 0) {
    return `${dateStr} (${t('common.today')})`
  }
  if (days === 1) {
    return `${dateStr} (${t('common.tomorrow')})`
  }

  return t('userSubscriptions.daysRemaining', { days }) + ` (${dateStr})`
}

function getExpirationClass(expiresAt: string): string {
  const now = new Date()
  const expires = new Date(expiresAt)
  const diff = expires.getTime() - now.getTime()
  const days = Math.ceil(diff / (1000 * 60 * 60 * 24))

  if (days <= 0) return 'font-mono tabular-nums text-primary-700 dark:text-primary-300 font-medium'
  if (days <= 3) return 'font-mono tabular-nums text-primary-700 dark:text-primary-300'
  return 'font-mono tabular-nums text-gray-700 dark:text-gray-300'
}

function formatDurationParts(parts: RemainingDurationParts): string {
  if (parts.days > 0) {
    return `${parts.days}d ${parts.hours}h`
  }

  if (parts.hours > 0) {
    return `${parts.hours}h ${parts.minutes}m`
  }

  return `${parts.minutes}m`
}

function formatResetTime(windowStart: string | null, windowHours: number): string {
  if (!windowStart) return t('userSubscriptions.windowNotActive')

  const start = new Date(windowStart)
  const end = new Date(start.getTime() + windowHours * 60 * 60 * 1000)
  const parts = getRemainingDurationParts(end)

  return parts ? formatDurationParts(parts) : t('userSubscriptions.windowNotActive')
}
</script>
