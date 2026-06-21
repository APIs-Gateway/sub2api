<template>
  <AppLayout>
    <BillingRulesCard :models-below="true" class="mb-4" />

    <!-- Toolbar -->
    <div class="mb-4 flex flex-wrap items-center gap-3">
      <div class="relative w-full sm:w-80">
        <Icon name="search" size="md" class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400 dark:text-gray-500" />
        <input
          v-model="searchQuery"
          type="text"
          :placeholder="t('availableChannels.searchPlaceholder')"
          class="input pl-10"
        />
      </div>
      <RouterLink to="/payment" class="btn btn-primary shrink-0">{{ t('availableChannels.buyPlans') }}</RouterLink>
      <button @click="loadChannels" :disabled="loading" class="btn btn-secondary shrink-0" :title="t('common.refresh', 'Refresh')">
        <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
      </button>
    </div>

    <div v-if="loading" class="flex justify-center py-16">
      <div class="h-8 w-8 animate-spin rounded-full border-2 border-primary-500 border-t-transparent" />
    </div>

    <template v-else-if="groupTabs.length > 0">
      <!-- 分组 tab 切换 -->
      <div class="mb-3 flex gap-2 overflow-x-auto pb-1">
        <button
          v-for="g in groupTabs"
          :key="g.id"
          type="button"
          @click="selectedGroupId = g.id"
          :class="[
            'flex shrink-0 items-center gap-1.5 rounded-lg border px-3 py-1.5 text-sm transition-colors',
            selectedGroup && selectedGroup.id === g.id
              ? 'border-primary-400 bg-primary-50 text-primary-700 dark:border-primary-500/50 dark:bg-primary-500/10 dark:text-primary-300'
              : 'border-gray-200 bg-white text-gray-600 hover:bg-gray-50 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-300 dark:hover:bg-dark-700',
          ]"
        >
          <PlatformIcon v-if="g.platform" :platform="(g.platform as GroupPlatform)" size="xs" />
          <span class="font-medium">{{ g.name }}</span>
          <span class="rounded bg-black/10 px-1.5 py-0.5 text-[10px] font-semibold dark:bg-white/10">{{ formatRate(g.rate) }}x</span>
        </button>
      </div>

      <!-- 选中分组的说明 + 实付提示 -->
      <p v-if="selectedGroup" class="mb-3 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
        <span v-if="selectedGroup.description">{{ selectedGroup.description }}</span>
        <span class="text-gray-400 dark:text-gray-500">{{ t('availableChannels.payHint') }} {{ formatRate(selectedGroup.rate) }}x</span>
      </p>

      <!-- 该分组的模型卡 -->
      <div v-if="displayModels.length > 0" class="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
        <ModelPriceCard
          v-for="m in displayModels"
          :key="m.name"
          :model="m"
          :platform-hint="selectedGroup?.platform"
          :no-pricing-label="t('availableChannels.noPricing')"
        />
      </div>
      <div v-else class="rounded-xl border border-dashed border-gray-200 py-12 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-gray-400">
        {{ t('availableChannels.noModels') }}
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
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import BillingRulesCard from '@/components/common/BillingRulesCard.vue'
import ModelPriceCard from '@/components/channels/ModelPriceCard.vue'
import userChannelsAPI, { type UserAvailableChannel, type UserSupportedModel } from '@/api/channels'
import userGroupsAPI from '@/api/groups'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { GroupPlatform } from '@/types'

const { t } = useI18n()
const appStore = useAppStore()

const channels = ref<UserAvailableChannel[]>([])
const userGroupRates = ref<Record<number, number>>({})
const loading = ref(false)
const searchQuery = ref('')
const selectedGroupId = ref<number | null>(null)

interface GroupTab {
  id: number
  name: string
  platform: string
  subscriptionType: string
  rate: number
  description: string
  models: UserSupportedModel[]
}

// 以分组为外层：每个分组聚合其所在所有渠道-平台 section 的模型(按名去重)。
const groupTabs = computed<GroupTab[]>(() => {
  const map = new Map<
    number,
    { id: number; name: string; platform: string; subscriptionType: string; rate: number; description: string; models: Map<string, UserSupportedModel> }
  >()
  for (const ch of channels.value) {
    for (const sec of ch.platforms) {
      for (const g of sec.groups) {
        let e = map.get(g.id)
        if (!e) {
          e = {
            id: g.id,
            name: g.name,
            platform: g.platform || sec.platform,
            subscriptionType: g.subscription_type,
            rate: userGroupRates.value[g.id] ?? g.rate_multiplier,
            description: g.description || '',
            models: new Map<string, UserSupportedModel>(),
          }
          map.set(g.id, e)
        }
        for (const m of sec.supported_models) {
          const cur = e.models.get(m.name)
          if (!cur || (!cur.pricing && m.pricing)) e.models.set(m.name, m)
        }
      }
    }
  }
  return Array.from(map.values())
    .map((e) => ({ ...e, models: Array.from(e.models.values()) }))
    .sort(
      (a, b) =>
        (a.subscriptionType === 'subscription' ? 1 : 0) - (b.subscriptionType === 'subscription' ? 1 : 0) ||
        a.rate - b.rate ||
        a.name.localeCompare(b.name),
    )
})

// 选中分组：未选或失效时回退第一个 tab。
const selectedGroup = computed<GroupTab | null>(() => {
  const tabs = groupTabs.value
  if (tabs.length === 0) return null
  return tabs.find((tab) => tab.id === selectedGroupId.value) ?? tabs[0]
})

const displayModels = computed<UserSupportedModel[]>(() => {
  const g = selectedGroup.value
  if (!g) return []
  const q = searchQuery.value.trim().toLowerCase()
  const models = q ? g.models.filter((m) => m.name.toLowerCase().includes(q)) : g.models
  return [...models].sort((a, b) => a.name.localeCompare(b.name))
})

function formatRate(r: number): string {
  return Number(r.toPrecision(10)).toString()
}

async function loadChannels() {
  loading.value = true
  try {
    const [list, rates] = await Promise.all([
      userChannelsAPI.getAvailable(),
      userGroupsAPI.getUserGroupRates().catch((err: unknown) => {
        console.error('Failed to load user group rates:', err)
        return {} as Record<number, number>
      }),
    ])
    channels.value = list
    userGroupRates.value = rates
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  } finally {
    loading.value = false
  }
}

onMounted(loadChannels)
</script>
