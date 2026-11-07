<template>
  <span class="num font-medium" :class="dir === 'up' || dir === 'mixed' ? 'text-primary-700 dark:text-primary-300' : 'text-gray-700 dark:text-gray-300'">
    <template v-if="missing">{{ t('admin.pricingOps.snapshots.trend.missing') }}</template>
    <template v-else-if="dir === 'flat'">{{ t('admin.pricingOps.snapshots.trend.flat') }}</template>
    <template v-else>
      {{ t(`admin.pricingOps.snapshots.trend.${dir}`) }}
      <template v-if="percent !== null && percent !== undefined">{{ Math.abs(percent).toFixed(0) }}%</template>
    </template>
  </span>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { Direction } from '../snapshotModel'

withDefaults(defineProps<{ dir: Direction; percent?: number | null; missing?: boolean }>(), { percent: null, missing: false })
const { t } = useI18n()
</script>
