<template>
  <BaseDialog :show="show" :title="t('admin.pricingConfig.write.create.title')" width="normal" @close="close">
    <form class="space-y-4" data-test="create-model-form" @submit.prevent="submit">
      <div>
        <label class="input-label">{{ t('admin.pricingConfig.models.columns.platform') }}</label>
        <Select v-model="platform" :options="platformOptions" />
      </div>
      <div>
        <label class="input-label" for="cm-key">{{ t('admin.pricingConfig.write.create.modelKey') }}</label>
        <input id="cm-key" v-model="modelKey" type="text" class="input" :class="{ 'input-error': keyInvalid }" autocomplete="off" data-test="create-model-key" />
        <p v-if="keyInvalid" class="input-error-text">{{ t('admin.pricingConfig.write.create.keyInvalid') }}</p>
      </div>
      <div>
        <label class="input-label" for="cm-name">{{ t('admin.pricingConfig.write.create.displayName') }}</label>
        <input id="cm-name" v-model="displayName" type="text" class="input" autocomplete="off" />
      </div>
      <div>
        <label class="input-label" for="cm-alias">{{ t('admin.pricingConfig.drawer.aliases') }}</label>
        <input id="cm-alias" v-model="aliasText" type="text" class="input" :placeholder="t('admin.pricingConfig.write.create.aliasPlaceholder')" autocomplete="off" />
      </div>
      <div>
        <label class="input-label">{{ t('admin.pricingConfig.drawer.referenceModel') }}</label>
        <Select v-model="reference" :options="referenceOptions" :placeholder="t('admin.pricingConfig.write.create.referencePlaceholder')" clearable />
        <p class="input-hint">{{ t('admin.pricingConfig.write.create.referenceHint') }}</p>
      </div>
      <p class="text-xs text-gray-500 dark:text-dark-300">{{ t('admin.pricingConfig.write.create.draftHint') }}</p>

      <div v-if="usageConfirm" class="note" data-test="create-usage">
        <p class="font-medium">{{ t('admin.pricingConfig.write.usage.requests', { n: usageConfirm.requests, days: usageConfirm.days }) }}</p>
        <label class="mt-1.5 flex items-start gap-2 text-sm">
          <input v-model="usageAcked" type="checkbox" class="mt-0.5 h-4 w-4 rounded border-gray-300 accent-primary-600" data-test="create-usage-ack" />
          <span>{{ t('admin.pricingConfig.write.create.usageAck') }}</span>
        </label>
      </div>
      <WriteErrorNote v-if="error" :error="error" @action="onAction" />
    </form>
    <template #footer>
      <button type="button" class="btn btn-secondary" :disabled="busy" @click="close">{{ t('common.cancel') }}</button>
      <button type="button" class="btn btn-primary" :disabled="!canSubmit" data-test="create-model-submit" @click="submit">
        {{ t('admin.pricingConfig.write.create.submit') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import WriteErrorNote from './WriteErrorNote.vue'
import { usePricingData } from '../usePricingData'
import { toWriteError, type ErrorAction, type WriteError } from '../pricingErrors'
import { PLATFORM_ORDER, platformLabel } from '../pricingModel'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const { t } = useI18n()
const app = useAppStore()
const { rows, platforms, refresh } = usePricingData()

const platform = ref<string | number | boolean | null>('')
const modelKey = ref('')
const displayName = ref('')
const aliasText = ref('')
const reference = ref<string | number | boolean | null>(null)
const busy = ref(false)
const error = ref<WriteError | null>(null)
const usageConfirm = ref<{ requests: number; days: number } | null>(null)
const usageAcked = ref(false)

const platformOptions = computed(() => {
  const set = new Set<string>([...PLATFORM_ORDER, ...platforms.value])
  return [...set].map((p) => ({ value: p, label: platformLabel(p) }))
})
const referenceOptions = computed(() =>
  rows.value.filter((r) => r.platform === platform.value && r.status === 'active').map((r) => ({ value: r.key, label: r.key }))
)

watch(
  () => props.show,
  (open) => {
    if (!open) return
    platform.value = platforms.value[0] ?? PLATFORM_ORDER[0]
    modelKey.value = ''
    displayName.value = ''
    aliasText.value = ''
    reference.value = null
    error.value = null
    usageConfirm.value = null
    usageAcked.value = false
  }
)
watch(platform, () => {
  reference.value = null
})

const trimmedKey = computed(() => modelKey.value.trim())
const keyInvalid = computed(() => modelKey.value !== '' && (trimmedKey.value === '' || trimmedKey.value.includes('*') || /\s/.test(trimmedKey.value)))
const canSubmit = computed(
  () => !busy.value && !!platform.value && trimmedKey.value !== '' && !keyInvalid.value && (!usageConfirm.value || usageAcked.value)
)

function close() {
  if (!busy.value) emit('close')
}

async function submit() {
  if (!canSubmit.value) return
  busy.value = true
  error.value = null
  try {
    const key = trimmedKey.value
    const aliases = aliasText.value
      .split(/[,，\s]+/)
      .map((a) => a.trim())
      .filter(Boolean)
    await adminAPI.pricing.createCatalogEntry({
      model_key: key,
      platform: String(platform.value),
      display_name: displayName.value.trim() || key,
      aliases,
      reference_model: reference.value ? String(reference.value) : null,
      status: 'draft',
      note: '',
      confirm_usage: !!usageConfirm.value && usageAcked.value
    })
    app.showSuccess(t('admin.pricingConfig.write.create.done', { model: key }))
    await refresh()
    emit('close')
  } catch (err) {
    const we = toWriteError(err)
    if (we.reason === 'MODEL_CATALOG_USAGE_CONFIRM_REQUIRED') {
      usageConfirm.value = { requests: Number(we.metadata.requests ?? 0), days: Number(we.metadata.window_days ?? 7) }
      usageAcked.value = false
    } else {
      error.value = we
    }
  } finally {
    busy.value = false
  }
}

function onAction(action: ErrorAction) {
  if (action === 'refresh') void refresh()
}
</script>

<style scoped>
.note {
  @apply rounded-md border-l-2 border-primary-600 bg-primary-50 px-3 py-2.5 text-sm text-primary-900;
  @apply dark:border-primary-500 dark:bg-primary-950/40 dark:text-primary-100;
}
</style>
