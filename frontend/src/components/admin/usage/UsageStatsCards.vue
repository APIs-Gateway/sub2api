<template>
  <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
    <div class="card p-5">
      <div class="mb-3 flex items-start justify-between">
        <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('usage.totalRequests') }}</p>
        <Icon name="document" size="sm" class="text-gray-300 dark:text-dark-600" :stroke-width="1.5" />
      </div>
      <p class="text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">{{ stats?.total_requests?.toLocaleString() || '0' }}</p>
      <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('usage.inSelectedRange') }}</p>
    </div>
    <div class="card p-5">
      <div class="mb-3 flex items-start justify-between">
        <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('usage.totalTokens') }}</p>
        <svg class="h-4 w-4 text-gray-300 dark:text-dark-600" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="m21 7.5-9-5.25L3 7.5m18 0-9 5.25m9-5.25v9l-9 5.25M3 7.5l9 5.25M3 7.5v9l9 5.25m0-9v9" /></svg>
      </div>
      <p class="text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">{{ formatTokens(stats?.total_tokens || 0) }}</p>
      <p class="mt-1 flex flex-wrap items-center gap-x-1 text-xs text-gray-500 dark:text-gray-400">
        <span>{{ t('usage.in') }}: {{ formatTokens(stats?.total_input_tokens || 0) }}</span>
        <span>/</span>
        <span>{{ t('usage.out') }}: {{ formatTokens(stats?.total_output_tokens || 0) }}</span>
        <span>/</span>
        <span class="group relative inline-flex cursor-help items-center gap-0.5" tabindex="0">
          <span>{{ t('usage.cacheTotal') }}: {{ formatTokens(stats?.total_cache_tokens || 0) }}</span>
          <Icon name="infoCircle" size="xs" class="text-gray-400" :stroke-width="2" />
          <span
            class="pointer-events-none absolute left-1/2 top-full z-30 mt-2 w-56 -translate-x-1/2 rounded-lg border border-gray-200 bg-white p-3 text-left text-xs text-gray-700 opacity-0 shadow-lg transition-opacity duration-150 group-hover:opacity-100 group-focus:opacity-100 dark:border-dark-600 dark:bg-dark-800 dark:text-dark-200"
          >
            <span class="mb-2 block font-medium text-gray-900 dark:text-white">
              {{ t('usage.cacheBreakdown') }}
            </span>
            <span class="flex items-center justify-between gap-3">
              <span>{{ t('usage.cacheCreationTokensLabel') }}</span>
              <span class="tabular-nums">
                {{ formatTokens(stats?.total_cache_creation_tokens || 0) }}
              </span>
            </span>
            <span class="mt-1 flex items-center justify-between gap-3">
              <span>{{ t('usage.cacheReadTokensLabel') }}</span>
              <span class="tabular-nums">
                {{ formatTokens(stats?.total_cache_read_tokens || 0) }}
              </span>
            </span>
          </span>
        </span>
      </p>
    </div>
    <div class="card p-5">
      <div class="mb-3 flex items-start justify-between">
        <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('usage.totalCost') }}</p>
        <Icon name="dollar" size="sm" class="text-gray-300 dark:text-dark-600" :stroke-width="1.5" />
      </div>
      <p class="text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">${{ (stats?.total_actual_cost || 0).toFixed(4) }}</p>
      <p class="mt-1 text-xs text-gray-400 dark:text-gray-500">
        <span class="text-orange-500">{{ t('usage.accountCost') }} ${{ (stats?.total_account_cost || 0).toFixed(4) }}</span>
        <span> · </span>
        <span>{{ t('usage.standardCost') }} ${{ (stats?.total_cost || 0).toFixed(4) }}</span>
      </p>
    </div>
    <div class="card p-5">
      <div class="mb-3 flex items-start justify-between">
        <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('usage.avgDuration') }}</p>
        <Icon name="clock" size="sm" class="text-gray-300 dark:text-dark-600" :stroke-width="1.5" />
      </div>
      <p class="text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">{{ formatDuration(stats?.average_duration_ms || 0) }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { AdminUsageStatsResponse } from '@/api/admin/usage'
import Icon from '@/components/icons/Icon.vue'

defineProps<{ stats: AdminUsageStatsResponse | null }>()

const { t } = useI18n()

const formatDuration = (ms: number) =>
  ms < 1000 ? `${ms.toFixed(0)}ms` : `${(ms / 1000).toFixed(2)}s`

const formatTokens = (value: number) => {
  if (value >= 1e9) return (value / 1e9).toFixed(2) + 'B'
  if (value >= 1e6) return (value / 1e6).toFixed(2) + 'M'
  if (value >= 1e3) return (value / 1e3).toFixed(2) + 'K'
  return value.toLocaleString()
}
</script>
