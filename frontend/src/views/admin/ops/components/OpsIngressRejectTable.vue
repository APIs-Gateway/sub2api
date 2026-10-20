<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useMediaQuery } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import EmptyState from '@/components/common/EmptyState.vue'
import Pagination from '@/components/common/Pagination.vue'
import Select from '@/components/common/Select.vue'
import { opsAPI, type OpsIngressRejectListResponse } from '@/api/admin/ops'
import { formatDateTime } from '../utils/opsFormatters'
import { formatNumber } from '@/utils/format'

const props = withDefaults(defineProps<{ refreshToken?: number }>(), { refreshToken: 0 })
const { t } = useI18n()
const isDesktopViewport = useMediaQuery('(min-width: 768px)')

type TimeRange = '5m' | '30m' | '1h' | '6h' | '24h' | '7d' | '30d'

const loading = ref(false)
const errorMessage = ref('')
const response = ref<OpsIngressRejectListResponse | null>(null)
const timeRange = ref<TimeRange>('1h')
const reason = ref('')
const routeFamily = ref('')
const protocol = ref('')
const page = ref(1)
const pageSize = ref(20)

const items = computed(() => response.value?.items ?? [])
const total = computed(() => response.value?.total ?? 0)
const timeRangeOptions = computed(() => ['5m', '30m', '1h', '6h', '24h', '7d', '30d'].map(value => ({
  value,
  label: t(`admin.ops.timeRange.${value}`)
})))
const reasonOptions = computed(() => [
  '', 'query_api_key_deprecated', 'api_key_required', 'invalid_api_key', 'invalid_auth_rate_limited',
  'api_key_auth_overloaded', 'api_key_disabled', 'ip_restricted', 'user_inactive', 'group_deleted',
  'group_disabled', 'group_not_allowed', 'group_unassigned', 'other'
].map(value => ({ value, label: value ? value : t('admin.ops.ingressRejects.allReasons') })))
const routeFamilyOptions = computed(() => [
  '', 'antigravity', 'gemini', 'codex', 'messages', 'responses', 'chat_completions', 'images', 'videos',
  'embeddings', 'models', 'usage', 'billing', 'alpha_search', 'antigravity_gemini', 'antigravity_models', 'other'
].map(value => ({ value, label: value ? value : t('admin.ops.ingressRejects.allRouteFamilies') })))
const protocolOptions = computed(() => ['google', 'anthropic', 'openai', 'gateway', 'http', 'ws', 'other'].map(value => ({
  value,
  label: value
})).concat({ value: '', label: t('admin.ops.ingressRejects.allProtocols') }))

function buildParams() {
  return {
    time_range: timeRange.value,
    reason: reason.value || undefined,
    route_family: routeFamily.value || undefined,
    protocol: protocol.value || undefined,
    page: page.value,
    page_size: pageSize.value
  }
}

async function loadData() {
  loading.value = true
  errorMessage.value = ''
  try {
    const next = await opsAPI.listIngressRejects(buildParams())
    const totalPages = Math.max(1, Math.ceil(next.total / Math.max(1, next.page_size)))
    response.value = next
    if (next.total > 0 && page.value > totalPages) {
      page.value = totalPages
      return
    }
  } catch (err: any) {
    response.value = null
    errorMessage.value = err?.message || t('admin.ops.ingressRejects.failedToLoad')
  } finally {
    loading.value = false
  }
}

watch(
  () => [timeRange.value, reason.value, routeFamily.value, protocol.value, page.value, pageSize.value, props.refreshToken] as const,
  (next, previous) => {
    const queryChanged = !!previous && (
      next.slice(0, 4).some((value, index) => value !== previous[index]) ||
      next[5] !== previous[5]
    )
    if (queryChanged && page.value !== 1) {
      page.value = 1
      return
    }
    void loadData()
  },
  { immediate: true }
)
</script>

<template>
  <section class="card p-4 md:p-5">
    <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
      <div>
        <h3 class="text-sm font-bold text-gray-900 dark:text-white">{{ t('admin.ops.ingressRejects.title') }}</h3>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.ingressRejects.description') }}</p>
      </div>
      <div class="flex flex-wrap gap-2">
        <div class="w-32"><Select v-model="timeRange" :options="timeRangeOptions" /></div>
        <div class="w-44"><Select v-model="reason" :options="reasonOptions" /></div>
        <div class="w-40"><Select v-model="routeFamily" :options="routeFamilyOptions" /></div>
        <div class="w-32"><Select v-model="protocol" :options="protocolOptions" /></div>
      </div>
    </div>

    <div v-if="errorMessage" class="mb-4 rounded-lg bg-red-50 px-3 py-2 text-xs text-red-600 dark:bg-red-900/20 dark:text-red-400">
      {{ errorMessage }}
    </div>
    <div v-if="loading" class="py-8 text-center text-sm text-gray-500 dark:text-gray-400">{{ t('admin.ops.loadingText') }}</div>
    <EmptyState
      v-else-if="items.length === 0"
      :title="t('common.noData')"
      :description="t('admin.ops.ingressRejects.empty')"
    />
    <div v-else>
      <div class="overflow-hidden rounded-xl border border-gray-200 dark:border-dark-700">
        <div class="max-h-[420px] overflow-auto">
          <div v-if="!isDesktopViewport" class="divide-y divide-gray-100 dark:divide-dark-800">
            <div v-for="row in items" :key="row.id" class="space-y-2 p-3 text-xs">
              <div class="flex items-center justify-between gap-2">
                <span class="font-medium text-gray-900 dark:text-gray-100">{{ row.reject_reason }}</span>
                <span class="font-semibold text-gray-700 dark:text-gray-200">{{ formatNumber(row.request_count) }}</span>
              </div>
              <div class="grid grid-cols-2 gap-x-3 gap-y-1 text-gray-500 dark:text-gray-400">
                <span>{{ t('admin.ops.ingressRejects.table.routeFamily') }}: {{ row.route_family }}</span>
                <span>{{ t('admin.ops.ingressRejects.table.protocol') }}: {{ row.protocol }}</span>
                <span>{{ t('admin.ops.ingressRejects.table.clientNetwork') }}: {{ row.client_ip }}</span>
                <span>{{ formatDateTime(row.bucket_start) }}</span>
              </div>
            </div>
          </div>
          <table v-else class="min-w-full text-left text-xs md:text-sm">
            <thead class="sticky top-0 z-10 bg-white dark:bg-dark-800">
              <tr class="border-b border-gray-200 text-gray-500 dark:border-dark-700 dark:text-gray-400">
                <th class="px-2 py-2 font-semibold">{{ t('admin.ops.ingressRejects.table.bucket') }}</th>
                <th class="px-2 py-2 font-semibold">{{ t('admin.ops.ingressRejects.table.reason') }}</th>
                <th class="px-2 py-2 font-semibold">{{ t('admin.ops.ingressRejects.table.routeFamily') }}</th>
                <th class="px-2 py-2 font-semibold">{{ t('admin.ops.ingressRejects.table.protocol') }}</th>
                <th class="px-2 py-2 font-semibold">{{ t('admin.ops.ingressRejects.table.clientNetwork') }}</th>
                <th class="px-2 py-2 text-right font-semibold">{{ t('admin.ops.ingressRejects.table.requests') }}</th>
                <th class="px-2 py-2 font-semibold">{{ t('admin.ops.ingressRejects.table.lastSeen') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="row in items" :key="row.id" class="border-b border-gray-100 text-gray-700 last:border-b-0 dark:border-dark-800 dark:text-gray-200">
                <td class="whitespace-nowrap px-2 py-2">{{ formatDateTime(row.bucket_start) }}</td>
                <td class="px-2 py-2 font-medium">{{ row.reject_reason }}</td>
                <td class="px-2 py-2">{{ row.route_family }}</td>
                <td class="px-2 py-2">{{ row.protocol }}</td>
                <td class="px-2 py-2 font-mono">{{ row.client_ip }}</td>
                <td class="px-2 py-2 text-right">{{ formatNumber(row.request_count) }}</td>
                <td class="whitespace-nowrap px-2 py-2">{{ formatDateTime(row.last_seen) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
    <Pagination v-if="!loading && total > pageSize" v-model:page="page" v-model:page-size="pageSize" :total="total" />
  </section>
</template>
