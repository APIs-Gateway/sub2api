<template>
  <SideDrawer :show="show" :title="title" :subtitle="t('admin.pricingOps.costRules.drawerSubtitle', { group: groupName })" width="wide" @close="emit('close')">
    <form class="space-y-6" data-test="cost-form" @submit.prevent="submit">
      <section class="space-y-4">
        <div>
          <label class="input-label" for="cr-name">{{ t('admin.pricingOps.costRules.field.name') }}</label>
          <input id="cr-name" v-model="form.name" type="text" class="input" :maxlength="NAME_MAX" data-test="cr-name" />
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <div>
            <label class="input-label" for="cr-groups">{{ t('admin.pricingOps.costRules.field.groupIds') }}</label>
            <input id="cr-groups" v-model="form.groupIdsText" type="text" class="input num" data-test="cr-groups" />
            <p class="input-hint">{{ t('admin.pricingOps.costRules.field.groupIdsHint') }}</p>
          </div>
          <div>
            <label class="input-label" for="cr-accounts">{{ t('admin.pricingOps.costRules.field.accountIds') }}</label>
            <input id="cr-accounts" v-model="form.accountIdsText" type="text" class="input num" data-test="cr-accounts" />
            <p class="input-hint">{{ t('admin.pricingOps.costRules.field.accountIdsHint') }}</p>
          </div>
          <div>
            <label class="input-label" for="cr-sort">{{ t('admin.pricingOps.costRules.field.sortOrder') }}</label>
            <input id="cr-sort" v-model="form.sortOrder" type="number" step="1" class="input num" data-test="cr-sort" />
            <p class="input-hint">{{ t('admin.pricingOps.costRules.field.sortOrderHint') }}</p>
          </div>
          <label class="flex items-center gap-2 self-end pb-2 text-sm text-gray-700 dark:text-gray-300">
            <input v-model="form.enabled" type="checkbox" class="h-4 w-4 rounded border-gray-300 accent-primary-600" data-test="cr-enabled" />
            {{ t('admin.pricingOps.costRules.field.enabled') }}
          </label>
        </div>
      </section>

      <section class="space-y-3">
        <div>
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.pricingOps.costRules.pricesTitle') }}</h3>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-300">{{ t('admin.pricingOps.costRules.pricesHint') }}</p>
        </div>

        <div v-for="(r, i) in form.rows" :key="i" class="space-y-3 rounded-md border border-gray-200 p-4 dark:border-dark-700" :data-test="`cr-row-${i}`">
          <div class="flex items-center justify-between gap-3">
            <p class="text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.pricingOps.costRules.rowTitle', { n: i + 1 }) }}</p>
            <button v-if="form.rows.length > 1" type="button" class="text-xs font-medium text-gray-600 underline decoration-gray-300 underline-offset-2 hover:text-gray-900 dark:text-gray-300 dark:hover:text-white" :data-test="`cr-remove-${i}`" @click="removeRow(i)">
              {{ t('admin.pricingOps.costRules.removeRow') }}
            </button>
          </div>
          <div class="grid gap-3 sm:grid-cols-2">
            <div>
              <label class="input-label" :for="`cr-platform-${i}`">{{ t('admin.pricingOps.costRules.field.platform') }}</label>
              <select :id="`cr-platform-${i}`" v-model="r.platform" class="input">
                <option value="">{{ t('admin.pricingOps.costRules.anyPlatform') }}</option>
                <option v-for="p in PLATFORM_ORDER" :key="p" :value="p">{{ platformLabel(p) }}</option>
              </select>
            </div>
            <div>
              <label class="input-label" :for="`cr-mode-${i}`">{{ t('admin.pricingOps.costRules.field.billingMode') }}</label>
              <select :id="`cr-mode-${i}`" v-model="r.mode" class="input" :data-test="`cr-mode-${i}`">
                <option v-for="m in MODES" :key="m" :value="m">{{ t(`admin.pricingOps.costRules.mode.${m}`) }}</option>
              </select>
            </div>
          </div>
          <div>
            <label class="input-label" :for="`cr-models-${i}`">{{ t('admin.pricingOps.costRules.field.models') }}</label>
            <textarea :id="`cr-models-${i}`" v-model="r.modelsText" rows="3" class="input num" :data-test="`cr-models-${i}`" />
            <p class="input-hint">{{ t('admin.pricingOps.costRules.field.modelsHint') }}</p>
          </div>

          <div v-if="r.mode === 'token'" class="grid gap-3 sm:grid-cols-2">
            <div v-for="f in TOKEN_FIELDS" :key="f.key">
              <label class="input-label" :for="`cr-${f.key}-${i}`">{{ t(`admin.pricingOps.costRules.field.${f.key}`) }}</label>
              <input :id="`cr-${f.key}-${i}`" v-model="r[f.key]" type="number" min="0" step="any" class="input num" :data-test="`cr-${f.key}-${i}`" />
            </div>
          </div>
          <div v-else class="grid gap-3 sm:grid-cols-2">
            <div>
              <label class="input-label" :for="`cr-perRequest-${i}`">{{ t('admin.pricingOps.costRules.field.perRequest') }}</label>
              <input :id="`cr-perRequest-${i}`" v-model="r.perRequest" type="number" min="0" step="any" class="input num" :data-test="`cr-perRequest-${i}`" />
            </div>
            <div v-if="r.mode === 'image'">
              <label class="input-label" :for="`cr-imageOutput-${i}`">{{ t('admin.pricingOps.costRules.field.imageOutput') }}</label>
              <input :id="`cr-imageOutput-${i}`" v-model="r.imageOutput" type="number" min="0" step="any" class="input num" />
            </div>
          </div>
          <p class="text-xs text-gray-500 dark:text-dark-300">
            {{ unitText(r.mode) }}
          </p>
          <p v-if="r.intervals.length" class="text-xs text-gray-500 dark:text-dark-300" data-test="cr-intervals-note">
            {{ t('admin.pricingOps.costRules.intervalsKept', { n: r.intervals.length }) }}
          </p>
        </div>

        <button type="button" class="btn btn-secondary btn-sm" :disabled="form.rows.length >= ROWS_MAX" data-test="cr-add-row" @click="form.rows.push(emptyRow())">
          {{ t('admin.pricingOps.costRules.addRow') }}
        </button>
      </section>

      <p v-if="shownError" class="rounded-md border border-primary-300 bg-primary-50/60 px-4 py-3 text-sm text-primary-900 dark:border-primary-800 dark:bg-primary-950/30 dark:text-primary-100" role="alert" data-test="cr-error">
        {{ shownError }}
      </p>
    </form>

    <template #footer>
      <button type="button" class="btn btn-secondary" :disabled="saving" @click="emit('close')">{{ t('common.cancel') }}</button>
      <button type="button" class="btn btn-primary" :disabled="saving" data-test="cr-save" @click="submit">
        {{ saving ? t('common.saving') : t('common.save') }}
      </button>
    </template>
  </SideDrawer>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CostBillingMode, CostRuleBody, StoredCostRule } from '@/api/admin/pricingOps'
import SideDrawer from './SideDrawer.vue'
import { buildRuleBody, emptyForm, emptyRow, fromRule, NAME_MAX, ROWS_MAX, type RuleForm } from '../costRuleForm'
import { PLATFORM_ORDER, platformLabel } from '../pricingModel'

const props = defineProps<{
  show: boolean
  /** 编辑时传规则，新建时为 null */
  rule: StoredCostRule | null
  groupId: number
  groupName: string
  saving: boolean
  /** 提交后服务端返回的错误文案 */
  serverError: string
}>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'save', body: CostRuleBody): void }>()

const { t } = useI18n()
const MODES: CostBillingMode[] = ['token', 'per_request', 'image']
const TOKEN_FIELDS = [
  { key: 'input' },
  { key: 'output' },
  { key: 'cacheWrite' },
  { key: 'cacheRead' }
] as const

const form = reactive<RuleForm>(emptyForm(props.groupId))
const localError = ref('')

const title = computed(() => (props.rule ? t('admin.pricingOps.costRules.editTitle') : t('admin.pricingOps.costRules.createTitle')))
const shownError = computed(() => localError.value || props.serverError)

watch(
  () => props.show,
  (open) => {
    if (!open) return
    localError.value = ''
    Object.assign(form, props.rule ? fromRule(props.rule) : emptyForm(props.groupId))
  },
  { immediate: true }
)

function unitText(mode: CostBillingMode): string {
  if (mode === 'token') return t('admin.pricingOps.costRules.unitToken')
  if (mode === 'image') return `${t('admin.pricingOps.costRules.unitRequest')}；${t('admin.pricingOps.costRules.unitImageToken')}`
  return t('admin.pricingOps.costRules.unitRequest')
}

function removeRow(i: number) {
  form.rows.splice(i, 1)
}

function submit() {
  localError.value = ''
  const r = buildRuleBody(form)
  if (!r.ok) {
    localError.value = t(`admin.pricingOps.costRules.error.${r.error.key}`, r.error.params ?? {})
    return
  }
  emit('save', r.body)
}
</script>
