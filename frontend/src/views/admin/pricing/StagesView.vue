<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="space-y-3">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <p class="max-w-3xl text-sm text-gray-600 dark:text-gray-300" data-test="stage-intro">
              {{ t('admin.pricingOps.stages.intro') }}
            </p>
            <button type="button" class="btn btn-secondary" :disabled="state.loading" data-test="refresh" @click="refresh">
              <Icon name="refresh" size="md" :class="state.loading ? 'animate-spin' : ''" />
              <span class="ml-1.5">{{ t('common.refresh') }}</span>
            </button>
          </div>
          <p v-if="state.failed > 0" class="warn-note" data-test="read-failed">
            <Icon name="exclamationTriangle" size="sm" class="flex-shrink-0" />
            {{ t('admin.pricingOps.stages.readFailed', { n: state.failed }) }}
          </p>
        </div>
      </template>

      <template #table>
        <div v-if="state.error && !state.loaded" class="state-block" data-test="load-error">
          <p class="font-serif text-lg text-gray-900 dark:text-white">{{ t('admin.pricingOps.loadError') }}</p>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-300">{{ state.error }}</p>
          <button type="button" class="btn btn-secondary mt-4" @click="refresh">{{ t('admin.pricingOps.retry') }}</button>
        </div>
        <div v-else-if="!state.loaded" class="state-block" data-test="loading"><LoadingSpinner /></div>
        <div v-else class="table-wrapper">
          <table class="w-full" data-test="stage-table">
            <thead class="sticky top-0 z-10">
              <tr>
                <th>{{ t('admin.pricingOps.stages.col.group') }}</th>
                <th>{{ t('admin.pricingOps.stages.col.stage') }}</th>
                <th>{{ t('admin.pricingOps.stages.col.shadow') }}</th>
                <th class="text-right">{{ t('admin.pricingOps.stages.col.actions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="g in state.groups" :key="g.id" :data-test="`stage-row-${g.id}`">
                <td>
                  <span class="font-medium text-gray-900 dark:text-white">{{ g.name }}</span>
                  <span class="ml-2 inline-flex items-center gap-1 text-xs text-gray-500 dark:text-dark-300">
                    <PlatformIcon :platform="asGroupPlatform(g.platform)" size="xs" />{{ platformLabel(g.platform) }}
                  </span>
                </td>
                <td>
                  <div v-if="g.view?.stage" class="inline-flex rounded-md border border-gray-300 p-0.5 dark:border-dark-600" role="group" :aria-label="t('admin.pricingOps.stages.stageOf', { group: g.name })">
                    <button
                      v-for="s in STAGES"
                      :key="s"
                      type="button"
                      class="seg"
                      :class="{ 'is-on': g.view.stage === s }"
                      :aria-pressed="g.view.stage === s"
                      :disabled="!canPick(g, s)"
                      :title="s === 'v2' ? t('admin.pricingOps.stages.v2Unavailable') : undefined"
                      :data-test="`stage-${g.id}-${s}`"
                      @click="ask(g, s)"
                    >
                      {{ t(`admin.pricingOps.stages.stage.${s}`) }}
                      <span v-if="s === 'v2' && g.view.stage !== 'v2'" class="ml-1 text-[10px] font-normal opacity-80">{{ t('admin.pricingOps.stages.unavailable') }}</span>
                    </button>
                  </div>
                  <span v-else-if="g.view" class="text-sm text-gray-500 dark:text-dark-300" :data-test="`no-config-${g.id}`">{{ t('admin.pricingOps.stages.noConfig') }}</span>
                  <span v-else class="text-sm text-gray-400 dark:text-dark-400">—</span>
                  <p v-if="g.view?.stage" class="mt-1 text-xs text-gray-500 dark:text-dark-300">{{ t(`admin.pricingOps.stages.stageHint.${g.view.stage}`) }}</p>
                </td>
                <td class="num whitespace-nowrap text-sm" :data-test="`shadow-${g.id}`">
                  <template v-if="summaryOf(g.id)">
                    <p>{{ t('admin.pricingOps.stages.compared', { n: summaryOf(g.id)!.compared }) }}</p>
                    <p :class="summaryOf(g.id)!.abnormal > 0 ? 'font-medium text-primary-700 dark:text-primary-300' : 'text-gray-500 dark:text-dark-300'">
                      {{ t('admin.pricingOps.stages.abnormal', { n: summaryOf(g.id)!.abnormal }) }}
                    </p>
                    <p class="text-xs text-gray-500 dark:text-dark-300">{{ t('admin.pricingOps.stages.expected', { n: summaryOf(g.id)!.expected }) }}</p>
                  </template>
                  <span v-else class="text-gray-400 dark:text-dark-400">{{ stats ? t('admin.pricingOps.stages.noShadowData') : '—' }}</span>
                </td>
                <td class="text-right">
                  <button v-if="g.view?.stage === 'shadow' || (summaryOf(g.id)?.abnormal ?? 0) + (summaryOf(g.id)?.expected ?? 0) > 0" type="button" class="btn btn-secondary btn-sm" :data-test="`samples-${g.id}`" @click="openSamples(g)">
                    {{ t('admin.pricingOps.stages.samples') }}
                  </button>
                </td>
              </tr>
              <tr v-if="state.groups.length === 0">
                <td colspan="4" class="!py-14 text-center text-sm text-gray-500 dark:text-dark-300" data-test="stage-empty">{{ t('admin.pricingOps.stages.empty') }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-if="state.loaded" class="px-1 pt-3 text-xs text-gray-500 dark:text-dark-300">{{ t('admin.pricingOps.stages.statsNote') }}</p>
      </template>
    </TablePageLayout>

    <!-- 切换确认 -->
    <BaseDialog :show="!!pending" :title="t('admin.pricingOps.stages.confirmTitle')" width="normal" @close="closePending">
      <div v-if="pending" class="space-y-3 text-sm text-gray-700 dark:text-gray-300" data-test="stage-dialog">
        <p>{{ t('admin.pricingOps.stages.confirmLine', { group: pending.group.name, from: stageName(pending.group.view?.stage), to: stageName(pending.to) }) }}</p>
        <p>{{ t(`admin.pricingOps.stages.confirmNote.${pending.to}`) }}</p>
        <div v-if="summaryOf(pending.group.id)" class="note" data-test="stage-dialog-summary">
          <p class="font-medium">{{ t('admin.pricingOps.stages.summaryTitle') }}</p>
          <p class="num mt-1">
            {{ t('admin.pricingOps.stages.compared', { n: summaryOf(pending.group.id)!.compared }) }}，{{ t('admin.pricingOps.stages.abnormal', { n: summaryOf(pending.group.id)!.abnormal }) }}，{{ t('admin.pricingOps.stages.expected', { n: summaryOf(pending.group.id)!.expected }) }}
          </p>
        </div>
        <p v-if="pendingError" class="note note-signal" role="alert" data-test="stage-error">{{ pendingError }}</p>
      </div>
      <template #footer>
        <button type="button" class="btn btn-secondary" :disabled="switching" @click="closePending">{{ t('common.cancel') }}</button>
        <button type="button" class="btn btn-primary" :disabled="switching" data-test="stage-confirm" @click="confirm">
          {{ switching ? t('admin.pricingOps.stages.switching') : t('admin.pricingOps.stages.confirmButton') }}
        </button>
      </template>
    </BaseDialog>

    <!-- 差异样本 -->
    <SideDrawer :show="!!samplesGroup" :title="t('admin.pricingOps.stages.samplesTitle', { group: samplesGroup?.name ?? '' })" :subtitle="t('admin.pricingOps.stages.samplesSubtitle')" width="wide" @close="samplesGroup = null">
      <div v-if="samplesLoading" class="state-block !min-h-[8rem]"><LoadingSpinner /></div>
      <p v-else-if="samplesError" class="note note-signal" role="alert" data-test="samples-error">{{ samplesError }}</p>
      <p v-else-if="samples.length === 0" class="text-sm text-gray-500 dark:text-dark-300" data-test="samples-empty">{{ t('admin.pricingOps.stages.samplesEmpty') }}</p>
      <ul v-else class="divide-y divide-gray-100 dark:divide-dark-800" data-test="samples-list">
        <li v-for="(s, i) in samples" :key="i" class="py-3">
          <p class="flex flex-wrap items-baseline gap-x-3 text-sm">
            <span class="font-medium text-gray-900 dark:text-white">{{ s.model || '—' }}</span>
            <span>{{ kindName(s.kind) }}</span>
            <span :class="s.class === 'translation' ? 'font-medium text-primary-700 dark:text-primary-300' : 'text-gray-500 dark:text-dark-300'">{{ className(s.class) }}</span>
            <span class="num ml-auto text-xs text-gray-500 dark:text-dark-300">{{ formatDateTime(s.created_at) }}</span>
          </p>
          <details class="mt-1">
            <summary class="cursor-pointer text-xs text-gray-500 dark:text-dark-300">{{ t('admin.pricingOps.stages.sampleDetail') }}</summary>
            <div class="mt-1 grid gap-2 sm:grid-cols-2">
              <div>
                <p class="text-xs text-gray-500 dark:text-dark-300">{{ t('admin.pricingOps.stages.viewLegacy') }}</p>
                <pre class="num mt-0.5 overflow-x-auto rounded bg-gray-50 p-2 text-xs dark:bg-dark-900/40">{{ pretty(s.legacy_view) }}</pre>
              </div>
              <div>
                <p class="text-xs text-gray-500 dark:text-dark-300">{{ t('admin.pricingOps.stages.viewNew') }}</p>
                <pre class="num mt-0.5 overflow-x-auto rounded bg-gray-50 p-2 text-xs dark:bg-dark-900/40">{{ pretty(s.v2_view) }}</pre>
              </div>
            </div>
          </details>
        </li>
      </ul>
    </SideDrawer>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import { formatDateTime } from '@/utils/format'
import {
  getShadowSamples,
  getShadowStats,
  switchGroupStage,
  type OpsStage,
  type ShadowSample,
  type ShadowStats
} from '@/api/admin/pricingOps'
import SideDrawer from './components/SideDrawer.vue'
import { opsErrorText } from './opsErrors'
import { asGroupPlatform, platformLabel } from './pricingModel'
import { useGroupOps, type OpsGroup } from './useGroupOps'

const STAGES: OpsStage[] = ['legacy', 'shadow', 'v2']

const { t, te } = useI18n()
const app = useAppStore()
const { state, load, reloadGroup } = useGroupOps()

const stats = ref<ShadowStats | null>(null)
const pending = ref<{ group: OpsGroup; to: 'legacy' | 'shadow' } | null>(null)
const pendingError = ref('')
const switching = ref(false)
const samplesGroup = ref<OpsGroup | null>(null)
const samples = ref<ShadowSample[]>([])
const samplesLoading = ref(false)
const samplesError = ref('')

const summaries = computed(() => {
  const map = new Map<number, { compared: number; abnormal: number; expected: number }>()
  if (!stats.value) return map
  const slot = (id: number) => {
    let s = map.get(id)
    if (!s) map.set(id, (s = { compared: 0, abnormal: 0, expected: 0 }))
    return s
  }
  for (const c of stats.value.compared) slot(c.group_id).compared += c.count
  for (const d of stats.value.diffs) {
    const s = slot(d.group_id)
    if (d.class === 'translation') s.abnormal += d.count
    else if (d.class === 'expected') s.expected += d.count
  }
  return map
})

function summaryOf(groupId: number) {
  return summaries.value.get(groupId) ?? null
}

/** v2 现在后端不开放，永远置灰；已经在 v2 的分组不能从这里改回；当前阶段不能再点。 */
function canPick(g: OpsGroup, s: OpsStage): boolean {
  if (s === 'v2') return false
  const cur = g.view?.stage
  return !!cur && cur !== 'v2' && cur !== s
}

function stageName(s: OpsStage | null | undefined) {
  return s ? t(`admin.pricingOps.stages.stage.${s}`) : '—'
}

function kindName(kind: string) {
  const key = `admin.pricingOps.stages.kind.${kind}`
  return te(key) ? t(key) : kind
}

function className(cls: string) {
  const key = `admin.pricingOps.stages.class.${cls}`
  return te(key) ? t(key) : cls
}

function pretty(v: unknown): string {
  if (v === undefined || v === null) return '—'
  try {
    return JSON.stringify(v, null, 2)
  } catch {
    return String(v)
  }
}

function ask(g: OpsGroup, s: OpsStage) {
  if (!canPick(g, s) || s === 'v2') return
  pendingError.value = ''
  pending.value = { group: g, to: s }
}

function closePending() {
  if (!switching.value) pending.value = null
}

async function confirm() {
  const p = pending.value
  if (!p) return
  switching.value = true
  pendingError.value = ''
  try {
    const r = await switchGroupStage(p.group.id, p.to)
    app.showSuccess(t('admin.pricingOps.stages.switched', { group: p.group.name, to: stageName(r.to) }))
    pending.value = null
    await reloadGroup(p.group.id)
  } catch (err) {
    pendingError.value = opsErrorText(err, t, te)
  } finally {
    switching.value = false
  }
}

async function openSamples(g: OpsGroup) {
  samplesGroup.value = g
  samples.value = []
  samplesError.value = ''
  samplesLoading.value = true
  try {
    samples.value = await getShadowSamples(g.id)
  } catch (err) {
    samplesError.value = opsErrorText(err, t, te)
  } finally {
    samplesLoading.value = false
  }
}

async function loadStats() {
  try {
    stats.value = await getShadowStats()
  } catch {
    // 对照结果是辅助信息，读不到就不显示，不挡切换
    stats.value = null
  }
}

async function refresh() {
  await Promise.all([load(), loadStats()])
}

onMounted(refresh)
</script>

<style scoped>
.seg {
  @apply rounded px-3 py-1 text-xs font-medium text-gray-600 transition-colors dark:text-gray-300;
  @apply hover:bg-gray-100 disabled:cursor-not-allowed dark:hover:bg-dark-700;
}
.seg:disabled:not(.is-on) {
  @apply opacity-40 hover:bg-transparent dark:hover:bg-transparent;
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
</style>
