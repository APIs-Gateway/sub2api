<template>
  <AppLayout>
    <BillingRulesCard :models-below="true" class="mb-4" />

    <!-- 搜索 + 平台筛选 -->
    <div class="mb-3 flex flex-wrap items-center gap-3">
      <div class="relative w-full sm:w-80">
        <Icon name="search" size="md" class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 dark:text-gray-500" />
        <input
          v-model="searchQuery"
          type="search"
          :placeholder="t('availableChannels.searchPlaceholder')"
          :aria-label="t('availableChannels.searchPlaceholder')"
          class="input pl-10"
        />
      </div>
      <div class="flex-1" />
      <RouterLink to="/payment" class="btn btn-primary shrink-0">{{ t('availableChannels.buyPlans') }}</RouterLink>
      <button @click="loadChannels" :disabled="loading" class="btn btn-secondary shrink-0" :title="t('common.refresh', 'Refresh')">
        <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
      </button>
    </div>

    <div v-if="loading && catalog.length === 0" class="flex justify-center py-16">
      <div class="h-8 w-8 animate-spin rounded-full border-2 border-primary-500 border-t-transparent" />
    </div>

    <template v-else-if="catalog.length > 0">
      <div class="mb-3 flex gap-2 overflow-x-auto pb-1" role="group" :aria-label="t('availableChannels.platform')">
        <button
          v-for="chip in platformChips"
          :key="chip.value"
          type="button"
          :aria-pressed="platformFilter === chip.value"
          :class="[
            'flex shrink-0 items-center gap-1.5 rounded-lg border px-3 py-1.5 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500',
            platformFilter === chip.value
              ? 'border-gray-400 bg-gray-100 font-semibold text-gray-900 dark:border-dark-500 dark:bg-dark-700 dark:text-white'
              : 'border-gray-200 bg-white text-gray-600 hover:bg-gray-50 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-300 dark:hover:bg-dark-700',
          ]"
          @click="platformFilter = chip.value"
        >
          <span>{{ chip.label }}</span>
          <span class="text-xs text-gray-500 dark:text-gray-400">{{ chip.count }}</span>
        </button>
      </div>

      <ul
        v-if="visibleModels.length > 0"
        class="overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800"
        data-test="model-list"
      >
        <ModelCatalogRow
          v-for="m in visibleModels"
          :key="m.key"
          :model="m"
          :expanded="expandedKeys.has(m.key)"
          @toggle="toggleModel(m.key)"
        />
      </ul>
      <div v-else class="rounded-xl border border-dashed border-gray-200 py-12 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-gray-400">
        {{ t('availableChannels.noResults') }}
      </div>
    </template>

    <div v-else class="rounded-xl border border-dashed border-gray-200 py-16 text-center dark:border-dark-700">
      <Icon name="inbox" size="xl" class="mx-auto mb-3 h-12 w-12 text-gray-400" />
      <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('availableChannels.empty') }}</p>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import BillingRulesCard from '@/components/common/BillingRulesCard.vue'
import ModelCatalogRow from '@/components/channels/ModelCatalogRow.vue'
import userChannelsAPI, { type UserPriceCatalog } from '@/api/channels'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { platformLabel } from '@/utils/platformColors'
import { buildPriceCatalog } from '@/utils/modelCatalog'

const { t } = useI18n()
const appStore = useAppStore()

const prices = ref<UserPriceCatalog | null>(null)
const loading = ref(false)
const searchQuery = ref('')
const platformFilter = ref('all')
const expandedKeys = ref<Set<string>>(new Set())

const catalog = computed(() => buildPriceCatalog(prices.value))

const searchedModels = computed(() => {
  const q = searchQuery.value.trim().toLowerCase()
  return q ? catalog.value.filter((m) => m.name.toLowerCase().includes(q)) : catalog.value
})

const platformChips = computed(() => {
  const counts = new Map<string, number>()
  for (const m of searchedModels.value) counts.set(m.platform, (counts.get(m.platform) ?? 0) + 1)
  const chips = [{ value: 'all', label: t('availableChannels.allPlatforms'), count: searchedModels.value.length }]
  for (const [value, count] of [...counts.entries()].sort((a, b) => a[0].localeCompare(b[0]))) {
    chips.push({ value, label: platformLabel(value), count })
  }
  return chips
})

const visibleModels = computed(() =>
  platformFilter.value === 'all'
    ? searchedModels.value
    : searchedModels.value.filter((m) => m.platform === platformFilter.value),
)

function toggleModel(key: string) {
  const next = new Set(expandedKeys.value)
  if (!next.delete(key)) next.add(key)
  expandedKeys.value = next
}

async function loadChannels() {
  loading.value = true
  try {
    // 价格与倍率都由后端按各分组的价格阶段取好、乘好（含用户专属倍率），前端不再自己乘。
    prices.value = await userChannelsAPI.getPrices()
    // 套餐倍率列所需的 /subscriptions/pricing 由 useRateDisplay 共享缓存请求（只在人民币模式、
    // 支付开启时请求，失败静默，整列隐藏），这里不再单独取。
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  } finally {
    loading.value = false
  }
}

onMounted(loadChannels)
</script>
