<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="flex flex-wrap items-center gap-3">
          <select v-model="status" class="input w-full sm:w-44" :title="t('points.admin.withdrawals.filterStatus')" @change="reload">
            <option value="">{{ t('common.all') }}</option>
            <option value="pending">{{ t('points.withdrawals.statuses.pending') }}</option>
            <option value="paid">{{ t('points.withdrawals.statuses.paid') }}</option>
            <option value="rejected">{{ t('points.withdrawals.statuses.rejected') }}</option>
          </select>
          <button class="btn btn-secondary px-2 md:px-3" :disabled="loading" :title="t('common.refresh')" @click="load">
            <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
          </button>
        </div>
      </template>

      <template #table>
        <DataTable :columns="columns" :data="items" :loading="loading" row-key="id">
          <template #empty>
            <p class="text-sm text-gray-600 dark:text-gray-400">{{ t('points.admin.withdrawals.empty') }}</p>
          </template>
          <template #cell-user="{ row }">
            <div class="text-sm text-gray-900 dark:text-white">{{ row.user_email || row.username || ('#' + row.user_id) }}</div>
          </template>
          <template #cell-points="{ row }">
            <span class="font-mono text-sm tabular-nums text-gray-900 dark:text-white">{{ row.points.toLocaleString() }}</span>
          </template>
          <template #cell-net="{ row }">
            <span class="font-mono text-sm tabular-nums text-gray-900 dark:text-white">{{ formatWithdrawalCurrency(row.net_amount, row) }}</span>
          </template>
          <template #cell-method="{ row }">
            {{ row.payout_method === 'alipay' ? t('points.withdraw.alipay') : t('points.withdraw.usdt') }}
          </template>
          <template #cell-payout="{ row }">
            <template v-if="row.payout_method === 'alipay'">
              <div class="text-sm text-gray-900 dark:text-white">{{ row.payout_alipay_name }}</div>
              <div class="max-w-xs break-all font-mono text-xs text-gray-600 dark:text-gray-400">{{ row.payout_alipay_account }}</div>
            </template>
            <div v-else class="max-w-xs text-xs text-gray-600 dark:text-gray-400">
              <div class="font-medium text-gray-900 dark:text-white">{{ row.payout_usdt_chain || 'USDT' }}</div>
              <div class="break-all font-mono">{{ row.payout_usdt_address }}</div>
              <div v-if="row.usd_cny_rate_at" class="mt-1 font-mono">{{ t('points.withdraw.usdtRate') }} {{ row.usd_cny_rate_at.toFixed(2) }}</div>
            </div>
          </template>
          <template #cell-status="{ row }">
            <span class="rounded px-2 py-0.5 text-xs" :class="statusClass(row.status)">{{ t('points.withdrawals.statuses.' + row.status) }}</span>
          </template>
          <template #cell-created_at="{ row }">
            <span class="text-sm text-gray-600 dark:text-gray-400">{{ formatDateTime(row.created_at) }}</span>
          </template>
          <template #cell-actions="{ row }">
            <div v-if="row.status === 'pending'" class="flex justify-end gap-2">
              <button class="btn btn-secondary btn-sm" :disabled="busy" @click="openReview('approve', row)">{{ t('points.admin.withdrawals.approve') }}</button>
              <button class="btn btn-danger btn-sm" :disabled="busy" @click="openReview('reject', row)">{{ t('points.admin.withdrawals.reject') }}</button>
            </div>
            <span v-else class="text-xs text-gray-400">—</span>
          </template>
        </DataTable>
      </template>

      <template #pagination>
        <Pagination
          v-if="total > 0"
          :page="page"
          :total="total"
          :page-size="pageSize"
          @update:page="handlePageChange"
          @update:pageSize="handlePageSizeChange"
        />
      </template>
    </TablePageLayout>

    <BaseDialog
      :show="review.show"
      :title="review.action === 'approve' ? t('points.admin.withdrawals.approve') : t('points.admin.withdrawals.reject')"
      width="narrow"
      :close-on-escape="!review.submitting"
      @close="closeReview"
    >
      <div v-if="review.target" class="space-y-4">
        <div class="rounded-lg border border-gray-100 bg-gray-50 p-4 dark:border-dark-700 dark:bg-dark-800">
          <div class="text-sm font-medium text-gray-900 dark:text-white">{{ review.target.user_email || review.target.username || ('#' + review.target.user_id) }}</div>
          <div class="mt-1 flex items-center gap-2 text-sm text-gray-600 dark:text-gray-400">
            <span class="font-mono tabular-nums">{{ review.target.points.toLocaleString() }}</span>
            <span>·</span>
            <span class="font-mono tabular-nums">{{ formatWithdrawalCurrency(review.target.net_amount, review.target) }}</span>
          </div>
        </div>
        <p class="text-sm text-gray-700 dark:text-gray-300">
          {{ review.action === 'approve' ? t('points.admin.withdrawals.approveConfirm') : t('points.admin.withdrawals.rejectConfirm') }}
        </p>
        <div v-if="review.action === 'reject'" class="space-y-1">
          <label class="input-label">{{ t('points.admin.withdrawals.note') }}</label>
          <textarea
            v-model="review.note"
            rows="3"
            class="input resize-none"
            :placeholder="t('points.admin.withdrawals.notePlaceholder')"
          ></textarea>
        </div>
      </div>

      <template #footer>
        <button class="btn btn-secondary" :disabled="review.submitting" @click="closeReview">{{ t('common.cancel') }}</button>
        <button
          :class="review.action === 'approve' ? 'btn btn-primary' : 'btn btn-danger'"
          :disabled="review.submitting"
          @click="confirmReview"
        >
          {{ t('common.confirm') }}
        </button>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Column } from '@/components/common/types'
import {
  listPointsWithdrawals,
  approveWithdrawal,
  rejectWithdrawal,
} from '@/api/admin/points'
import type { PointsWithdrawal } from '@/api/points'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatCurrency, formatDateTime } from '@/utils/format'

const { t } = useI18n()
const appStore = useAppStore()
const loading = ref(true)
const busy = ref(false)
const items = ref<PointsWithdrawal[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const status = ref('')

const columns = computed<Column[]>(() => [
  { key: 'user', label: t('points.admin.withdrawals.user') },
  { key: 'points', label: t('points.admin.withdrawals.points') },
  { key: 'net', label: t('points.admin.withdrawals.net') },
  { key: 'method', label: t('points.admin.withdrawals.method') },
  { key: 'payout', label: t('points.admin.withdrawals.payout') },
  { key: 'status', label: t('points.admin.withdrawals.status') },
  { key: 'created_at', label: t('points.admin.withdrawals.createdAt') },
  { key: 'actions', label: t('points.admin.withdrawals.actions') },
])

const review = reactive<{
  show: boolean
  action: 'approve' | 'reject'
  target: PointsWithdrawal | null
  note: string
  submitting: boolean
}>({
  show: false,
  action: 'approve',
  target: null,
  note: '',
  submitting: false,
})

function statusClass(s: string): string {
  if (s === 'paid') return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300'
  if (s === 'rejected') return 'bg-gray-200 text-gray-600 dark:bg-dark-700 dark:text-gray-400'
  return 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300'
}

function withdrawalCurrency(row: PointsWithdrawal): 'CNY' | 'USD' {
  if (row.payout_currency === 'CNY' || row.payout_currency === 'USD') return row.payout_currency
  return row.payout_method === 'usdt' ? 'USD' : 'CNY'
}

function formatWithdrawalCurrency(amount: number, row: PointsWithdrawal): string {
  return formatCurrency(amount, withdrawalCurrency(row))
}

async function load(): Promise<void> {
  loading.value = true
  try {
    const res = await listPointsWithdrawals({ status: status.value, page: page.value, page_size: pageSize.value })
    items.value = res.items
    total.value = res.total
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('points.loadFailed')))
  } finally {
    loading.value = false
  }
}

function reload(): void {
  page.value = 1
  void load()
}

function handlePageChange(p: number): void {
  page.value = p
  void load()
}

function handlePageSizeChange(size: number): void {
  pageSize.value = size
  page.value = 1
  void load()
}

function openReview(action: 'approve' | 'reject', w: PointsWithdrawal): void {
  review.action = action
  review.target = w
  review.note = ''
  review.show = true
}

function closeReview(): void {
  if (review.submitting) return
  review.show = false
  review.target = null
}

async function confirmReview(): Promise<void> {
  const w = review.target
  if (!w) return
  review.submitting = true
  busy.value = true
  try {
    if (review.action === 'approve') {
      await approveWithdrawal(w.id, {})
      appStore.showSuccess(t('points.admin.withdrawals.approved'))
    } else {
      await rejectWithdrawal(w.id, { note: review.note.trim() || undefined })
      appStore.showSuccess(t('points.admin.withdrawals.rejected'))
    }
    review.show = false
    review.target = null
    await load()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('points.actionFailed')))
  } finally {
    review.submitting = false
    busy.value = false
  }
}

onMounted(() => {
  void load()
})
</script>
