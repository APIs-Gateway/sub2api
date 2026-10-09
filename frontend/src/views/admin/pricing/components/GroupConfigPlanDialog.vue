<template>
  <BaseDialog :show="!!request" :title="t('admin.pricingConfig.write.group.planTitle', { name: groupName })" width="wide" @close="close">
    <div class="space-y-4" data-test="group-plan">
      <div v-if="phase === 'previewing' || (phase === 'idle' && !error)" class="flex min-h-[8rem] items-center justify-center"><LoadingSpinner /></div>
      <WriteErrorNote v-if="error" :error="error" @action="onAction" />
      <template v-if="ticket">
        <p class="text-sm text-gray-700 dark:text-gray-300">{{ ticket.changed ? t('admin.pricingConfig.write.group.planIntro') : t('admin.pricingConfig.write.group.planNoChange') }}</p>

        <div v-if="ticket.price_delta === 'up' || ticket.price_delta === 'unknown'" class="note-signal" role="alert" data-test="group-delta-warn">
          <p class="font-medium">{{ t(`admin.pricingConfig.write.delta.${ticket.price_delta}`) }}</p>
          <p class="mt-0.5 text-xs opacity-80">{{ t(`admin.pricingConfig.write.delta.${ticket.price_delta}Hint`) }}</p>
        </div>
        <p v-else-if="ticket.touches_price" class="text-sm text-gray-700 dark:text-gray-300">{{ t('admin.pricingConfig.write.group.touchesPrice') }}</p>

        <div class="overflow-hidden rounded-md border border-gray-200 dark:border-dark-700">
          <table class="w-full text-sm" data-test="group-plan-table">
            <thead>
              <tr class="border-b border-gray-300 text-left text-xs font-medium text-gray-500 dark:border-dark-600 dark:text-dark-400">
                <th class="px-4 py-3">{{ t('admin.pricingConfig.write.group.field') }}</th>
                <th class="px-4 py-3">{{ t('admin.pricingConfig.write.col.before') }}</th>
                <th class="px-4 py-3">{{ t('admin.pricingConfig.write.col.after') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in rows" :key="r.key" class="border-b border-gray-100 last:border-b-0 dark:border-dark-800" :data-test="`group-plan-${r.key}`">
                <td class="px-4 py-3 align-top font-medium text-gray-900 dark:text-white">{{ r.label }}</td>
                <td class="px-4 py-3 align-top text-gray-500 dark:text-dark-300"><span class="whitespace-pre-line">{{ r.before }}</span></td>
                <td class="px-4 py-3 align-top text-gray-900 dark:text-white"><span class="whitespace-pre-line">{{ r.after }}</span></td>
              </tr>
              <tr v-if="!rows.length">
                <td colspan="3" class="px-4 py-8 text-center text-gray-500 dark:text-dark-300">{{ t('admin.pricingConfig.write.noChanges') }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <div v-if="warnings.length" class="note-signal" data-test="group-plan-warnings">
          <p class="font-medium">{{ t('admin.pricingConfig.write.warnTitle', { n: warnings.length }) }}</p>
          <ul class="mt-1 space-y-0.5">
            <li v-for="(w, i) in warnings" :key="i">{{ w.model_key }}：{{ t(`admin.pricingConfig.write.issue.${w.reason}`, { target: w.target ?? '' }) }}</li>
          </ul>
        </div>
        <label v-if="warnings.length" class="flex items-start gap-2 text-sm text-gray-700 dark:text-gray-300" data-test="group-plan-ack">
          <input v-model="acked" type="checkbox" class="mt-0.5 h-4 w-4 rounded border-gray-300 accent-primary-600" />
          <span>{{ t('admin.pricingConfig.write.ack', { n: warnings.length }) }}</span>
        </label>
      </template>
    </div>
    <template #footer>
      <button type="button" class="btn btn-secondary" :disabled="phase === 'committing'" @click="close">{{ t('admin.pricingConfig.write.back') }}</button>
      <button type="button" class="btn btn-primary" :disabled="!canConfirm" data-test="group-plan-confirm" @click="confirm">
        {{ phase === 'committing' ? t('admin.pricingConfig.write.submitting') : t('admin.pricingConfig.write.submit') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import { adminAPI } from '@/api/admin'
import type { GroupConfigRequest, GroupConfigState, GroupConfigTicket } from '@/api/admin/pricing'
import { useAppStore } from '@/stores/app'
import WriteErrorNote from './WriteErrorNote.vue'
import { usePricingData } from '../usePricingData'
import { toWriteError, type ErrorAction, type WriteError } from '../pricingErrors'
import { FEATURE_LABEL_KEY } from '../groupConfig'

const props = defineProps<{ groupId: number; request: GroupConfigRequest | null }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'done'): void }>()

const { t } = useI18n()
const app = useAppStore()
const { state, refresh } = usePricingData()

type Phase = 'idle' | 'previewing' | 'ready' | 'committing'
const phase = ref<Phase>('idle')
const ticket = ref<GroupConfigTicket | null>(null)
const error = ref<WriteError | null>(null)
const acked = ref(false)

const groupName = computed(() => state.groups.find((g) => g.id === props.groupId)?.name ?? `#${props.groupId}`)
const warnings = computed(() => ticket.value?.precheck?.warnings ?? [])
const canConfirm = computed(() => phase.value === 'ready' && !!ticket.value && (warnings.value.length === 0 || acked.value))

async function runPreview() {
  if (!props.request) return
  phase.value = 'previewing'
  error.value = null
  ticket.value = null
  acked.value = false
  try {
    ticket.value = await adminAPI.pricing.previewGroupConfig(props.groupId, props.request)
    phase.value = 'ready'
  } catch (err) {
    error.value = toWriteError(err)
    phase.value = 'idle'
  }
}

watch(
  () => props.request,
  (req) => {
    phase.value = 'idle'
    ticket.value = null
    error.value = null
    if (req) void runPreview()
  },
  { immediate: true }
)

function mappingText(list: GroupConfigState['model_mapping']): string {
  return list.length ? list.map((m) => `${m.src} → ${m.dst}`).join('\n') : t('admin.pricingConfig.write.group.none')
}

function featureText(f: Record<string, unknown>): string {
  const keys = Object.keys(f)
  if (!keys.length) return t('admin.pricingConfig.write.group.none')
  return keys
    .sort()
    .map((k) => {
      const v = f[k]
      const label = FEATURE_LABEL_KEY[k] ? t(FEATURE_LABEL_KEY[k]) : k
      return `${label}：${typeof v === 'boolean' ? t(v ? 'common.enabled' : 'common.disabled') : t('admin.pricingConfig.write.group.perPlatform')}`
    })
    .join('\n')
}

const rows = computed(() => {
  const tk = ticket.value
  if (!tk) return []
  const b = tk.before
  const a = tk.after
  const g = (key: string) => t(`admin.pricingConfig.write.group.${key}`)
  const out: { key: string; label: string; before: string; after: string }[] = []
  if (b.access_mode !== a.access_mode)
    out.push({ key: 'access', label: g('accessTitle'), before: t(`admin.pricingConfig.group.access.${b.access_mode}`), after: t(`admin.pricingConfig.group.access.${a.access_mode}`) })
  if (b.billing_model_source !== a.billing_model_source) {
    const name = (v: string | null) => (v ? t(`admin.pricingConfig.write.group.billing.${v}`) : g('billing.none'))
    out.push({ key: 'billing', label: g('billingSource'), before: name(b.billing_model_source), after: name(a.billing_model_source) })
  }
  if (b.cost_mode !== a.cost_mode)
    out.push({ key: 'cost', label: g('costMode'), before: t(`admin.pricingConfig.write.group.cost.${b.cost_mode}`), after: t(`admin.pricingConfig.write.group.cost.${a.cost_mode}`) })
  if (JSON.stringify(b.model_mapping ?? []) !== JSON.stringify(a.model_mapping ?? []))
    out.push({ key: 'mapping', label: g('mappingTitle'), before: mappingText(b.model_mapping ?? []), after: mappingText(a.model_mapping ?? []) })
  if (JSON.stringify(b.features ?? {}) !== JSON.stringify(a.features ?? {}))
    out.push({ key: 'features', label: g('featuresTitle'), before: featureText(b.features ?? {}), after: featureText(a.features ?? {}) })
  return out
})

function close() {
  if (phase.value !== 'committing') emit('close')
}

async function confirm() {
  if (!canConfirm.value || !props.request || !ticket.value) return
  phase.value = 'committing'
  error.value = null
  try {
    await adminAPI.pricing.commitGroupConfig(props.groupId, ticket.value.approval_id, props.request)
    app.showSuccess(t('admin.pricingConfig.write.group.saved'))
    await refresh()
    emit('done')
  } catch (err) {
    error.value = toWriteError(err)
    phase.value = 'ready'
  }
}

async function onAction(action: ErrorAction) {
  if (action === 'refresh') {
    await refresh()
    app.showWarning(t('admin.pricingConfig.write.refreshed'))
    emit('close')
  } else if (action === 'repreview') {
    await runPreview()
  }
}
</script>

<style scoped>
.note-signal {
  @apply rounded-md border-l-2 border-primary-600 bg-primary-50 px-3 py-2.5 text-sm text-primary-900;
  @apply dark:border-primary-500 dark:bg-primary-950/40 dark:text-primary-100;
}
</style>
