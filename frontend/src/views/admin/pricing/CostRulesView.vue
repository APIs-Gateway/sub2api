<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="space-y-3">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <div class="flex flex-wrap items-center gap-3">
              <label class="text-sm text-gray-600 dark:text-gray-300" for="cost-group">{{ t('admin.pricingOps.costRules.group') }}</label>
              <Select v-model="groupId" class="w-72" :options="groupOptions" data-test="cost-group" />
            </div>
            <div class="flex flex-wrap items-center gap-3">
              <CurrencyModeSwitch />
              <button type="button" class="btn btn-secondary" :disabled="state.loading" data-test="refresh" @click="refresh">
                <Icon name="refresh" size="md" :class="state.loading ? 'animate-spin' : ''" />
                <span class="ml-1.5">{{ t('common.refresh') }}</span>
              </button>
              <button type="button" class="btn btn-primary" :disabled="!canWrite" :title="canWrite ? undefined : lockedReason" data-test="create-rule" @click="openCreate">
                {{ t('admin.pricingOps.costRules.create') }}
              </button>
            </div>
          </div>
          <p class="text-sm text-gray-600 dark:text-gray-300" data-test="cost-note">{{ t('admin.pricingOps.costRules.note') }}</p>
          <p v-if="group && !canWrite" class="warn-note" data-test="locked-note">
            <Icon name="lock" size="sm" class="flex-shrink-0" />{{ lockedReason }}
          </p>
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
        <div v-else-if="!group" class="state-block" data-test="no-groups">
          <p class="font-serif text-lg text-gray-900 dark:text-white">{{ t('admin.pricingOps.costRules.noGroups') }}</p>
        </div>
        <div v-else class="table-wrapper">
          <table class="w-full" data-test="cost-table">
            <thead class="sticky top-0 z-10">
              <tr>
                <th class="w-16">{{ t('admin.pricingOps.costRules.col.order') }}</th>
                <th>{{ t('admin.pricingOps.costRules.col.name') }}</th>
                <th>{{ t('admin.pricingOps.costRules.col.match') }}</th>
                <th>{{ t('admin.pricingOps.costRules.col.prices') }}</th>
                <th class="text-right">{{ t('admin.pricingOps.costRules.col.actions') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="r in rules" :key="r.id" :data-test="`rule-${r.id}`">
                <td class="num">{{ r.sort_order }}</td>
                <td>
                  <p class="font-medium text-gray-900 dark:text-white">{{ r.name }}</p>
                  <p class="mt-0.5 flex flex-wrap items-center gap-2 text-xs text-gray-500 dark:text-dark-300">
                    <span>{{ t(`admin.pricingOps.costRules.source.${r.source}`) }}</span>
                    <span v-if="!r.enabled" class="badge badge-gray" :data-test="`disabled-${r.id}`">{{ t('admin.pricingOps.costRules.disabled') }}</span>
                  </p>
                </td>
                <td class="text-sm">
                  <p>{{ r.group_ids.length ? t('admin.pricingOps.costRules.matchGroups', { n: r.group_ids.length }) : t('admin.pricingOps.costRules.matchAllGroups') }}</p>
                  <p class="text-gray-500 dark:text-dark-300">{{ r.account_ids.length ? t('admin.pricingOps.costRules.matchAccounts', { n: r.account_ids.length }) : t('admin.pricingOps.costRules.matchAllAccounts') }}</p>
                </td>
                <td class="num text-sm">
                  <p v-for="(p, i) in r.prices.slice(0, MAX_LINES)" :key="i" class="whitespace-nowrap">
                    <span class="text-gray-900 dark:text-white">{{ modelsLabel(p.models) }}</span>
                    <span class="ml-2 text-gray-600 dark:text-gray-300">{{ priceLine(p.price) }}</span>
                  </p>
                  <p v-if="r.prices.length > MAX_LINES" class="text-xs text-gray-500 dark:text-dark-300">{{ t('admin.pricingOps.costRules.moreRows', { n: r.prices.length - MAX_LINES }) }}</p>
                </td>
                <td class="whitespace-nowrap text-right">
                  <template v-if="isRuleEditable(r)">
                    <button type="button" class="btn btn-secondary btn-sm" :disabled="!canWrite" :data-test="`edit-${r.id}`" @click="openEdit(r)">{{ t('common.edit') }}</button>
                    <button type="button" class="btn btn-secondary btn-sm ml-2" :disabled="!canWrite" :data-test="`delete-${r.id}`" @click="askDelete(r)">{{ t('common.delete') }}</button>
                  </template>
                  <span v-else class="text-xs text-gray-500 dark:text-dark-300" :data-test="`derived-${r.id}`">{{ t('admin.pricingOps.costRules.derivedReadonly') }}</span>
                </td>
              </tr>
              <tr v-if="rules.length === 0">
                <td colspan="5" class="!py-14 text-center" data-test="cost-empty">
                  <p class="font-serif text-lg text-gray-900 dark:text-white">{{ t('admin.pricingOps.costRules.emptyTitle') }}</p>
                  <p class="mt-1 text-sm text-gray-500 dark:text-dark-300">{{ t('admin.pricingOps.costRules.emptyHint') }}</p>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </TablePageLayout>

    <CostRuleDrawer
      :show="drawerOpen"
      :rule="editing"
      :group-id="group?.id ?? 0"
      :group-name="group?.name ?? ''"
      :saving="saving"
      :server-error="serverError"
      @close="closeDrawer"
      @save="save"
    />

    <ConfirmDialog
      :show="!!deleting"
      :title="t('admin.pricingOps.costRules.deleteTitle')"
      :message="t('admin.pricingOps.costRules.deleteMessage', { name: deleting?.name ?? '' })"
      :confirm-text="t('common.delete')"
      danger
      @confirm="remove"
      @cancel="deleting = null"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import CurrencyModeSwitch from '@/components/common/CurrencyModeSwitch.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import { createCostRule, deleteCostRule, updateCostRule, type CostPrice, type CostRuleBody, type StoredCostRule } from '@/api/admin/pricingOps'
import CostRuleDrawer from './components/CostRuleDrawer.vue'
import { isRuleEditable, priceSummary } from './costRuleForm'
import { isStaleError, opsErrorText } from './opsErrors'
import { platformLabel } from './pricingModel'
import { useGroupOps } from './useGroupOps'
import { usePricingFormat } from './usePricingFormat'

const MAX_LINES = 3

const { t, te } = useI18n()
const app = useAppStore()
const { official } = usePricingFormat()
const { state, load, reloadGroup } = useGroupOps()

const groupId = ref<string | number | boolean | null>(null)
const drawerOpen = ref(false)
const editing = ref<StoredCostRule | null>(null)
const saving = ref(false)
const serverError = ref('')
const deleting = ref<StoredCostRule | null>(null)

const groupOptions = computed(() =>
  state.groups.map((g) => ({ value: g.id, label: `${g.name}（${platformLabel(g.platform)}）` }))
)
const group = computed(() => state.groups.find((g) => g.id === groupId.value) ?? null)
const rules = computed(() => [...(group.value?.view?.costRules ?? [])].sort((a, b) => a.sort_order - b.sort_order || a.id - b.id))
const revision = computed(() => group.value?.view?.revision ?? null)

/** 只有 v2 分组、且读到了价格配置的才能写；其余分组的规则是渠道配置推出的，只能看。 */
const canWrite = computed(() => group.value?.view?.stage === 'v2' && revision.value !== null)
const lockedReason = computed(() => {
  const v = group.value?.view
  if (!v) return t('admin.pricingOps.costRules.locked.unreadable')
  if (v.revision === null) return t('admin.pricingOps.costRules.locked.noConfig')
  return t('admin.pricingOps.costRules.locked.notV2')
})

function modelsLabel(models: string[]): string {
  if (models.length <= 1) return models[0] ?? '—'
  return t('admin.pricingOps.costRules.modelsMore', { first: models[0], n: models.length, rest: models.length - 1 })
}

/** 成本是真实的美元成本：¥ 为主、$ 小字，沿用价格配置页的写法。 */
function money(usd: number): string {
  const m = official(usd)
  return m.sub ? `${m.main}（${m.sub}）` : m.main
}

function priceLine(p: CostPrice): string {
  const s = priceSummary(p)
  if (p.billing_mode === 'token') {
    return t('admin.pricingOps.costRules.priceToken', {
      input: s.in === null ? '—' : money(s.in),
      output: s.out === null ? '—' : money(s.out)
    })
  }
  if (s.perRequest === null) return t('admin.pricingOps.costRules.priceIntervals')
  return t('admin.pricingOps.costRules.pricePerRequest', { price: money(s.perRequest) })
}

function openCreate() {
  editing.value = null
  serverError.value = ''
  drawerOpen.value = true
}

function openEdit(r: StoredCostRule) {
  editing.value = r
  serverError.value = ''
  drawerOpen.value = true
}

function closeDrawer() {
  if (!saving.value) drawerOpen.value = false
}

/** 写入出错时的统一处理：基线过期就重读这个分组，让管理员在最新数据上重来。 */
async function handleWriteError(err: unknown): Promise<string> {
  const text = opsErrorText(err, t, te)
  if (isStaleError(err) && group.value) await reloadGroup(group.value.id)
  return text
}

async function save(body: CostRuleBody) {
  const g = group.value
  if (!g || revision.value === null) return
  saving.value = true
  serverError.value = ''
  try {
    if (editing.value) await updateCostRule(g.id, editing.value.id, revision.value, body)
    else await createCostRule(g.id, revision.value, body)
    // 以服务端为准重读：revision、规则顺序和价格行都不在本地拼
    await reloadGroup(g.id)
    drawerOpen.value = false
    app.showSuccess(t(editing.value ? 'admin.pricingOps.costRules.updated' : 'admin.pricingOps.costRules.created'))
  } catch (err) {
    serverError.value = await handleWriteError(err)
  } finally {
    saving.value = false
  }
}

function askDelete(r: StoredCostRule) {
  deleting.value = r
}

async function remove() {
  const r = deleting.value
  const g = group.value
  deleting.value = null
  if (!r || !g || revision.value === null) return
  try {
    await deleteCostRule(g.id, r.id, revision.value)
    await reloadGroup(g.id)
    app.showSuccess(t('admin.pricingOps.costRules.deleted'))
  } catch (err) {
    app.showError(await handleWriteError(err))
  }
}

async function refresh() {
  await load()
  if (!group.value) groupId.value = state.groups[0]?.id ?? null
}

onMounted(refresh)
</script>

<style scoped>
.state-block {
  @apply flex min-h-[16rem] flex-col items-center justify-center px-6 py-14 text-center;
}

.warn-note {
  @apply flex items-center gap-1.5 text-sm text-primary-700 dark:text-primary-300;
}
</style>
