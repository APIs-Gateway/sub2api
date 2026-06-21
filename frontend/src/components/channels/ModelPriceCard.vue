<template>
  <div
    class="flex flex-col rounded-xl border bg-white p-4 transition-shadow hover:shadow-md dark:bg-dark-800"
    :class="borderClass"
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
        <template v-if="model.pricing.billing_mode === BILLING_MODE_TOKEN">
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

      <!-- 阶梯定价 -->
      <div
        v-if="model.pricing.intervals && model.pricing.intervals.length > 0"
        class="mt-2 border-t border-gray-100 pt-2 dark:border-dark-700/70"
      >
        <div class="mb-1 text-[11px] font-medium text-gray-500 dark:text-gray-400">{{ t('availableChannels.pricing.intervals') }}</div>
        <div class="space-y-0.5">
          <div v-for="(iv, idx) in model.pricing.intervals" :key="idx" class="flex justify-between text-[11px] text-gray-600 dark:text-gray-300">
            <span class="text-gray-400">{{ iv.tier_label || formatRange(iv.min_tokens, iv.max_tokens) }}</span>
            <span class="font-mono">{{ formatInterval(iv) }}</span>
          </div>
        </div>
      </div>

      <!-- 本档位实付（× 倍率） -->
      <div
        v-if="showEffective"
        class="mt-3 rounded-lg bg-primary-50 px-2.5 py-1.5 dark:bg-primary-500/10"
      >
        <div class="mb-0.5 text-[10px] font-medium text-primary-600 dark:text-primary-300">
          {{ t('availableChannels.effectiveTitle', { rate: formatRate(rateMultiplier) }) }}
        </div>
        <div class="flex flex-wrap gap-x-3 gap-y-0.5 text-[11px] text-primary-700 dark:text-primary-300">
          <span>{{ t('availableChannels.pricing.inputPrice') }} {{ effDisplay(model.pricing.input_price) }}</span>
          <span>{{ t('availableChannels.pricing.outputPrice') }} {{ effDisplay(model.pricing.output_price) }}</span>
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
import { platformBorderClass } from '@/utils/platformColors'

const props = withDefaults(
  defineProps<{
    model: UserSupportedModel
    /** 当前所选分组的倍率：用于展示「本档位实付」= 官方单价 × 倍率。 */
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
const borderClass = computed(() =>
  platform.value ? platformBorderClass(platform.value) : 'border-gray-200 dark:border-dark-700',
)

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

// 仅当 token 计费、倍率 != 1、且有输入或输出价时展示「本档位实付」。
const showEffective = computed(
  () =>
    props.model.pricing?.billing_mode === BILLING_MODE_TOKEN &&
    Math.abs(props.rateMultiplier - 1) > 1e-9 &&
    (props.model.pricing.input_price != null || props.model.pricing.output_price != null),
)

function show(v: number | null | undefined): boolean {
  return v != null && v > 0
}

function effDisplay(v: number | null): string {
  if (v == null) return '-'
  return `${formatScaled(v * props.rateMultiplier, perMillionScale)} ${perMillionUnit.value}`
}

function formatRate(r: number): string {
  return Number(r.toPrecision(10)).toString()
}

function formatRange(min: number, max: number | null): string {
  return `(${min}, ${max == null ? '∞' : max}]`
}

function formatInterval(iv: UserPricingInterval): string {
  if (props.model.pricing?.billing_mode === BILLING_MODE_TOKEN) {
    return `${formatScaled(iv.input_price, perMillionScale)} / ${formatScaled(iv.output_price, perMillionScale)}`
  }
  return formatScaled(iv.per_request_price, 1)
}
</script>
