<template>
  <AppLayout>
    <div class="space-y-6">
      <div class="card p-6">
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
            <button class="btn btn-secondary btn-sm" @click="addFaceValue">
              <Icon name="plus" size="sm" />
              <span>{{ t('common.add') }}</span>
            </button>
          </div>
          <div class="space-y-3">
            <div v-for="(row, index) in form.face_values" :key="index" class="grid gap-3 rounded-lg border border-gray-200 p-3 dark:border-dark-700 sm:grid-cols-[1fr_1fr_auto]">
              <div>
                <label class="input-label">{{ t('admin.affiliates.cashback.redeemValue') }}</label>
                <input v-model.number="row.redeem_value" type="number" min="0" step="0.00000001" class="input" />
              </div>
              <div>
                <label class="input-label">{{ t('admin.affiliates.cashback.baseAmount') }}</label>
                <input v-model.number="row.cashback_base_amount" type="number" min="0" step="0.00000001" class="input" />
              </div>
              <div class="flex items-end">
                <button class="btn btn-danger btn-sm w-full sm:w-auto" @click="removeFaceValue(index)">
                  {{ t('common.delete') }}
                </button>
              </div>
            </div>
            <div v-if="form.face_values.length === 0" class="rounded-lg border border-dashed border-gray-300 p-6 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-dark-400">
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

const { t } = useI18n()
const appStore = useAppStore()
const saving = ref(false)
const loadingRecords = ref(false)
const records = ref<InviteCashbackRecord[]>([])
const form = reactive({ enabled: false, rate_percent: 20, face_values: [] as CashbackFaceValue[] })
const filters = reactive({ search: '', sort_by: 'created_at', sort_order: 'desc' as 'asc' | 'desc' })
const pagination = reactive({ page: 1, page_size: 20, total: 0 })
let debounceTimer: ReturnType<typeof setTimeout> | null = null

const columns = computed<Column[]>(() => [
  { key: 'inviter', label: t('admin.affiliates.records.inviter'), sortable: true },
  { key: 'invitee', label: t('admin.affiliates.records.invitee'), sortable: true },
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
    form.face_values = [...(data.face_values || [])]
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
      face_values: form.face_values
        .map((row) => ({
          redeem_value: Number(row.redeem_value) || 0,
          cashback_base_amount: Number(row.cashback_base_amount) || 0,
        }))
        .filter((row) => row.redeem_value > 0 && row.cashback_base_amount > 0),
    })
    form.enabled = data.enabled
    form.rate_percent = data.rate_percent
    form.face_values = [...(data.face_values || [])]
    appStore.showSuccess(t('common.saved'))
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
  } finally {
    saving.value = false
  }
}

function addFaceValue() {
  form.face_values.push({ redeem_value: 0, cashback_base_amount: 0 })
}

function removeFaceValue(index: number) {
  form.face_values.splice(index, 1)
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
