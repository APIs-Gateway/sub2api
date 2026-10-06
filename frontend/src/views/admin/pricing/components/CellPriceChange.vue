<template>
  <div class="price-change num">
    <span v-if="!before && !after" class="text-gray-400 dark:text-dark-400">—</span>
    <template v-else-if="same">
      <div class="text-gray-900 dark:text-gray-100">{{ fmt(after)?.main }}</div>
      <div v-if="fmt(after)?.sub" class="sub">{{ fmt(after)?.sub }}</div>
    </template>
    <template v-else>
      <div class="flex items-center gap-1.5 text-gray-900 dark:text-gray-100">
        <span v-if="before" class="text-gray-400 line-through decoration-gray-300 dark:text-dark-400 dark:decoration-dark-500">{{ fmt(before)?.main }}</span>
        <span v-if="before" class="text-gray-400 dark:text-dark-400" aria-hidden="true">→</span>
        <span class="font-medium" :class="{ 'text-primary-700 dark:text-primary-300': up }">{{ after ? fmt(after)?.main : '—' }}</span>
      </div>
      <div v-if="fmt(after)?.sub" class="sub">{{ before ? fmt(before)?.sub + ' → ' : '' }}{{ fmt(after)?.sub }}</div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { usePricingFormat } from '../usePricingFormat'

const props = defineProps<{ before: number | null; after: number | null }>()
const { paid } = usePricingFormat()

const same = computed(() => props.before === props.after)
const up = computed(() => props.before !== null && props.after !== null && props.after > props.before)

function fmt(v: number | null) {
  return v === null ? null : paid(v)
}
</script>

<style scoped>
.price-change {
  @apply whitespace-nowrap text-sm leading-5;
}
.price-change .sub {
  @apply text-[11px] leading-4 text-gray-500 dark:text-dark-300;
}
</style>
