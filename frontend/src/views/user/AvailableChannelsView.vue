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

    <template v-else>
      <div
        v-if="modelCards.length > 0"
        class="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4"
      >
        <ModelPriceCard
          v-for="c in modelCards"
          :key="c.model.name"
          :model="c.model"
          :rates="c.rates"
          :platform-hint="c.platform"
          :no-pricing-label="t('availableChannels.noPricing')"
        />
      </div>

      <div
        v-else
        class="rounded-xl border border-dashed border-gray-200 py-16 text-center dark:border-dark-700"
      >
        <Icon name="inbox" size="xl" class="mx-auto mb-3 h-12 w-12 text-gray-400" />
        <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('availableChannels.empty') }}</p>
      </div>
    </template>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import BillingRulesCard from '@/components/common/BillingRulesCard.vue'
import ModelPriceCard from '@/components/channels/ModelPriceCard.vue'
import userChannelsAPI, { type UserAvailableChannel, type UserSupportedModel } from '@/api/channels'
import userGroupsAPI from '@/api/groups'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t } = useI18n()
const appStore = useAppStore()

const channels = ref<UserAvailableChannel[]>([])
const userGroupRates = ref<Record<number, number>>({})
const loading = ref(false)
const searchQuery = ref('')

interface ModelCard {
  model: UserSupportedModel
  platform: string
  rates: number[]
}

// 一个模型一张卡：跨渠道/平台去重，价格取首个非空定价，倍率聚合所有提供该模型的分组（去重升序）。
const modelCards = computed<ModelCard[]>(() => {
  const q = searchQuery.value.trim().toLowerCase()
  const map = new Map<string, { model: UserSupportedModel; rates: Set<number>; platform: string }>()
  for (const ch of channels.value) {
    for (const sec of ch.platforms) {
      const secRates = sec.groups.map((g) => userGroupRates.value[g.id] ?? g.rate_multiplier)
      for (const m of sec.supported_models) {
        let e = map.get(m.name)
        if (!e) {
          e = { model: m, rates: new Set<number>(), platform: m.platform || sec.platform }
          map.set(m.name, e)
        } else if (!e.model.pricing && m.pricing) {
          e.model = m // 优先保留带定价的条目
        }
        for (const r of secRates) e.rates.add(r)
      }
    }
  }
  let cards = Array.from(map.values()).map((e) => ({
    model: e.model,
    platform: e.platform,
    rates: Array.from(e.rates).sort((a, b) => a - b),
  }))
  if (q) cards = cards.filter((c) => c.model.name.toLowerCase().includes(q))
  return cards.sort((a, b) => a.model.name.localeCompare(b.model.name))
})

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
