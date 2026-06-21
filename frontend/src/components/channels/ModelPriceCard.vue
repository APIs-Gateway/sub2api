<template>
  <div
    class="flex flex-col rounded-xl border bg-white p-3 transition-shadow hover:shadow-md dark:bg-dark-800"
    :class="borderClass"
  >
    <!-- Header: 平台图标 + 模型名 -->
    <div class="mb-2 flex items-center gap-1.5">
      <PlatformIcon v-if="platform" :platform="(platform as GroupPlatform)" size="sm" />
      <span class="min-w-0 flex-1 truncate text-sm font-semibold text-gray-900 dark:text-white" :title="model.name">
        {{ model.name }}
      </span>
    </div>

    <!-- 价格 -->
    <div v-if="!model.pricing" class="text-xs text-gray-400">{{ noPricingLabel }}</div>
    <div v-else class="space-y-1 text-xs">
      <template v-if="model.pricing.billing_mode === BILLING_MODE_TOKEN">
        <PricingRow :label="t('availableChannels.pricing.inputPrice')" :value="model.pricing.input_price" :unit="perMillionUnit" :scale="perMillionScale" />
        <PricingRow :label="t('availableChannels.pricing.outputPrice')" :value="model.pricing.output_price" :unit="perMillionUnit" :scale="perMillionScale" />
        <PricingRow v-if="hasCacheRead" :label="t('availableChannels.pricing.cacheReadPrice')" :value="model.pricing.cache_read_price" :unit="perMillionUnit" :scale="perMillionScale" />
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

    <!-- 档位倍率 -->
    <div v-if="rates.length > 0" class="mt-2 flex flex-wrap items-center gap-1 border-t border-gray-100 pt-2 dark:border-dark-700/70">
      <span class="text-[10px] text-gray-400 dark:text-gray-500">{{ t('availableChannels.tierLabel') }}</span>
      <span
        v-for="r in rates"
        :key="r"
        class="rounded bg-gray-100 px-1.5 py-0.5 text-[10px] font-semibold text-gray-600 dark:bg-dark-700 dark:text-gray-300"
      >
        {{ formatRate(r) }}x
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import PricingRow from './PricingRow.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import { BILLING_MODE_TOKEN, BILLING_MODE_PER_REQUEST, BILLING_MODE_IMAGE } from '@/constants/channel'
import type { UserSupportedModel } from '@/api/channels'
import type { GroupPlatform } from '@/types'
import { platformBorderClass } from '@/utils/platformColors'

const props = withDefaults(
  defineProps<{
    model: UserSupportedModel
    /** 提供该模型的分组的去重倍率（升序）。 */
    rates?: number[]
    /** model.platform 缺失时的兜底平台（仅着色）。 */
    platformHint?: string
    noPricingLabel?: string
  }>(),
  { rates: () => [], platformHint: '', noPricingLabel: '' },
)

const { t } = useI18n()

const perMillionScale = 1_000_000
const perMillionUnit = computed(() => t('availableChannels.pricing.unitPerMillion'))
const perRequestUnit = computed(() => t('availableChannels.pricing.unitPerRequest'))

const platform = computed(() => props.model.platform || props.platformHint || '')
const borderClass = computed(() =>
  platform.value ? platformBorderClass(platform.value) : 'border-gray-200 dark:border-dark-700',
)
const hasCacheRead = computed(
  () => props.model.pricing?.cache_read_price != null && props.model.pricing.cache_read_price > 0,
)

function formatRate(r: number): string {
  return Number(r.toPrecision(10)).toString()
}
</script>
