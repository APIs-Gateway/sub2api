<template>
  <AppLayout>
    <div class="space-y-6">
      <div v-if="loadError && !loaded" class="state-block" data-test="load-error">
        <p class="font-serif text-lg text-gray-900 dark:text-white">{{ t('admin.pricingOps.loadError') }}</p>
        <p class="mt-1 text-sm text-gray-500 dark:text-dark-300">{{ loadError }}</p>
        <button type="button" class="btn btn-secondary mt-4" @click="reload">{{ t('admin.pricingOps.retry') }}</button>
      </div>
      <div v-else-if="!loaded" class="state-block" data-test="loading"><LoadingSpinner /></div>

      <template v-else-if="overview">
        <!-- 当前生效的价格版本 -->
        <section class="card p-5" data-test="snapshot-active">
          <div class="flex flex-wrap items-start justify-between gap-4">
            <div class="min-w-0">
              <p class="text-sm text-gray-500 dark:text-dark-300">{{ t('admin.pricingOps.snapshots.activeTitle') }}</p>
              <template v-if="overview.active">
                <p class="num mt-1 text-2xl font-medium text-gray-900 dark:text-white">{{ overview.active.label }}</p>
                <p class="num mt-1 text-xs text-gray-500 dark:text-dark-300">
                  {{
                    t('admin.pricingOps.snapshots.activeMeta', {
                      hash: shortHash(overview.active.contentSha256),
                      count: overview.active.modelCount,
                      time: formatDateTime(overview.active.approvedAt ?? overview.active.fetchedAt)
                    })
                  }}
                </p>
              </template>
              <p v-else class="mt-1 text-base text-gray-900 dark:text-white">{{ t('admin.pricingOps.snapshots.notPinnedTitle') }}</p>
            </div>
            <div class="flex items-center gap-3">
              <p v-if="overview.pending.length > 0" class="flex items-center gap-1.5 text-sm text-gray-700 dark:text-gray-300" data-test="has-new">
                <span class="h-2 w-2 rounded-full bg-primary-500" aria-hidden="true" />{{ t('admin.pricingOps.snapshots.hasNew') }}
              </p>
              <button
                v-if="overview.mode === 'pinned'"
                type="button"
                class="btn btn-primary"
                :disabled="pulling"
                data-test="pull"
                @click="pull"
              >
                <Icon name="refresh" size="md" class="mr-2" :class="pulling ? 'animate-spin' : ''" />
                {{ pulling ? t('admin.pricingOps.snapshots.pulling') : t('admin.pricingOps.snapshots.pull') }}
              </button>
              <button v-else type="button" class="btn btn-primary" data-test="pin" @click="pinOpen = true">
                {{ t('admin.pricingOps.snapshots.pin') }}
              </button>
            </div>
          </div>
          <p class="mt-3 text-xs text-gray-500 dark:text-dark-300">
            {{ overview.mode === 'pinned' ? t('admin.pricingOps.snapshots.pullNote') : t('admin.pricingOps.snapshots.notPinnedNote') }}
          </p>
        </section>

        <!-- 没有待批准的版本 -->
        <section v-if="overview.mode === 'pinned' && !candidate" class="card py-14 text-center" data-test="snapshot-empty">
          <p class="font-serif text-lg text-gray-900 dark:text-white">{{ t('admin.pricingOps.snapshots.emptyTitle') }}</p>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-300">{{ t('admin.pricingOps.snapshots.emptyHint') }}</p>
        </section>

        <template v-if="candidate">
          <!-- 候选切换（通常只有一个） -->
          <div v-if="overview.pending.length > 1" class="flex flex-wrap gap-2" role="tablist" data-test="candidate-tabs">
            <button
              v-for="c in overview.pending"
              :key="c.id"
              type="button"
              role="tab"
              class="filter-chip"
              :class="{ 'is-on': c.id === candidate.id }"
              :aria-selected="c.id === candidate.id"
              @click="selectCandidate(c.id)"
            >
              {{ c.label }}
            </button>
          </div>

          <section>
            <div class="mb-3 flex flex-wrap items-baseline justify-between gap-2">
              <h2 class="font-serif text-lg font-medium text-gray-900 dark:text-white">
                {{ t('admin.pricingOps.snapshots.candidateTitle', { label: candidate.label }) }}
              </h2>
              <div class="flex items-center gap-3">
                <p class="num text-xs text-gray-500 dark:text-dark-300">
                  {{ t('admin.pricingOps.snapshots.candidateMeta', { hash: shortHash(candidate.contentSha256), time: formatDateTime(candidate.fetchedAt) }) }}
                </p>
                <button type="button" class="btn btn-secondary btn-sm" data-test="reject" @click="rejectOpen = true">
                  {{ t('admin.pricingOps.snapshots.reject') }}
                </button>
              </div>
            </div>

            <div v-if="planError" class="note note-signal" role="alert" data-test="plan-error">
              <p class="font-medium">{{ planError }}</p>
              <button type="button" class="btn btn-secondary btn-sm mt-2" @click="loadPlan">{{ t('admin.pricingOps.retry') }}</button>
            </div>
            <div v-else-if="!plan" class="state-block !min-h-[8rem]" data-test="plan-loading"><LoadingSpinner /></div>

            <template v-else>
              <div class="metric-strip" data-test="snapshot-stats">
                <div class="metric-cell"><span class="metric-label">{{ t('admin.pricingOps.snapshots.stats.added') }}</span><span class="num-primary">{{ plan.stats.added }}</span></div>
                <div class="metric-cell"><span class="metric-label">{{ t('admin.pricingOps.snapshots.stats.removed') }}</span><span class="num-primary">{{ plan.stats.removed }}</span></div>
                <div class="metric-cell"><span class="metric-label">{{ t('admin.pricingOps.snapshots.stats.changed') }}</span><span class="num-primary">{{ plan.stats.changed }}</span></div>
                <div class="metric-cell"><span class="metric-label">{{ t('admin.pricingOps.snapshots.stats.unchanged') }}</span><span class="num-primary">{{ plan.stats.unchanged }}</span></div>
              </div>
            </template>
          </section>

          <template v-if="plan">
            <!-- 批准后会没有价格的分组 -->
            <div v-if="plan.exposureError" class="note note-signal" role="alert" data-test="exposure-error">
              <p class="inline-flex items-center gap-1.5 font-medium">
                <Icon name="exclamationTriangle" size="xs" />{{ t('admin.pricingOps.snapshots.exposureTitle') }}
              </p>
              <ul v-if="violations.length" class="mt-1 space-y-0.5 text-sm">
                <li v-for="(v, i) in violations" :key="i">{{ t('admin.pricingOps.snapshots.exposureItem', { group: groupName(v.group), model: v.model, reason: exposureReason(v.reason) }) }}</li>
              </ul>
              <p class="mt-1.5 text-xs opacity-80">{{ t('admin.pricingOps.snapshots.exposureHint') }}</p>
            </div>

            <!-- 批准后实际会变价的模型 -->
            <section data-test="effective-changes">
              <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.pricingOps.snapshots.effectiveTitle') }}</h3>
              <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-300">{{ t('admin.pricingOps.snapshots.effectiveHint') }}</p>
              <p v-if="plan.effectiveChangesError" class="warn-note mt-2" data-test="effective-error">
                <Icon name="exclamationTriangle" size="sm" class="flex-shrink-0" />{{ t('admin.pricingOps.snapshots.effectiveFailed') }}
              </p>
              <p v-else-if="plan.effectiveChanges.length === 0" class="mt-2 text-sm text-gray-600 dark:text-gray-300" data-test="effective-none">
                {{ t('admin.pricingOps.snapshots.effectiveNone') }}
              </p>
              <div v-else class="mt-2 max-h-72 overflow-auto rounded-md border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900">
                <table class="w-full min-w-[40rem] text-sm">
                  <thead>
                    <tr class="border-b border-gray-300 text-left text-xs font-medium text-gray-500 dark:border-dark-600 dark:text-dark-400">
                      <th class="px-4 py-2.5">{{ t('admin.pricingOps.snapshots.col.model') }}</th>
                      <th class="px-4 py-2.5">{{ t('admin.pricingOps.snapshots.col.input') }}</th>
                      <th class="px-4 py-2.5">{{ t('admin.pricingOps.snapshots.col.output') }}</th>
                      <th class="px-4 py-2.5">{{ t('admin.pricingOps.snapshots.col.trend') }}</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="c in plan.effectiveChanges" :key="c.model" class="border-b border-gray-100 last:border-b-0 dark:border-dark-800" :data-test="`effective-${c.model}`">
                      <td class="px-4 py-2.5 font-medium text-gray-900 dark:text-white">{{ c.model }}</td>
                      <td class="num whitespace-nowrap px-4 py-2.5"><SnapshotPriceChange :before="c.old_missing ? null : c.old_input_per_mtok" :after="c.new_missing ? null : c.new_input_per_mtok" /></td>
                      <td class="num whitespace-nowrap px-4 py-2.5"><SnapshotPriceChange :before="c.old_missing ? null : c.old_output_per_mtok" :after="c.new_missing ? null : c.new_output_per_mtok" /></td>
                      <td class="whitespace-nowrap px-4 py-2.5"><TrendText :dir="effectiveDirection(c, plan.entries)" :missing="c.new_missing" /></td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </section>

            <!-- 全部差异 -->
            <section>
              <div class="mb-3 flex flex-wrap items-center gap-3">
                <h3 class="mr-2 text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.pricingOps.snapshots.diffTitle') }}</h3>
                <input v-model="search" type="text" class="input w-full sm:w-56" :placeholder="t('admin.pricingOps.snapshots.searchPlaceholder')" data-test="diff-search" />
                <button v-for="f in filterOptions" :key="f" type="button" class="filter-chip" :class="{ 'is-on': filter === f }" :aria-pressed="filter === f" :data-test="`filter-${f}`" @click="filter = f">
                  {{ t(`admin.pricingOps.snapshots.filter.${f}`) }}
                </button>
                <div class="ml-auto"><CurrencyModeSwitch /></div>
              </div>

              <div class="overflow-x-auto rounded-md border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900">
                <table class="w-full min-w-[56rem] text-sm" data-test="snapshot-table">
                  <thead>
                    <tr class="border-b border-gray-300 text-left text-xs font-medium text-gray-500 dark:border-dark-600 dark:text-dark-400">
                      <th class="px-4 py-3">{{ t('admin.pricingOps.snapshots.col.model') }}</th>
                      <th class="px-4 py-3">{{ t('admin.pricingOps.snapshots.col.change') }}</th>
                      <th class="px-4 py-3">{{ t('admin.pricingOps.snapshots.col.input') }}</th>
                      <th class="px-4 py-3">{{ t('admin.pricingOps.snapshots.col.output') }}</th>
                      <th class="px-4 py-3">{{ t('admin.pricingOps.snapshots.col.trend') }}</th>
                      <th class="px-4 py-3">{{ t('admin.pricingOps.snapshots.col.decision') }}</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="e in shown" :key="e.model_key" class="border-b border-gray-100 align-top dark:border-dark-800" :data-test="`snap-row-${e.model_key}`">
                      <td class="px-4 py-3">
                        <p class="font-medium text-gray-900 dark:text-white">{{ e.model_key }}</p>
                        <p v-if="otherChanged(e) > 0" class="mt-0.5 text-xs text-gray-500 dark:text-dark-300" :title="e.changed_fields.join(', ')">
                          {{ t('admin.pricingOps.snapshots.otherFields', { n: otherChanged(e) }) }}
                        </p>
                      </td>
                      <td class="px-4 py-3"><span class="badge badge-gray">{{ t(`admin.pricingOps.snapshots.type.${e.change_type}`) }}</span></td>
                      <td class="num whitespace-nowrap px-4 py-3"><SnapshotPriceChange :before="pricesOf(e).oldIn" :after="pricesOf(e).newIn" /></td>
                      <td class="num whitespace-nowrap px-4 py-3"><SnapshotPriceChange :before="pricesOf(e).oldOut" :after="pricesOf(e).newOut" /></td>
                      <td class="whitespace-nowrap px-4 py-3">
                        <template v-if="entryDirection(e)">
                          <TrendText :dir="entryDirection(e)!" :percent="entryPercent(e)" />
                        </template>
                        <span v-else class="text-gray-400 dark:text-dark-400">—</span>
                      </td>
                      <td class="px-4 py-3">
                        <div class="inline-flex rounded-md border border-gray-300 p-0.5 dark:border-dark-600" role="group" :aria-label="t('admin.pricingOps.snapshots.decisionOf', { model: e.model_key })">
                          <button type="button" class="seg" :class="{ 'is-on': !holds.has(e.model_key) }" :aria-pressed="!holds.has(e.model_key)" :data-test="`approve-${e.model_key}`" @click="setHold(e.model_key, false)">
                            {{ t('admin.pricingOps.snapshots.approve') }}
                          </button>
                          <button type="button" class="seg" :class="{ 'is-on': holds.has(e.model_key) }" :aria-pressed="holds.has(e.model_key)" :data-test="`hold-${e.model_key}`" @click="setHold(e.model_key, true)">
                            {{ t('admin.pricingOps.snapshots.hold') }}
                          </button>
                        </div>
                      </td>
                    </tr>
                    <tr v-if="shown.length === 0">
                      <td colspan="6" class="px-4 py-12 text-center text-sm text-gray-500 dark:text-dark-300" data-test="diff-empty">{{ t('admin.pricingOps.snapshots.diffEmpty') }}</td>
                    </tr>
                  </tbody>
                </table>
              </div>
              <div v-if="matched.length > shown.length" class="mt-3 text-center">
                <button type="button" class="btn btn-secondary btn-sm" data-test="show-more" @click="limit += PAGE">
                  {{ t('admin.pricingOps.snapshots.showMore', { shown: shown.length, total: matched.length }) }}
                </button>
              </div>
            </section>

            <div class="action-bar" data-test="action-bar">
              <p class="text-sm text-gray-900 dark:text-white">
                {{ t('admin.pricingOps.snapshots.summary', { approve: approveCount, hold: holds.size }) }}
                <span v-if="recalculating" class="ml-2 text-xs text-gray-500 dark:text-dark-300">{{ t('admin.pricingOps.snapshots.recalculating') }}</span>
              </p>
              <div class="flex items-center gap-2">
                <button type="button" class="btn btn-secondary btn-sm" data-test="approve-all" @click="approveAll">{{ t('admin.pricingOps.snapshots.approveAll') }}</button>
                <button type="button" class="btn btn-secondary btn-sm" :disabled="upCount === 0" data-test="hold-up" @click="holdAllUp">
                  {{ t('admin.pricingOps.snapshots.holdUp', { n: upCount }) }}
                </button>
                <button type="button" class="btn btn-primary btn-sm" :disabled="!canSubmit" data-test="submit-approval" @click="openConfirm">
                  {{ t('admin.pricingOps.snapshots.submit') }}
                </button>
              </div>
            </div>
          </template>
        </template>

        <!-- 版本记录 -->
        <section>
          <h2 class="font-serif text-lg font-medium text-gray-900 dark:text-white">{{ t('admin.pricingOps.snapshots.historyTitle') }}</h2>
          <div class="mt-3 overflow-x-auto rounded-md border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900">
            <table class="w-full min-w-[40rem] text-sm" data-test="snapshot-log">
              <thead>
                <tr class="border-b border-gray-300 text-left text-xs font-medium text-gray-500 dark:border-dark-600 dark:text-dark-400">
                  <th class="px-4 py-3">{{ t('admin.pricingOps.snapshots.col.version') }}</th>
                  <th class="px-4 py-3">{{ t('admin.pricingOps.snapshots.col.status') }}</th>
                  <th class="px-4 py-3">{{ t('admin.pricingOps.snapshots.col.hash') }}</th>
                  <th class="px-4 py-3">{{ t('admin.pricingOps.snapshots.col.fetchedAt') }}</th>
                  <th class="px-4 py-3">{{ t('admin.pricingOps.snapshots.col.approvedAt') }}</th>
                  <th class="px-4 py-3">{{ t('admin.pricingOps.snapshots.col.approver') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="h in overview.history" :key="h.id" class="border-b border-gray-100 last:border-b-0 dark:border-dark-800">
                  <td class="num px-4 py-3 font-medium text-gray-900 dark:text-white">{{ h.label }}</td>
                  <td class="px-4 py-3">{{ t(`admin.pricingOps.snapshots.status.${h.status}`) }}</td>
                  <td class="num px-4 py-3">{{ shortHash(h.contentSha256) }}</td>
                  <td class="num px-4 py-3">{{ formatDateTime(h.fetchedAt) }}</td>
                  <td class="num px-4 py-3">{{ h.approvedAt ? formatDateTime(h.approvedAt) : '—' }}</td>
                  <td class="num px-4 py-3">{{ h.approvedBy ? `#${h.approvedBy}` : '—' }}</td>
                </tr>
                <tr v-if="overview.history.length === 0">
                  <td colspan="6" class="px-4 py-10 text-center text-sm text-gray-500 dark:text-dark-300">{{ t('admin.pricingOps.snapshots.historyEmpty') }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
      </template>
    </div>

    <!-- 批准确认：先看差异再确认 -->
    <BaseDialog :show="confirmOpen" :title="t('admin.pricingOps.snapshots.confirmTitle')" width="normal" @close="closeConfirm">
      <div class="space-y-3 text-sm text-gray-700 dark:text-gray-300" data-test="approval-dialog">
        <p>{{ t('admin.pricingOps.snapshots.confirmSummary', { approve: approveCount, hold: holds.size }) }}</p>
        <p v-if="plan && plan.effectiveChanges.length" class="font-medium text-gray-900 dark:text-white" data-test="confirm-effective">
          {{ t('admin.pricingOps.snapshots.confirmEffective', { n: plan.effectiveChanges.length, up: effectiveUp }) }}
        </p>
        <p>{{ t('admin.pricingOps.snapshots.confirmNote') }}</p>
        <p v-if="confirmError" class="note note-signal" role="alert" data-test="approval-error">{{ confirmError }}</p>
      </div>
      <template #footer>
        <button type="button" class="btn btn-secondary" :disabled="submitting" @click="closeConfirm">{{ t('common.cancel') }}</button>
        <button type="button" class="btn btn-primary" :disabled="submitting || !canSubmit" data-test="approval-confirm" @click="approve">
          {{ submitting ? t('admin.pricingOps.snapshots.approving') : t('admin.pricingOps.snapshots.confirmApprove') }}
        </button>
      </template>
    </BaseDialog>

    <ConfirmDialog
      :show="rejectOpen"
      :title="t('admin.pricingOps.snapshots.rejectTitle') + (candidate ? `：${candidate.label}` : '')"
      :message="t('admin.pricingOps.snapshots.rejectMessage')"
      :confirm-text="t('admin.pricingOps.snapshots.reject')"
      danger
      @confirm="reject"
      @cancel="rejectOpen = false"
    />
    <ConfirmDialog
      :show="pinOpen"
      :title="t('admin.pricingOps.snapshots.pinTitle')"
      :message="t('admin.pricingOps.snapshots.pinMessage')"
      :confirm-text="t('admin.pricingOps.snapshots.pin')"
      @confirm="pin"
      @cancel="pinOpen = false"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import CurrencyModeSwitch from '@/components/common/CurrencyModeSwitch.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import { formatDateTime } from '@/utils/format'
import { adminAPI } from '@/api/admin'
import {
  approveSnapshot,
  fetchSnapshotCandidate,
  getSnapshotOverview,
  pinSnapshot,
  previewSnapshot,
  rejectSnapshot,
  type SnapshotMeta,
  type SnapshotOverview,
  type SnapshotPlan
} from '@/api/admin/pricingOps'
import SnapshotPriceChange from './components/SnapshotPriceChange.vue'
import TrendText from './components/TrendText.vue'
import { isStaleError, opsErrorText, parseExposureViolations } from './opsErrors'
import {
  effectiveDirection,
  entryDirection,
  entryPercent,
  entryPrices,
  filterEntries,
  isUpward,
  otherChangedFieldCount,
  type DiffFilter
} from './snapshotModel'

const { t, te } = useI18n()
const app = useAppStore()

const PAGE = 100
const RECALC_DELAY_MS = 350
const filterOptions: DiffFilter[] = ['all', 'added', 'removed', 'changed', 'up']

const loaded = ref(false)
const loadError = ref('')
const overview = ref<SnapshotOverview | null>(null)
const selectedId = ref<number | null>(null)
const plan = ref<SnapshotPlan | null>(null)
const planError = ref('')
const planPending = ref(false)
// 最后一次成功预览所基于的搁置名单（排序后拼成串）；和本地 holds 对不上就说明预览已经过时
const planBasis = ref<string | null>(null)
const holds = ref<Set<string>>(new Set())
const filter = ref<DiffFilter>('all')
const search = ref('')
const limit = ref(PAGE)
const pulling = ref(false)
const submitting = ref(false)
const confirmOpen = ref(false)
const confirmError = ref('')
const rejectOpen = ref(false)
const pinOpen = ref(false)
const groupNames = ref<Record<string, string>>({})

let planSeq = 0
let recalcTimer: ReturnType<typeof setTimeout> | null = null

const candidate = computed<SnapshotMeta | null>(() => overview.value?.pending.find((c) => c.id === selectedId.value) ?? null)
const matched = computed(() => (plan.value ? filterEntries(plan.value.entries, filter.value, search.value) : []))
const shown = computed(() => matched.value.slice(0, limit.value))
const approveCount = computed(() => (plan.value ? plan.value.entries.length - holds.value.size : 0))
const upCount = computed(() => plan.value?.entries.filter((e) => isUpward(entryDirection(e)) && !holds.value.has(e.model_key)).length ?? 0)
const effectiveUp = computed(() => plan.value?.effectiveChanges.filter((c) => isUpward(effectiveDirection(c, plan.value?.entries))).length ?? 0)
const violations = computed(() => (plan.value ? parseExposureViolations(plan.value.exposureError) : []))
const holdsInSync = computed(() => planBasis.value !== null && planBasis.value === holdKey(holds.value))
// 请求在途、定时器已排上、或本地搁置名单和预览所基于的名单不一致：都算重新计算中
const recalculating = computed(() => planPending.value || (!!plan.value && !holdsInSync.value))
const canSubmit = computed(
  () => !!plan.value && !recalculating.value && !planError.value && approveCount.value > 0 && !plan.value.exposureError && !!plan.value.planHash
)

const pricesOf = entryPrices
const otherChanged = otherChangedFieldCount

function holdKey(names: Iterable<string>): string {
  return [...names].sort().join('\n')
}

function shortHash(h: string) {
  return h ? h.slice(0, 8) : '—'
}

function groupName(id: string) {
  return groupNames.value[id] ?? (id ? `#${id}` : t('admin.pricingOps.snapshots.unknownGroup'))
}

function exposureReason(reason: string) {
  const key = `admin.pricingOps.snapshots.exposureReason.${reason}`
  return te(key) ? t(key) : reason
}

function fail(err: unknown): string {
  return opsErrorText(err, t, te)
}

async function loadOverview(keepSelection = true) {
  const o = await getSnapshotOverview()
  overview.value = o
  if (!keepSelection || !o.pending.some((c) => c.id === selectedId.value)) {
    selectedId.value = o.pending[0]?.id ?? null
    holds.value = new Set()
    plan.value = null
  }
}

async function reload() {
  loadError.value = ''
  try {
    await loadOverview()
    loaded.value = true
    if (selectedId.value !== null) await loadPlan()
  } catch (err) {
    loadError.value = fail(err)
  }
}

async function loadPlan() {
  const id = selectedId.value
  if (id === null) return
  const seq = ++planSeq
  planPending.value = true
  planError.value = ''
  try {
    const sent = [...holds.value]
    const result = await previewSnapshot(id, sent)
    // 只接受最新一次请求的响应：旧响应（含期间又点了搁置、已排上重算的）直接丢弃
    if (seq !== planSeq) return
    plan.value = result
    planBasis.value = holdKey(sent)
    // 搁置名单以本地为准，响应只更新计划和 plan_hash，不覆盖用户在请求期间的点击
  } catch (err) {
    if (seq !== planSeq) return
    planError.value = fail(err)
    if (isStaleError(err)) await refreshAfterStale()
  } finally {
    if (seq === planSeq) planPending.value = false
  }
}

function scheduleRecalc() {
  // 作废在途请求：它基于的名单已经过时
  planSeq++
  planPending.value = true
  if (recalcTimer) clearTimeout(recalcTimer)
  recalcTimer = setTimeout(loadPlan, RECALC_DELAY_MS)
}

function selectCandidate(id: number) {
  if (id === selectedId.value) return
  selectedId.value = id
  holds.value = new Set()
  plan.value = null
  limit.value = PAGE
  void loadPlan()
}

function setHold(model: string, hold: boolean) {
  const next = new Set(holds.value)
  if (hold) next.add(model)
  else next.delete(model)
  holds.value = next
  scheduleRecalc()
}

function approveAll() {
  if (holds.value.size === 0) return
  holds.value = new Set()
  scheduleRecalc()
}

function holdAllUp() {
  if (!plan.value) return
  const next = new Set(holds.value)
  for (const e of plan.value.entries) if (isUpward(entryDirection(e))) next.add(e.model_key)
  holds.value = next
  scheduleRecalc()
}

async function pull() {
  pulling.value = true
  try {
    const r = await fetchSnapshotCandidate()
    await loadOverview()
    if (r.unchanged) app.showInfo(t('admin.pricingOps.snapshots.pullUnchanged'))
    else app.showSuccess(t('admin.pricingOps.snapshots.pullDone'))
    if (selectedId.value !== null && !plan.value) await loadPlan()
  } catch (err) {
    app.showError(fail(err))
  } finally {
    pulling.value = false
  }
}

async function pin() {
  pinOpen.value = false
  try {
    await pinSnapshot()
    app.showSuccess(t('admin.pricingOps.snapshots.pinDone'))
    await reload()
  } catch (err) {
    app.showError(fail(err))
  }
}

async function reject() {
  rejectOpen.value = false
  if (selectedId.value === null) return
  try {
    await rejectSnapshot(selectedId.value)
    app.showSuccess(t('admin.pricingOps.snapshots.rejectDone'))
    await loadOverview(false)
    if (selectedId.value !== null) await loadPlan()
  } catch (err) {
    app.showError(fail(err))
    if (isStaleError(err)) await refreshAfterStale()
  }
}

function openConfirm() {
  confirmError.value = ''
  confirmOpen.value = true
}

function closeConfirm() {
  if (!submitting.value) confirmOpen.value = false
}

async function approve() {
  if (!plan.value || selectedId.value === null || !canSubmit.value) return
  submitting.value = true
  confirmError.value = ''
  try {
    await approveSnapshot(selectedId.value, [...holds.value], plan.value.planHash)
    confirmOpen.value = false
    app.showSuccess(t('admin.pricingOps.snapshots.approved', { approve: approveCount.value, hold: holds.value.size }))
    holds.value = new Set()
    plan.value = null
    await loadOverview(false)
    if (selectedId.value !== null) await loadPlan()
  } catch (err) {
    // 错误都要让管理员看到：会话问题、预览过期、校验没过都有各自的提示
    confirmError.value = fail(err)
    if (isStaleError(err)) {
      await refreshAfterStale()
      confirmOpen.value = false
      app.showError(confirmError.value)
    }
  } finally {
    submitting.value = false
  }
}

/** 预览过期或基线变了：重新取总览与预览，让管理员重新看差异再确认。 */
async function refreshAfterStale() {
  const seq = ++planSeq
  try {
    await loadOverview()
    if (selectedId.value !== null) {
      const sent = [...holds.value]
      const result = await previewSnapshot(selectedId.value, sent)
      if (seq === planSeq) {
        plan.value = result
        planBasis.value = holdKey(sent)
        planError.value = ''
      }
    }
  } catch (err) {
    if (seq === planSeq) planError.value = fail(err)
  } finally {
    if (seq === planSeq) planPending.value = false
  }
}

async function loadGroupNames() {
  try {
    const groups = await adminAPI.groups.getAll()
    groupNames.value = Object.fromEntries(groups.map((g) => [String(g.id), g.name]))
  } catch {
    // 只用于把分组 ID 换成名字，读不到就显示 ID
  }
}

onMounted(() => {
  void reload()
  void loadGroupNames()
})

onBeforeUnmount(() => {
  if (recalcTimer) clearTimeout(recalcTimer)
  planSeq++
})
</script>

<style scoped>
.filter-chip {
  @apply rounded-md border border-gray-300 px-3 py-2 text-sm font-medium text-gray-600 transition-colors;
  @apply hover:bg-gray-100 dark:border-dark-600 dark:text-gray-300 dark:hover:bg-dark-800;
}
.filter-chip.is-on {
  @apply border-gray-900 bg-gray-100 text-gray-900 dark:border-gray-100 dark:bg-dark-700 dark:text-white;
}

.seg {
  @apply rounded px-3 py-1 text-xs font-medium text-gray-600 transition-colors dark:text-gray-300;
  @apply hover:bg-gray-100 dark:hover:bg-dark-700;
}
.seg.is-on {
  @apply bg-gray-900 text-white hover:bg-gray-900 dark:bg-gray-100 dark:text-gray-900 dark:hover:bg-gray-100;
}

.state-block {
  @apply flex min-h-[16rem] flex-col items-center justify-center px-6 py-14 text-center;
}

.warn-note {
  @apply flex items-center gap-1.5 text-sm text-primary-700 dark:text-primary-300;
}

.note {
  @apply rounded-md border border-gray-200 bg-gray-50 px-4 py-3 text-sm text-gray-700 dark:border-dark-700 dark:bg-dark-900/40 dark:text-gray-300;
}
.note-signal {
  @apply border-primary-300 bg-primary-50/60 text-primary-900 dark:border-primary-800 dark:bg-primary-950/30 dark:text-primary-100;
}

.action-bar {
  @apply sticky bottom-4 z-20 flex flex-wrap items-center justify-between gap-3 rounded-md border border-gray-300 bg-white px-4 py-3 shadow-overlay;
  @apply dark:border-dark-600 dark:bg-dark-800;
}
</style>
