<template>
  <span class="whitespace-nowrap text-gray-700 dark:text-gray-300" data-test="plan-rate">
    {{ t('groups.planRatePrefix') }}
    <template v-if="rateView.planStruck !== undefined">
      <!-- 专属倍率：默认的套餐低至划掉，专属的高亮 -->
      <span class="mr-0.5 line-through opacity-50">{{ rateView.planStruck }}x</span>
      <span class="font-bold">{{ rateView.plan }}x</span>
    </template>
    <template v-else>{{ rateView.plan }}x</template>
  </span>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'

import type { RateView } from '@/utils/rateDisplay'

/**
 * 「套餐低至 0.056x」。只在 rateView.plan 存在时使用（人民币模式、套餐可买、取到了套餐定价），
 * 字号和对齐由使用处通过 class 决定。文字用 gray-700 / gray-300，保证在徽标和药丸底色上对比度够。
 */
defineProps<{ rateView: Pick<RateView, 'plan' | 'planStruck'> }>()

const { t } = useI18n()
</script>
