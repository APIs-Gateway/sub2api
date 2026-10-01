<template>
  <div
    class="rounded-xl border border-gray-200 bg-gray-50 p-4 dark:border-dark-700 dark:bg-dark-800/50"
  >
    <div class="flex items-start gap-3">
      <div
        class="mt-0.5 flex h-7 w-7 shrink-0 items-center justify-center rounded-lg bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-400"
      >
        <svg class="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2">
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            d="M9 8h6M9 12h6m-6 4h3M6 3h12a1 1 0 011 1v17l-3.5-2-3.5 2-3.5-2L5 21V4a1 1 0 011-1z"
          />
        </svg>
      </div>

      <div class="min-w-0 flex-1 space-y-2">
        <h3 class="text-sm font-bold text-gray-900 dark:text-white">{{ t('billingRules.title') }}</h3>

        <template v-if="isFiat">
          <p class="text-xs leading-relaxed text-gray-600 dark:text-gray-300">{{ t('billingRules.fiat.intro') }}</p>
          <ul class="space-y-1 text-xs leading-relaxed text-gray-600 dark:text-gray-300">
            <li v-for="text in fiatBullets" :key="text" class="flex gap-1.5">
              <span class="select-none text-gray-400 dark:text-gray-500">•</span>
              <span>{{ text }}</span>
            </li>
          </ul>
        </template>

        <template v-else>
          <p class="text-xs leading-relaxed text-gray-600 dark:text-gray-300">{{ t('billingRules.intro') }}</p>
          <ul class="space-y-1 text-xs leading-relaxed text-gray-600 dark:text-gray-300">
            <li v-for="text in usdBullets" :key="text" class="flex gap-1.5">
              <span class="select-none text-gray-400 dark:text-gray-500">•</span>
              <span>{{ text }}</span>
            </li>
          </ul>
        </template>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import { useCurrencyDisplay } from '@/composables/useCurrencyDisplay'

const props = withDefaults(defineProps<{ modelsBelow?: boolean }>(), { modelsBelow: false })

const { t } = useI18n()
const { isFiat } = useCurrencyDisplay()

const fiatBullets = computed(() => [
  ...(props.modelsBelow ? [] : [t('billingRules.fiat.pricesLink')]),
  t('billingRules.fiat.balance'),
  t('billingRules.fiat.plan'),
  t('billingRules.fiat.group'),
])
const usdBullets = computed(() => [
  props.modelsBelow ? t('billingRules.modelPriceHere') : t('billingRules.modelPriceLink'),
  t('billingRules.rate'),
  t('billingRules.plan'),
])
</script>
