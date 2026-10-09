<template>
  <SideDrawer :show="!!group" :title="group ? t('admin.pricingConfig.write.group.title', { name: group.name }) : ''" :subtitle="subtitle" width="wide" @close="emit('close')">
    <div v-if="group" class="space-y-8" data-test="group-drawer">
      <p v-if="!writable" class="note" data-test="group-readonly">
        <Icon name="lock" size="sm" class="mr-1.5 inline-block align-text-bottom" />{{ t('admin.pricingConfig.write.group.readonly') }}
      </p>

      <!-- 准入方式 -->
      <section>
        <h3 class="section-title">{{ t('admin.pricingConfig.write.group.accessTitle') }}</h3>
        <div class="mt-3 grid grid-cols-2 gap-2" role="radiogroup" :aria-label="t('admin.pricingConfig.write.group.accessTitle')">
          <button
            v-for="opt in accessOptions"
            :key="opt"
            type="button"
            role="radio"
            :aria-checked="draft.access === opt"
            :disabled="!writable"
            class="rounded-md border px-3 py-2.5 text-left text-sm transition-colors disabled:cursor-not-allowed disabled:opacity-60"
            :class="
              draft.access === opt
                ? 'border-gray-900 bg-gray-100 text-gray-900 dark:border-gray-100 dark:bg-dark-700 dark:text-white'
                : 'border-gray-200 text-gray-600 hover:border-gray-300 dark:border-dark-600 dark:text-gray-300 dark:hover:border-dark-500'
            "
            :data-test="`access-${opt}`"
            @click="draft.access = opt"
          >
            <span class="block font-medium">{{ t(`admin.pricingConfig.group.access.${opt}`) }}</span>
            <span class="mt-0.5 block text-xs text-gray-500 dark:text-dark-300">{{ t(`admin.pricingConfig.write.group.accessHint.${opt}`) }}</span>
          </button>
        </div>
      </section>

      <!-- 计费 -->
      <section>
        <h3 class="section-title">{{ t('admin.pricingConfig.write.group.billingTitle') }}</h3>
        <div class="mt-3 space-y-4">
          <div>
            <label class="input-label">{{ t('admin.pricingConfig.write.group.billingSource') }}</label>
            <Select v-model="draft.billingSource" :options="billingOptions" :disabled="!writable" data-test="billing-source" />
          </div>
          <div>
            <label class="input-label">{{ t('admin.pricingConfig.write.group.costMode') }}</label>
            <Select v-model="draft.cost" :options="costOptions" :disabled="!writable" data-test="cost-mode" />
          </div>
        </div>
      </section>

      <!-- 模型映射 -->
      <section>
        <div class="flex items-center justify-between gap-3">
          <h3 class="section-title">{{ t('admin.pricingConfig.write.group.mappingTitle') }}</h3>
          <button v-if="writable" type="button" class="btn btn-secondary btn-sm" data-test="mapping-add" @click="draft.mapping.push({ src: '', dst: '' })">
            {{ t('admin.pricingConfig.write.group.addMapping') }}
          </button>
        </div>
        <p v-if="draft.mapping.length === 0" class="mt-3 text-sm text-gray-500 dark:text-dark-300">{{ t('admin.pricingConfig.write.group.noMapping') }}</p>
        <ul v-else class="mt-3 space-y-2">
          <li v-for="(m, i) in draft.mapping" :key="i" class="flex items-center gap-2" :data-test="`mapping-row-${i}`">
            <input v-model="m.src" type="text" class="input num py-1.5 text-sm" :disabled="!writable" :placeholder="t('admin.channels.form.mappingSource')" :aria-label="t('admin.channels.form.mappingSource')" />
            <Icon name="arrowRight" size="sm" class="flex-shrink-0 text-gray-400 dark:text-dark-400" />
            <input v-model="m.dst" type="text" class="input num py-1.5 text-sm" :disabled="!writable" :placeholder="t('admin.channels.form.mappingTarget')" :aria-label="t('admin.channels.form.mappingTarget')" />
            <button v-if="writable" type="button" class="rounded p-1.5 text-gray-400 hover:bg-gray-100 hover:text-gray-700 dark:hover:bg-dark-700 dark:hover:text-gray-200" :aria-label="t('common.delete')" @click="draft.mapping.splice(i, 1)">
              <Icon name="x" size="sm" />
            </button>
          </li>
        </ul>
        <p v-if="mappingBad" class="input-error-text mt-2" data-test="mapping-bad">{{ t('admin.pricingConfig.write.group.mappingInvalid') }}</p>
      </section>

      <!-- 功能开关 -->
      <section v-if="featureKeys.length">
        <h3 class="section-title">{{ t('admin.pricingConfig.write.group.featuresTitle') }}</h3>
        <ul class="mt-3 space-y-3">
          <li v-for="k in featureKeys" :key="k" class="flex items-center justify-between gap-3">
            <span class="text-sm text-gray-800 dark:text-gray-200">{{ featureLabel(k) }}</span>
            <span v-if="isToggleFeature(draft.features[k])" :class="writable ? '' : 'pointer-events-none opacity-60'">
              <Toggle :model-value="draft.features[k] as boolean" @update:model-value="(v: boolean) => (draft.features[k] = v)" />
            </span>
            <span v-else class="text-xs text-gray-500 dark:text-dark-300">{{ t('admin.pricingConfig.write.group.perPlatform') }}</span>
          </li>
        </ul>
      </section>

      <!-- 发布预检 -->
      <section data-test="publish-check">
        <div class="flex items-center justify-between gap-3">
          <h3 class="section-title">{{ t('admin.pricingConfig.write.group.publishTitle') }}</h3>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="checking" data-test="publish-check-btn" @click="runCheck">
            {{ checking ? t('admin.pricingConfig.write.group.checking') : t('admin.pricingConfig.write.group.check') }}
          </button>
        </div>
        <p class="mt-2 text-xs text-gray-500 dark:text-dark-300">{{ t('admin.pricingConfig.write.group.publishHint') }}</p>
        <WriteErrorNote v-if="checkError" class="mt-3" :error="checkError" />
        <div v-else-if="report" class="mt-3 space-y-2 text-sm" data-test="publish-report">
          <p v-if="!report.applicable" class="text-gray-700 dark:text-gray-300">{{ t('admin.pricingConfig.write.group.checkNotApplicable') }}</p>
          <p v-else-if="report.blocking.length === 0 && report.warnings.length === 0" class="text-gray-700 dark:text-gray-300">{{ t('admin.pricingConfig.write.group.checkOk') }}</p>
          <template v-else>
            <div v-if="report.blocking.length" class="note-signal" role="alert">
              <p class="font-medium">{{ t('admin.pricingConfig.write.group.checkBlocking', { n: report.blocking.length }) }}</p>
              <ul class="mt-1 space-y-0.5"><li v-for="(i, n) in report.blocking" :key="n">{{ issueText(i) }}</li></ul>
            </div>
            <div v-if="report.warnings.length" class="note-signal" data-test="publish-warnings">
              <p class="font-medium">{{ t('admin.pricingConfig.write.group.checkWarnings', { n: report.warnings.length }) }}</p>
              <ul class="mt-1 space-y-0.5"><li v-for="(i, n) in report.warnings" :key="n">{{ issueText(i) }}</li></ul>
            </div>
          </template>
        </div>
      </section>
    </div>

    <template #footer>
      <span v-if="dirty && mappingBad" class="mr-auto text-xs text-primary-700 dark:text-primary-300">{{ t('admin.pricingConfig.write.group.mappingInvalid') }}</span>
      <button type="button" class="btn btn-secondary" :disabled="!dirty" data-test="group-reset" @click="resetDraft">{{ t('admin.pricingConfig.write.group.reset') }}</button>
      <button type="button" class="btn btn-primary" :disabled="!writable || !dirty || mappingBad" data-test="group-save" @click="toPreview">
        {{ t('admin.pricingConfig.write.group.save') }}
      </button>
    </template>
  </SideDrawer>

  <GroupConfigPlanDialog v-if="group" :group-id="group.id" :request="planRequest" @close="planRequest = null" @done="onDone" />
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'
import { adminAPI } from '@/api/admin'
import type { GroupConfigRequest, PrecheckIssue, PrecheckReport } from '@/api/admin/pricing'
import SideDrawer from './SideDrawer.vue'
import GroupConfigPlanDialog from './GroupConfigPlanDialog.vue'
import WriteErrorNote from './WriteErrorNote.vue'
import { usePricingData } from '../usePricingData'
import { toWriteError, type WriteError } from '../pricingErrors'
import { platformLabel } from '../pricingModel'
import { findDerive, groupRevisionOf, isGroupWritable } from '../pricingWrite'
import {
  FEATURE_LABEL_KEY,
  buildConfigRequest,
  currentConfig,
  hasChanges,
  isToggleFeature,
  mappingInvalid,
  toDraft,
  type ConfigDraft
} from '../groupConfig'

const props = defineProps<{ groupId: number | null }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const { t } = useI18n()
const { state } = usePricingData()

const group = computed(() => state.groups.find((g) => g.id === props.groupId) ?? null)
const view = computed(() => (group.value ? findDerive(state.derives, group.value.id) : undefined))
const writable = computed(() => !!group.value && isGroupWritable(group.value, view.value))
const current = computed(() => currentConfig(view.value))
const subtitle = computed(() =>
  group.value ? `${platformLabel(group.value.platform)}，${t('admin.pricingConfig.group.rate', { rate: String(Number(group.value.rate.toFixed(4))) })}` : ''
)

const accessOptions = ['open', 'allowlist'] as const
const billingOptions = computed(() => [
  { value: null, label: t('admin.pricingConfig.write.group.billing.none') },
  { value: 'requested', label: t('admin.channels.form.billingModelSourceRequested') },
  { value: 'upstream', label: t('admin.channels.form.billingModelSourceUpstream') },
  { value: 'channel_mapped', label: t('admin.channels.form.billingModelSourceChannelMapped') }
])
const costOptions = computed(() =>
  (['account_rate', 'catalog_upstream', 'follow_billing'] as const).map((v) => ({ value: v, label: t(`admin.pricingConfig.write.group.cost.${v}`) }))
)

const draft = reactive<ConfigDraft>(toDraft(current.value))
function resetDraft() {
  Object.assign(draft, toDraft(current.value))
}
// 打开另一个分组，或数据刷新之后，草稿回到库里的现状
watch(() => [props.groupId, current.value], resetDraft, { deep: true })

const featureKeys = computed(() => Object.keys(draft.features).sort())
const featureLabel = (k: string) => (FEATURE_LABEL_KEY[k] ? t(FEATURE_LABEL_KEY[k]) : k)
const mappingBad = computed(() => mappingInvalid(draft.mapping))

const baseline = computed(() => groupRevisionOf(view.value) ?? 0)
const request = computed<GroupConfigRequest>(() => buildConfigRequest(current.value, draft, baseline.value))
const dirty = computed(() => hasChanges(request.value))

const planRequest = ref<GroupConfigRequest | null>(null)
function toPreview() {
  if (!writable.value || !dirty.value || mappingBad.value) return
  planRequest.value = { ...request.value }
}
function onDone() {
  planRequest.value = null
  report.value = null
}

// ---- 发布预检
const report = ref<PrecheckReport | null>(null)
const checkError = ref<WriteError | null>(null)
const checking = ref(false)
watch(
  () => props.groupId,
  () => {
    report.value = null
    checkError.value = null
  }
)

async function runCheck() {
  if (!group.value) return
  checking.value = true
  checkError.value = null
  report.value = null
  try {
    report.value = await adminAPI.pricing.getPublishCheck(group.value.id)
  } catch (err) {
    checkError.value = toWriteError(err)
  } finally {
    checking.value = false
  }
}

function issueText(i: PrecheckIssue): string {
  return `${i.model_key}：${t(`admin.pricingConfig.write.issue.${i.reason}`, { target: i.target ?? '' })}`
}
</script>

<style scoped>
.section-title {
  @apply font-serif text-base font-medium text-gray-900 dark:text-white;
}
.note {
  @apply rounded-md border-l-2 border-gray-400 bg-gray-100 px-3 py-2.5 text-sm text-gray-800;
  @apply dark:border-dark-400 dark:bg-dark-900/60 dark:text-gray-200;
}
.note-signal {
  @apply rounded-md border-l-2 border-primary-600 bg-primary-50 px-3 py-2.5 text-sm text-primary-900;
  @apply dark:border-primary-500 dark:bg-primary-950/40 dark:text-primary-100;
}
</style>
