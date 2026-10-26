<template>
  <div class="space-y-6">
    <!-- Date Range Filter -->
    <div class="card p-4">
      <div class="flex flex-wrap items-center gap-4">
        <div class="flex items-center gap-2">
          <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('dashboard.timeRange') }}:</span>
          <DateRangePicker :start-date="startDate" :end-date="endDate" @update:startDate="$emit('update:startDate', $event)" @update:endDate="$emit('update:endDate', $event)" @change="$emit('dateRangeChange', $event)" />
        </div>
        <button @click="$emit('refresh')" :disabled="loading" class="btn btn-secondary">
          {{ t('common.refresh') }}
        </button>
        <div class="ml-auto flex items-center gap-2">
          <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('dashboard.granularity') }}:</span>
          <div class="w-28">
            <Select :model-value="granularity" :options="[{value:'day', label:t('dashboard.day')}, {value:'hour', label:t('dashboard.hour')}]" @update:model-value="$emit('update:granularity', $event)" @change="$emit('granularityChange')" />
          </div>
        </div>
      </div>
    </div>

    <!-- Charts Grid -->
    <div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
      <!-- Model Distribution Chart -->
      <div class="card relative overflow-hidden p-4">
        <div v-if="loading" class="absolute inset-0 z-10 flex items-center justify-center bg-white/50 backdrop-blur-sm dark:bg-dark-800/50">
          <LoadingSpinner size="md" />
        </div>
        <h3 class="mb-4 text-sm font-semibold text-gray-900 dark:text-white">{{ t('dashboard.modelDistribution') }}</h3>
        <!-- 官方价列出现时（美元口径）表格有 5 列，横排放不下，环形图改到表格上方 -->
        <div :class="['flex flex-col items-center gap-4', showOfficial ? '' : 'sm:flex-row']">
          <div class="h-36 w-36 shrink-0">
            <Doughnut v-if="modelData" :data="modelData" :options="doughnutOptions" />
            <div v-else class="flex h-full items-center justify-center text-sm text-gray-500 dark:text-gray-400">{{ t('dashboard.noDataAvailable') }}</div>
          </div>
          <div class="max-h-48 w-full min-w-0 flex-1 overflow-auto">
            <table class="w-full text-xs">
              <thead>
                <tr class="text-gray-500 dark:text-gray-400">
                  <th class="pb-2 text-left">{{ t('dashboard.model') }}</th>
                  <th class="whitespace-nowrap pb-2 pl-2.5 text-right">{{ t('dashboard.requests') }}</th>
                  <th class="whitespace-nowrap pb-2 pl-2.5 text-right">{{ t('dashboard.tokens') }}</th>
                  <th class="whitespace-nowrap pb-2 pl-2.5 text-right">{{ t('dashboard.actual') }}</th>
                  <th v-if="showOfficial" class="whitespace-nowrap pb-2 pl-2.5 text-right">{{ t('dashboard.standard') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="model in models" :key="model.model" class="border-t border-gray-100 dark:border-gray-700">
                  <td class="max-w-[84px] truncate py-1.5 font-medium text-gray-900 dark:text-white" :title="model.model">{{ model.model }}</td>
                  <td class="num-secondary whitespace-nowrap py-1.5 pl-2.5 text-right text-gray-600 dark:text-gray-400">{{ formatNumber(model.requests) }}</td>
                  <td class="num-secondary whitespace-nowrap py-1.5 pl-2.5 text-right text-gray-600 dark:text-gray-400" :title="formatNumber(model.total_tokens)">{{ formatTokens(model.total_tokens) }}</td>
                  <td class="num-secondary whitespace-nowrap py-1.5 pl-2.5 text-right text-green-600 dark:text-green-400" :title="formatMixed(model.actual_cost, model.actual_cost_fiat, EXACT_DIGITS)">{{ formatMixed(model.actual_cost, model.actual_cost_fiat) }}</td>
                  <td v-if="showOfficial" class="num-secondary whitespace-nowrap py-1.5 pl-2.5 text-right font-normal text-gray-400 dark:text-gray-500" :title="formatStandard(model.cost, EXACT_DIGITS)">{{ formatStandard(model.cost) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>

      <!-- Token Usage Trend Chart -->
      <TokenUsageTrend :trend-data="trend" :loading="loading" unified-typography :format-actual-cost="formatTrendActualCost" :format-standard-cost="formatTrendStandardCost" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import Select from '@/components/common/Select.vue'
import { Doughnut } from 'vue-chartjs'
import TokenUsageTrend from '@/components/charts/TokenUsageTrend.vue'
import { EXACT_DIGITS, type MoneyDigits, useCurrencyDisplay } from '@/composables/useCurrencyDisplay'
import type { TrendDataPoint, ModelStat } from '@/types'
import { formatCount as formatNumber, formatCompactCount } from '@/utils/numberFormat'
import { CHART_FONT_FAMILY } from '@/utils/chartTypography'
import { Chart as ChartJS, CategoryScale, LinearScale, PointElement, LineElement, ArcElement, Title, Tooltip, Legend, Filler } from 'chart.js'
ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, ArcElement, Title, Tooltip, Legend, Filler)

const props = defineProps<{ loading: boolean, startDate: string, endDate: string, granularity: string, trend: TrendDataPoint[], models: ModelStat[] }>()
defineEmits(['update:startDate', 'update:endDate', 'update:granularity', 'dateRangeChange', 'granularityChange', 'refresh'])
const { t } = useI18n()
// 实扣列用服务端分桶折算的人民币；官方价（standard）列在人民币模式下按官方价汇率换成 ¥，
// 后端没提供汇率时整列隐藏，不混排 $。
const { formatMixed, isFiat, officialCnyRate, formatOfficial } = useCurrencyDisplay()
const showOfficial = computed(() => !isFiat.value || officialCnyRate.value > 0)
const formatStandard = (usd: number, digits?: MoneyDigits) => formatOfficial(usd, digits) ?? ''
const formatTrendStandardCost = (point: TrendDataPoint) => (showOfficial.value ? formatStandard(point.cost) : null)
const formatTrendActualCost = (point: TrendDataPoint) => formatMixed(point.actual_cost, point.actual_cost_fiat)

const modelData = computed(() => !props.models?.length ? null : {
  labels: props.models.map((m: ModelStat) => m.model),
  datasets: [{
    data: props.models.map((m: ModelStat) => m.total_tokens),
    backgroundColor: ['#3b82f6', '#10b981', '#f59e0b', '#ef4444', '#8b5cf6', '#ec4899', '#06b6d4', '#84cc16']
  }]
})

const formatTokens = (value: number) => formatCompactCount(value, { allowBillions: false })

const doughnutOptions = {
  font: { family: CHART_FONT_FAMILY },
  responsive: true,
  maintainAspectRatio: false,
  plugins: {
    legend: { display: false },
    tooltip: {
      callbacks: {
        label: (context: any) => `${context.label}: ${formatTokens(context.parsed)} tokens`
      }
    }
  }
}
</script>
