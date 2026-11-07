<template>
  <span class="inline-flex flex-col leading-snug">
    <span v-if="changed" class="text-xs text-gray-400 line-through dark:text-dark-400" data-test="price-before">{{ text(before) }}</span>
    <span class="font-medium text-gray-900 dark:text-white" data-test="price-after">
      {{ text(after) }}
      <span v-if="subText" class="ml-1 text-xs font-normal text-gray-500 dark:text-dark-300">{{ subText }}</span>
    </span>
  </span>
</template>

<script setup lang="ts">
/** 官方价的前后对比：每百万 Token，¥ 为主、$ 为小字（沿用价格配置页的金额格式）。 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { usePricingFormat } from '../usePricingFormat'

const props = defineProps<{ before: number | null; after: number | null }>()
const { t } = useI18n()
const { official } = usePricingFormat()

const changed = computed(() => props.before !== props.after)

function text(v: number | null): string {
  return v === null ? t('admin.pricingOps.snapshots.noPrice') : official(v).main
}

const subText = computed(() => (props.after === null ? null : official(props.after).sub))
</script>
