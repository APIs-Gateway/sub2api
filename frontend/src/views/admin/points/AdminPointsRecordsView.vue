<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="flex flex-wrap items-center gap-3">
          <select v-model="kind" class="input w-full sm:w-44" :title="t('points.ledger.kind')" @change="reload">
            <option value="">{{ t('common.all') }}</option>
            <option v-for="k in kinds" :key="k" :value="k">{{ kindLabel(k) }}</option>
          </select>
          <div class="relative w-full md:w-72">
            <Icon name="search" size="md" class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
            <input v-model.trim="search" type="text" class="input pl-10" :placeholder="t('common.search')" @keyup.enter="reload" />
          </div>
          <button class="btn btn-secondary px-2 md:px-3" :disabled="loading" :title="t('common.refresh')" @click="load">
            <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
          </button>
        </div>
      </template>

      <template #table>
        <DataTable :columns="columns" :data="items" :loading="loading" row-key="id">
          <template #empty>
            <p class="text-sm text-gray-600 dark:text-gray-400">{{ t('points.admin.records.empty') }}</p>
          </template>
          <template #cell-user="{ row }">
            <div class="space-y-0.5">
              <div class="font-mono text-sm font-semibold text-gray-900 dark:text-white">#{{ row.user_id }}</div>
              <div class="text-sm text-gray-700 dark:text-gray-300">{{ row.user_email || '-' }}</div>
              <div v-if="row.username" class="text-xs text-gray-500 dark:text-dark-400">{{ row.username }}</div>
            </div>
          </template>
          <template #cell-kind="{ row }">
            {{ kindLabel(row.kind) }}
          </template>
          <template #cell-points="{ row }">
            <span
              class="font-mono text-sm tabular-nums"
              :class="row.points >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-gray-900 dark:text-white'"
            >{{ row.points >= 0 ? '+' : '' }}{{ row.points.toLocaleString() }}</span>
          </template>
          <template #cell-available_after="{ row }">
            <div class="space-y-0.5 font-mono text-sm tabular-nums text-gray-600 dark:text-dark-300">
              <div>{{ t('points.stats.available') }} {{ row.available_after != null ? row.available_after.toLocaleString() : '—' }}</div>
              <div>{{ t('points.stats.frozen') }} {{ row.frozen_after != null ? row.frozen_after.toLocaleString() : '—' }}</div>
            </div>
          </template>
          <template #cell-created_at="{ row }">
            <span class="text-sm text-gray-600 dark:text-gray-400">{{ formatDateTime(row.created_at) }}</span>
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
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Column } from '@/components/common/types'
import { listPointsLedgerAdmin } from '@/api/admin/points'
import type { PointsLedgerEntry } from '@/api/points'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatDateTime } from '@/utils/format'

const { t } = useI18n()
const appStore = useAppStore()
const loading = ref(true)
const items = ref<PointsLedgerEntry[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const kind = ref('')
const search = ref('')

const kinds = ['earn', 'clawback', 'thaw', 'to_balance', 'withdraw_hold', 'withdraw_paid', 'withdraw_refund', 'to_plan', 'adjust']

const columns = computed<Column[]>(() => [
  { key: 'user', label: t('points.admin.records.user') },
  { key: 'kind', label: t('points.admin.records.kind') },
  { key: 'points', label: t('points.admin.records.points') },
  { key: 'available_after', label: t('points.ledger.balanceAfter') },
  { key: 'created_at', label: t('points.admin.records.time') },
])

function kindLabel(k: string): string {
  const key = `points.ledger.kinds.${k}`
  const label = t(key)
  return label === key ? k : label
}

async function load(): Promise<void> {
  loading.value = true
  try {
    const res = await listPointsLedgerAdmin({ kind: kind.value, search: search.value, page: page.value, page_size: pageSize.value })
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

onMounted(() => {
  void load()
})
</script>
