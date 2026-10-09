<template>
  <BaseDialog :show="!!model" :title="title" width="normal" @close="close">
    <div v-if="model" class="space-y-4 text-sm text-gray-700 dark:text-gray-300" data-test="transition-body">
      <template v-if="kind === 'retire'">
        <p>{{ t('admin.pricingConfig.write.retire.intro') }}</p>
        <div v-if="loading" class="flex min-h-[4rem] items-center justify-center"><LoadingSpinner /></div>
        <template v-else-if="usage">
          <div class="rounded-md border border-gray-200 px-4 py-3 dark:border-dark-700">
            <p class="num" :class="usage.requests > 0 ? 'font-semibold text-primary-700 dark:text-primary-300' : 'text-gray-700 dark:text-gray-300'" data-test="retire-usage">
              {{ t('admin.pricingConfig.write.usage.requests', { n: usage.requests, days: usage.window_days }) }}
            </p>
            <p v-if="usage.last_used_at" class="mt-0.5 text-xs text-gray-500 dark:text-dark-300">
              {{ t('admin.pricingConfig.write.usage.lastUsed', { time: formatTime(usage.last_used_at) }) }}
            </p>
            <p class="mt-1.5 text-xs text-gray-500 dark:text-dark-300">
              {{ openGroupNames.length ? t('admin.pricingConfig.write.retire.groups', { names: openGroupNames.join('、') }) : t('admin.pricingConfig.write.retire.noGroups') }}
            </p>
          </div>
          <label v-if="needsConfirm" class="flex items-start gap-2" data-test="retire-ack">
            <input v-model="acked" type="checkbox" class="mt-0.5 h-4 w-4 rounded border-gray-300 accent-primary-600" />
            <span>{{ t('admin.pricingConfig.write.retire.ack', { n: usage.requests }) }}</span>
          </label>
        </template>
      </template>
      <p v-else>{{ t('admin.pricingConfig.write.reactivate.intro') }}</p>
      <WriteErrorNote v-if="error" :error="error" @action="onAction" />
    </div>
    <template #footer>
      <button type="button" class="btn btn-secondary" :disabled="busy" @click="close">{{ t('common.cancel') }}</button>
      <button type="button" class="btn btn-primary" :disabled="!canConfirm" data-test="transition-confirm" @click="confirm">
        {{ kind === 'retire' ? t('admin.pricingConfig.write.retire.confirm') : t('admin.pricingConfig.write.reactivate.confirm') }}
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
import type { CatalogUsage } from '@/api/admin/pricing'
import { useAppStore } from '@/stores/app'
import WriteErrorNote from './WriteErrorNote.vue'
import { usePricingData } from '../usePricingData'
import { toWriteError, type ErrorAction, type WriteError } from '../pricingErrors'
import { cellKey, cellView, isOpenCell, type ModelRow } from '../pricingModel'

const props = defineProps<{ model: ModelRow | null; kind: 'retire' | 'reactivate' }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const { t } = useI18n()
const app = useAppStore()
const { state, refresh } = usePricingData()

const loading = ref(false)
const busy = ref(false)
const usage = ref<CatalogUsage | null>(null)
const needsConfirm = ref(false)
const acked = ref(false)
const error = ref<WriteError | null>(null)

const entry = computed(() => (props.model ? state.catalog.find((e) => e.platform === props.model!.platform && e.model_key === props.model!.key) : undefined))
const title = computed(() =>
  props.model ? t(`admin.pricingConfig.write.${props.kind}.title`, { model: props.model.key }) : ''
)

const openGroupNames = computed(() => {
  const m = props.model
  if (!m) return []
  return state.groups
    .filter((g) => g.platform === m.platform && isOpenCell(cellView(state.cells[cellKey(g.id, m.key)], g)))
    .map((g) => g.name)
})

const canConfirm = computed(() => {
  if (busy.value || loading.value || !entry.value) return false
  if (props.kind === 'retire') return !!usage.value && (!needsConfirm.value || acked.value)
  return true
})

watch(
  () => props.model,
  async (m) => {
    usage.value = null
    needsConfirm.value = false
    acked.value = false
    error.value = null
    if (!m || props.kind !== 'retire') return
    const e = entry.value
    if (!e) return
    loading.value = true
    try {
      const preview = await adminAPI.pricing.previewCatalogTransition(e.id, 'retired')
      usage.value = preview.usage
      needsConfirm.value = preview.confirm_required
    } catch (err) {
      error.value = toWriteError(err)
    } finally {
      loading.value = false
    }
  },
  { immediate: true }
)

function close() {
  if (!busy.value) emit('close')
}

async function confirm() {
  const e = entry.value
  if (!e || !canConfirm.value) return
  busy.value = true
  error.value = null
  try {
    await adminAPI.pricing.transitionCatalog(e.id, props.kind === 'retire' ? 'retired' : 'active', needsConfirm.value && acked.value)
    app.showSuccess(t(`admin.pricingConfig.write.${props.kind}.done`, { model: e.model_key }))
    await refresh()
    emit('close')
  } catch (err) {
    const we = toWriteError(err)
    // 预览之后用量变成了需要确认：补上用量，让管理员勾选后再提交
    if (we.reason === 'MODEL_CATALOG_USAGE_CONFIRM_REQUIRED') {
      needsConfirm.value = true
      acked.value = false
      usage.value = {
        requests: Number(we.metadata.requests ?? 0),
        window_days: Number(we.metadata.window_days ?? 7),
        last_used_at: we.metadata.last_used_at ?? null
      }
    } else {
      error.value = we
    }
  } finally {
    busy.value = false
  }
}

async function onAction(action: ErrorAction) {
  if (action === 'refresh') {
    await refresh()
    emit('close')
  }
}

function formatTime(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString()
}
</script>
