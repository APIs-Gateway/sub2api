<template>
  <AppLayout>
    <div class="space-y-6">
      <div v-if="props.showSettings" class="card p-6">
        <div class="flex flex-col gap-4 lg:flex-row lg:items-end lg:justify-between">
          <div class="grid gap-4 sm:grid-cols-2 lg:flex lg:items-end">
            <label class="flex items-center gap-3 rounded-lg border border-gray-200 px-3 py-2 dark:border-dark-700">
              <input v-model="form.enabled" type="checkbox" class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
              <span class="text-sm font-medium text-gray-800 dark:text-gray-200">{{ t('admin.affiliates.cashback.enabled') }}</span>
            </label>
            <div>
              <label class="input-label">{{ t('admin.affiliates.cashback.rate') }}</label>
              <input v-model.number="form.rate_percent" type="number" min="0" max="100" step="0.01" class="input w-full sm:w-40" />
            </div>
          </div>
          <button class="btn btn-primary" :disabled="saving" @click="saveSettings">
            <Icon v-if="saving" name="refresh" size="sm" class="animate-spin" />
            <span>{{ saving ? t('common.saving') : t('common.save') }}</span>
          </button>
        </div>

        <div class="mt-6">
          <div class="mb-3 flex items-center justify-between">
            <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.affiliates.cashback.faceValues') }}</h3>
          </div>
          <p class="mb-4 text-sm text-gray-500 dark:text-dark-400">
            {{ t('admin.affiliates.cashback.balanceHint') }}
          </p>
          <div class="space-y-3">
            <div v-for="(row, index) in form.subscription_mappings" :key="`${row.group_id}-${row.validity_days}-${index}`" class="grid gap-3 rounded-lg border border-gray-200 p-3 dark:border-dark-700 sm:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
              <div>
                <label class="input-label">{{ t('admin.affiliates.cashback.subscriptionItem') }}</label>
                <div class="rounded-lg border border-gray-200 bg-gray-50 px-3 py-2 text-sm text-gray-800 dark:border-dark-700 dark:bg-dark-900 dark:text-gray-200">
                  <div class="font-medium">{{ row.display_name }}</div>
                  <div class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ row.platform }} · #{{ row.group_id }}</div>
                </div>
              </div>
              <div>
                <label class="input-label">{{ t('admin.affiliates.cashback.baseAmount') }}</label>
                <input v-model.number="row.cashback_base_amount" type="number" min="0" step="0.00000001" class="input" />
              </div>
            </div>
            <div v-if="form.subscription_mappings.length === 0" class="rounded-lg border border-dashed border-gray-300 p-6 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-dark-400">
              {{ t('admin.affiliates.cashback.noFaceValues') }}
            </div>
          </div>
        </div>
      </div>

      <div class="card p-6">
        <div class="mb-4 flex flex-wrap items-center gap-3">
          <div class="relative w-full md:w-80">
            <Icon name="search" size="md" class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
            <input v-model="filters.search" type="text" class="input pl-10" :placeholder="t('admin.affiliates.records.searchPlaceholder')" @input="debounceLoad" />
          </div>
          <button class="btn btn-secondary px-2 md:px-3" :disabled="loadingRecords" @click="loadRecords">
            <Icon name="refresh" size="md" :class="loadingRecords ? 'animate-spin' : ''" />
          </button>
        </div>
        <DataTable :columns="columns" :data="records" :loading="loadingRecords" :server-side-sort="true" @sort="handleSort">
          <template #cell-inviter="{ row }">{{ row.inviter_email || row.inviter_username || '-' }}</template>
          <template #cell-invitee="{ row }">{{ row.invitee_email || row.invitee_username || '-' }}</template>
          <template #cell-redeem_code_type="{ row }">{{ row.redeem_code_type || '-' }}</template>
          <template #cell-subscription_item="{ row }">{{ row.validity_days ? `${row.validity_days} 天 (${row.subscription_group || '-'})` : '-' }}</template>
          <template #cell-redeem_value="{ row }">{{ formatCurrency(row.redeem_value) }}</template>
          <template #cell-cashback_base_amount="{ row }">{{ formatCurrency(row.cashback_base_amount) }}</template>
          <template #cell-cashback_rate_percent="{ row }">{{ formatPercent(row.cashback_rate_percent) }}</template>
          <template #cell-cashback_amount="{ row }"><span class="font-medium text-emerald-600 dark:text-emerald-400">{{ formatCurrency(row.cashback_amount) }}</span></template>
          <template #cell-inviter_balance_after="{ row }">{{ row.inviter_balance_after == null ? '-' : formatCurrency(row.inviter_balance_after) }}</template>
          <template #cell-created_at="{ row }">{{ formatDateTime(row.created_at) }}</template>
        </DataTable>
        <Pagination
          v-if="pagination.total > 0"
          class="mt-4"
          :page="pagination.page"
          :total="pagination.total"
          :page-size="pagination.page_size"
          @update:page="handlePageChange"
          @update:pageSize="handlePageSizeChange"
        />
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Column } from '@/components/common/types'
import { useAppStore } from '@/stores/app'
import { formatCurrency, formatDateTime } from '@/utils/format'
import { extractApiErrorMessage } from '@/utils/apiError'
import { getCashbackSettings, updateCashbackSettings, listCashbackRecords, type CashbackFaceValue } from '@/api/admin/inviteCashback'
import type { InviteCashbackRecord } from '@/api/inviteCashback'

const props = withDefaults(defineProps<{
  showSettings?: boolean
}>(), {
  showSettings: true,
})

const { t } = useI18n()
const appStore = useAppStore()
const saving = ref(false)
const loadingRecords = ref(false)
const records = ref<InviteCashbackRecord[]>([])
const form = reactive({ enabled: false, rate_percent: 20, subscription_mappings: [] as CashbackFaceValue[] })
const filters = reactive({ search: '', sort_by: 'created_at', sort_order: 'desc' as 'asc' | 'desc' })
const pagination = reactive({ page: 1, page_size: 20, total: 0 })
let debounceTimer: ReturnType<typeof setTimeout> | null = null

const columns = computed<Column[]>(() => [
  { key: 'inviter', label: t('admin.affiliates.records.inviter'), sortable: true },
  { key: 'invitee', label: t('admin.affiliates.records.invitee'), sortable: true },
  { key: 'redeem_code_type', label: t('admin.affiliates.cashback.redeemType'), sortable: true },
  { key: 'subscription_item', label: t('admin.affiliates.cashback.subscriptionItem') },
  { key: 'redeem_code', label: t('admin.affiliates.cashback.redeemCode') },
  { key: 'redeem_value', label: t('admin.affiliates.cashback.redeemValue'), sortable: true },
  { key: 'cashback_base_amount', label: t('admin.affiliates.cashback.baseAmount'), sortable: true },
  { key: 'cashback_rate_percent', label: t('admin.affiliates.cashback.rate'), sortable: true },
  { key: 'cashback_amount', label: t('admin.affiliates.cashback.amount'), sortable: true },
  { key: 'inviter_balance_after', label: t('admin.affiliates.records.balanceAfter') },
  { key: 'created_at', label: t('admin.affiliates.records.rebatedAt'), sortable: true },
])

function formatPercent(value: number): string {
  const rounded = Math.round(Number(value || 0) * 100) / 100
  return `${Number.isInteger(rounded) ? rounded : rounded.toString()}%`
}

async function loadSettings() {
  try {
    const data = await getCashbackSettings()
    form.enabled = data.enabled
    form.rate_percent = data.rate_percent
    form.subscription_mappings = [...(data.subscription_mappings || [])]
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
  }
}

async function saveSettings() {
  saving.value = true
  try {
    const data = await updateCashbackSettings({
      enabled: form.enabled,
      rate_percent: Math.min(100, Math.max(0, Number(form.rate_percent) || 0)),
      subscription_mappings: form.subscription_mappings
        .map((row) => ({
          group_id: Number(row.group_id) || 0,
          group_name: row.group_name,
          group_description: row.group_description,
          platform: row.platform,
          validity_days: Number(row.validity_days) || 0,
          display_name: row.display_name,
          cashback_base_amount: Number(row.cashback_base_amount) || 0,
        }))
        .filter((row) => row.group_id > 0 && row.validity_days > 0 && row.cashback_base_amount > 0),
    })
    form.enabled = data.enabled
    form.rate_percent = data.rate_percent
    form.subscription_mappings = [...(data.subscription_mappings || [])]
    appStore.showSuccess(t('common.saved'))
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
  } finally {
    saving.value = false
  }
}

async function loadRecords() {
  loadingRecords.value = true
  try {
    const res = await listCashbackRecords({
      page: pagination.page,
      page_size: pagination.page_size,
      search: filters.search,
      sort_by: filters.sort_by,
      sort_order: filters.sort_order,
    })
    records.value = res.items || []
    pagination.total = res.total || 0
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
  } finally {
    loadingRecords.value = false
  }
}

function debounceLoad() {
  if (debounceTimer) clearTimeout(debounceTimer)
  debounceTimer = setTimeout(() => {
    pagination.page = 1
    void loadRecords()
  }, 300)
}

function handleSort(key: string, order: 'asc' | 'desc') {
  filters.sort_by = key
  filters.sort_order = order
  pagination.page = 1
  void loadRecords()
}

function handlePageChange(page: number) {
  pagination.page = page
  void loadRecords()
}

function handlePageSizeChange(size: number) {
  pagination.page_size = size
  pagination.page = 1
  void loadRecords()
}

onMounted(() => {
  void loadSettings()
  void loadRecords()
})
</script>
