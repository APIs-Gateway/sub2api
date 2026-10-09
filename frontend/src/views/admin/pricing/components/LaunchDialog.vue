<template>
  <BaseDialog :show="!!model" :title="title" width="extra-wide" @close="close">
    <div v-if="model" data-test="launch-dialog">
      <!-- 第一步：选分组 -->
      <div v-if="step === 'pick'" class="space-y-5" data-test="launch-pick">
        <div v-if="usage && usage.requests > 0" class="note" data-test="launch-usage-note">
          <p class="font-medium">{{ t('admin.pricingConfig.write.launch.usageNote', { n: usage.requests, days: usage.window_days }) }}</p>
          <p class="mt-0.5 text-xs opacity-80">{{ t('admin.pricingConfig.write.launch.usageHint') }}</p>
        </div>

        <p class="text-sm text-gray-700 dark:text-gray-300">{{ t('admin.pricingConfig.write.launch.intro') }}</p>

        <!-- 照抄某个模型的勾选 -->
        <div class="flex flex-wrap items-end gap-3 rounded-md border border-gray-200 bg-gray-50 px-4 py-3 dark:border-dark-700 dark:bg-dark-900/40">
          <div class="min-w-[14rem] flex-1">
            <label class="input-label">{{ t('admin.pricingConfig.write.launch.copyLabel') }}</label>
            <Select v-model="refKey" :options="refOptions" :placeholder="t('admin.pricingConfig.write.launch.copyPlaceholder')" />
          </div>
          <button type="button" class="btn btn-secondary" :disabled="!refKey" data-test="copy-ref" @click="applyRef">
            {{ t('admin.pricingConfig.write.launch.copyButton') }}
          </button>
          <p class="basis-full text-xs text-gray-500 dark:text-dark-300">{{ t('admin.pricingConfig.write.launch.copyHint') }}</p>
        </div>

        <p v-if="groups.length === 0" class="text-sm text-gray-500 dark:text-dark-300">{{ t('admin.pricingConfig.drawer.noGroups') }}</p>
        <ul v-else class="divide-y divide-gray-100 overflow-hidden rounded-md border border-gray-200 dark:divide-dark-800 dark:border-dark-700">
          <li
            v-for="g in groups"
            :key="g.id"
            class="grid grid-cols-[auto_minmax(0,1fr)] items-center gap-x-4 gap-y-1 px-4 py-3 sm:grid-cols-[auto_minmax(0,1fr)_11rem]"
            :class="writable(g) ? '' : 'bg-gray-50 dark:bg-dark-900/40'"
            :data-test="`launch-group-${g.id}`"
          >
            <input
              :id="`lg-${g.id}`"
              type="checkbox"
              class="h-4 w-4 rounded border-gray-300 accent-primary-600 disabled:opacity-40"
              :checked="checked.has(g.id)"
              :disabled="!writable(g)"
              :data-test="`launch-check-${g.id}`"
              @change="toggle(g.id)"
            />
            <label :for="`lg-${g.id}`" class="min-w-0" :class="writable(g) ? 'cursor-pointer' : 'cursor-not-allowed'">
              <span class="block truncate text-sm font-medium text-gray-900 dark:text-white">{{ g.name }}</span>
              <span class="mt-0.5 flex flex-wrap items-center gap-x-3 text-xs text-gray-500 dark:text-dark-300">
                <span class="num">{{ t('admin.pricingConfig.group.rate', { rate: trimNum(g.rate) }) }}</span>
                <span v-if="g.accessMode">{{ t(`admin.pricingConfig.group.access.${g.accessMode}`) }}</span>
                <span v-if="!writable(g)" class="flex items-center gap-1"><Icon name="lock" size="xs" />{{ t('admin.pricingConfig.write.launch.unswitched') }}</span>
              </span>
            </label>
            <div v-if="checked.has(g.id)" class="col-span-2 sm:col-span-1">
              <input
                v-model="extras[g.id]"
                type="number"
                step="0.05"
                min="0.01"
                class="input num py-1.5 text-xs"
                :class="{ 'input-error': badExtra(g.id) }"
                :placeholder="t('admin.pricingConfig.write.launch.extraPlaceholder')"
                :aria-label="t('admin.pricingConfig.write.launch.extraAria', { name: g.name })"
                :data-test="`launch-extra-${g.id}`"
              />
            </div>
          </li>
        </ul>
        <WriteErrorNote v-if="plan.error.value" :error="plan.error.value" @action="onAction" />
      </div>

      <!-- 第二步：预览 -->
      <div v-else class="space-y-4" data-test="launch-preview">
        <div v-if="plan.phase.value === 'previewing'" class="flex min-h-[8rem] items-center justify-center"><LoadingSpinner /></div>
        <PlanPreview v-if="plan.ticket.value" v-model:acked="acked" :ticket="plan.ticket.value" />
        <p v-if="plan.ticket.value" class="text-xs text-gray-500 dark:text-dark-300">{{ unpickedNote }}</p>
        <WriteErrorNote v-if="plan.error.value" :error="plan.error.value" @action="onAction" />
        <div v-if="finalError" class="space-y-2" data-test="launch-final-error">
          <p class="text-sm font-medium text-primary-700 dark:text-primary-300">{{ t('admin.pricingConfig.write.launch.cellsDone') }}</p>
          <WriteErrorNote :error="finalError" @action="onAction" />
        </div>
      </div>
    </div>

    <template #footer>
      <template v-if="step === 'pick'">
        <button type="button" class="btn btn-secondary" @click="close">{{ t('common.cancel') }}</button>
        <button v-if="canAsIs" type="button" class="btn btn-secondary" :disabled="busy" data-test="launch-as-is" @click="launchAsIs">
          {{ t('admin.pricingConfig.write.launch.asIs') }}
        </button>
        <button type="button" class="btn btn-primary" :disabled="checked.size === 0 || anyBadExtra || plan.busy.value" data-test="launch-preview-btn" @click="toPreview">
          {{ t('admin.pricingConfig.write.launch.preview') }}
        </button>
      </template>
      <template v-else>
        <button type="button" class="btn btn-secondary" :disabled="busy || cellsDone" @click="backToPick">{{ t('admin.pricingConfig.write.back') }}</button>
        <button type="button" class="btn btn-primary" :disabled="!canConfirm" data-test="launch-confirm" @click="confirm">
          {{ busy ? t('admin.pricingConfig.write.submitting') : t('admin.pricingConfig.write.launch.confirm') }}
        </button>
      </template>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import { adminAPI } from '@/api/admin'
import type { CatalogUsage, CellOp } from '@/api/admin/pricing'
import { useAppStore } from '@/stores/app'
import PlanPreview from './PlanPreview.vue'
import WriteErrorNote from './WriteErrorNote.vue'
import { useCellPlan } from '../useCellPlan'
import { usePricingData } from '../usePricingData'
import { toWriteError, type ErrorAction, type WriteError } from '../pricingErrors'
import { cellKey, cellView, isOpenCell, type ModelRow, type PricingGroup } from '../pricingModel'
import { findDerive, isGroupWritable, parseExtra, planWarnings, storedCellOf, upsert } from '../pricingWrite'

const props = defineProps<{ model: ModelRow | null }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const { t } = useI18n()
const app = useAppStore()
const { state, rows, refresh } = usePricingData()
const plan = useCellPlan()

const step = ref<'pick' | 'preview'>('pick')
const checked = reactive(new Set<number>())
const extras = reactive<Record<number, string | number>>({})
const refKey = ref<string | number | null>(null)
const acked = ref(false)
const busy = ref(false)
const cellsDone = ref(false)
const finalError = ref<WriteError | null>(null)
const usage = ref<CatalogUsage | null>(null)

const title = computed(() => (props.model ? t('admin.pricingConfig.write.launch.title', { model: props.model.key }) : ''))
const entry = computed(() =>
  props.model ? state.catalog.find((e) => e.platform === props.model!.platform && e.model_key === props.model!.key) : undefined
)
const groups = computed(() => (props.model ? state.groups.filter((g) => g.platform === props.model!.platform) : []))
const writable = (g: PricingGroup) => isGroupWritable(g, findDerive(state.derives, g.id))

const refOptions = computed(() => {
  const m = props.model
  if (!m) return []
  return rows.value
    .filter((r) => r.platform === m.platform && r.status === 'active' && r.key !== m.key)
    .map((r) => ({ value: r.key, label: r.key }))
})

watch(
  () => props.model,
  async (m) => {
    step.value = 'pick'
    checked.clear()
    Object.keys(extras).forEach((k) => delete extras[Number(k)])
    acked.value = false
    busy.value = false
    cellsDone.value = false
    finalError.value = null
    usage.value = null
    plan.reset()
    if (!m) return
    // 默认带出参考模型，只是下拉里的选项，不会自动勾选
    refKey.value = m.referenceModel && refOptions.value.some((o) => o.value === m.referenceModel) ? m.referenceModel : null
    // 草稿转成上线不会查用量，所以借「草稿 → 下线」的预览读近 7 天用量；未登记的模型没有条目，读不到
    const e = entry.value
    if (e && e.status === 'draft') {
      try {
        usage.value = (await adminAPI.pricing.previewCatalogTransition(e.id, 'retired')).usage
      } catch {
        usage.value = null
      }
    }
  },
  { immediate: true }
)

function toggle(id: number) {
  if (checked.has(id)) checked.delete(id)
  else checked.add(id)
}

function applyRef() {
  const key = refKey.value ? String(refKey.value) : ''
  if (!key) return
  for (const g of groups.value) {
    if (writable(g) && isOpenCell(cellView(state.cells[cellKey(g.id, key)], g))) checked.add(g.id)
  }
  app.showSuccess(t('admin.pricingConfig.write.launch.copied', { model: key }))
}

const badExtra = (id: number) => parseExtra(extras[id]) === null
const anyBadExtra = computed(() => [...checked].some(badExtra))

/** 已经有流量、或还没登记的模型，可以不动分组直接上线。 */
const canAsIs = computed(() => !props.model?.registered || (usage.value?.requests ?? 0) > 0)

/**
 * 勾选的分组开放（默认官方价 × 分组倍率，填了额外倍率才加价）；没勾的开放分组写显式关闭，
 * 没勾的白名单分组不写单元格（已有的删掉）。还没切换的分组不动。
 */
function buildOps(): CellOp[] {
  const m = props.model
  if (!m) return []
  const ops: CellOp[] = []
  for (const g of groups.value) {
    if (!writable(g)) continue
    const stored = storedCellOf(findDerive(state.derives, g.id), m.key)
    if (checked.has(g.id)) {
      const extra = parseExtra(extras[g.id])
      const spec =
        typeof extra === 'number' && extra !== 1
          ? { open: true, mode: 'extra' as const, extra }
          : { open: true, mode: 'inherit' as const }
      ops.push(upsert(g, m.key, stored, spec))
    } else if (g.accessMode === 'open') {
      if (!(stored && !stored.open)) ops.push(upsert(g, m.key, stored, { open: false, mode: 'inherit' }))
    } else if (stored) {
      ops.push({ group_id: g.id, model_key: m.key, kind: 'delete', baseline_revision: stored.revision })
    }
  }
  return ops
}

const unpickedNote = computed(() => {
  const names = groups.value.filter((g) => writable(g) && !checked.has(g.id)).map((g) => g.name)
  return names.length
    ? t('admin.pricingConfig.write.launch.unpicked', { n: names.length, names: names.join('、') })
    : ''
})

async function toPreview() {
  acked.value = false
  step.value = 'preview'
  if (!(await plan.preview(buildOps()))) step.value = 'pick'
}

function backToPick() {
  plan.reset()
  step.value = 'pick'
}

const canConfirm = computed(() => {
  const ticket = plan.ticket.value
  if (busy.value || !ticket) return false
  if (plan.phase.value !== 'ready' && !cellsDone.value) return false
  return planWarnings(ticket).length === 0 || acked.value
})

/** 把目录状态改成上线：已登记就转换状态，未登记就以上线状态加入目录。 */
async function activate() {
  const m = props.model
  if (!m) return
  const e = entry.value
  if (e) await adminAPI.pricing.transitionCatalog(e.id, 'active', false)
  else {
    await adminAPI.pricing.createCatalogEntry({
      model_key: m.key,
      platform: m.platform,
      display_name: m.displayName,
      aliases: [],
      reference_model: null,
      status: 'active',
      note: '',
      confirm_usage: false
    })
  }
}

async function finish() {
  const m = props.model
  app.showSuccess(t('admin.pricingConfig.write.launch.done', { model: m?.key ?? '' }))
  await refresh()
  emit('close')
}

async function confirm() {
  if (!canConfirm.value) return
  busy.value = true
  finalError.value = null
  try {
    if (!cellsDone.value) {
      const ok = await plan.commit()
      if (!ok) return
      cellsDone.value = true
    }
    try {
      await activate()
    } catch (err) {
      finalError.value = toWriteError(err)
      return
    }
    await finish()
  } finally {
    busy.value = false
  }
}

async function launchAsIs() {
  busy.value = true
  try {
    await activate()
    await finish()
  } catch (err) {
    plan.error.value = toWriteError(err)
  } finally {
    busy.value = false
  }
}

async function onAction(action: ErrorAction) {
  if (action === 'refresh') {
    await refresh()
    app.showWarning(t('admin.pricingConfig.write.refreshed'))
    emit('close')
  } else if (action === 'repreview') {
    acked.value = false
    await plan.repreview()
  }
}

function close() {
  if (busy.value) return
  // 分组的单元格已经写入、只是状态没改成功时，关闭前刷新一次，免得下次操作先撞上基线过期
  if (cellsDone.value) void refresh()
  emit('close')
}

function trimNum(n: number): string {
  return String(Number(n.toFixed(4)))
}
</script>

<style scoped>
.note {
  @apply rounded-md border-l-2 border-gray-400 bg-gray-100 px-3 py-2.5 text-sm text-gray-800;
  @apply dark:border-dark-400 dark:bg-dark-900/60 dark:text-gray-200;
}
</style>
