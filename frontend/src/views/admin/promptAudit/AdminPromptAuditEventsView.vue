<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="flex flex-wrap items-center gap-3">
          <select
            v-model="decision"
            class="input w-full sm:w-40"
            :title="t('admin.promptAudit.filters.decision')"
            @change="reload"
          >
            <option value="">{{ t('common.all') }}</option>
            <option v-for="d in decisionOptions" :key="d" :value="d">{{ decisionLabel(t, d) }}</option>
          </select>
          <select
            v-model="riskLevel"
            class="input w-full sm:w-40"
            :title="t('admin.promptAudit.filters.riskLevel')"
            @change="reload"
          >
            <option value="">{{ t('common.all') }}</option>
            <option v-for="r in riskLevelOptions" :key="r" :value="r">{{ riskLevelLabel(t, r) }}</option>
          </select>
          <div class="relative w-full md:w-72">
            <Icon name="search" size="md" class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
            <input
              v-model.trim="keyword"
              type="text"
              class="input pl-10"
              :placeholder="t('admin.promptAudit.filters.keywordPlaceholder')"
              @keyup.enter="reload"
            />
          </div>
          <button class="btn btn-secondary px-2 md:px-3" :disabled="loading" :title="t('common.refresh')" @click="load">
            <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
          </button>
        </div>
      </template>

      <template #table>
        <DataTable :columns="columns" :data="items" :loading="loading" row-key="id">
          <template #empty>
            <p class="text-sm text-gray-600 dark:text-gray-400">{{ t('admin.promptAudit.empty') }}</p>
          </template>
          <template #cell-created_at="{ row }">
            <span class="text-sm text-gray-600 dark:text-gray-400">{{ formatDateTime(row.created_at) }}</span>
          </template>
          <template #cell-decision="{ row }">
            <span class="rounded px-2 py-0.5 text-xs font-medium" :class="decisionClass(row.decision)">
              {{ decisionLabel(t, row.decision) }}
            </span>
          </template>
          <template #cell-risk_level="{ row }">
            <span class="rounded px-2 py-0.5 text-xs font-medium" :class="riskLevelClass(row.risk_level)">
              {{ riskLevelLabel(t, row.risk_level) }}
            </span>
          </template>
          <template #cell-request="{ row }">
            <div class="space-y-0.5">
              <div class="max-w-[220px] truncate text-sm text-gray-900 dark:text-white" :title="row.snapshot?.endpoint">
                {{ row.snapshot?.endpoint || '-' }}
              </div>
              <div class="max-w-[220px] truncate text-xs text-gray-500 dark:text-dark-400" :title="row.snapshot?.model">
                {{ row.snapshot?.model || '-' }}
              </div>
            </div>
          </template>
          <template #cell-user="{ row }">
            <div class="space-y-0.5">
              <div class="font-mono text-sm font-semibold text-gray-900 dark:text-white">#{{ row.snapshot?.user_id }}</div>
              <div class="text-sm text-gray-700 dark:text-gray-300">
                {{ row.snapshot?.user_email || row.snapshot?.username || '-' }}
              </div>
              <div v-if="row.snapshot?.api_key_name" class="text-xs text-gray-500 dark:text-dark-400">
                {{ row.snapshot.api_key_name }}
              </div>
            </div>
          </template>
          <template #cell-preview="{ row }">
            <div class="max-w-xs space-y-0.5">
              <div
                class="truncate text-sm text-gray-700 dark:text-gray-300"
                :title="row.snapshot?.redacted_preview"
              >
                {{ row.snapshot?.redacted_preview || '-' }}
              </div>
              <div
                v-if="row.snapshot?.prompt_hash"
                class="truncate font-mono text-xs text-gray-500 dark:text-dark-400"
                :title="row.snapshot.prompt_hash"
              >
                {{ row.snapshot.prompt_hash }}
              </div>
            </div>
          </template>
          <template #cell-actions="{ row }">
            <button class="btn btn-secondary btn-sm" @click="openDetail(row.id)">{{ t('admin.promptAudit.view') }}</button>
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

    <PromptAuditEventDetailModal :show="detailShow" :event-id="detailEventId" @close="closeDetail" />
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
import { listPromptAuditEvents } from '@/api/admin/promptAudit'
import type { PromptAuditDecision, PromptAuditEvent, PromptAuditRiskLevel } from '@/api/admin/promptAudit'
import PromptAuditEventDetailModal from './components/PromptAuditEventDetailModal.vue'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatDateTime } from '@/utils/format'
import { decisionClass, decisionLabel, riskLevelClass, riskLevelLabel } from './promptAuditPresentation'

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(true)
const items = ref<PromptAuditEvent[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(20)
const decision = ref('')
const riskLevel = ref('')
const keyword = ref('')

const decisionOptions: PromptAuditDecision[] = ['pass', 'flag', 'critical']
const riskLevelOptions: PromptAuditRiskLevel[] = ['low', 'medium', 'high', 'critical']

const columns = computed<Column[]>(() => [
  { key: 'created_at', label: t('admin.promptAudit.table.time') },
  { key: 'decision', label: t('admin.promptAudit.table.decision') },
  { key: 'risk_level', label: t('admin.promptAudit.table.riskLevel') },
  { key: 'request', label: t('admin.promptAudit.table.request') },
  { key: 'user', label: t('admin.promptAudit.table.user') },
  { key: 'preview', label: t('admin.promptAudit.table.preview') },
  { key: 'actions', label: t('admin.promptAudit.table.actions') },
])

const detailShow = ref(false)
const detailEventId = ref<number | null>(null)

function openDetail(id: number): void {
  detailEventId.value = id
  detailShow.value = true
}

function closeDetail(): void {
  detailShow.value = false
}

async function load(): Promise<void> {
  loading.value = true
  try {
    const res = await listPromptAuditEvents({
      decision: decision.value,
      risk_level: riskLevel.value,
      keyword: keyword.value,
      page: page.value,
      page_size: pageSize.value,
    })
    items.value = res.items
    total.value = res.total
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('admin.promptAudit.loadFailed')))
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
