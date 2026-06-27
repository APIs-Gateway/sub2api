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
          <!-- 手动透支「借一天」：仅在配了日额度时出现；条件不满足时置灰并以 title 说明原因。
               中性次按钮（透支非错误，clay/primary 保留给 Signal）；代价提示交确认框。 -->
          <button
            v-if="showOverdraftButton"
            type="button"
            :disabled="!canOverdraft"
            :title="canOverdraft ? '' : overdraftDisabledReason"
            class="btn btn-secondary btn-sm"
            @click="openOverdraftConfirm"
          >
            {{ t('userSubscriptions.overdraftBtn.label') }}
          </button>
          <button type="button" class="btn btn-primary btn-sm" @click="openLifecycle('renew')">
            {{ t('payment.renewNow') }}
          </button>
          <button type="button" class="btn btn-secondary btn-sm" @click="openLifecycle('change')">
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

      <!-- 三窗口用量 vs 限额（限额挂卡，不挂 group）：只展示已配置（limit>0）的窗口，与迷你进度口径一致。 -->
      <template v-if="hasAnyLimit">
        <div v-for="w in configuredWindows" :key="w.key" class="space-y-2">
          <div class="flex items-center justify-between">
            <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ w.label }}</span>
            <span class="font-mono tabular-nums text-sm text-gray-900 dark:text-white">
              ${{ (w.used || 0).toFixed(2) }} / ${{ (w.limit ?? 0).toFixed(2) }}
            </span>
          </div>
          <div class="relative h-2 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-600">
            <div
              class="absolute inset-y-0 left-0 rounded-full transition-all duration-300"
              :class="getProgressBarClass(w.used, w.limit)"
              :style="{ width: getProgressWidth(w.used, w.limit) }"
            ></div>
          </div>
          <p v-if="w.resetsAt" class="text-xs text-gray-600 dark:text-gray-400">
            {{ t('userSubscriptions.resetIn', { time: formatResetTime(w.resetsAt) }) }}
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

    <ConfirmDialog
      :show="showOverdraftConfirm"
      :title="t('userSubscriptions.overdraftBtn.confirmTitle')"
      :message="t('userSubscriptions.overdraftBtn.confirmMessage')"
      :confirm-text="t('userSubscriptions.overdraftBtn.confirmOk')"
      danger
      @confirm="confirmOverdraft"
      @cancel="showOverdraftConfirm = false"
    >
      <p
        v-if="overdraftRemaining != null"
        class="text-xs font-medium text-gray-500 dark:text-gray-400"
      >
        {{ t('userSubscriptions.overdraftBtn.remaining', { n: overdraftRemaining }) }}
      </p>
    </ConfirmDialog>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import SubscriptionLifecycleDialog from '@/components/subscription/SubscriptionLifecycleDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import subscriptionsAPI from '@/api/subscriptions'
import { useAppStore } from '@/stores'
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
const appStore = useAppStore()

// 东八区常量：窗口/有效期边界一律按东八区自然日算（东八区无 DST，固定 +08:00）。
const SH_TZ = 'Asia/Shanghai'
const SH_OFFSET_MS = 8 * 60 * 60 * 1000

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

// 三窗口用量 vs 限额（限额挂卡；limit 为 null/0 = 该窗口不限）。
// resetsAt = 下一个东八区自然窗口边界（日/周用固定时长精确，月按自然月，避免 720h 近似在 2 月 / 31 天月份算错）。
const windows = computed(() => {
  const s = props.subscription
  const build = (
    key: 'daily' | 'weekly' | 'monthly',
    label: string,
    used: number | undefined,
    limit: number | null | undefined,
    windowStart: string | null | undefined
  ) => ({
    key,
    label,
    used,
    limit: limit ?? null,
    resetsAt: windowStart ? nextWindowReset(windowStart, key) : null
  })
  return [
    build(
      'daily',
      t('userSubscriptions.daily'),
      s.daily_usage_usd,
      s.daily_limit_usd,
      s.daily_window_start
    ),
    build(
      'weekly',
      t('userSubscriptions.weekly'),
      s.weekly_usage_usd,
      s.weekly_limit_usd,
      s.weekly_window_start
    ),
    build(
      'monthly',
      t('userSubscriptions.monthly'),
      s.monthly_usage_usd,
      s.monthly_limit_usd,
      s.monthly_window_start
    )
  ]
})
// 与迷你进度统一口径：两处都只展示已配置（limit>0）的窗口，三窗口全空 → ∞ 不限额徽标。
const configuredWindows = computed(() =>
  windows.value.filter((w) => w.limit != null && w.limit > 0)
)
const hasAnyLimit = computed(() => configuredWindows.value.length > 0)

// ─── 手动透支「借一天」（三窗口模型）─────────────────────────────────────────
const showOverdraftConfirm = ref(false)
const overdrafting = ref(false)
const overdraftIdemKey = ref('')

// 当日额度已撞满（先看后端惰性重置后的 daily_usage；无日限额则谈不上撞满）。
const dailyMaxed = computed(() => {
  const s = props.subscription
  const limit = s.daily_limit_usd
  return limit != null && limit > 0 && (s.daily_usage_usd || 0) >= limit
})
// 仍有可借的未来天：expires_at 晚于「今天结束」（东八区明日 00:00）。
const hasFutureDay = computed(() => {
  const exp = props.subscription.expires_at
  return exp != null && new Date(exp).getTime() > endOfTodaySHms()
})
// 用户级本月透支剩余次数（后端 #7/#8 提供）；无值 = 不前置拦截，交服务端兜底。
const overdraftRemaining = computed(() => props.subscription.monthly_overdraft_remaining ?? null)
// 仅在「生效卡 + 配了日额度」时出现按钮（无日限额永远撞不满，透支无意义）。
const showOverdraftButton = computed(() => {
  const s = props.subscription
  return s.status === 'active' && s.daily_limit_usd != null && s.daily_limit_usd > 0
})
const canOverdraft = computed(
  () =>
    showOverdraftButton.value &&
    dailyMaxed.value &&
    hasFutureDay.value &&
    (overdraftRemaining.value == null || overdraftRemaining.value > 0)
)
const overdraftDisabledReason = computed(() => {
  if (!dailyMaxed.value) return t('userSubscriptions.overdraftBtn.disabledNotMaxed')
  if (!hasFutureDay.value) return t('userSubscriptions.overdraftBtn.disabledNoFutureDay')
  if (overdraftRemaining.value != null && overdraftRemaining.value <= 0)
    return t('userSubscriptions.overdraftBtn.disabledExhausted')
  return ''
})

function openOverdraftConfirm() {
  // 每次开确认框生成一把幂等键：确认按钮连点 / 重放都用同键，服务端去重防重复借天。
  overdraftIdemKey.value =
    globalThis.crypto?.randomUUID?.() ?? `od-${Date.now()}-${Math.round(Math.random() * 1e9)}`
  showOverdraftConfirm.value = true
}

async function confirmOverdraft() {
  if (overdrafting.value) return
  overdrafting.value = true
  try {
    await subscriptionsAPI.borrowOverdraftDay(overdraftIdemKey.value)
    appStore.showSuccess(t('userSubscriptions.overdraftBtn.success'))
    showOverdraftConfirm.value = false
    emit('saved')
  } catch (e) {
    appStore.showError(overdraftErrorMessage(e))
  } finally {
    overdrafting.value = false
  }
}

// 把后端错误码映射为本地化文案；未知码回退到服务端 message,再回退到通用文案。
function overdraftErrorMessage(e: unknown): string {
  const err = e as {
    response?: {
      data?: { error?: { code?: string; message?: string }; code?: string; message?: string }
    }
  }
  const data = err?.response?.data
  const code = data?.error?.code ?? data?.code
  const map: Record<string, string> = {
    OVERDRAFT_NO_ACTIVE_CARD: t('userSubscriptions.overdraftBtn.errors.noActiveCard'),
    OVERDRAFT_DAILY_NOT_EXHAUSTED: t('userSubscriptions.overdraftBtn.errors.dailyNotExhausted'),
    OVERDRAFT_MONTHLY_LIMIT: t('userSubscriptions.overdraftBtn.errors.monthlyLimit'),
    OVERDRAFT_NO_FUTURE_DAY: t('userSubscriptions.overdraftBtn.errors.noFutureDay')
  }
  if (code && map[code]) return map[code]
  return data?.error?.message ?? data?.message ?? t('userSubscriptions.overdraftBtn.errors.generic')
}

// nextWindowReset 返回下一个东八区自然窗口重置时刻。
// 日/周 = window_start + 固定时长（东八区无 DST，精确）；月 = 下个自然月 1 日 00:00(+08:00)。
function nextWindowReset(windowStart: string, kind: 'daily' | 'weekly' | 'monthly'): Date {
  const start = new Date(windowStart)
  if (kind === 'monthly') {
    const parts = new Intl.DateTimeFormat('en-US', {
      timeZone: SH_TZ,
      year: 'numeric',
      month: 'numeric'
    }).formatToParts(start)
    const year = Number(parts.find((p) => p.type === 'year')?.value)
    const month = Number(parts.find((p) => p.type === 'month')?.value) // 1-12（东八区当前月）
    // Date.UTC 月份 0-indexed：传入 1-indexed 当前月 = 下个月 1 日（自动跨年）；东八区 00:00 = UTC −8h。
    return new Date(Date.UTC(year, month, 1, 0, 0, 0) - SH_OFFSET_MS)
  }
  const hours = kind === 'weekly' ? 168 : 24
  return new Date(start.getTime() + hours * 60 * 60 * 1000)
}

// endOfTodaySHms 返回「今天结束」= 东八区明日 00:00 的时间戳（ms）。
function endOfTodaySHms(): number {
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone: SH_TZ,
    year: 'numeric',
    month: 'numeric',
    day: 'numeric'
  }).formatToParts(new Date())
  const year = Number(parts.find((p) => p.type === 'year')?.value)
  const month = Number(parts.find((p) => p.type === 'month')?.value) // 1-12
  const day = Number(parts.find((p) => p.type === 'day')?.value)
  return Date.UTC(year, month - 1, day + 1, 0, 0, 0) - SH_OFFSET_MS
}

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

function formatResetTime(resetsAt: Date | null): string {
  if (!resetsAt) return t('userSubscriptions.windowNotActive')
  const parts = getRemainingDurationParts(resetsAt)
  return parts ? formatDurationParts(parts) : t('userSubscriptions.windowNotActive')
}
</script>
