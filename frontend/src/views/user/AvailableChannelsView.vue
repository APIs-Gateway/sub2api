<template>
  <AppLayout>
    <BillingRulesCard :models-below="true" class="mb-4" />

    <!-- Search bar -->
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
      <button @click="loadChannels" :disabled="loading" class="btn btn-secondary" :title="t('common.refresh', 'Refresh')">
        <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
      </button>
    </div>

    <div v-if="loading" class="flex justify-center py-16">
      <div class="h-8 w-8 animate-spin rounded-full border-2 border-primary-500 border-t-transparent" />
    </div>

    <template v-else>
      <!-- 按量档位 -->
      <section v-if="standardTiers.length > 0" class="mb-8">
        <h2 class="mb-1 text-sm font-bold text-gray-900 dark:text-white">{{ t('availableChannels.tiersTitle') }}</h2>
        <p class="mb-3 text-xs text-gray-500 dark:text-gray-400">{{ t('availableChannels.tiersHint') }}</p>
        <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          <div
            v-for="g in standardTiers"
            :key="g.id"
            class="flex flex-col rounded-xl border border-gray-200 bg-white p-4 transition-shadow hover:shadow-md dark:border-dark-700 dark:bg-dark-800"
          >
            <GroupOptionItem
              :name="g.name"
              :platform="(g.platform as GroupPlatform)"
              :subscription-type="(g.subscription_type as SubscriptionType)"
              :rate-multiplier="g.rate_multiplier"
              :user-rate-multiplier="userGroupRates[g.id] ?? null"
              :description="g.description || ''"
              :show-checkmark="false"
            />
            <div class="mt-3 flex flex-wrap gap-1 border-t border-gray-100 pt-3 dark:border-dark-700/70">
              <SupportedModelChip
                v-for="m in g.models"
                :key="m.name"
                :model="m"
                pricing-key-prefix="availableChannels.pricing"
                :no-pricing-label="t('availableChannels.noPricing')"
                :show-platform="false"
                :platform-hint="g.platform"
              />
              <span v-if="g.models.length === 0" class="text-xs text-gray-400">{{ t('availableChannels.noModels') }}</span>
            </div>
          </div>
        </div>
      </section>

      <!-- 月套餐 -->
      <section v-if="subscriptionTiers.length > 0" class="mb-4">
        <div class="mb-3 flex items-center justify-between gap-3">
          <div>
            <h2 class="text-sm font-bold text-gray-900 dark:text-white">{{ t('availableChannels.plansTitle') }}</h2>
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('availableChannels.plansHint') }}</p>
          </div>
          <RouterLink to="/payment" class="btn btn-primary shrink-0">{{ t('availableChannels.goBuy') }}</RouterLink>
        </div>
        <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
          <div
            v-for="g in subscriptionTiers"
            :key="g.id"
            class="flex flex-col rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800"
          >
            <GroupOptionItem
              :name="g.name"
              :platform="(g.platform as GroupPlatform)"
              :subscription-type="(g.subscription_type as SubscriptionType)"
              :description="g.description || ''"
              :show-checkmark="false"
            />
            <div v-if="g.models.length > 0" class="mt-3 flex flex-wrap gap-1 border-t border-gray-100 pt-3 dark:border-dark-700/70">
              <SupportedModelChip
                v-for="m in g.models"
                :key="m.name"
                :model="m"
                pricing-key-prefix="availableChannels.pricing"
                :no-pricing-label="t('availableChannels.noPricing')"
                :show-platform="false"
                :platform-hint="g.platform"
              />
            </div>
          </div>
        </div>
      </section>

      <!-- Empty -->
      <div
        v-if="standardTiers.length === 0 && subscriptionTiers.length === 0"
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
import GroupOptionItem from '@/components/common/GroupOptionItem.vue'
import SupportedModelChip from '@/components/channels/SupportedModelChip.vue'
import BillingRulesCard from '@/components/common/BillingRulesCard.vue'
import userChannelsAPI, {
  type UserAvailableChannel,
  type UserAvailableGroup,
  type UserSupportedModel,
} from '@/api/channels'
import userGroupsAPI from '@/api/groups'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { GroupPlatform, SubscriptionType } from '@/types'

const { t } = useI18n()
const appStore = useAppStore()

const channels = ref<UserAvailableChannel[]>([])
const userGroupRates = ref<Record<number, number>>({})
const loading = ref(false)
const searchQuery = ref('')

// 分组卡片：每个分组一张卡，模型 = 该分组所在所有渠道-平台 section 的支持模型并集。
interface GroupTierCard extends UserAvailableGroup {
  models: UserSupportedModel[]
}

const groupCards = computed<GroupTierCard[]>(() => {
  const q = searchQuery.value.trim().toLowerCase()
  const map = new Map<number, { group: UserAvailableGroup; models: Map<string, UserSupportedModel> }>()
  for (const ch of channels.value) {
    for (const sec of ch.platforms) {
      for (const g of sec.groups) {
        let entry = map.get(g.id)
        if (!entry) {
          entry = { group: g, models: new Map<string, UserSupportedModel>() }
          map.set(g.id, entry)
        }
        for (const m of sec.supported_models) {
          if (!entry.models.has(m.name)) entry.models.set(m.name, m)
        }
      }
    }
  }
  let cards = Array.from(map.values()).map((e) => ({ ...e.group, models: Array.from(e.models.values()) }))
  if (q) {
    cards = cards.filter(
      (c) =>
        c.name.toLowerCase().includes(q) ||
        (c.description || '').toLowerCase().includes(q) ||
        c.models.some((m) => m.name.toLowerCase().includes(q)),
    )
  }
  // 倍率升序，名称兜底排序，展示稳定
  return cards.sort((a, b) => a.rate_multiplier - b.rate_multiplier || a.name.localeCompare(b.name))
})

const standardTiers = computed(() => groupCards.value.filter((g) => g.subscription_type !== 'subscription'))
const subscriptionTiers = computed(() => groupCards.value.filter((g) => g.subscription_type === 'subscription'))

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
