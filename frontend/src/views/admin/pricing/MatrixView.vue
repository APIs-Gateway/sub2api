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
          <button
            type="button"
            class="btn"
            :class="editing ? 'btn-secondary' : 'btn-primary'"
            :disabled="!state.loaded || (!editing && !anyWritable)"
            :title="!editing && state.loaded && !anyWritable ? t('admin.pricingConfig.write.nothingWritable') : undefined"
            :aria-pressed="editing"
            data-test="edit"
            @click="toggleEditing"
          >
            {{ editing ? t('admin.pricingConfig.write.doneEditing') : t('admin.pricingConfig.matrix.edit') }}
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
        <span v-if="editing" class="flex items-center gap-1.5" data-test="legend-select">{{ t('admin.pricingConfig.write.selectHint') }}</span>
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
                <div class="flex items-center justify-between gap-2">
                  <span>{{ t('admin.pricingConfig.models.columns.model') }}</span>
                  <button v-if="editing" type="button" class="mx-link" data-test="select-all" @click="selectAll">{{ t('admin.pricingConfig.write.selectAll') }}</button>
                </div>
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
                <button
                  v-else-if="editing && groupWritable(g)"
                  type="button"
                  class="mx-link mt-1.5"
                  :data-test="`select-col-${g.id}`"
                  @click="toggleColumn(g)"
                >
                  {{ t('admin.pricingConfig.write.selectColumn') }}
                </button>
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
                <button
                  v-if="editing && m.status === 'active'"
                  type="button"
                  class="mx-link mt-1"
                  :data-test="`select-row-${m.key}`"
                  @click="toggleRow(m)"
                >
                  {{ t('admin.pricingConfig.write.selectRow') }}
                </button>
              </th>
              <td v-for="g in cols" :key="g.id" class="mx-td" :class="cellClass(g, m)" :data-test="`cell-${g.id}|${m.key}`">
                <button
                  v-if="editing && editable(g, m)"
                  type="button"
                  class="mx-cell mx-cell-btn"
                  :aria-pressed="isSelected(g, m)"
                  :title="t('admin.pricingConfig.write.cellTip')"
                  :data-test="`pick-${g.id}|${m.key}`"
                  @click="toggleCell(g, m)"
                >
                  <CellFace v-if="viewFor(g, m)" :view="viewFor(g, m)!" />
                  <span v-else class="text-gray-400 dark:text-dark-400">—</span>
                  <span v-if="isSelected(g, m)" class="mx-check" aria-hidden="true"><Icon name="check" size="xs" /></span>
                </button>
                <div v-else class="mx-cell">
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

      <!-- 选中格子后的操作栏 -->
      <div v-if="editing && selected.size > 0" class="action-bar" data-test="action-bar">
        <p class="text-sm text-gray-900 dark:text-white">
          {{ t('admin.pricingConfig.write.selected', { n: selected.size }) }}
          <span class="text-gray-500 dark:text-dark-300">{{ t('admin.pricingConfig.write.selectedDetail', { models: selectedModels, groups: selectedGroups }) }}</span>
        </p>
        <div class="flex flex-wrap items-center gap-2">
          <button type="button" class="btn btn-secondary btn-sm" data-test="op-open" @click="runOp('open')">{{ t('admin.pricingConfig.write.op.open') }}</button>
          <button type="button" class="btn btn-secondary btn-sm" data-test="op-close" @click="runOp('close')">{{ t('admin.pricingConfig.write.op.close') }}</button>
          <button type="button" class="btn btn-secondary btn-sm" data-test="op-extra" @click="extraOpen = true">{{ t('admin.pricingConfig.write.op.extra') }}</button>
          <button type="button" class="btn btn-secondary btn-sm" data-test="op-clear" @click="runOp('clear')">{{ t('admin.pricingConfig.write.op.clear') }}</button>
          <button type="button" class="btn btn-secondary btn-sm" data-test="op-custom" @click="customOpen = true">{{ t('admin.pricingConfig.write.op.custom') }}</button>
          <button type="button" class="btn btn-ghost btn-sm" data-test="op-deselect" @click="selected.clear()">{{ t('admin.pricingConfig.write.op.deselect') }}</button>
        </div>
      </div>
    </div>

    <ExtraRateDialog :show="extraOpen" :count="openSelectedCount" :skipped="selected.size - openSelectedCount" @close="extraOpen = false" @confirm="onExtra" />
    <CustomPriceDialog :show="customOpen" :count="openSelectedCount" @close="customOpen = false" @confirm="onCustom" />
    <CellPlanDialog :ops="planOps" :title="planTitle" @close="planOps = null" @done="onPlanDone" />

    <ModelDrawer :model="drawerModel" @close="drawerId = null" @action="onModelAction" />
    <ModelActionHost :action="modelAction" @close="modelAction = null" />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import CurrencyModeSwitch from '@/components/common/CurrencyModeSwitch.vue'
import CellFace from './components/CellFace.vue'
import ModelDrawer from './components/ModelDrawer.vue'
import ModelActionHost from './components/ModelActionHost.vue'
import ExtraRateDialog from './components/ExtraRateDialog.vue'
import CustomPriceDialog from './components/CustomPriceDialog.vue'
import CellPlanDialog from './components/CellPlanDialog.vue'
import { usePricingData } from './usePricingData'
import { usePricingFormat } from './usePricingFormat'
import { useAppStore } from '@/stores/app'
import type { CellOp, CustomPrice } from '@/api/admin/pricing'
import type { ModelAction } from './components/modelAction'
import {
  findDerive,
  isCellEditable,
  isGroupWritable,
  opClear,
  opClose,
  opCustom,
  opExtra,
  opOpen,
  storedCellOf
} from './pricingWrite'
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
const app = useAppStore()
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

// ---- 批量调整：选格子 → 选动作 → 预览 → 提交
const editing = ref(false)
const selected = reactive(new Set<string>())
const extraOpen = ref(false)
const customOpen = ref(false)
const planOps = ref<CellOp[] | null>(null)
const planTitle = ref('')
const modelAction = ref<ModelAction | null>(null)

const deriveOf = (g: PricingGroup) => findDerive(state.derives, g.id)
const groupWritable = (g: PricingGroup) => isGroupWritable(g, deriveOf(g))
const editable = (g: PricingGroup, m: ModelRow) => isCellEditable(g, deriveOf(g), m)
const anyWritable = computed(() => state.groups.some((g) => groupWritable(g)))
const keyOf = (g: PricingGroup, m: ModelRow) => cellKey(g.id, m.key)
const isSelected = (g: PricingGroup, m: ModelRow) => selected.has(keyOf(g, m))

function toggleEditing() {
  editing.value = !editing.value
  if (!editing.value) selected.clear()
}

function toggleCell(g: PricingGroup, m: ModelRow) {
  const k = keyOf(g, m)
  if (selected.has(k)) selected.delete(k)
  else selected.add(k)
}

/** 一组格子：全选中则取消，否则全部选上。 */
function toggleMany(pairs: [PricingGroup, ModelRow][]) {
  const keys = pairs.filter(([g, m]) => editable(g, m)).map(([g, m]) => keyOf(g, m))
  if (keys.length === 0) return
  const all = keys.every((k) => selected.has(k))
  keys.forEach((k) => (all ? selected.delete(k) : selected.add(k)))
}
const toggleRow = (m: ModelRow) => toggleMany(cols.value.map((g) => [g, m]))
const toggleColumn = (g: PricingGroup) => toggleMany(visibleRows.value.map((m) => [g, m]))
const selectAll = () => toggleMany(visibleRows.value.flatMap((m) => cols.value.map((g) => [g, m] as [PricingGroup, ModelRow])))

const selectedPairs = computed(() => {
  const out: [PricingGroup, ModelRow][] = []
  for (const g of cols.value) for (const m of rows.value) if (m.platform === platform.value && selected.has(keyOf(g, m))) out.push([g, m])
  return out
})
const selectedModels = computed(() => new Set(selectedPairs.value.map(([, m]) => m.key)).size)
const selectedGroups = computed(() => new Set(selectedPairs.value.map(([g]) => g.id)).size)
const isOpenNow = (g: PricingGroup, m: ModelRow) => isOpenCell(viewFor(g, m))
const openSelectedCount = computed(() => selectedPairs.value.filter(([g, m]) => isOpenNow(g, m)).length)

function stage(builder: (g: PricingGroup, m: ModelRow, stored: ReturnType<typeof storedCellOf>) => CellOp | null, title: string) {
  const ops: CellOp[] = []
  for (const [g, m] of selectedPairs.value) {
    const op = builder(g, m, storedCellOf(deriveOf(g), m.key))
    if (op) ops.push(op)
  }
  if (ops.length === 0) {
    app.showWarning(t('admin.pricingConfig.write.nothingToChange'))
    return
  }
  planTitle.value = title
  planOps.value = ops
}

function runOp(kind: 'open' | 'close' | 'clear') {
  const builders = { open: opOpen, close: opClose, clear: opClear }
  stage((g, m, stored) => builders[kind](g, m.key, stored), t(`admin.pricingConfig.write.title.${kind}`))
}

function onExtra(value: number) {
  extraOpen.value = false
  stage((g, m, stored) => opExtra(g, m.key, stored, value), t('admin.pricingConfig.write.title.extra'))
}

function onCustom(price: CustomPrice) {
  customOpen.value = false
  stage((g, m, stored) => opCustom(g, m.key, stored, price), t('admin.pricingConfig.write.title.custom'))
}

function onPlanDone() {
  planOps.value = null
  selected.clear()
}

function onModelAction(action: ModelAction) {
  drawerId.value = null
  modelAction.value = action
}

watch(platform, () => selected.clear())

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

.mx-link {
  @apply text-[11px] font-normal text-gray-500 underline decoration-gray-300 underline-offset-2 hover:text-gray-900 hover:decoration-gray-900;
  @apply dark:text-dark-300 dark:decoration-dark-500 dark:hover:text-white dark:hover:decoration-white;
}

.mx-cell-btn {
  @apply cursor-pointer transition-colors hover:bg-gray-100 dark:hover:bg-dark-800;
  @apply focus-visible:outline focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-primary-500;
}
.mx-cell-btn[aria-pressed='true'] {
  @apply bg-gray-100 shadow-[inset_0_0_0_2px_theme(colors.gray.800)] dark:bg-dark-700 dark:shadow-[inset_0_0_0_2px_theme(colors.gray.200)];
}
.mx-check {
  @apply absolute right-1.5 top-1.5 text-gray-800 dark:text-gray-100;
}

.action-bar {
  @apply sticky bottom-4 z-30 flex flex-wrap items-center justify-between gap-x-6 gap-y-2 rounded-md border border-gray-300 bg-white px-4 py-3;
  @apply shadow-overlay dark:border-dark-600 dark:bg-dark-800;
}

/* 没有任何价格：斜纹 */
.mx-td.is-unpriced {
  background-image: repeating-linear-gradient(135deg, rgba(204, 120, 92, 0.16) 0 5px, transparent 5px 11px);
}
.dark .mx-td.is-unpriced {
  background-image: repeating-linear-gradient(135deg, rgba(209, 141, 103, 0.2) 0 5px, transparent 5px 11px);
}
</style>
