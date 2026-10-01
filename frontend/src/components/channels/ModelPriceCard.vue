<template>
  <div
    class="flex flex-col rounded-xl border border-gray-200 bg-white p-4 transition-colors hover:border-gray-300 dark:border-dark-700 dark:bg-dark-800 dark:hover:border-dark-600"
  >
    <!-- Header: 平台 + 模型名 + 计费模式 -->
    <div class="mb-3 flex items-center gap-2">
      <PlatformIcon v-if="platform" :platform="(platform as GroupPlatform)" size="sm" />
      <span class="min-w-0 flex-1 truncate text-sm font-bold text-gray-900 dark:text-white" :title="model.name">
        {{ model.name }}
      </span>
      <span
        v-if="model.pricing"
        class="shrink-0 rounded bg-gray-100 px-1.5 py-0.5 text-[10px] font-medium text-gray-500 dark:bg-dark-700 dark:text-gray-400"
      >
        {{ billingModeLabel }}
      </span>
    </div>

    <div v-if="!model.pricing" class="text-xs text-gray-400">{{ noPricingLabel }}</div>

    <!--
      人民币模式：直接给出用户真正付的钱。余额价 = 官方价 × 分组倍率 ÷ 充值倍率；
      套餐价 = 官方价 × 分组倍率 × 套餐卡单价 u(D)。官方美元价降为一行灰色参考。
    -->
    <template v-else-if="isFiat">
      <div class="mb-1 flex items-center justify-between text-[11px] text-gray-400 dark:text-gray-500">
        <span>{{ t('availableChannels.fiat.balancePrice') }}</span>
        <span>{{ fiatUnit }}</span>
      </div>
      <div class="space-y-1.5 text-sm" data-test="balance-prices">
        <div v-for="row in fiatRows" :key="row.key" class="flex justify-between gap-2">
          <span class="text-gray-500 dark:text-gray-400">{{ row.label }}</span>
          <span class="font-mono font-medium text-gray-900 dark:text-white">{{ formatFiatPrice(row.price, balanceFactor) }}</span>
        </div>
      </div>

      <p v-if="hasIntervals" class="mt-1 text-[10px] text-gray-400 dark:text-gray-500">
        {{ t('availableChannels.fiat.firstTierHint') }}
      </p>

      <div
        v-if="subscriptionRange"
        class="mt-3 rounded-lg bg-gray-50 px-2.5 py-2 dark:bg-dark-900/30"
        data-test="subscription-prices"
      >
        <div class="mb-1 text-[10px] font-medium text-gray-500 dark:text-gray-400">
          {{ subscriptionRange.exact ? t('availableChannels.fiat.yourSubscriptionPrice') : t('availableChannels.fiat.subscriptionPrice') }}
        </div>
        <div class="flex flex-wrap gap-x-3 gap-y-0.5 text-[11px] text-gray-700 dark:text-gray-300">
          <span v-for="row in fiatRows" :key="row.key">
            <span class="text-gray-400 dark:text-gray-500">{{ row.label }}</span> {{ formatSubscriptionPrice(row.price) }}
          </span>
        </div>
      </div>

      <p class="mt-2 text-[10px] text-gray-400 dark:text-gray-500" data-test="official-prices">
        {{ t('availableChannels.fiat.officialPrice') }}
        <template v-for="(row, idx) in fiatRows" :key="row.key">
          <span v-if="idx > 0"> · </span>{{ row.label }} {{ formatScaled(row.price, priceScale) }}
        </template>
        {{ fiatUnitPlain }}
      </p>
    </template>

    <template v-else>
      <!-- 官方单价 -->
      <div class="space-y-1.5 text-sm">
        <template v-if="isToken">
          <PricingRow :label="t('availableChannels.pricing.inputPrice')" :value="model.pricing.input_price" :unit="perMillionUnit" :scale="perMillionScale" />
          <PricingRow :label="t('availableChannels.pricing.outputPrice')" :value="model.pricing.output_price" :unit="perMillionUnit" :scale="perMillionScale" />
          <PricingRow v-if="show(model.pricing.cache_read_price)" :label="t('availableChannels.pricing.cacheReadPrice')" :value="model.pricing.cache_read_price" :unit="perMillionUnit" :scale="perMillionScale" />
          <PricingRow v-if="show(model.pricing.cache_write_price)" :label="t('availableChannels.pricing.cacheWritePrice')" :value="model.pricing.cache_write_price" :unit="perMillionUnit" :scale="perMillionScale" />
        </template>
        <PricingRow
          v-else-if="model.pricing.billing_mode === BILLING_MODE_PER_REQUEST"
          :label="t('availableChannels.pricing.perRequestPrice')"
          :value="model.pricing.per_request_price"
          :unit="perRequestUnit"
          :scale="1"
        />
        <PricingRow
          v-else-if="model.pricing.billing_mode === BILLING_MODE_IMAGE"
          :label="t('availableChannels.pricing.imageOutputPrice')"
          :value="model.pricing.image_output_price"
          :unit="perRequestUnit"
          :scale="1"
        />
      </div>

      <!-- 官方阶梯定价 -->
      <div v-if="hasIntervals" class="mt-2 border-t border-gray-100 pt-2 dark:border-dark-700/70">
        <div class="mb-1 flex items-center justify-between gap-2 text-[11px] font-medium text-gray-500 dark:text-gray-400">
          <span>{{ t('availableChannels.pricing.intervals') }}</span>
          <span>{{ intervalUnit }}</span>
        </div>
        <div class="space-y-0.5">
          <div v-for="(iv, idx) in model.pricing.intervals" :key="idx" class="flex items-start justify-between gap-2 text-[11px] text-gray-600 dark:text-gray-300">
            <span class="shrink-0 text-gray-400">{{ iv.tier_label || formatRange(iv.min_tokens, iv.max_tokens) }}</span>
            <span class="flex max-w-[70%] flex-wrap justify-end gap-x-2 gap-y-0.5 text-right font-mono tabular-nums">
              <span v-for="price in intervalPriceRows(iv, 1)" :key="price.key">
                <span class="text-gray-400">{{ price.label }}</span> {{ price.value }}
              </span>
            </span>
          </div>
        </div>
      </div>

      <!-- 本档位实付（× 倍率），按计费模式逐项算 -->
      <div v-if="showEffective" class="mt-3 rounded-lg border-t border-gray-100 bg-gray-50 px-2.5 py-2 dark:border-dark-700/60 dark:bg-dark-900/30">
        <div class="mb-1 text-[10px] font-medium text-gray-500 dark:text-gray-400">
          {{ t('availableChannels.effectiveTitle', { rate: formatRate(rateMultiplier) }) }}
        </div>

        <!-- token + 阶梯：逐档实付 -->
        <div v-if="isToken && hasIntervals" class="space-y-0.5">
          <div v-for="(iv, idx) in model.pricing.intervals" :key="idx" class="flex items-start justify-between gap-2 text-[11px] text-gray-700 dark:text-gray-300">
            <span class="shrink-0 text-gray-400 dark:text-gray-500">{{ iv.tier_label || formatRange(iv.min_tokens, iv.max_tokens) }}</span>
            <span class="flex max-w-[70%] flex-wrap justify-end gap-x-2 gap-y-0.5 text-right font-mono tabular-nums">
              <span v-for="price in intervalPriceRows(iv, rateMultiplier)" :key="price.key">
                <span class="text-gray-400 dark:text-gray-500">{{ price.label }}</span> {{ price.value }}
              </span>
            </span>
          </div>
        </div>

        <!-- token 无阶梯：输入/输出实付 -->
        <div v-else-if="isToken" class="flex flex-wrap gap-x-3 gap-y-0.5 text-[11px] text-gray-700 dark:text-gray-300">
          <span>{{ t('availableChannels.pricing.inputPrice') }} {{ effPerMillion(model.pricing.input_price) }}</span>
          <span>{{ t('availableChannels.pricing.outputPrice') }} {{ effPerMillion(model.pricing.output_price) }}</span>
        </div>

        <!-- 按次/按图实付 -->
        <div v-else class="font-mono text-[11px] text-gray-700 dark:text-gray-300">
          {{ effPerUnit }}
        </div>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import PricingRow from './PricingRow.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import { formatScaled } from '@/utils/pricing'
import { BILLING_MODE_TOKEN, BILLING_MODE_PER_REQUEST, BILLING_MODE_IMAGE } from '@/constants/channel'
import type { UserPricingInterval, UserSupportedModel } from '@/api/channels'
import type { GroupPlatform } from '@/types'
import { useCurrencyDisplay } from '@/composables/useCurrencyDisplay'

/** 套餐卡单价 u（1 个额度值多少人民币）。exact 表示是用户当前那张卡，否则是可购买的区间。 */
export interface SubscriptionUnitRange {
  min: number
  max: number
  exact: boolean
}

const props = withDefaults(
  defineProps<{
    model: UserSupportedModel
    /** 当前所选分组倍率：实付 = 官方单价 × 倍率（含阶梯逐档）。 */
    rateMultiplier?: number
    platformHint?: string
    noPricingLabel?: string
    /** 人民币模式下的套餐价；缺省时只展示余额价。 */
    subscriptionUnit?: SubscriptionUnitRange | null
  }>(),
  { rateMultiplier: 1, platformHint: '', noPricingLabel: '', subscriptionUnit: null },
)

const { t } = useI18n()
const { isFiat, rechargeMultiplier, formatFiat } = useCurrencyDisplay()

const perMillionScale = 1_000_000
const perMillionUnit = computed(() => t('availableChannels.pricing.unitPerMillion'))
const perRequestUnit = computed(() => t('availableChannels.pricing.unitPerRequest'))

const platform = computed(() => props.model.platform || props.platformHint || '')

const isToken = computed(() => props.model.pricing?.billing_mode === BILLING_MODE_TOKEN)
const hasIntervals = computed(() => (props.model.pricing?.intervals?.length ?? 0) > 0)
const intervalUnit = computed(() => (isToken.value ? perMillionUnit.value : perRequestUnit.value))

const billingModeLabel = computed(() => {
  switch (props.model.pricing?.billing_mode) {
    case BILLING_MODE_TOKEN:
      return t('availableChannels.pricing.billingModeToken')
    case BILLING_MODE_PER_REQUEST:
      return t('availableChannels.pricing.billingModePerRequest')
    case BILLING_MODE_IMAGE:
      return t('availableChannels.pricing.billingModeImage')
    default:
      return ''
  }
})

// 倍率 != 1 且有定价时展示实付（含阶梯 / 按次 / 按图）。
const showEffective = computed(() => Math.abs(props.rateMultiplier - 1) > 1e-9 && props.model.pricing != null)

const effPerUnit = computed(() => {
  const p = props.model.pricing
  if (!p) return '-'
  const v = p.billing_mode === BILLING_MODE_IMAGE ? p.image_output_price : p.per_request_price
  return v == null ? '-' : `${formatScaled(v * props.rateMultiplier, 1)} ${perRequestUnit.value}`
})

function show(v: number | null | undefined): boolean {
  return v != null && v > 0
}

function effPerMillion(v: number | null): string {
  if (v == null) return '-'
  return `${formatScaled(v * props.rateMultiplier, perMillionScale)} ${perMillionUnit.value}`
}

interface IntervalPriceRow {
  key: string
  label: string
  value: string
}

function intervalPriceRows(iv: UserPricingInterval, mult: number): IntervalPriceRow[] {
  if (!isToken.value) {
    return show(iv.per_request_price)
      ? [{ key: 'per-request', label: t('availableChannels.pricing.perRequestPrice'), value: formatScaled(iv.per_request_price! * mult, 1) }]
      : []
  }

  const fields: Array<{ key: string; label: string; price: number | null }> = [
    { key: 'input', label: t('availableChannels.pricing.inputPrice'), price: iv.input_price },
    { key: 'output', label: t('availableChannels.pricing.outputPrice'), price: iv.output_price },
    { key: 'cache-read', label: t('availableChannels.pricing.cacheReadPrice'), price: iv.cache_read_price },
    { key: 'cache-write', label: t('availableChannels.pricing.cacheWritePrice'), price: iv.cache_write_price },
  ]

  return fields
    .filter((field) => show(field.price))
    .map((field) => ({
      key: field.key,
      label: field.label,
      value: formatScaled(field.price! * mult, perMillionScale),
    }))
}

// ---- 人民币模式 ----

interface FiatRow {
  key: string
  label: string
  price: number
}

/** 按 token 计费按每百万 token 展示，按次 / 按图按每次展示。 */
const priceScale = computed(() => (isToken.value ? perMillionScale : 1))
const fiatUnit = computed(() => (isToken.value ? perMillionUnit.value : perRequestUnit.value))
const fiatUnitPlain = computed(() => fiatUnit.value)

/** 1 美元官方价用余额要付多少人民币。 */
const balanceFactor = computed(() => props.rateMultiplier / rechargeMultiplier.value)

/**
 * 要展示的价格项。阶梯定价按第一档展示（绝大多数请求落在第一档），
 * 完整阶梯切到美元口径查看。
 */
const fiatRows = computed<FiatRow[]>(() => {
  const p = props.model.pricing
  if (!p) return []
  if (!isToken.value) {
    const v = p.billing_mode === BILLING_MODE_IMAGE ? p.image_output_price : p.per_request_price
    const label =
      p.billing_mode === BILLING_MODE_IMAGE
        ? t('availableChannels.pricing.imageOutputPrice')
        : t('availableChannels.pricing.perRequestPrice')
    return v != null && v > 0 ? [{ key: 'unit', label, price: v }] : []
  }
  const first = hasIntervals.value ? p.intervals![0] : null
  const fields: Array<{ key: string; label: string; price: number | null | undefined }> = [
    { key: 'input', label: t('availableChannels.pricing.inputPrice'), price: first?.input_price ?? p.input_price },
    { key: 'output', label: t('availableChannels.pricing.outputPrice'), price: first?.output_price ?? p.output_price },
    { key: 'cache-read', label: t('availableChannels.pricing.cacheReadPrice'), price: first?.cache_read_price ?? p.cache_read_price },
    { key: 'cache-write', label: t('availableChannels.pricing.cacheWritePrice'), price: first?.cache_write_price ?? p.cache_write_price },
  ]
  return fields
    .filter((f): f is { key: string; label: string; price: number } => show(f.price))
})

function formatFiatPrice(usdPrice: number, factor: number): string {
  return formatFiat(Number((usdPrice * priceScale.value * factor).toPrecision(10)))
}

function formatSubscriptionPrice(usdPrice: number): string {
  const range = props.subscriptionUnit
  if (!range) return '-'
  const low = formatFiatPrice(usdPrice, props.rateMultiplier * range.min)
  if (range.exact || Math.abs(range.max - range.min) < 1e-12) return low
  return `${low}–${formatFiatPrice(usdPrice, props.rateMultiplier * range.max)}`
}

const subscriptionRange = computed(() => {
  const r = props.subscriptionUnit
  return r && r.min > 0 && r.max > 0 && fiatRows.value.length > 0 ? r : null
})

function formatRate(r: number): string {
  return Number(r.toPrecision(10)).toString()
}

function formatRange(min: number, max: number | null): string {
  return `(${min}, ${max == null ? '∞' : max}]`
}
</script>
