<template>
  <div class="grid grid-cols-2 gap-4 lg:grid-cols-4">
    <!-- Today Revenue -->
    <div class="card p-5">
      <div class="mb-3 flex items-start justify-between">
        <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('payment.admin.todayRevenue') }}</p>
        <Icon name="dollar" size="sm" class="text-gray-300 dark:text-dark-600" :stroke-width="1.5" />
      </div>
      <p class="text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">${{ formatMoney(stats.today_amount) }}</p>
      <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
        {{ stats.today_count }} {{ t('payment.admin.orders') }}
      </p>
    </div>

    <!-- Total Revenue -->
    <div class="card p-5">
      <div class="mb-3 flex items-start justify-between">
        <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('payment.admin.totalRevenue') }}</p>
        <Icon name="creditCard" size="sm" class="text-gray-300 dark:text-dark-600" :stroke-width="1.5" />
      </div>
      <p class="text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">${{ formatMoney(stats.total_amount) }}</p>
      <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
        {{ stats.total_count }} {{ t('payment.admin.orders') }}
      </p>
    </div>

    <!-- Today Orders -->
    <div class="card p-5">
      <div class="mb-3 flex items-start justify-between">
        <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('payment.admin.todayOrders') }}</p>
        <Icon name="chart" size="sm" class="text-gray-300 dark:text-dark-600" :stroke-width="1.5" />
      </div>
      <p class="text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">{{ stats.today_count }}</p>
    </div>

    <!-- Average Amount -->
    <div class="card p-5">
      <div class="mb-3 flex items-start justify-between">
        <p class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('payment.admin.avgAmount') }}</p>
        <Icon name="chart" size="sm" class="text-gray-300 dark:text-dark-600" :stroke-width="1.5" />
      </div>
      <p class="text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">${{ formatMoney(stats.avg_amount) }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { DashboardStats } from '@/types/payment'

const { t } = useI18n()

defineProps<{
  stats: DashboardStats
}>()

function formatMoney(value: number): string {
  return value.toFixed(2)
}
</script>
