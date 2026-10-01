<template>
  <!-- 核心指标：账本式指标条——数字坐在纸上、发丝线分格、无卡盒、无角标。
       所有格子的主数字同字号同字重（.num-primary）：余额不再单独放大，靠排在第一格体现位置。 -->
  <div class="grid grid-cols-2 gap-px overflow-hidden rounded-md border border-gray-200 bg-gray-200 dark:border-dark-700 dark:bg-dark-700 lg:grid-cols-4">
    <!-- Balance -->
    <div v-if="!isSimple" class="metric-cell bg-gray-50 dark:bg-dark-950">
      <span class="metric-label">{{ t('dashboard.balance') }}</span>
      <NumText tier="primary" :text="formatWallet(balance)" data-test="balance-value" />
      <span class="num-aux">{{ t('common.available') }}</span>
    </div>

    <!-- API Keys -->
    <div class="metric-cell bg-gray-50 dark:bg-dark-950">
      <span class="metric-label">{{ t('dashboard.apiKeys') }}</span>
      <NumText tier="primary" :text="formatNumber(stats?.total_api_keys || 0)" />
      <span class="num-aux">{{ formatNumber(stats?.active_api_keys || 0) }} {{ t('common.active') }}</span>
    </div>

    <!-- Today Requests -->
    <div class="metric-cell bg-gray-50 dark:bg-dark-950">
      <span class="metric-label">{{ t('dashboard.todayRequests') }}</span>
      <NumText tier="primary" :text="formatNumber(stats?.today_requests || 0)" />
      <span class="num-aux">{{ t('common.total') }}: {{ formatNumber(stats?.total_requests || 0) }}</span>
    </div>

    <!-- Today Cost：悬停给出精确值（界面上按统一规则收口，不丢信息） -->
    <div class="metric-cell bg-gray-50 dark:bg-dark-950">
      <span class="metric-label">{{ t('dashboard.todayCost') }}</span>
      <span class="flex items-baseline gap-2">
        <NumText
          tier="primary"
          :text="formatMixed(stats?.today_actual_cost || 0, stats?.today_actual_cost_fiat)"
          :title="`${t('dashboard.actual')}: ${formatMixed(stats?.today_actual_cost || 0, stats?.today_actual_cost_fiat, EXACT_DIGITS)}`"
        />
        <!-- 官方价是美元口径的对照值，人民币模式下与实付并列只会让人误读，只在美元模式展示 -->
        <NumText
          v-if="!isFiat"
          tier="secondary"
          class="font-normal text-gray-400 dark:text-gray-500"
          :text="`/ ${formatUsd(stats?.today_cost || 0)}`"
          :title="`${t('dashboard.standard')}: ${formatUsd(stats?.today_cost || 0, EXACT_DIGITS)}`"
        />
      </span>
      <span class="num-aux" :title="formatMixed(stats?.total_actual_cost || 0, stats?.total_actual_cost_fiat, EXACT_DIGITS)">
        {{ t('common.total') }}: {{ formatMixed(stats?.total_actual_cost || 0, stats?.total_actual_cost_fiat) }}
      </span>
    </div>
  </div>

  <!-- 次级指标条 -->
  <div class="grid grid-cols-2 gap-px overflow-hidden rounded-md border border-gray-200 bg-gray-200 dark:border-dark-700 dark:bg-dark-700 lg:grid-cols-4">
    <!-- Today Tokens -->
    <div class="metric-cell bg-gray-50 dark:bg-dark-950">
      <span class="metric-label">{{ t('dashboard.todayTokens') }}</span>
      <NumText tier="primary" :text="formatTokens(stats?.today_tokens || 0)" :title="formatNumber(stats?.today_tokens || 0)" />
      <span class="num-aux">{{ t('dashboard.input') }} {{ formatTokens(stats?.today_input_tokens || 0) }} · {{ t('dashboard.output') }} {{ formatTokens(stats?.today_output_tokens || 0) }} · {{ t('dashboard.cache') }} {{ formatTokens((stats?.today_cache_creation_tokens || 0) + (stats?.today_cache_read_tokens || 0)) }}</span>
    </div>

    <!-- Total Tokens -->
    <div class="metric-cell bg-gray-50 dark:bg-dark-950">
      <span class="metric-label">{{ t('dashboard.totalTokens') }}</span>
      <NumText tier="primary" :text="formatTokens(stats?.total_tokens || 0)" :title="formatNumber(stats?.total_tokens || 0)" />
      <span class="num-aux">{{ t('dashboard.input') }} {{ formatTokens(stats?.total_input_tokens || 0) }} · {{ t('dashboard.output') }} {{ formatTokens(stats?.total_output_tokens || 0) }} · {{ t('dashboard.cache') }} {{ formatTokens((stats?.total_cache_creation_tokens || 0) + (stats?.total_cache_read_tokens || 0)) }}</span>
    </div>

    <!-- Performance (RPM/TPM) -->
    <div class="metric-cell bg-gray-50 dark:bg-dark-950">
      <span class="metric-label">{{ t('dashboard.performance') }}</span>
      <NumText tier="primary" :text="`${formatTokens(stats?.rpm || 0)} RPM`" />
      <span class="num-aux">{{ formatTokens(stats?.tpm || 0) }} TPM</span>
    </div>

    <!-- Avg Response Time -->
    <div class="metric-cell bg-gray-50 dark:bg-dark-950">
      <span class="metric-label">{{ t('dashboard.avgResponse') }}</span>
      <NumText tier="primary" :text="formatDuration(stats?.average_duration_ms || 0)" />
      <span class="num-aux">{{ t('dashboard.averageTime') }}</span>
    </div>
  </div>

  <!-- 每日签到条（在按平台拆分上方） -->
  <CheckinCard />

  <!-- Row 3: Per-platform breakdown -->
  <div v-if="!isSimple && platformCards.length > 0" class="card p-4">
    <div class="mb-3 flex items-center justify-between">
      <h3 class="font-serif text-base text-gray-900 dark:text-white">{{ t('dashboard.platformBreakdown') }}</h3>
      <span class="text-xs text-gray-500 dark:text-gray-400">
        {{ t('dashboard.platformCount', { count: platformCount }) }}
      </span>
    </div>
    <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
      <div
        v-for="item in platformCards"
        :key="item.platform"
        data-testid="platform-card"
        :data-platform="item.platform"
        :class="[
          'rounded-md border p-3',
          item.isOther
            ? 'border-dashed border-gray-300 bg-gray-100 dark:border-dark-500 dark:bg-dark-700/30'
            : 'border-gray-200 dark:border-dark-600'
        ]"
      >
        <div class="flex items-center justify-between">
          <span class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ item.isOther ? t('dashboard.platformOther') : platformLabel(item.platform) }}
          </span>
          <NumText
            tier="secondary"
            class="text-sm text-gray-900 dark:text-white"
            :text="formatMixed(item.total_actual_cost, item.total_actual_cost_fiat)"
            :title="`${t('dashboard.actual')}: ${formatMixed(item.total_actual_cost, item.total_actual_cost_fiat, EXACT_DIGITS)}`"
          />
        </div>
        <div class="mt-2 space-y-1 text-xs">
          <div class="flex items-center justify-between">
            <span class="text-gray-500 dark:text-gray-400">{{ t('dashboard.todayCost') }}</span>
            <NumText
              tier="secondary"
              class="text-gray-900 dark:text-white"
              :text="formatMixed(item.today_actual_cost, item.today_actual_cost_fiat)"
              :title="formatMixed(item.today_actual_cost, item.today_actual_cost_fiat, EXACT_DIGITS)"
            />
          </div>
          <div class="flex items-center justify-between">
            <span class="text-gray-500 dark:text-gray-400">{{ t('dashboard.requests') }}</span>
            <NumText
              tier="secondary"
              class="text-gray-700 dark:text-gray-300"
              :text="item.total_requests > 0 ? formatNumber(item.total_requests) : '-'"
            />
          </div>
          <div class="flex items-center justify-between">
            <span class="text-gray-500 dark:text-gray-400">{{ t('dashboard.tokens') }}</span>
            <NumText
              tier="secondary"
              class="text-gray-700 dark:text-gray-300"
              :text="item.total_tokens > 0 ? formatTokens(item.total_tokens) : '-'"
              :title="item.total_tokens > 0 ? formatNumber(item.total_tokens) : undefined"
            />
          </div>
        </div>

        <!-- Quota 区：仅当 quota 配置存在、非 __other__ 且至少有一个窗口配了 limit 时显示 -->
        <div v-if="hasAnyLimit(item.quota) && !item.isOther" class="mt-3 space-y-1.5 border-t border-gray-200 pt-2 dark:border-dark-700">
          <p class="font-serif text-[11px] italic text-gray-500">
            {{ t('dashboard.platformQuota.title') }}
          </p>
          <template v-for="w in (['daily', 'weekly', 'monthly'] as const)" :key="w">
            <div v-if="quotaVal(item.quota, `${w}_limit_usd`) != null" class="space-y-0.5">
              <!-- limit=0：完全禁用 -->
              <template v-if="(quotaVal(item.quota, `${w}_limit_usd`) as number) === 0">
                <div class="flex items-center justify-between text-xs">
                  <span class="text-gray-600 dark:text-gray-300">{{ t(`dashboard.platformQuota.${w}`) }}</span>
                  <span class="text-primary-600 dark:text-primary-400">{{ t('dashboard.platformQuota.disabled') }}</span>
                </div>
                <div class="h-1.5 w-full overflow-hidden rounded-full bg-gray-200 dark:bg-dark-700">
                  <div class="h-full w-full rounded-full bg-primary-600" />
                </div>
              </template>
              <!-- limit>0：正常用量进度条 -->
              <template v-else>
                <div class="flex items-center justify-between text-xs">
                  <span class="text-gray-600 dark:text-gray-300">{{ t(`dashboard.platformQuota.${w}`) }}</span>
                  <span class="num-secondary text-gray-700 dark:text-gray-200">{{ formatLimit((quotaVal(item.quota, `${w}_usage_usd`) as number) ?? 0) }} / {{ formatLimit(quotaVal(item.quota, `${w}_limit_usd`) as number) }}</span>
                </div>
                <div class="h-1.5 w-full overflow-hidden rounded-full bg-gray-200 dark:bg-dark-700">
                  <div
                    class="h-full rounded-full transition-all"
                    :class="quotaBarClass(calcPercent((quotaVal(item.quota, `${w}_usage_usd`) as number) ?? 0, quotaVal(item.quota, `${w}_limit_usd`) as number))"
                    :style="{ width: calcPercent((quotaVal(item.quota, `${w}_usage_usd`) as number) ?? 0, quotaVal(item.quota, `${w}_limit_usd`) as number) + '%' }"
                  />
                </div>
                <p v-if="quotaVal(item.quota, `${w}_window_resets_at`)" class="num-aux text-[10px] text-gray-400 dark:text-gray-500">
                  {{ t('dashboard.platformQuota.resetsAt', { time: formatResetTime(quotaVal(item.quota, `${w}_window_resets_at`) as string) }) }}
                </p>
              </template>
            </div>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PlatformDashboardStats, UserDashboardStats as UserStatsType } from '@/api/usage'
import type { PlatformQuotaItem } from '@/types'
import CheckinCard from '@/components/user/dashboard/CheckinCard.vue'
import NumText from '@/components/common/NumText.vue'
import { EXACT_DIGITS, useCurrencyDisplay } from '@/composables/useCurrencyDisplay'
import { useSourceFiatRate } from '@/composables/useSourceFiatRate'
import { formatCompactCount, formatCount, formatDurationMs } from '@/utils/numberFormat'

interface FusedPlatformCard {
  platform: string
  total_actual_cost: number
  today_actual_cost: number
  // 服务端分桶折算的人民币值；缺省表示后端未提供（formatMixed 会回落到美元）
  total_actual_cost_fiat?: number
  today_actual_cost_fiat?: number
  total_requests: number
  total_tokens: number
  isOther?: boolean
  quota?: PlatformQuotaItem
}

const props = defineProps<{
  stats: UserStatsType
  balance: number
  isSimple: boolean
  platformQuotas?: PlatformQuotaItem[] | null
}>()
const { t } = useI18n()
const { isFiat, formatUsd, formatWallet, formatMixed } = useCurrencyDisplay()
// 平台限额统计的是额度（钱包和订阅卡混扣），只能按当前扣费来源近似折算
const { formatLimit } = useSourceFiatRate()

const PLATFORM_LABELS: Record<string, string> = {
  anthropic: 'Claude',
  openai: 'OpenAI',
  gemini: 'Gemini',
  antigravity: 'Antigravity'
}

const platformLabel = (p: string) => PLATFORM_LABELS[p] ?? p

// 处理"各平台之和 < 总值"的差值：后端按平台聚合时过滤了无法归属平台的行
// （group 与 account 都缺 platform）。这里把差值作为"其他"卡片显式展示，
// 避免 Row 1 总值与 Row 3 平台拆分加总对不上、用户困惑。
const OTHER_THRESHOLD = 0.0001
const platformCards = computed<FusedPlatformCard[]>(() => {
  // 建立 by_platform Map
  const byPlat = new Map<string, PlatformDashboardStats>()
  for (const item of props.stats?.by_platform ?? []) byPlat.set(item.platform, item)

  // 建立 quota Map。三档全空的记录不产生卡片，挂到卡片上也不渲染配额区。
  const byQuota = new Map<string, PlatformQuotaItem>()
  for (const q of props.platformQuotas ?? []) byQuota.set(q.platform, q)

  // 卡片集合 = 有用量的平台 ∪ 至少配置了一档限额的平台。
  // 三档全空的限额记录等价于不限额，不单独产生卡片。
  // 后端 by_platform / quota 接口均不会返回 platform='__other__'，
  // 无需显式排除；__other__ 由下方差值补差逻辑单独追加。
  const platforms = new Set<string>(byPlat.keys())
  for (const [platform, q] of byQuota) {
    if (hasAnyLimit(q)) platforms.add(platform)
  }

  const PLATFORM_ORDER = ['anthropic', 'openai', 'gemini', 'antigravity', 'grok']
  const cards: FusedPlatformCard[] = []

  for (const p of platforms) {
    const stat = byPlat.get(p)
    cards.push({
      platform: p,
      total_actual_cost: stat?.total_actual_cost ?? 0,
      today_actual_cost: stat?.today_actual_cost ?? 0,
      total_actual_cost_fiat: knownFiat(stat?.total_actual_cost, stat?.total_actual_cost_fiat),
      today_actual_cost_fiat: knownFiat(stat?.today_actual_cost, stat?.today_actual_cost_fiat),
      total_requests: stat?.total_requests ?? 0,
      total_tokens: stat?.total_tokens ?? 0,
      quota: byQuota.get(p),
    })
  }

  // 排序：按 PLATFORM_ORDER，未知平台按名称排序
  cards.sort((a, b) => {
    const ai = PLATFORM_ORDER.indexOf(a.platform)
    const bi = PLATFORM_ORDER.indexOf(b.platform)
    if (ai === -1 && bi === -1) return a.platform.localeCompare(b.platform)
    if (ai === -1) return 1
    if (bi === -1) return -1
    return ai - bi
  })

  // __other__ 补差逻辑：只对 by_platform 有 usage 数据的总和计算
  const total = props.stats?.total_actual_cost ?? 0
  const today = props.stats?.today_actual_cost ?? 0
  const sumTotal = cards.reduce((s, c) => s + c.total_actual_cost, 0)
  const sumToday = cards.reduce((s, c) => s + c.today_actual_cost, 0)
  const diffTotal = Math.max(0, total - sumTotal)
  const diffToday = Math.max(0, today - sumToday)

  if (diffTotal > OTHER_THRESHOLD || diffToday > OTHER_THRESHOLD) {
    cards.push({
      platform: '__other__',
      total_actual_cost: diffTotal,
      today_actual_cost: diffToday,
      // 人民币同样按「总值 − 各平台之和」补差；任何一方缺人民币值就整体缺省
      total_actual_cost_fiat: fiatDiff(
        knownFiat(total, props.stats?.total_actual_cost_fiat),
        cards.map((c) => c.total_actual_cost_fiat)
      ),
      today_actual_cost_fiat: fiatDiff(
        knownFiat(today, props.stats?.today_actual_cost_fiat),
        cards.map((c) => c.today_actual_cost_fiat)
      ),
      total_requests: 0,
      total_tokens: 0,
      isOther: true,
    })
  }

  return cards
})

/**
 * 后端人民币字段带 omitempty：额度为 0 时字段缺省，此时人民币就是 0；
 * 额度非 0 却缺字段，说明后端没提供，返回 undefined。
 */
function knownFiat(credits: number | undefined, fiat: number | undefined): number | undefined {
  if (typeof fiat === 'number' && Number.isFinite(fiat)) return fiat
  return credits ? undefined : 0
}

/** 「其他」卡的人民币 = 总值 − 各平台之和；任何一项未知就整体未知。 */
function fiatDiff(total: number | undefined, parts: Array<number | undefined>): number | undefined {
  if (total === undefined || parts.some((p) => p === undefined)) return undefined
  return Math.max(0, total - parts.reduce<number>((sum, p) => sum + (p ?? 0), 0))
}

// 标题右侧的平台计数 = 实际渲染的平台卡片数，不含"其他"差额卡。
const platformCount = computed(() => platformCards.value.filter((c) => !c.isOther).length)

// Quota helpers

type QuotaWindow = 'daily' | 'weekly' | 'monthly'
type QuotaField = `${QuotaWindow}_limit_usd` | `${QuotaWindow}_usage_usd` | `${QuotaWindow}_window_resets_at`

function quotaVal(q: PlatformQuotaItem | undefined, key: QuotaField): PlatformQuotaItem[QuotaField] {
  return q?.[key]
}

function hasAnyLimit(q: PlatformQuotaItem | undefined): boolean {
  if (!q) return false
  return q.daily_limit_usd != null || q.weekly_limit_usd != null || q.monthly_limit_usd != null
}

function calcPercent(usage: number, limit: number): number {
  if (!limit || limit <= 0) return 0
  return Math.min(100, Math.max(0, Math.round((usage / limit) * 100)))
}

// 反彩虹：进度条用黏土（图表是黏土的指定岗位），逼近上限时升到 Signal（深黏土）
function quotaBarClass(p: number): string {
  if (p >= 95) return 'bg-primary-700'
  return 'bg-primary-500'
}

function formatResetTime(iso: string | null | undefined): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString(undefined, {
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  })
}

// 计数与 Token 的写法全站统一，见 utils/numberFormat
const formatNumber = formatCount
const formatTokens = (n: number) => formatCompactCount(n, { allowBillions: false })
const formatDuration = formatDurationMs
</script>
