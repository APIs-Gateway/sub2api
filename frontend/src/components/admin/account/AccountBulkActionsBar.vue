<template>
  <div class="mb-4 rounded-lg bg-primary-50 p-3 dark:bg-primary-900/20">
    <div class="flex items-center justify-between">
      <div class="flex flex-wrap items-center gap-2">
        <span v-if="selectedIds.length > 0" class="text-sm font-medium text-primary-900 dark:text-primary-100">
          {{ t('admin.accounts.bulkActions.selected', { count: selectedIds.length }) }}
        </span>
        <span v-else class="text-sm font-medium text-primary-900 dark:text-primary-100">
          {{ t('admin.accounts.bulkEdit.title') }}
        </span>
        <template v-if="selectedIds.length > 0">
        <button
          @click="$emit('select-page')"
          class="text-xs font-medium text-primary-700 hover:text-primary-800 dark:text-primary-300 dark:hover:text-primary-200"
        >
          {{ t('admin.accounts.bulkActions.selectCurrentPage') }}
        </button>
        <span class="text-gray-300 dark:text-primary-800">•</span>
        <button
          @click="$emit('clear')"
          class="text-xs font-medium text-primary-700 hover:text-primary-800 dark:text-primary-300 dark:hover:text-primary-200"
        >
          {{ t('admin.accounts.bulkActions.clear') }}
        </button>
        </template>
      </div>
      <div class="flex gap-2">
        <template v-if="selectedIds.length > 0">
          <button @click="$emit('delete')" class="btn btn-danger btn-sm">{{ t('admin.accounts.bulkActions.delete') }}</button>
          <button @click="$emit('reset-status')" class="btn btn-secondary btn-sm">{{ t('admin.accounts.bulkActions.resetStatus') }}</button>
          <button @click="$emit('refresh-token')" class="btn btn-secondary btn-sm">{{ t('admin.accounts.bulkActions.refreshToken') }}</button>
          <button @click="$emit('probe-upstream-billing')" class="btn btn-secondary btn-sm">{{ t('admin.accounts.bulkActions.probeUpstreamBilling') }}</button>
          <button @click="$emit('toggle-schedulable', true)" class="btn btn-success btn-sm">{{ t('admin.accounts.bulkActions.enableScheduling') }}</button>
          <button @click="$emit('toggle-schedulable', false)" class="btn btn-warning btn-sm">{{ t('admin.accounts.bulkActions.disableScheduling') }}</button>
          <button @click="$emit('edit-selected')" class="btn btn-primary btn-sm">{{ t('admin.accounts.bulkActions.edit') }}</button>
        </template>
        <button @click="$emit('edit-filtered')" class="btn btn-primary btn-sm">
          {{ t('admin.accounts.bulkEdit.submit') }}
        </button>
      </div>
    </div>
    <!-- 勾选表头全选后，提示可以把选择范围扩大到全部筛选结果（跨页） -->
    <div
      v-if="canSelectAllFiltered || allFilteredSelected"
      class="mt-2 flex flex-wrap items-center gap-x-1 border-t border-primary-100 pt-2 text-sm text-primary-900 dark:border-primary-800 dark:text-primary-100"
      data-testid="select-all-filtered-banner"
      role="status"
    >
      <template v-if="allFilteredSelected">
        <span>{{ t('admin.accounts.bulkActions.allFilteredSelected', { count: selectedIds.length }) }}</span>
      </template>
      <template v-else>
        <span>{{ t('admin.accounts.bulkActions.pageSelected', { count: pageSelectedCount }) }}</span>
        <button
          type="button"
          :disabled="selectingAllFiltered"
          data-testid="select-all-filtered"
          class="font-medium text-primary-700 hover:text-primary-800 disabled:cursor-wait disabled:opacity-60 dark:text-primary-300 dark:hover:text-primary-200"
          @click="$emit('select-all-filtered')"
        >
          {{ t('admin.accounts.bulkActions.selectAllFiltered', { total: totalCount }) }}
        </button>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'

withDefaults(
  defineProps<{
    selectedIds: number[]
    /** 当前页已选中的账号数 */
    pageSelectedCount?: number
    /** 当前筛选条件命中的账号总数 */
    totalCount?: number
    /** 表头全选已勾选，且筛选结果不止当前页：显示「选中全部筛选结果」入口 */
    canSelectAllFiltered?: boolean
    /** 已选中全部筛选结果 */
    allFilteredSelected?: boolean
    /** 正在获取全部筛选结果的 ID */
    selectingAllFiltered?: boolean
  }>(),
  {
    pageSelectedCount: 0,
    totalCount: 0,
    canSelectAllFiltered: false,
    allFilteredSelected: false,
    selectingAllFiltered: false
  }
)
defineEmits([
  'delete',
  'edit-selected',
  'edit-filtered',
  'clear',
  'select-page',
  'select-all-filtered',
  'toggle-schedulable',
  'reset-status',
  'refresh-token',
  'probe-upstream-billing'
])
const { t } = useI18n()
</script>
