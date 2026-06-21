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
        <p class="text-xs leading-relaxed text-gray-600 dark:text-gray-300">{{ t('billingRules.intro') }}</p>

        <div
          class="flex flex-wrap items-center gap-x-2 gap-y-1 rounded-lg bg-white px-3 py-2 dark:bg-dark-900/40"
        >
          <span class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('billingRules.formulaLabel') }}</span>
          <span class="text-xs font-semibold text-gray-800 dark:text-gray-200">= {{ t('billingRules.formula') }}</span>
        </div>

        <ul class="space-y-1 text-xs leading-relaxed text-gray-600 dark:text-gray-300">
          <li class="flex gap-1.5">
            <span class="select-none text-gray-400 dark:text-gray-500">•</span>
            <span>{{ modelsBelow ? t('billingRules.modelPriceHere') : t('billingRules.modelPriceLink') }}</span>
          </li>
          <li class="flex gap-1.5">
            <span class="select-none text-gray-400 dark:text-gray-500">•</span>
            <span>{{ t('billingRules.rate') }}</span>
          </li>
          <li class="flex gap-1.5">
            <span class="select-none text-gray-400 dark:text-gray-500">•</span>
            <span>{{ t('billingRules.plan') }}</span>
          </li>
        </ul>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'

withDefaults(defineProps<{ modelsBelow?: boolean }>(), { modelsBelow: false })

const { t } = useI18n()
</script>
