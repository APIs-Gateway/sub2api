<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="space-y-3">
          <div class="flex flex-col justify-between gap-4 lg:flex-row lg:items-start">
            <div class="flex flex-1 flex-wrap items-center gap-3">
              <div class="relative w-full sm:w-64">
                <Icon name="search" size="md" class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 dark:text-gray-500" />
                <input
                  v-model="search"
                  type="text"
                  :placeholder="t('admin.pricingConfig.models.searchPlaceholder')"
                  class="input pl-10"
                  data-test="model-search"
                />
              </div>
              <Select v-model="platformFilter" :options="platformOptions" class="w-40" />
              <Select v-model="statusFilter" :options="statusOptions" class="w-36" />
              <button
                type="button"
                class="filter-chip"
                :class="{ 'is-on': unpricedOnly }"
                :aria-pressed="unpricedOnly"
                data-test="filter-unpriced"
                @click="unpricedOnly = !unpricedOnly"
              >
                {{ t('admin.pricingConfig.models.filterUnpriced') }}
              </button>
              <button
                type="button"
                class="filter-chip"
                :class="{ 'is-on': unregisteredOnly }"
                :aria-pressed="unregisteredOnly"
                data-test="filter-unregistered"
                @click="unregisteredOnly = !unregisteredOnly"
              >
                {{ t('admin.pricingConfig.models.filterUnregistered') }}
              </button>
            </div>
            <div class="flex w-full flex-shrink-0 flex-wrap items-center justify-end gap-3 lg:w-auto">
              <CurrencyModeSwitch />
              <button type="button" class="btn btn-secondary" :disabled="state.loading" data-test="refresh" @click="refresh">
                <Icon name="refresh" size="md" :class="state.loading ? 'animate-spin' : ''" />
                <span class="ml-1.5">{{ t('common.refresh') }}</span>
              </button>
              <button
                type="button"
                class="btn btn-primary"
                disabled
                :title="t('admin.pricingConfig.comingSoon')"
                data-test="create-model"
              >
                {{ t('admin.pricingConfig.models.create') }}
              </button>
            </div>
          </div>
          <p class="text-sm text-gray-500 dark:text-dark-300" data-test="readonly-note">
            {{ t('admin.pricingConfig.models.readonlyNote') }}
          </p>
          <p v-if="state.deriveFailed > 0" class="warn-note" data-test="derive-failed">
            <Icon name="exclamationTriangle" size="sm" class="flex-shrink-0" />
            {{ t('admin.pricingConfig.deriveFailed', { n: state.deriveFailed }) }}
          </p>
        </div>
      </template>

      <template #table>
        <div v-if="state.error && !state.loaded" class="state-block" data-test="load-error">
          <p class="font-serif text-lg text-gray-900 dark:text-white">{{ t('admin.pricingConfig.loadError') }}</p>
          <p class="mt-1 text-sm text-gray-500 dark:text-dark-300">{{ state.error }}</p>
          <button type="button" class="btn btn-secondary mt-4" @click="refresh">{{ t('admin.pricingConfig.retry') }}</button>
        </div>
        <div v-else-if="!state.loaded" class="state-block" data-test="loading">
          <LoadingSpinner />
        </div>
        <div v-else class="table-wrapper">
          <table class="w-full" data-test="models-table">
            <thead class="sticky top-0 z-10">
              <tr>
                <th>{{ t('admin.pricingConfig.models.columns.model') }}</th>
                <th>{{ t('admin.pricingConfig.models.columns.platform') }}</th>
                <th>{{ t('admin.pricingConfig.models.columns.status') }}</th>
                <th>
                  {{ t('admin.pricingConfig.models.columns.officialPrice') }}
                  <span class="ml-1 text-xs font-normal opacity-70">{{ t('admin.pricingConfig.models.officialPriceHint') }}</span>
                </th>
                <th>{{ t('admin.pricingConfig.models.columns.source') }}</th>
                <th>{{ t('admin.pricingConfig.models.columns.openGroups') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="m in filtered"
                :key="m.id"
                class="cursor-pointer transition-colors hover:bg-gray-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 dark:hover:bg-dark-800/60"
                :class="{ 'bg-gray-50 dark:bg-dark-800/40': drawerId === m.id }"
                tabindex="0"
                :data-test="`model-row-${m.key}`"
                @click="drawerId = m.id"
                @keydown.enter="drawerId = m.id"
              >
                <td>
                  <span class="font-medium text-gray-900 dark:text-white">{{ m.key }}</span>
                  <span v-if="m.displayName !== m.key" class="ml-2 text-xs text-gray-500 dark:text-dark-300">{{ m.displayName }}</span>
                </td>
                <td>
                  <span class="inline-flex items-center gap-1.5">
                    <PlatformIcon :platform="asGroupPlatform(m.platform)" size="sm" />
                    {{ platformLabel(m.platform) }}
                  </span>
                </td>
                <td>
                  <span class="inline-flex items-center gap-1.5 whitespace-nowrap">
                    <span class="status-dot" :class="dotClass(m)" aria-hidden="true" />
                    {{ statusText(m) }}
                  </span>
                </td>
                <td class="num whitespace-nowrap">
                  <template v-if="refOf(m)?.priced && refOf(m)?.per_mtok">
                    <span :data-test="`official-${m.key}`">{{ officialLine(m).main }}</span>
                    <span v-if="officialLine(m).sub" class="ml-1.5 text-xs text-gray-500 dark:text-dark-300">{{ officialLine(m).sub }}</span>
                  </template>
                  <span v-else class="text-gray-400 dark:text-dark-400">—</span>
                </td>
                <td class="whitespace-nowrap">
                  <span v-if="refOf(m) && !refOf(m)?.priced" class="inline-flex items-center gap-1 font-medium text-primary-700 dark:text-primary-300" :data-test="`unpriced-${m.key}`">
                    <Icon name="exclamationTriangle" size="xs" />{{ t('admin.pricingConfig.source.none') }}
                  </span>
                  <span v-else-if="refOf(m)">{{ t(`admin.pricingConfig.source.${sourceKey(refOf(m)?.source)}`) }}</span>
                  <span v-else class="text-gray-400 dark:text-dark-400">—</span>
                </td>
                <td class="num whitespace-nowrap" :data-test="`open-count-${m.key}`">
                  <template v-if="statsOf(m).groupCount > 0">
                    <span :class="statsOf(m).openCount === 0 ? 'text-gray-400 dark:text-dark-400' : ''">{{ statsOf(m).openCount }}</span>
                    <span class="text-gray-400 dark:text-dark-400"> / {{ statsOf(m).groupCount }}</span>
                  </template>
                  <span v-else class="text-gray-400 dark:text-dark-400">—</span>
                </td>
              </tr>
              <tr v-if="filtered.length === 0">
                <td colspan="6" class="!py-14 text-center" data-test="models-empty">
                  <p class="font-serif text-lg text-gray-900 dark:text-white">{{ t('admin.pricingConfig.models.emptyTitle') }}</p>
                  <p class="mt-1 text-sm text-gray-500 dark:text-dark-300">{{ t('admin.pricingConfig.models.emptyHint') }}</p>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </TablePageLayout>

    <ModelDrawer :model="drawerModel" @close="drawerId = null" />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import CurrencyModeSwitch from '@/components/common/CurrencyModeSwitch.vue'
import ModelDrawer from './components/ModelDrawer.vue'
import { usePricingData } from './usePricingData'
import { usePricingFormat } from './usePricingFormat'
import { asGroupPlatform, modelStats, platformLabel, sourceKey, type ModelRow } from './pricingModel'

const { t } = useI18n()
const { state, rows, platforms, load, refresh } = usePricingData()
const { official } = usePricingFormat()

const search = ref('')
const platformFilter = ref<string | number | boolean | null>('')
const statusFilter = ref<string | number | boolean | null>('')
const unpricedOnly = ref(false)
const unregisteredOnly = ref(false)
const drawerId = ref<string | null>(null)

const platformOptions = computed(() => [
  { value: '', label: t('admin.pricingConfig.models.allPlatforms') },
  ...platforms.value.map((p) => ({ value: p, label: platformLabel(p) }))
])
const statusOptions = computed(() => [
  { value: '', label: t('admin.pricingConfig.models.allStatuses') },
  { value: 'draft', label: t('admin.pricingConfig.status.draft') },
  { value: 'active', label: t('admin.pricingConfig.status.active') },
  { value: 'retired', label: t('admin.pricingConfig.status.retired') }
])

const refOf = (m: ModelRow) => state.refs[m.key]
const statsOf = (m: ModelRow) => modelStats(m, state.groups, state.cells)

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  return rows.value.filter((m) => {
    if (q && !m.key.toLowerCase().includes(q) && !m.displayName.toLowerCase().includes(q)) return false
    if (platformFilter.value && m.platform !== platformFilter.value) return false
    if (statusFilter.value && m.status !== statusFilter.value) return false
    if (unpricedOnly.value && !(refOf(m) && !refOf(m)?.priced)) return false
    if (unregisteredOnly.value && m.registered) return false
    return true
  })
})

const drawerModel = computed(() => rows.value.find((m) => m.id === drawerId.value) ?? null)

function statusText(m: ModelRow): string {
  return t(`admin.pricingConfig.status.${m.status ?? 'unregistered'}`)
}

function dotClass(m: ModelRow): string {
  return `is-${m.status ?? 'unregistered'}`
}

/** 官方参考价「输入 / 输出」，¥ 为主，$ 为小字。 */
function officialLine(m: ModelRow): { main: string; sub: string | null } {
  const per = refOf(m)?.per_mtok
  if (!per) return { main: '', sub: null }
  const input = official(per.input)
  const output = official(per.output)
  return {
    main: `${input.main} / ${output.main}`,
    sub: input.sub && output.sub ? `${input.sub} / ${output.sub}` : null
  }
}

onMounted(() => {
  void load()
})
</script>

<style scoped>
.filter-chip {
  @apply rounded-md border border-gray-300 px-3 py-2.5 text-sm font-medium text-gray-600 transition-colors;
  @apply hover:bg-gray-100 dark:border-dark-600 dark:text-gray-300 dark:hover:bg-dark-800;
}
.filter-chip.is-on {
  @apply border-gray-900 bg-gray-100 text-gray-900 dark:border-gray-100 dark:bg-dark-700 dark:text-white;
}

.state-block {
  @apply flex min-h-[16rem] flex-col items-center justify-center px-6 py-14 text-center;
}

.warn-note {
  @apply flex items-center gap-1.5 text-sm text-primary-700 dark:text-primary-300;
}

.status-dot {
  @apply inline-block h-2 w-2 flex-shrink-0 rounded-full border;
}
.status-dot.is-active {
  @apply border-gray-800 bg-gray-800 dark:border-gray-200 dark:bg-gray-200;
}
.status-dot.is-draft {
  @apply border-gray-400 bg-transparent dark:border-dark-300;
}
.status-dot.is-retired {
  @apply border-gray-300 bg-gray-300 dark:border-dark-500 dark:bg-dark-500;
}
.status-dot.is-unregistered {
  @apply border-primary-500 bg-transparent;
}
</style>
