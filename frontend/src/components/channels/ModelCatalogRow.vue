<template>
  <li class="border-b border-gray-200 last:border-b-0 dark:border-dark-700" data-test="catalog-row">
    <button
      type="button"
      class="flex w-full items-center gap-3 px-3 py-3 text-left transition-colors hover:bg-gray-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary-500 disabled:cursor-default disabled:hover:bg-transparent dark:hover:bg-dark-800/60 sm:px-4"
      :aria-expanded="expandable ? expanded : undefined"
      :aria-controls="expandable ? panelId : undefined"
      :disabled="!expandable"
      @click="emit('toggle')"
    >
      <PlatformIcon :platform="(model.platform as GroupPlatform)" size="sm" class="shrink-0" />
      <span class="min-w-0 flex-1 sm:flex sm:items-baseline sm:gap-4">
        <span class="block min-w-0 sm:w-72 sm:shrink-0">
          <span class="block truncate text-sm font-semibold text-gray-900 dark:text-white" :title="model.name">{{ model.name }}</span>
          <span class="block text-xs text-gray-500 dark:text-gray-400">{{ platformLabel(model.platform) }}</span>
        </span>
        <span v-if="startRows.length > 0" class="mt-1 flex flex-wrap items-baseline gap-x-4 gap-y-0.5 text-sm tabular-nums sm:mt-0" data-test="start-prices">
          <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('availableChannels.startingFrom') }}</span>
          <span v-for="row in startRows" :key="row.key">
            <span class="text-xs text-gray-500 dark:text-gray-400">{{ row.label }}</span>
            <span class="ml-1 font-medium text-gray-900 dark:text-white">{{ row.price }}</span>
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
      <!-- 起价：整页唯一醒目的地方 -->
      <div class="flex flex-wrap items-start gap-x-10 gap-y-4" data-test="hero-prices">
        <div v-for="row in heroRows" :key="row.key">
          <div class="catalog-hero">{{ row.price }}</div>
          <div class="mt-1 text-xs text-gray-600 dark:text-gray-300">{{ row.label }}</div>
          <div v-if="row.official" class="text-xs text-gray-400 line-through dark:text-gray-500" data-test="official-price">
            {{ row.official }}
          </div>
        </div>
        <div class="self-end pb-0.5 text-xs text-gray-500 dark:text-gray-400">{{ unitLabel }}</div>
      </div>

      <!-- 按分组 -->
      <div class="mt-5">
        <h3 class="mb-2 text-sm font-semibold text-gray-900 dark:text-white">{{ t('availableChannels.byGroup') }}</h3>
        <div class="overflow-x-auto rounded-lg border border-gray-200 dark:border-dark-700">
          <table class="w-full min-w-[34rem] border-collapse text-sm" data-test="group-table">
            <thead>
              <tr class="border-b border-gray-200 bg-gray-50 text-xs font-medium text-gray-600 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-300">
                <th scope="col" class="px-3 py-2 text-left font-medium">{{ t('availableChannels.group') }}</th>
                <template v-if="isToken">
                  <th scope="col" class="px-3 py-2 text-right font-medium">{{ t('availableChannels.pricing.inputPrice') }}</th>
                  <th v-if="hasCache" scope="col" class="px-3 py-2 text-right font-medium">{{ t('availableChannels.cache') }}</th>
                  <th scope="col" class="px-3 py-2 text-right font-medium">{{ t('availableChannels.pricing.outputPrice') }}</th>
                </template>
                <th v-else scope="col" class="px-3 py-2 text-right font-medium">{{ t('availableChannels.price') }}</th>
                <th v-if="showPlan" scope="col" class="px-3 py-2 text-right font-medium">
                  {{ unit?.exact ? t('availableChannels.yourPlanPrice') : t('availableChannels.planPrice') }}
                  <span v-if="isToken" class="block text-[11px] font-normal text-gray-500 dark:text-gray-400">{{ t('availableChannels.inOut') }}</span>
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
                  <span
                    class="ml-1.5 cursor-help text-xs text-gray-500 dark:text-gray-400"
                    :title="row.rateTitle"
                    data-test="rate-tag"
                  >{{ row.rateText }}x</span>
                  <span
                    v-if="row.lowest && idx === 0"
                    class="ml-1.5 rounded bg-primary-100 px-1.5 py-0.5 text-[11px] font-medium text-primary-800 dark:bg-primary-900/40 dark:text-primary-300"
                  >{{ t('availableChannels.lowest') }}</span>
                </th>
                <template v-if="isToken">
                  <td class="px-3 py-2 text-right tabular-nums">{{ row.cells.input }}</td>
                  <td v-if="hasCache" class="px-3 py-2 text-right tabular-nums">{{ row.cells.cacheRead }}</td>
                  <td class="px-3 py-2 text-right tabular-nums">{{ row.cells.output }}</td>
                </template>
                <td v-else class="px-3 py-2 text-right tabular-nums">{{ row.cells.unit }}</td>
                <td v-if="showPlan" class="px-3 py-2 text-right tabular-nums text-gray-700 dark:text-gray-300" data-test="plan-cell">{{ row.plan }}</td>
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
                <th scope="col" class="px-3 py-2 text-left font-medium">{{ t('availableChannels.context') }}</th>
                <template v-if="isToken">
                  <th scope="col" class="px-3 py-2 text-right font-medium">{{ t('availableChannels.pricing.inputPrice') }}</th>
                  <th v-if="tierHasCache" scope="col" class="px-3 py-2 text-right font-medium">{{ t('availableChannels.cache') }}</th>
                  <th scope="col" class="px-3 py-2 text-right font-medium">{{ t('availableChannels.pricing.outputPrice') }}</th>
                </template>
                <th v-else scope="col" class="px-3 py-2 text-right font-medium">{{ t('availableChannels.price') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(tier, idx) in tierRows" :key="idx" class="border-b border-gray-100 last:border-b-0 dark:border-dark-700/60">
                <th scope="row" class="px-3 py-2 text-left font-normal text-gray-700 dark:text-gray-300">{{ tier.label }}</th>
                <template v-if="isToken">
                  <td class="px-3 py-2 text-right tabular-nums">{{ tier.cells.input }}</td>
                  <td v-if="tierHasCache" class="px-3 py-2 text-right tabular-nums">{{ tier.cells.cacheRead }}</td>
                  <td class="px-3 py-2 text-right tabular-nums">{{ tier.cells.output }}</td>
                </template>
                <td v-else class="px-3 py-2 text-right tabular-nums">{{ tier.cells.unit }}</td>
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
import { platformLabel } from '@/utils/platformColors'
import {
  balancePrice,
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
const { isFiat, rechargeMultiplier, formatFiat, formatOfficial } = useCurrencyDisplay()

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

/** 美元模式展示的额度价 / 官方价：去掉多余的尾零。 */
function usd(n: number): string {
  return `$${Number(n.toPrecision(10)).toString()}`
}
function money(n: number): string {
  return isFiat.value ? formatFiat(n) : usd(n)
}
function formatPlan(p: PlanPrice | null): string {
  if (!p) return '-'
  return p.exact ? formatFiat(p.min) : `${formatFiat(p.min)}–${formatFiat(p.max)}`
}

interface Cells {
  input: string
  cacheRead: string
  output: string
  unit: string
}

function cellsOf(set: PriceSet, rate: number): Cells {
  const f = (v: number | null) => (v == null ? '-' : money(balancePrice(v, rate, kind.value, ctx.value)))
  return { input: f(set.input), cacheRead: f(set.cacheRead), output: f(set.output), unit: f(set.unit) }
}

function planOf(set: PriceSet, rate: number): string {
  const p = (v: number | null) => (v == null ? null : planPrice(v, rate, kind.value, ctx.value))
  if (!isToken.value) return formatPlan(p(set.unit))
  return `${formatPlan(p(set.input))} / ${formatPlan(p(set.output))}`
}

interface PriceLine {
  key: string
  label: string
  price: string
  official: string | null
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
      ]
    : [{ key: 'unit', label: t('availableChannels.pricing.perRequestPrice'), v: first.unit }]
  return defs
    .filter((d): d is { key: string; label: string; v: number } => d.v != null)
    .map((d) => {
      const price = money(balancePrice(d.v, rate, kind.value, ctx.value))
      // 官方价：人民币模式缺汇率时为 null（隐藏）；与展示价相同时不重复出现。
      const official = isFiat.value
        ? formatOfficial(officialPrice(d.v, kind.value))
        : usd(officialPrice(d.v, kind.value))
      return { key: d.key, label: d.label, price, official: official && official !== price ? official : null }
    })
}

const heroRows = computed(() => linesOf(props.model.cheapest))
/** 列表行只放最核心的输入 / 输出（按次模型就是单价）。 */
const startRows = computed(() => heroRows.value.filter((r) => r.key !== 'cacheRead'))

const hasCache = computed(() => props.model.entries.some((e) => e.pricing.first.cacheRead != null))
const showPlan = computed(() =>
  props.model.entries.some((e) =>
    [e.pricing.first.input, e.pricing.first.output, e.pricing.first.unit].some(
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
      rateTitle: g.hasCustomRate
        ? t('availableChannels.rateTooltipCustom', { base: formatRate(g.baseRate) })
        : t('availableChannels.rateTooltip'),
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
const tierHasCache = computed(() => props.model.cheapest?.pricing.tiers.some((x) => x.prices.cacheRead != null) ?? false)
</script>

<style scoped>
/* 页面唯一的衬线大数字：Fraunces + 陶土色 */
.catalog-hero {
  @apply font-serif text-2xl font-medium leading-none text-primary-700 dark:text-primary-400 sm:text-3xl;
  font-variant-numeric: lining-nums tabular-nums;
}
</style>
