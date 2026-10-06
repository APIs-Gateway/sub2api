<template>
  <div class="space-y-3" data-test="stage-preview">
    <p v-if="preview.kind === 'noop'" data-test="preview-line">{{ t('admin.pricingOps.stages.noopLine', { group: groupName, to: stageName(preview.to) }) }}</p>
    <p v-else data-test="preview-line">
      {{ t(fromV2Rollback ? 'admin.pricingOps.stages.rollbackLine' : 'admin.pricingOps.stages.confirmLine', { group: groupName, from: stageName(preview.from), to: stageName(preview.to) }) }}
    </p>
    <p v-if="preview.kind !== 'noop'">{{ noteText }}</p>

    <!-- 价格方向：证明不了就是 unknown，要让管理员看见 -->
    <div v-if="preview.kind !== 'noop' && preview.price_delta === 'unknown'" class="note note-signal" role="alert" data-test="delta-unknown">
      <p class="flex items-center gap-1.5 font-medium">
        <Icon name="exclamationTriangle" size="sm" class="flex-shrink-0" />
        {{ t('admin.pricingOps.stages.priceDelta.unknown') }}
      </p>
      <p class="mt-1">{{ t('admin.pricingOps.stages.priceUnknownNote') }}</p>
    </div>
    <p v-else-if="preview.kind !== 'noop'" class="font-medium" :class="preview.price_delta === 'up' ? 'text-primary-700 dark:text-primary-300' : ''" :data-test="`delta-${preview.price_delta}`">
      {{ t(`admin.pricingOps.stages.priceDelta.${preview.price_delta}`) }}
    </p>

    <!-- 闸门：只有切到 v2 才有 -->
    <section v-if="gate" class="note" data-test="gate">
      <p class="flex items-center gap-1.5 font-medium" :data-test="gate.passed ? 'gate-passed' : 'gate-failed'">
        <Icon :name="gate.passed ? 'checkCircle' : 'xCircle'" size="sm" class="flex-shrink-0" :class="gate.passed ? 'text-emerald-600' : 'text-primary-700 dark:text-primary-300'" />
        {{ t(`admin.pricingOps.stages.gate.${gate.passed ? 'passed' : 'failed'}`) }}
      </p>
      <ul class="mt-2 space-y-1">
        <li data-test="gate-observation" :class="gate.observation.satisfied ? '' : 'font-medium'">{{ observationText }}</li>
        <li data-test="gate-shadow">{{ gate.shadow.translation_diffs > 0 ? t('admin.pricingOps.stages.gate.shadowDiffs', { n: gate.shadow.translation_diffs }) : t('admin.pricingOps.stages.gate.shadowClean') }}</li>
        <li data-test="gate-replay" :class="gate.replay.present && gate.replay.passed ? '' : 'font-medium'">{{ replayText }}</li>
        <li v-if="gate.replay.present" data-test="gate-binding" :class="gate.replay.binding_current ? '' : 'font-medium'">
          {{ t(`admin.pricingOps.stages.gate.${gate.replay.binding_current ? 'bindingCurrent' : 'bindingStale'}`) }}
        </li>
      </ul>
      <ul v-if="gate.failures.length" class="mt-2 list-disc space-y-0.5 pl-5 text-primary-900 dark:text-primary-100" data-test="gate-failures">
        <li v-for="f in gate.failures" :key="f.code" :data-test="`failure-${f.code}`">{{ failureText(f.code) }}</li>
      </ul>
    </section>

    <!-- 回拨 -->
    <section v-if="preview.rollback" class="note" data-test="rollback">
      <p class="font-medium">{{ t('admin.pricingOps.stages.rollback.title') }}</p>
      <ul class="mt-1 space-y-0.5">
        <li data-test="rollback-archived">{{ t('admin.pricingOps.stages.rollback.archived', { cells: preview.rollback.archived_cells, rules: preview.rollback.archived_rules }) }}</li>
        <li data-test="rollback-drift">
          {{ drift.changed ? t('admin.pricingOps.stages.rollback.drift', { inserted: drift.cells_inserted, updated: drift.cells_updated, deleted: drift.cells_deleted, rules: drift.rules_replaced }) : t('admin.pricingOps.stages.rollback.noDrift') }}
        </li>
        <li v-if="drift.config_changed">{{ t('admin.pricingOps.stages.rollback.configChanged') }}</li>
      </ul>
    </section>

    <!-- 已知差异汇总 -->
    <section v-if="preview.kind === 'advance' && preview.to === 'v2'" data-test="accepted">
      <p class="font-medium">{{ t('admin.pricingOps.stages.accepted.title') }}</p>
      <p v-if="preview.accepted_differences.length === 0" class="mt-1 text-gray-500 dark:text-dark-300" data-test="accepted-empty">{{ t('admin.pricingOps.stages.accepted.empty') }}</p>
      <ul v-else class="mt-1 divide-y divide-gray-100 dark:divide-dark-800">
        <li v-for="(d, i) in preview.accepted_differences" :key="i" class="flex flex-wrap items-baseline gap-x-2 py-1" :data-test="`accepted-${i}`">
          <span class="text-xs text-gray-500 dark:text-dark-300">{{ sourceName(d.source) }}</span>
          <span>{{ reasonText(d.reason) }}</span>
          <span v-if="d.model" class="num font-medium text-gray-900 dark:text-white">{{ d.model }}</span>
          <span v-if="d.count > 0" class="num text-xs text-gray-500 dark:text-dark-300">{{ t('admin.pricingOps.stages.accepted.count', { n: d.count }) }}</span>
          <span v-if="d.price_delta !== 'none'" class="ml-auto text-xs" :class="d.price_delta === 'unknown' || d.price_delta === 'up' ? 'font-medium text-primary-700 dark:text-primary-300' : 'text-gray-500 dark:text-dark-300'">
            {{ t(`admin.pricingOps.stages.priceDelta.${d.price_delta}`) }}
          </span>
        </li>
      </ul>
    </section>

    <p v-if="preview.expires_at && preview.executable && preview.approval_id" class="text-xs text-gray-500 dark:text-dark-300" data-test="expires">
      {{ t('admin.pricingOps.stages.previewExpiresAt', { time: formatDateTime(preview.expires_at) }) }}
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { formatDateTime } from '@/utils/format'
import type { OpsStage, StageDrift, StagePreview } from '@/api/admin/pricingOps'
import { observationRemainingHours, roundHours } from '../stageSwitchModel'

const props = defineProps<{ preview: StagePreview; groupName: string }>()
const { t, te } = useI18n()

const fromV2Rollback = computed(() => !!props.preview.rollback)
const gate = computed(() => props.preview.gate ?? null)
const drift = computed<StageDrift>(
  () => props.preview.rollback?.drift ?? { changed: false, config_changed: false, cells_inserted: 0, cells_updated: 0, cells_deleted: 0, rules_replaced: 0 }
)

function stageName(s: OpsStage) {
  return t(`admin.pricingOps.stages.stage.${s}`)
}

const noteText = computed(() => {
  const { to } = props.preview
  if (to === 'v2') return t('admin.pricingOps.stages.confirmNoteV2')
  // 只有从 v2 回拨才有 rollback；shadow 回 legacy 后端也叫 rollback，但分组本来就按渠道配置计费
  if (props.preview.rollback) return t('admin.pricingOps.stages.confirmNoteRollback')
  return t(`admin.pricingOps.stages.confirmNote.${to}`)
})

const observationText = computed(() => {
  const o = gate.value?.observation
  if (!o) return ''
  const params = { observed: roundHours(o.observed_hours), required: roundHours(o.required_hours), left: observationRemainingHours(o), eligible: formatDateTime(o.eligible_at) }
  return t(`admin.pricingOps.stages.gate.${o.satisfied ? 'observationDone' : 'observationShort'}`, params)
})

const replayText = computed(() => {
  const r = gate.value?.replay
  if (!r || !r.present) return t('admin.pricingOps.stages.gate.replayMissing')
  const params = { rows: r.rows_replayed, diffs: r.translation_diffs, errored: r.rows_errored }
  return t(`admin.pricingOps.stages.gate.${r.passed ? 'replayPassed' : 'replayFailed'}`, params)
})

function failureText(code: string) {
  const key = `admin.pricingOps.errors.${code}`
  return te(key) ? t(key) : code
}

function sourceName(source: string) {
  const key = `admin.pricingOps.stages.accepted.source.${source}`
  return te(key) ? t(key) : source
}

function reasonText(reason: string) {
  const key = `admin.pricingOps.stages.accepted.reason.${reason}`
  return te(key) ? t(key) : reason
}
</script>

<style scoped>
.note {
  @apply rounded-md border border-gray-200 bg-gray-50 px-4 py-3 text-sm text-gray-700 dark:border-dark-700 dark:bg-dark-900/40 dark:text-gray-300;
}
.note-signal {
  @apply border-primary-300 bg-primary-50/60 text-primary-900 dark:border-primary-800 dark:bg-primary-950/30 dark:text-primary-100;
}
</style>
