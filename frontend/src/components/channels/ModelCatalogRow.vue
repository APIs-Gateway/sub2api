<template>
  <li class="border-b border-gray-200 last:border-b-0 dark:border-dark-700" data-test="catalog-row">
    <button
      type="button"
      class="flex w-full items-center gap-3 px-3 py-3 text-left transition-colors hover:bg-gray-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary-500 disabled:cursor-default disabled:hover:bg-transparent dark:hover:bg-dark-800/60 sm:px-4"
      :aria-expanded="expandable ? expanded : undefined"
      :aria-controls="expandable && expanded ? panelId : undefined"
      :disabled="!expandable"
      @click="emit('toggle')"
    >
      <PlatformIcon :platform="(model.platform as GroupPlatform)" size="sm" class="shrink-0" />
      <span class="min-w-0 flex-1 sm:flex sm:items-baseline sm:gap-4">
        <span class="block min-w-0 sm:w-72 sm:shrink-0">
          <span class="block truncate text-sm font-semibold text-gray-900 dark:text-white" :title="model.name">{{ model.name }}</span>
          <span class="block text-xs text-gray-500 dark:text-gray-400">{{ platformLabel(model.platform) }}</span>
        </span>
        <span v-if="startRows.length > 0" class="mt-1 flex flex-wrap items-baseline gap-x-4 gap-y-0.5 text-sm sm:mt-0" data-test="start-prices">
          <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('availableChannels.startingFrom') }}</span>
          <span v-for="row in startRows" :key="row.key">
            <span class="text-xs text-gray-500 dark:text-gray-400">{{ row.label }}</span>
            <NumText tier="secondary" class="ml-1 text-gray-900 dark:text-white" :text="row.price" />
          </span>
          <span class="text-xs text-gray-500 dark:text-gray-400">{{ unitLabel }}</span>
        </span>
        <span v-else class="mt-1 block text-xs text-gray-400 dark:text-gray-500 sm:mt-0">{{ t('availableChannels.noPricing') }}</span>
      </span>
      <span v-if="expandable" class="flex shrink-0 items-center gap-1 text-xs text-gray-500 dark:text-gray-400">
        <span class="hidden sm:inline">{{ expanded ? t('availableChannels.collapse') : t('availableChannels.expand') }}</span>
        <Icon
          name="chevronDown"
          size="sm"
          :class="['transition-transform duration-150 motion-reduce:transition-none', expanded ? 'rotate-180' : '']"
        />
      </span>
    </button>

    <div v-if="expandable && expanded" :id="panelId" class="px-3 pb-5 pt-1 sm:px-4 sm:pl-12" data-test="catalog-panel">
      <!-- 起价：主指标一档，与全站卡片里的大数字同字号同字重 -->
      <div class="flex flex-wrap items-start gap-x-10 gap-y-4" data-test="hero-prices">
        <div v-for="row in heroRows" :key="row.key">
          <NumText tier="primary" class="block" :text="row.price" />
          <div class="mt-1 text-xs text-gray-600 dark:text-gray-300">{{ row.label }}</div>
          <div v-if="row.official" class="num-aux" data-test="official-price">
            {{ t('availableChannels.officialPrice') }}
            <span :class="row.officialStruck ? 'text-gray-400 line-through dark:text-gray-500' : ''">{{ row.official }}</span>
          </div>
        </div>
        <div class="self-end pb-0.5 text-xs text-gray-500 dark:text-gray-400">{{ unitLabel }}</div>
      </div>

      <!-- 按分组 -->
      <div class="mt-5">
        <h3 class="mb-1 text-sm font-semibold text-gray-900 dark:text-white">{{ t('availableChannels.byGroup') }}</h3>
        <p class="mb-2 text-xs text-gray-500 dark:text-gray-400" data-test="rate-note">{{ t('availableChannels.rateNote') }}</p>
        <div class="overflow-x-auto rounded-lg border border-gray-200 dark:border-dark-700">
          <table class="w-full min-w-[34rem] border-collapse text-sm" data-test="group-table">
            <thead>
              <tr class="border-b border-gray-200 bg-gray-50 text-xs font-medium text-gray-600 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-300">
                <th scope="col" class="px-3 py-2 text-left font-medium">{{ t('availableChannels.group') }}</th>
                <template v-if="isToken">
                  <th v-for="col in groupColumns" :key="col" scope="col" class="px-3 py-2 text-right font-medium">{{ columnLabels[col] }}</th>
                </template>
                <th v-else scope="col" class="px-3 py-2 text-right font-medium">{{ t('availableChannels.price') }}</th>
                <th v-if="showPlan" scope="col" class="px-3 py-2 text-right font-medium">
                  {{ unit?.exact ? t('availableChannels.yourPlanPrice') : t('availableChannels.planPrice') }}
                  <span v-if="showInOut" class="block text-[11px] font-normal text-gray-500 dark:text-gray-400">{{ t('availableChannels.inOut') }}</span>
                </th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="(row, idx) in groupRows"
                :key="row.id"
                :class="[
                  'border-b border-gray-100 last:border-b-0 dark:border-dark-700/60',
                  row.lowest ? 'bg-primary-50/70 dark:bg-primary-900/10' : '',
                ]"
                :data-lowest="row.lowest ? 'true' : undefined"
              >
                <th scope="row" class="px-3 py-2 text-left font-normal">
                  <span class="font-medium text-gray-900 dark:text-white">{{ row.name }}</span>
                  <span class="ml-1.5 text-xs text-gray-500 dark:text-gray-400" data-test="rate-tag">{{ row.rateText }}x</span>
                  <span v-if="row.customRateNote" class="ml-1 text-xs text-gray-500 dark:text-gray-400" data-test="rate-custom">{{ row.customRateNote }}</span>
                  <span
                    v-if="row.lowest && idx === 0"
                    class="ml-1.5 rounded bg-primary-100 px-1.5 py-0.5 text-[11px] font-medium text-primary-800 dark:bg-primary-900/40 dark:text-primary-300"
                  >{{ t('availableChannels.lowest') }}</span>
                </th>
                <template v-if="isToken">
                  <td v-for="col in groupColumns" :key="col" class="px-3 py-2 text-right"><NumText tier="secondary" :text="row.cells[col]" /></td>
                </template>
                <td v-else class="px-3 py-2 text-right"><NumText tier="secondary" :text="row.cells.unit" /></td>
                <td v-if="showPlan" class="px-3 py-2 text-right text-gray-700 dark:text-gray-300" data-test="plan-cell"><NumText tier="secondary" :text="row.plan" /></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- 阶梯价：按起价所在分组换算 -->
      <div v-if="tierRows.length > 0" class="mt-5" data-test="tier-table">
        <h3 class="mb-2 text-sm font-semibold text-gray-900 dark:text-white">
          {{ t('availableChannels.tiersFor', { group: model.cheapest?.group.name }) }}
        </h3>
        <div class="overflow-x-auto rounded-lg border border-gray-200 dark:border-dark-700">
          <table class="w-full min-w-[28rem] border-collapse text-sm">
            <thead>
              <tr class="border-b border-gray-200 bg-gray-50 text-xs font-medium text-gray-600 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-300">
                <th scope="col" class="px-3 py-2 text-left font-medium">{{ tierHeader }}</th>
                <template v-if="isToken">
                  <th v-for="col in tierColumns" :key="col" scope="col" class="px-3 py-2 text-right font-medium">{{ columnLabels[col] }}</th>
                </template>
                <th v-else scope="col" class="px-3 py-2 text-right font-medium">{{ t('availableChannels.price') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(tier, idx) in tierRows" :key="idx" class="border-b border-gray-100 last:border-b-0 dark:border-dark-700/60">
                <th scope="row" class="px-3 py-2 text-left font-normal text-gray-700 dark:text-gray-300">{{ tier.label }}</th>
                <template v-if="isToken">
                  <td v-for="col in tierColumns" :key="col" class="px-3 py-2 text-right"><NumText tier="secondary" :text="tier.cells[col]" /></td>
                </template>
                <td v-else class="px-3 py-2 text-right"><NumText tier="secondary" :text="tier.cells.unit" /></td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </li>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import NumText from '@/components/common/NumText.vue'
import { platformLabel } from '@/utils/platformColors'
import { BILLING_MODE_IMAGE, BILLING_MODE_PER_REQUEST } from '@/constants/channel'
import {
  balancePrice,
  exceeds,
  formatTokenCount,
  officialPrice,
  planPrice,
  type CatalogModel,
  type GroupPrice,
  type ModelTier,
  type PlanPrice,
  type PriceSet,
  type PricingContext,
  type SubscriptionUnitRange,
} from '@/utils/modelCatalog'
import { useCurrencyDisplay } from '@/composables/useCurrencyDisplay'
import type { GroupPlatform } from '@/types'

const props = withDefaults(
  defineProps<{
    model: CatalogModel
    expanded?: boolean
    subscriptionUnit?: SubscriptionUnitRange | null
  }>(),
  { expanded: false, subscriptionUnit: null },
)
const emit = defineEmits<{ (e: 'toggle'): void }>()

const { t } = useI18n()
const { isFiat, rechargeMultiplier, officialCnyRate, formatFiat, formatUsd, formatOfficial } = useCurrencyDisplay()

const unit = computed(() => (isFiat.value ? props.subscriptionUnit : null))
const ctx = computed<PricingContext>(() => ({
  isFiat: isFiat.value,
  rechargeMultiplier: rechargeMultiplier.value,
  subscriptionUnit: props.subscriptionUnit,
}))

const panelId = computed(() => `model-panel-${props.model.key.replace(/[^a-zA-Z0-9_-]/g, '_')}`)
const expandable = computed(() => props.model.entries.length > 0)
const isToken = computed(() => props.model.kind === 'token')
const kind = computed(() => props.model.kind ?? 'token')
const unitLabel = computed(() =>
  isToken.value ? t('availableChannels.pricing.perMillion') : t('availableChannels.pricing.perRequest'),
)

/**
 * 价目表上的都是单价（每百万 Token / 每次请求）：金额统一规则之外，≥ 1 的价格
 * 保留到 4 位小数（1.875 不会被写成 1.88），< 1 保留 4 位有效数字。
 */
const UNIT_PRICE = { unitPrice: true } as const
function money(n: number): string {
  return isFiat.value ? formatFiat(n, UNIT_PRICE) : formatUsd(n, UNIT_PRICE)
}
function formatPlan(p: PlanPrice | null): string {
  if (!p) return '-'
  return p.exact ? formatFiat(p.min, UNIT_PRICE) : `${formatFiat(p.min, UNIT_PRICE)}–${formatFiat(p.max, UNIT_PRICE)}`
}

/** token 计费可能出现的价格列，按展示顺序排列。 */
const COLUMN_KEYS = ['input', 'cacheRead', 'cacheWrite', 'output', 'imageOutput'] as const
type ColumnKey = (typeof COLUMN_KEYS)[number]

const columnLabels = computed<Record<ColumnKey, string>>(() => ({
  input: t('availableChannels.pricing.inputPrice'),
  cacheRead: t('availableChannels.pricing.cacheReadPrice'),
  cacheWrite: t('availableChannels.pricing.cacheWritePrice'),
  output: t('availableChannels.pricing.outputPrice'),
  imageOutput: t('availableChannels.pricing.imageOutputPrice'),
}))

/** 输入 / 输出成对展示；缓存、图片输出只在有分组配置了才出现。 */
function visibleColumns(sets: PriceSet[]): ColumnKey[] {
  const has = (key: ColumnKey) => sets.some((s) => s[key] != null)
  const core = has('input') || has('output')
  return COLUMN_KEYS.filter((key) => (key === 'input' || key === 'output' ? core : has(key)))
}

type Cells = Record<ColumnKey | 'unit', string>

function cellsOf(set: PriceSet, rate: number): Cells {
  const f = (v: number | null) => (v == null ? '-' : money(balancePrice(v, rate, kind.value, ctx.value)))
  return {
    input: f(set.input),
    cacheRead: f(set.cacheRead),
    cacheWrite: f(set.cacheWrite),
    output: f(set.output),
    imageOutput: f(set.imageOutput),
    unit: f(set.unit),
  }
}

function planOf(set: PriceSet, rate: number): string {
  const p = (v: number | null) => (v == null ? null : planPrice(v, rate, kind.value, ctx.value))
  if (!isToken.value) return formatPlan(p(set.unit))
  if (set.input == null && set.output == null) return formatPlan(p(set.imageOutput))
  return `${formatPlan(p(set.input))} / ${formatPlan(p(set.output))}`
}

interface PriceLine {
  key: string
  label: string
  price: string
  /** 官方价文案；人民币模式缺汇率时为 null（隐藏）。 */
  official: string | null
  /** 官方价高于展示价时才加删除线；不高于时只作中性的对照。 */
  officialStruck: boolean
}

function linesOf(entry: GroupPrice | null): PriceLine[] {
  if (!entry) return []
  const { first } = entry.pricing
  const rate = entry.group.rate
  const defs: Array<{ key: string; label: string; v: number | null }> = isToken.value
    ? [
        { key: 'input', label: t('availableChannels.pricing.inputPrice'), v: first.input },
        { key: 'output', label: t('availableChannels.pricing.outputPrice'), v: first.output },
        { key: 'cacheRead', label: t('availableChannels.pricing.cacheReadPrice'), v: first.cacheRead },
        { key: 'cacheWrite', label: t('availableChannels.pricing.cacheWritePrice'), v: first.cacheWrite },
        { key: 'imageOutput', label: t('availableChannels.pricing.imageOutputPrice'), v: first.imageOutput },
      ]
    : [{ key: 'unit', label: t('availableChannels.pricing.perRequestPrice'), v: first.unit }]
  return defs
    .filter((d): d is { key: string; label: string; v: number } => d.v != null)
    .map((d) => {
      const balance = balancePrice(d.v, rate, kind.value, ctx.value)
      const price = money(balance)
      const official = officialPrice(d.v, kind.value)
      const officialText = formatOfficial(official, UNIT_PRICE)
      // 与展示价同一币种下比较；展示出来的数字相同时不算「更高」。
      const officialAmount = isFiat.value ? official * officialCnyRate.value : official
      return {
        key: d.key,
        label: d.label,
        price,
        official: officialText,
        officialStruck: officialText != null && officialText !== price && exceeds(officialAmount, balance),
      }
    })
}

const heroRows = computed(() => linesOf(props.model.cheapest))
/** 列表行只放最核心的价格；缓存价格留给展开后的明细。 */
const startRows = computed(() => heroRows.value.filter((r) => r.key !== 'cacheRead' && r.key !== 'cacheWrite'))

const groupColumns = computed(() =>
  isToken.value ? visibleColumns(props.model.entries.map((e) => e.pricing.first)) : [],
)
const showInOut = computed(() => groupColumns.value.includes('input'))
const showPlan = computed(() =>
  props.model.entries.some((e) =>
    [e.pricing.first.input, e.pricing.first.output, e.pricing.first.imageOutput, e.pricing.first.unit].some(
      (v) => v != null && planPrice(v, e.group.rate, kind.value, ctx.value) != null,
    ),
  ),
)

function formatRate(r: number): string {
  return Number(r.toPrecision(10)).toString()
}

const groupRows = computed(() =>
  props.model.entries.map((e, idx) => {
    const g = e.group
    return {
      id: g.id,
      name: g.name,
      rateText: formatRate(g.rate),
      customRateNote: g.hasCustomRate ? t('availableChannels.rateCustom', { base: formatRate(g.baseRate) }) : '',
      lowest: props.model.entries.length > 1 && idx === 0,
      cells: cellsOf(e.pricing.first, g.rate),
      plan: planOf(e.pricing.first, g.rate),
    }
  }),
)

function tierLabel(tier: ModelTier): string {
  const { label, min, max } = tier.range
  if (label) return label
  if (max == null) return t('availableChannels.tierAbove', { n: formatTokenCount(min) })
  if (min <= 0) return t('availableChannels.tierUpTo', { n: formatTokenCount(max) })
  return t('availableChannels.tierBetween', { min: formatTokenCount(min), max: formatTokenCount(max) })
}

const tierRows = computed(() => {
  const best = props.model.cheapest
  if (!best) return []
  return best.pricing.tiers.map((tier) => ({
    label: tierLabel(tier),
    cells: cellsOf(tier.prices, best.group.rate),
  }))
})
const tierColumns = computed(() =>
  isToken.value ? visibleColumns(props.model.cheapest?.pricing.tiers.map((x) => x.prices) ?? []) : [],
)

/** 阶梯表首列表头：token 按上下文长度分档，按次按档位，按图按分辨率。 */
const tierHeader = computed(() => {
  const mode = props.model.cheapest?.pricing.mode
  if (mode === BILLING_MODE_IMAGE) return t('availableChannels.resolution')
  if (mode === BILLING_MODE_PER_REQUEST) return t('availableChannels.tierName')
  return t('availableChannels.context')
})
</script>
