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
          <span v-if="peakShort" class="text-xs text-gray-500 dark:text-gray-400" data-test="peak-note-row">{{ peakShort }}</span>
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
      <p v-if="peakNote" class="mt-2 text-xs text-gray-600 dark:text-gray-300" data-test="peak-note">{{ peakNote }}</p>

      <!-- 按分组 -->
      <div class="mt-5">
        <h3 class="mb-1 text-sm font-semibold text-gray-900 dark:text-white">{{ t('availableChannels.byGroup') }}</h3>
        <p class="mb-2 text-xs text-gray-500 dark:text-gray-400" data-test="rate-note">{{ rateNote }}</p>
        <div v-for="sec in sections" :key="sec.kind" :class="sec.first ? '' : 'mt-3'" data-test="group-section">
          <h4 v-if="sections.length > 1" class="mb-1 text-xs text-gray-500 dark:text-gray-400" data-test="section-unit">{{ sec.unitLabel }}</h4>
          <div class="overflow-x-auto rounded-lg border border-gray-200 dark:border-dark-700">
          <table class="w-full min-w-[34rem] border-collapse text-sm" data-test="group-table">
            <thead>
              <tr class="border-b border-gray-200 bg-gray-50 text-xs font-medium text-gray-600 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-300">
                <th scope="col" class="px-3 py-2 text-left font-medium">{{ t('availableChannels.group') }}</th>
                <template v-if="sec.kind === 'token'">
                  <th v-for="col in sec.columns" :key="col" scope="col" class="px-3 py-2 text-right font-medium">{{ columnLabels[col] }}</th>
                </template>
                <th v-else scope="col" class="px-3 py-2 text-right font-medium">{{ t('availableChannels.price') }}</th>
                <th v-if="sec.showPlan" scope="col" class="px-3 py-2 text-right font-medium" data-test="plan-header">
                  {{ sec.hasCard ? t('availableChannels.yourPlanRate') : t('availableChannels.planRate') }}
                </th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="(row, idx) in sec.rows"
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
                  <span v-if="row.peakText" class="ml-1 text-xs text-gray-500 dark:text-gray-400" data-test="peak-tag">{{ row.peakText }}</span>
                  <span v-if="row.customRateNote" class="ml-1 text-xs text-gray-500 dark:text-gray-400" data-test="rate-custom">{{ row.customRateNote }}</span>
                  <span
                    v-if="row.lowest && idx === 0"
                    class="ml-1.5 rounded bg-primary-100 px-1.5 py-0.5 text-[11px] font-medium text-primary-800 dark:bg-primary-900/40 dark:text-primary-300"
                  >{{ t('availableChannels.lowest') }}</span>
                </th>
                <template v-if="sec.kind === 'token'">
                  <td v-for="col in sec.columns" :key="col" class="px-3 py-2 text-right"><NumText tier="secondary" :text="row.cells[col]" /></td>
                </template>
                <td v-else class="px-3 py-2 text-right"><NumText tier="secondary" :text="row.cells.unit" /></td>
                <td v-if="sec.showPlan" class="px-3 py-2 text-right text-gray-700 dark:text-gray-300" data-test="plan-cell"><span v-if="row.plan.lead" class="mr-1 text-xs text-gray-500 dark:text-gray-400" data-test="plan-lead">{{ row.plan.lead }}</span><NumText tier="secondary" :text="row.plan.value" /></td>
              </tr>
            </tbody>
          </table>
          </div>
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
  type CatalogModel,
  type GroupPrice,
  type ModelTier,
  type PriceKind,
  type PriceSet,
  type PricingContext,
} from '@/utils/modelCatalog'
import { useCurrencyDisplay } from '@/composables/useCurrencyDisplay'
import { useRateDisplay } from '@/composables/useRateDisplay'
import type { RateView } from '@/utils/rateDisplay'
import type { GroupPlatform } from '@/types'

const props = withDefaults(
  defineProps<{
    model: CatalogModel
    expanded?: boolean
  }>(),
  { expanded: false },
)
const emit = defineEmits<{ (e: 'toggle'): void }>()

const { t } = useI18n()
const { isFiat, rechargeMultiplier, officialCnyRate, formatFiat, formatUsd, formatOfficial } = useCurrencyDisplay()

const { rateView, planAvailable } = useRateDisplay()
const ctx = computed<PricingContext>(() => ({
  isFiat: isFiat.value,
  rechargeMultiplier: rechargeMultiplier.value,
}))

/**
 * 说明文字：人民币模式下倍率是等效倍率，价格里已包含；能开通套餐时再补一句套餐更省。
 * 美元模式、free 站（isFiat 为假）保持原来的说法。
 */
const rateNote = computed(() => {
  if (!isFiat.value) return t('availableChannels.rateNote')
  return planAvailable.value ? t('availableChannels.rateNoteFiat') : t('availableChannels.rateNoteFiatNoPlan')
})

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
/**
 * 人民币模式的价格位数（仅本页）：≥ ¥0.01 固定 2 位小数；< ¥0.01 走统一规则（4 位有效数字），
 * 避免极便宜的缓存价写成 ¥0.00；0 沿用 unitPrice 的显示。美元模式与 free 站仍用 UNIT_PRICE。
 */
function fiatDigits(amount: number) {
  if (amount >= 0.01) return { fractionDigits: 2 }
  return amount > 0 ? undefined : UNIT_PRICE
}
function money(n: number): string {
  return isFiat.value ? formatFiat(n, fiatDigits(n)) : formatUsd(n, UNIT_PRICE)
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

function cellsOf(set: PriceSet, k: PriceKind = kind.value): Cells {
  const f = (v: number | null) => (v == null ? '-' : money(balancePrice(v, k, ctx.value)))
  return {
    input: f(set.input),
    cacheRead: f(set.cacheRead),
    cacheWrite: f(set.cacheWrite),
    output: f(set.output),
    imageOutput: f(set.imageOutput),
    unit: f(set.unit),
  }
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
  const { first, official: officialSet } = entry.pricing
  const defs: Array<{ key: string; label: string; v: number | null; o: number | null }> = isToken.value
    ? [
        { key: 'input', label: t('availableChannels.pricing.inputPrice'), v: first.input, o: officialSet.input },
        { key: 'output', label: t('availableChannels.pricing.outputPrice'), v: first.output, o: officialSet.output },
        { key: 'cacheRead', label: t('availableChannels.pricing.cacheReadPrice'), v: first.cacheRead, o: officialSet.cacheRead },
        { key: 'cacheWrite', label: t('availableChannels.pricing.cacheWritePrice'), v: first.cacheWrite, o: officialSet.cacheWrite },
        { key: 'imageOutput', label: t('availableChannels.pricing.imageOutputPrice'), v: first.imageOutput, o: officialSet.imageOutput },
      ]
    : [{ key: 'unit', label: t('availableChannels.pricing.perRequestPrice'), v: first.unit, o: officialSet.unit }]
  return defs
    .filter((d): d is { key: string; label: string; v: number; o: number | null } => d.v != null)
    .map((d) => {
      const balance = balancePrice(d.v, kind.value, ctx.value)
      const price = money(balance)
      // 官方价是乘倍率之前的单价；后端没给时退回额度价，不会比展示价更高。
      const official = officialPrice(d.o ?? d.v, kind.value)
      const officialText = formatOfficial(official, isFiat.value ? fiatDigits(official * officialCnyRate.value) : UNIT_PRICE)
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

function formatRate(r: number): string {
  return Number(r.toPrecision(10)).toString()
}

/**
 * 同一个模型在不同分组计费方式可以不同：每个分组按自己的方式展示，同一方式的分组放在一张表里，
 * 价格只在同一张表内比较（最低标记也只在表内）。只有一种方式时就是一张表。
 */
const sections = computed(() =>
  props.model.kinds.map((k, i) => {
    const entries = props.model.entries.filter((e) => e.pricing.kind === k)
    const sets = entries.map((e) => e.pricing.first)
    // 余额倍率 r ÷ m、套餐倍率 r × u 都走 useRateDisplay，这里不自己换算。
    const views = entries.map((e) => rateView(e.group.baseRate, e.group.hasCustomRate ? e.group.rate : null))
    return {
      kind: k,
      first: i === 0,
      unitLabel: k === 'token' ? t('availableChannels.pricing.perMillion') : t('availableChannels.pricing.perRequest'),
      columns: k === 'token' ? visibleColumns(sets) : [],
      // 套餐倍率列：能出现套餐倍率（人民币模式、m ≠ 1、支付开启、取到 u_min）才整列显示。
      showPlan: planAvailable.value,
      hasCard: views.some((v) => v.yourPlan !== undefined),
      rows: entries.map((e, idx) => {
        const g = e.group
        const view = views[idx]
        return {
          id: g.id,
          name: g.name,
          rateText: view.main,
          customRateNote: view.mainStruck !== undefined ? t('availableChannels.rateCustom', { base: view.mainStruck }) : '',
          peakText: e.pricing.peakMultiplier ? peakShortOf(e.pricing.peakMultiplier) : '',
          lowest: entries.length > 1 && idx === 0,
          cells: cellsOf(e.pricing.first, k),
          plan: planCell(view),
        }
      }),
    }
  }),
)

/** 套餐倍率单元格：有生效卡写卡的精确倍率，否则写「低至」最低倍率；没有值写 `-`。 */
function planCell(view: RateView): { lead: string; value: string } {
  if (view.yourPlan !== undefined) return { lead: '', value: `${view.yourPlan}x` }
  if (view.plan !== undefined) return { lead: t('availableChannels.planRateLead'), value: `${view.plan}x` }
  return { lead: '', value: '-' }
}

/** 峰时倍率按分组标注：只有走默认价卡的分组带倍数，渠道自定义价的分组不带。 */
function peakShortOf(n: number): string {
  return t('availableChannels.peakShort', { n: formatRate(n) })
}
/** 列表行只标起价所在的那个分组；该分组没有峰时倍率就不标。 */
const peakShort = computed(() => (props.model.peakMultiplier ? peakShortOf(props.model.peakMultiplier) : ''))
/** 展开面板的说明：有任一分组带峰时倍率才出现，具体分组看分组表里的标注。 */
const peakNote = computed(() => (props.model.hasPeakGroup ? t('availableChannels.peakNote') : ''))

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
    cells: cellsOf(tier.prices),
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
