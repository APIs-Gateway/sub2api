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
        <button
          v-if="subscription.status === 'active'"
          class="rounded-md bg-gray-900 px-3 py-1.5 text-xs font-semibold text-white transition-colors hover:bg-gray-800 dark:bg-gray-100 dark:text-gray-900 dark:hover:bg-white"
          @click="
            router.push({
              path: '/purchase',
              query: { tab: 'subscription', group: String(subscription.group_id) }
            })
          "
        >
          {{ t('payment.renewNow') }}
        </button>
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

      <!-- Burn-down 订阅进度（新模型）：服务天数与额度消费进度分开展示 -->
      <div v-if="subscription.daily_amount_usd" class="space-y-2">
        <div class="flex items-center justify-between">
          <span class="text-sm font-medium text-gray-700 dark:text-gray-300">订阅进度</span>
          <span class="text-sm text-gray-600 dark:text-gray-400">
            已服务第
            <span class="font-mono tabular-nums text-gray-900 dark:text-white">{{
              burndownCalendarDay(subscription)
            }}</span>
            /
            <span class="font-mono tabular-nums text-gray-900 dark:text-white">{{
              burndownTotalDays(subscription)
            }}</span>
            天
          </span>
        </div>
        <div class="relative h-2 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-600">
          <div
            class="absolute inset-y-0 left-0 rounded-full bg-gray-900 transition-all duration-300 dark:bg-gray-100"
            :style="{
              width: getProgressWidth(subscription.consumed_usd, subscription.granted_total_usd)
            }"
          ></div>
        </div>
        <div
          class="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 text-xs text-gray-600 dark:text-gray-400"
        >
          <span>
            已消费
            <span class="font-mono tabular-nums text-gray-900 dark:text-white">{{
              burndownConsumptionDay(subscription)
            }}</span>
            天额度
          </span>
          <span
            >剩余订阅余额
            <span class="font-mono tabular-nums text-gray-900 dark:text-white"
              >${{ (subscription.remaining_usd || 0).toFixed(2) }}</span
            >
            /
            <span class="font-mono tabular-nums text-gray-900 dark:text-white"
              >${{ (subscription.granted_total_usd || 0).toFixed(2) }}</span
            ></span
          >
          <span v-if="(subscription.clawed_usd || 0) > 0"
            >已清扣
            <span class="font-mono tabular-nums text-gray-900 dark:text-white"
              >${{ (subscription.clawed_usd || 0).toFixed(2) }}</span
            ></span
          >
        </div>
        <p class="text-xs text-gray-600 dark:text-gray-400">
          每日额度
          <span class="font-mono tabular-nums text-gray-900 dark:text-white"
            >${{ (subscription.daily_amount_usd || 0).toFixed(2) }}</span
          >，可提前透支后续天额度；当天未用完部分次日 0 点（东八区）清扣作废
        </p>

        <!-- 自助：本卡最多往后透支天数（仅生效中可改） -->
        <div v-if="subscription.status === 'active'" class="space-y-2 pt-1">
          <div class="flex flex-wrap items-center gap-2">
            <span
              class="text-xs font-medium text-gray-700 dark:text-gray-300 whitespace-nowrap"
            >
              {{ t('userSubscriptions.overdraft.label') }}
            </span>
            <input
              v-model="overdraftEdit"
              type="number"
              min="0"
              :max="overdraftMax(subscription)"
              step="1"
              class="input w-24"
              :disabled="isOverdraftExhausted(subscription)"
              :placeholder="t('userSubscriptions.overdraft.placeholder')"
            />
            <button
              type="button"
              class="btn btn-secondary"
              :disabled="saving || isOverdraftExhausted(subscription)"
              @click="saveOverdraft"
            >
              {{ t('common.save') }}
            </button>
            <span class="text-xs text-gray-600 dark:text-gray-400">
              {{
                t('userSubscriptions.overdraft.usage', {
                  used: overdraftUsed(subscription),
                  max: overdraftMax(subscription),
                  remaining: overdraftRemaining(subscription)
                })
              }}
            </span>
          </div>
          <p class="text-xs text-gray-600 dark:text-gray-400">
            {{
              isOverdraftExhausted(subscription)
                ? t('userSubscriptions.overdraft.exhausted', { max: overdraftMax(subscription) })
                : t('userSubscriptions.overdraft.hint', { max: overdraftMax(subscription) })
            }}
          </p>
        </div>
      </div>

      <!-- Daily Usage -->
      <div
        v-if="subscription.group?.daily_limit_usd && !subscription.daily_amount_usd"
        class="space-y-2"
      >
        <div class="flex items-center justify-between">
          <span class="text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t('userSubscriptions.daily') }}
          </span>
          <span class="font-mono tabular-nums text-sm text-gray-900 dark:text-white">
            ${{ (subscription.daily_usage_usd || 0).toFixed(2) }} / ${{
              subscription.group.daily_limit_usd.toFixed(2)
            }}
          </span>
        </div>
        <div class="relative h-2 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-600">
          <div
            class="absolute inset-y-0 left-0 rounded-full transition-all duration-300"
            :class="
              getProgressBarClass(subscription.daily_usage_usd, subscription.group.daily_limit_usd)
            "
            :style="{
              width: getProgressWidth(
                subscription.daily_usage_usd,
                subscription.group.daily_limit_usd
              )
            }"
          ></div>
        </div>
        <p v-if="subscription.daily_window_start" class="text-xs text-gray-600 dark:text-gray-400">
          {{ formatDailyUsageWindow(subscription) }}
        </p>
      </div>

      <!-- Weekly Usage -->
      <div
        v-if="subscription.group?.weekly_limit_usd && !subscription.daily_amount_usd"
        class="space-y-2"
      >
        <div class="flex items-center justify-between">
          <span class="text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t('userSubscriptions.weekly') }}
          </span>
          <span class="font-mono tabular-nums text-sm text-gray-900 dark:text-white">
            ${{ (subscription.weekly_usage_usd || 0).toFixed(2) }} / ${{
              subscription.group.weekly_limit_usd.toFixed(2)
            }}
          </span>
        </div>
        <div class="relative h-2 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-600">
          <div
            class="absolute inset-y-0 left-0 rounded-full transition-all duration-300"
            :class="
              getProgressBarClass(
                subscription.weekly_usage_usd,
                subscription.group.weekly_limit_usd
              )
            "
            :style="{
              width: getProgressWidth(
                subscription.weekly_usage_usd,
                subscription.group.weekly_limit_usd
              )
            }"
          ></div>
        </div>
        <p v-if="subscription.weekly_window_start" class="text-xs text-gray-600 dark:text-gray-400">
          {{
            t('userSubscriptions.resetIn', {
              time: formatResetTime(subscription.weekly_window_start, 168)
            })
          }}
        </p>
      </div>

      <!-- Monthly Usage -->
      <div
        v-if="subscription.group?.monthly_limit_usd && !subscription.daily_amount_usd"
        class="space-y-2"
      >
        <div class="flex items-center justify-between">
          <span class="text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t('userSubscriptions.monthly') }}
          </span>
          <span class="font-mono tabular-nums text-sm text-gray-900 dark:text-white">
            ${{ (subscription.monthly_usage_usd || 0).toFixed(2) }} / ${{
              subscription.group.monthly_limit_usd.toFixed(2)
            }}
          </span>
        </div>
        <div class="relative h-2 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-600">
          <div
            class="absolute inset-y-0 left-0 rounded-full transition-all duration-300"
            :class="
              getProgressBarClass(
                subscription.monthly_usage_usd,
                subscription.group.monthly_limit_usd
              )
            "
            :style="{
              width: getProgressWidth(
                subscription.monthly_usage_usd,
                subscription.group.monthly_limit_usd
              )
            }"
          ></div>
        </div>
        <p
          v-if="subscription.monthly_window_start"
          class="text-xs text-gray-600 dark:text-gray-400"
        >
          {{
            t('userSubscriptions.resetIn', {
              time: formatResetTime(subscription.monthly_window_start, 720)
            })
          }}
        </p>
      </div>

      <!-- No limits configured - Unlimited badge -->
      <div
        v-if="
          !subscription.daily_amount_usd &&
          !subscription.group?.daily_limit_usd &&
          !subscription.group?.weekly_limit_usd &&
          !subscription.group?.monthly_limit_usd
        "
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
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useAppStore } from '@/stores/app'
import subscriptionsAPI from '@/api/subscriptions'
import type { UserSubscription } from '@/types'
import { formatDateOnly } from '@/utils/format'
import { platformBorderClass, platformBadgeClass, platformLabel } from '@/utils/platformColors'
import {
  getRemainingDurationParts,
  isOneTimeDailyQuota,
  type RemainingDurationParts
} from '@/utils/subscriptionQuota'

const props = defineProps<{
  subscription: UserSubscription
}>()

const emit = defineEmits<{
  (e: 'saved'): void
}>()

const { t } = useI18n()
const router = useRouter()
const appStore = useAppStore()

// 本卡「最多透支天数」的可编辑值（空串 = 关闭透支）。
const overdraftEdit = ref<number | string>('')
const saving = ref(false)

// 跟随传入卡片初始化编辑值：用满透支或未开启时显示为空（关闭）。
watch(
  () => props.subscription,
  (sub) => {
    overdraftEdit.value =
      isOverdraftExhausted(sub) || sub.max_overdraft_days == null ? '' : sub.max_overdraft_days
  },
  { immediate: true }
)

function platformAccentDotClass(_p: string): string {
  return 'bg-gray-400 dark:bg-dark-500'
}

function getProgressWidth(used: number | undefined, limit: number | null | undefined): string {
  if (!limit || limit === 0) return '0%'
  const percentage = Math.min(((used || 0) / limit) * 100, 100)
  return `${percentage}%`
}

function overdraftMax(sub: UserSubscription): number {
  return sub.max_overdraft_uses ?? 5
}

function overdraftUsed(sub: UserSubscription): number {
  return Math.max(0, sub.total_overdraft_count ?? 0)
}

function overdraftRemaining(sub: UserSubscription): number {
  const fromAPI = sub.remaining_overdraft_uses
  if (typeof fromAPI === 'number') return Math.max(0, fromAPI)
  return Math.max(0, overdraftMax(sub) - overdraftUsed(sub))
}

function isOverdraftExhausted(sub: UserSubscription): boolean {
  return sub.can_enable_overdraft === false || overdraftRemaining(sub) <= 0
}

// saveOverdraft 用户自助保存本卡「最多透支天数」（空/0 = 关闭透支，1..5 = 开启）。
async function saveOverdraft() {
  const sub = props.subscription
  const raw = overdraftEdit.value
  let days: number | null
  if (raw === '' || raw === null || raw === undefined) {
    days = null
  } else {
    const n = Number(raw)
    if (Number.isNaN(n) || !Number.isInteger(n) || n < 0 || n > overdraftMax(sub)) {
      appStore.showError(t('userSubscriptions.overdraft.invalid', { max: overdraftMax(sub) }))
      return
    }
    days = n === 0 ? null : n
  }
  if (days !== null && isOverdraftExhausted(sub)) {
    appStore.showError(t('userSubscriptions.overdraft.exhausted'))
    return
  }
  try {
    saving.value = true
    await subscriptionsAPI.setOverdraftDays(sub.id, days)
    appStore.showSuccess(t('userSubscriptions.overdraft.saved'))
    emit('saved')
  } catch (e: any) {
    appStore.showError(e.response?.data?.message || t('userSubscriptions.overdraft.failed'))
  } finally {
    saving.value = false
  }
}

// burndownTotalDays 返回 burn-down 订阅的总天数 = 发放总额 / 每日额度。
function burndownTotalDays(sub: UserSubscription): number {
  if (!sub.daily_amount_usd || sub.daily_amount_usd <= 0) return 0
  return Math.round((sub.granted_total_usd || 0) / sub.daily_amount_usd)
}

function burndownCalendarDay(sub: UserSubscription): number {
  if (typeof sub.calendar_day === 'number' && Number.isFinite(sub.calendar_day)) {
    return Math.max(0, Math.floor(sub.calendar_day))
  }
  return 0
}

function burndownConsumptionDay(sub: UserSubscription): number {
  if (typeof sub.consumption_day === 'number' && Number.isFinite(sub.consumption_day)) {
    return Math.max(0, Math.floor(sub.consumption_day))
  }
  return 0
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

function formatDailyUsageWindow(subscription: UserSubscription): string {
  if (isOneTimeDailyQuota(subscription) && subscription.expires_at) {
    const parts = getRemainingDurationParts(subscription.expires_at)
    if (!parts) return t('userSubscriptions.windowNotActive')
    return t('userSubscriptions.quotaEndsIn', { time: formatDurationParts(parts) })
  }

  return t('userSubscriptions.resetIn', {
    time: formatResetTime(subscription.daily_window_start, 24)
  })
}

function formatResetTime(windowStart: string | null, windowHours: number): string {
  if (!windowStart) return t('userSubscriptions.windowNotActive')

  const start = new Date(windowStart)
  const end = new Date(start.getTime() + windowHours * 60 * 60 * 1000)
  const parts = getRemainingDurationParts(end)

  return parts ? formatDurationParts(parts) : t('userSubscriptions.windowNotActive')
}
</script>
