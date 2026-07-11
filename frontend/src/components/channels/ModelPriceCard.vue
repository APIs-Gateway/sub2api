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

const props = withDefaults(
  defineProps<{
    model: UserSupportedModel
    /** 当前所选分组倍率：实付 = 官方单价 × 倍率（含阶梯逐档）。 */
    rateMultiplier?: number
    platformHint?: string
    noPricingLabel?: string
  }>(),
  { rateMultiplier: 1, platformHint: '', noPricingLabel: '' },
)

const { t } = useI18n()

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

function formatRate(r: number): string {
  return Number(r.toPrecision(10)).toString()
}

function formatRange(min: number, max: number | null): string {
  return `(${min}, ${max == null ? '∞' : max}]`
}
</script>
