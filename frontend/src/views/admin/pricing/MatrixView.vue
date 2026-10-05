<template>
  <AppLayout>
    <div class="space-y-4">
      <!-- 当前展示的是什么：分组还在用渠道配置时，这里是推出来的结果 -->
      <div v-if="state.loaded && hasUnswitchedGroups" class="notice" role="note" data-test="derived-notice">
        <Icon name="infoCircle" size="md" class="mt-0.5 flex-shrink-0 text-gray-500 dark:text-dark-300" />
        <div class="min-w-0">
          <p class="font-medium text-gray-900 dark:text-white">{{ t('admin.pricingConfig.matrix.noticeTitle') }}</p>
          <p class="mt-0.5 text-sm text-gray-600 dark:text-dark-300">
            {{ noticeBody }}
            <router-link to="/admin/channels/pricing" class="notice-link">{{ t('admin.pricingConfig.matrix.noticeLink') }}</router-link>
          </p>
        </div>
      </div>

      <!-- 平台页签 + 工具 -->
      <div class="flex flex-col gap-3 2xl:flex-row 2xl:items-end 2xl:justify-between">
        <div class="tabs flex-shrink-0 whitespace-nowrap" role="tablist" :aria-label="t('admin.pricingConfig.models.columns.platform')">
          <button
            v-for="p in platforms"
            :key="p"
            type="button"
            role="tab"
            :aria-selected="platform === p"
            class="tab flex items-center gap-1.5"
            :class="{ 'tab-active': platform === p }"
            :data-test="`tab-${p}`"
            @click="platform = p"
          >
            <PlatformIcon :platform="asGroupPlatform(p)" size="sm" />
            {{ platformLabel(p) }}
            <span class="num text-xs text-gray-400 dark:text-dark-400">{{ t('admin.pricingConfig.matrix.groupCount', { n: groupsOf(p).length }) }}</span>
          </button>
        </div>
        <div class="flex flex-wrap items-center gap-3">
          <div class="relative w-56">
            <Icon name="search" size="md" class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 dark:text-gray-500" />
            <input
              v-model="search"
              type="text"
              :placeholder="t('admin.pricingConfig.models.searchPlaceholder')"
              class="input pl-10"
              data-test="matrix-search"
            />
          </div>
          <button
            type="button"
            class="filter-chip"
            :class="{ 'is-on': hideClosed }"
            :aria-pressed="hideClosed"
            data-test="hide-closed"
            @click="hideClosed = !hideClosed"
          >
            {{ t('admin.pricingConfig.matrix.hideClosed') }}
          </button>
          <CurrencyModeSwitch />
          <button type="button" class="btn btn-secondary" :disabled="state.loading" data-test="refresh" @click="refresh">
            <Icon name="refresh" size="md" :class="state.loading ? 'animate-spin' : ''" />
            <span class="ml-1.5">{{ t('common.refresh') }}</span>
          </button>
          <button type="button" class="btn btn-primary" disabled :title="t('admin.pricingConfig.comingSoon')" data-test="edit">
            {{ t('admin.pricingConfig.matrix.edit') }}
          </button>
        </div>
      </div>

      <!-- 图例 -->
      <div class="flex flex-wrap items-center gap-x-5 gap-y-2 text-xs text-gray-500 dark:text-dark-300" :aria-label="t('admin.pricingConfig.matrix.legend')" data-test="legend">
        <span class="flex items-center gap-1.5"><CellFace :view="legend.open" variant="chip" />{{ t('admin.pricingConfig.matrix.legendOpen') }}</span>
        <span class="flex items-center gap-1.5"><CellFace :view="legend.extra" variant="chip" />{{ t('admin.pricingConfig.matrix.legendExtra') }}</span>
        <span class="flex items-center gap-1.5"><CellFace :view="legend.custom" variant="chip" />{{ t('admin.pricingConfig.matrix.legendCustom') }}</span>
        <span class="flex items-center gap-1.5"><CellFace :view="legend.unpriced" variant="chip" />{{ t('admin.pricingConfig.matrix.legendUnpriced') }}</span>
        <span class="flex items-center gap-1.5"><CellFace :view="legend.closed" variant="chip" />{{ t('admin.pricingConfig.matrix.legendClosed') }}</span>
        <span class="flex items-center gap-1.5"><Icon name="lock" size="xs" />{{ t('admin.pricingConfig.matrix.legendUnswitched') }}</span>
        <span class="flex items-center gap-1.5" data-test="legend-paid">{{ t('admin.pricingConfig.matrix.legendPaid') }}</span>
      </div>

      <div v-if="state.error && !state.loaded" class="card py-16 text-center" data-test="load-error">
        <p class="font-serif text-lg text-gray-900 dark:text-white">{{ t('admin.pricingConfig.loadError') }}</p>
        <p class="mt-1 text-sm text-gray-500 dark:text-dark-300">{{ state.error }}</p>
        <button type="button" class="btn btn-secondary mt-4" @click="refresh">{{ t('admin.pricingConfig.retry') }}</button>
      </div>
      <div v-else-if="!state.loaded" class="card flex min-h-[16rem] items-center justify-center" data-test="loading">
        <LoadingSpinner />
      </div>
      <div v-else-if="cols.length === 0" class="card py-16 text-center" data-test="no-groups">
        <p class="font-serif text-lg text-gray-900 dark:text-white">{{ t('admin.pricingConfig.matrix.noGroupsTitle', { platform: platformLabel(platform) }) }}</p>
        <p class="mt-1 text-sm text-gray-500 dark:text-dark-300">{{ t('admin.pricingConfig.matrix.noGroupsHint') }}</p>
      </div>
      <div v-else class="mx-wrap" data-test="matrix">
        <table class="mx-table">
          <thead>
            <tr>
              <th class="mx-corner">
                <span>{{ t('admin.pricingConfig.models.columns.model') }}</span>
                <div class="mx-corner-hint">{{ t('admin.pricingConfig.matrix.cornerHint') }}</div>
              </th>
              <th v-for="g in cols" :key="g.id" class="mx-col-head" :data-test="`col-${g.id}`">
                <span class="col-name">{{ g.name }}</span>
                <span class="col-meta">
                  <span class="num">{{ t('admin.pricingConfig.group.rate', { rate: trimNum(g.rate) }) }}</span>
                  <span v-if="g.accessMode">{{ t(`admin.pricingConfig.group.access.${g.accessMode}`) }}</span>
                </span>
                <span v-if="isGroupUnswitched(g)" class="col-stage" :data-test="`stage-${g.id}`">
                  <Icon name="lock" size="xs" />{{ t('admin.pricingConfig.group.unswitched') }}
                </span>
              </th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="m in visibleRows" :key="m.id" :data-test="`row-${m.key}`">
              <th class="mx-row-head">
                <button type="button" class="row-btn" :title="t('admin.pricingConfig.matrix.openDetail')" @click="drawerId = m.id">
                  <span class="row-name">{{ m.key }}</span>
                  <span v-if="m.status !== 'active'" class="row-badge" :class="{ 'is-unregistered': !m.registered }">
                    {{ t(`admin.pricingConfig.status.${m.status ?? 'unregistered'}`) }}
                  </span>
                  <span class="row-meta num">
                    <template v-if="officialOf(m)">{{ officialOf(m)!.main }}</template>
                    <span v-else-if="state.refs[m.key] && !state.refs[m.key].priced" class="text-primary-700 dark:text-primary-300">
                      {{ t('admin.pricingConfig.drawer.noOfficialPrice') }}
                    </span>
                  </span>
                  <span v-if="officialOf(m)?.sub" class="row-meta-sub num">{{ officialOf(m)!.sub }}</span>
                </button>
              </th>
              <td v-for="g in cols" :key="g.id" class="mx-td" :class="cellClass(g, m)" :data-test="`cell-${g.id}|${m.key}`">
                <div class="mx-cell">
                  <CellFace v-if="viewFor(g, m)" :view="viewFor(g, m)!" />
                  <span v-else class="text-gray-400 dark:text-dark-400">—</span>
                </div>
              </td>
            </tr>
            <tr v-if="visibleRows.length === 0">
              <td :colspan="cols.length + 1" class="px-4 py-12 text-center text-sm text-gray-500 dark:text-dark-300" data-test="matrix-empty">
                {{ t('admin.pricingConfig.matrix.empty') }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <p v-if="state.deriveFailed > 0" class="warn-note" data-test="derive-failed">
        <Icon name="exclamationTriangle" size="sm" class="flex-shrink-0" />
        {{ t('admin.pricingConfig.deriveFailed', { n: state.deriveFailed }) }}
      </p>
    </div>

    <ModelDrawer :model="drawerModel" @close="drawerId = null" />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import CurrencyModeSwitch from '@/components/common/CurrencyModeSwitch.vue'
import CellFace from './components/CellFace.vue'
import ModelDrawer from './components/ModelDrawer.vue'
import { usePricingData } from './usePricingData'
import { usePricingFormat } from './usePricingFormat'
import {
  cellKey,
  cellView,
  isGroupUnswitched,
  isOpenCell,
  asGroupPlatform,
  platformLabel,
  type CellView,
  type ModelRow,
  type PricingGroup
} from './pricingModel'

const { t } = useI18n()
const { state, rows, platforms, hasUnswitchedGroups, load, refresh } = usePricingData()
const { official } = usePricingFormat()

const platform = ref('')
const search = ref('')
const hideClosed = ref(false)
const drawerId = ref<string | null>(null)

const groupsOf = (p: string) => state.groups.filter((g) => g.platform === p)
const cols = computed(() => groupsOf(platform.value))
const drawerModel = computed(() => rows.value.find((m) => m.id === drawerId.value) ?? null)

// 数据到了之后，默认落在第一个有分组的平台
watch(
  platforms,
  (list) => {
    if (platform.value && list.includes(platform.value)) return
    platform.value = list.find((p) => groupsOf(p).length > 0) ?? list[0] ?? ''
  },
  { immediate: true }
)

const noticeBody = computed(() => {
  const total = state.groups.length
  const unswitched = state.groups.filter(isGroupUnswitched).length
  return unswitched === total
    ? t('admin.pricingConfig.matrix.noticeAll')
    : t('admin.pricingConfig.matrix.noticeSome', { n: unswitched, total })
})

function viewFor(g: PricingGroup, m: ModelRow): CellView | null {
  return cellView(state.cells[cellKey(g.id, m.key)], g)
}

const visibleRows = computed(() => {
  const q = search.value.trim().toLowerCase()
  return rows.value.filter((m) => {
    if (m.platform !== platform.value) return false
    if (q && !m.key.toLowerCase().includes(q) && !m.displayName.toLowerCase().includes(q)) return false
    if (hideClosed.value && !cols.value.some((g) => isOpenCell(viewFor(g, m)))) return false
    return true
  })
})

function cellClass(g: PricingGroup, m: ModelRow): string {
  const kind = viewFor(g, m)?.kind
  return kind === 'closed' || kind === 'error' ? 'is-closed' : kind === 'unpriced' ? 'is-unpriced' : ''
}

function officialOf(m: ModelRow): { main: string; sub: string | null } | null {
  const ref = state.refs[m.key]
  if (!ref?.priced || !ref.per_mtok) return null
  const input = official(ref.per_mtok.input)
  const output = official(ref.per_mtok.output)
  return {
    main: `${input.main} / ${output.main}`,
    sub: input.sub && output.sub ? `${input.sub} / ${output.sub}` : null
  }
}

function trimNum(n: number): string {
  return String(Number(n.toFixed(4)))
}

// 图例用的示例格子
const sample = { unswitched: false, usd: null, perRequestUsd: null, perRequestRange: null, extra: null, reason: null }
const legend: Record<'open' | 'extra' | 'custom' | 'unpriced' | 'closed', CellView> = {
  open: { ...sample, kind: 'open' },
  extra: { ...sample, kind: 'extra', extra: 1.2 },
  custom: { ...sample, kind: 'custom' },
  unpriced: { ...sample, kind: 'unpriced' },
  closed: { ...sample, kind: 'closed' }
}

onMounted(() => {
  void load()
})
</script>

<style scoped>
.notice {
  @apply flex items-start gap-3 rounded-md border border-gray-200 bg-gray-50 px-4 py-3;
  @apply dark:border-dark-700 dark:bg-dark-800/60;
}
.notice-link {
  @apply ml-1 text-gray-900 underline decoration-gray-300 underline-offset-2 hover:decoration-gray-900;
  @apply dark:text-white dark:decoration-dark-500 dark:hover:decoration-white;
}

.filter-chip {
  @apply rounded-md border border-gray-300 px-3 py-2.5 text-sm font-medium text-gray-600 transition-colors;
  @apply hover:bg-gray-100 dark:border-dark-600 dark:text-gray-300 dark:hover:bg-dark-800;
}
.filter-chip.is-on {
  @apply border-gray-900 bg-gray-100 text-gray-900 dark:border-gray-100 dark:bg-dark-700 dark:text-white;
}

.warn-note {
  @apply flex items-center gap-1.5 text-sm text-primary-700 dark:text-primary-300;
}

.mx-wrap {
  @apply overflow-auto rounded-md border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900;
  max-height: calc(100vh - 22rem);
}

.mx-table {
  @apply w-full border-separate border-spacing-0 text-sm;
}

/* 表头：小号灰字 + 强发丝底线 */
.mx-corner,
.mx-col-head {
  @apply sticky top-0 border-b border-gray-300 bg-white px-3 py-2.5 text-left align-top text-xs font-medium text-gray-500;
  @apply dark:border-dark-600 dark:bg-dark-900 dark:text-dark-300;
}

.mx-corner {
  @apply left-0 z-30 min-w-[15rem] border-r;
  @apply dark:border-dark-700;
}

.mx-corner-hint {
  @apply mt-1 text-[11px] font-normal text-gray-400 dark:text-dark-400;
}

.mx-col-head {
  @apply z-20 min-w-[9.5rem] border-r border-r-gray-100 dark:border-r-dark-800;
}

.col-name {
  @apply block text-[13px] font-semibold text-gray-900 dark:text-white;
}

.col-meta {
  @apply mt-0.5 flex flex-wrap gap-x-2 text-[11px] font-normal text-gray-500 dark:text-dark-300;
}

.col-stage {
  @apply mt-1.5 flex items-center gap-1 text-[11px] font-normal text-gray-500 dark:text-dark-300;
}

/* 行头：模型名固定在左边 */
.mx-row-head {
  @apply sticky left-0 z-10 min-w-[15rem] border-b border-r border-gray-100 bg-white px-3 py-2.5 text-left align-top font-normal;
  @apply dark:border-dark-800 dark:bg-dark-900;
}

.row-btn {
  @apply flex min-w-0 flex-col items-start gap-0.5 rounded text-left;
  @apply focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500;
}

.row-name {
  @apply text-[13px] font-medium text-gray-900 hover:underline dark:text-white;
}

.row-badge {
  @apply rounded border border-gray-300 px-1 text-[11px] text-gray-600 dark:border-dark-500 dark:text-dark-200;
}
.row-badge.is-unregistered {
  @apply border-primary-400 text-primary-700 dark:border-primary-600 dark:text-primary-300;
}

.row-meta {
  @apply text-[11px] text-gray-500 dark:text-dark-300;
}

.row-meta-sub {
  @apply text-[10px] leading-3 text-gray-400 dark:text-dark-400;
}

/* 单元格 */
.mx-td {
  @apply relative border-b border-r border-gray-100 p-0 align-top dark:border-dark-800;
}

.mx-cell {
  @apply block min-h-[3.5rem] w-full px-3 py-2.5 text-left;
}

.mx-td.is-closed {
  @apply bg-gray-50 dark:bg-dark-950/40;
}

/* 没有任何价格：斜纹 */
.mx-td.is-unpriced {
  background-image: repeating-linear-gradient(135deg, rgba(204, 120, 92, 0.16) 0 5px, transparent 5px 11px);
}
.dark .mx-td.is-unpriced {
  background-image: repeating-linear-gradient(135deg, rgba(209, 141, 103, 0.2) 0 5px, transparent 5px 11px);
}
</style>
