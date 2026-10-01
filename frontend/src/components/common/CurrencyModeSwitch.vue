<template>
  <!--
    计价单位切换：¥ = 实际付出的人民币，$ = 官方价口径的额度。
    倍率为 1（free 站）时两者相同，整个组件不渲染。
  -->
  <div
    v-if="canSwitch"
    class="flex items-center gap-0.5 rounded-md bg-gray-100 p-0.5 dark:bg-dark-700"
    role="group"
    :aria-label="t('usage.currencySwitchLabel')"
    :title="t('usage.currencySwitchLabel')"
  >
    <button
      v-for="opt in options"
      :key="opt.value"
      type="button"
      class="rounded font-medium transition-colors"
      :class="[
        size === 'sm' ? 'px-1.5 py-0.5 text-[10px]' : 'px-2 py-1 text-xs',
        mode === opt.value
          ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-600 dark:text-white'
          : 'text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200'
      ]"
      :aria-pressed="mode === opt.value"
      @click="setMode(opt.value)"
    >
      {{ opt.label }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import { useCurrencyDisplay } from '@/composables/useCurrencyDisplay'

withDefaults(defineProps<{ size?: 'sm' | 'md' }>(), { size: 'md' })

const { t } = useI18n()
const { canSwitch, mode, setMode } = useCurrencyDisplay()

const options = computed(() => [
  { value: 'fiat' as const, label: t('usage.currencyFiat') },
  { value: 'usd' as const, label: t('usage.currencyUsd') }
])
</script>
