<template>
  <div class="card">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="font-serif text-lg font-semibold text-gray-900 dark:text-white">{{ t('dashboard.needsAttention.title') }}</h2>
    </div>

    <!-- No items -->
    <div v-if="items.length === 0" class="px-6 py-5 text-sm text-gray-500 dark:text-gray-400">
      {{ t('dashboard.needsAttention.allGood') }}
    </div>

    <!-- Alert items -->
    <ul v-else class="divide-y divide-gray-100 dark:divide-dark-700">
      <li
        v-for="item in items"
        :key="item.key"
        class="flex items-start gap-4 px-6 py-4"
      >
        <!-- Indicator dot -->
        <span
          class="mt-1 h-2 w-2 flex-shrink-0 rounded-full"
          :class="item.urgent ? 'bg-primary-600' : 'bg-yellow-500'"
          aria-hidden="true"
        />
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">{{ item.title }}</p>
          <p v-if="item.description" class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">{{ item.description }}</p>
        </div>
        <button
          type="button"
          class="flex-shrink-0 rounded px-3 py-1.5 text-xs font-medium text-gray-700 ring-1 ring-gray-300 transition-colors hover:bg-gray-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 dark:text-gray-300 dark:ring-dark-600 dark:hover:bg-dark-800"
          @click="item.action"
        >
          {{ item.actionLabel }}
        </button>
      </li>
    </ul>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import type { ApiKey } from '@/types'
import { useAuthStore } from '@/stores/auth'
import { useCurrencyDisplay } from '@/composables/useCurrencyDisplay'

const props = defineProps<{
  /** Pre-fetched API keys list (from parent, no extra fetch needed). Pass empty array if not available. */
  apiKeys: ApiKey[]
}>()

const { t } = useI18n()
const router = useRouter()
const authStore = useAuthStore()
const { formatWallet } = useCurrencyDisplay()

interface AttentionItem {
  key: string
  title: string
  description?: string
  actionLabel: string
  action: () => void
  urgent: boolean
}

// balance_notify_threshold is in USD quota credits, same unit as user.balance.
// Source: backend/ent/user.go BalanceNotifyThreshold *float64, same field as balance.
const threshold = computed<number>(() => {
  return authStore.user?.balance_notify_threshold ?? 0
})

const balance = computed<number>(() => authStore.user?.balance ?? 0)

// threshold_type 'percentage' requires calculating a fraction of balance and is not
// yet implemented in the backend; only 'fixed' (absolute amount) is supported here.
// Undefined defaults to 'fixed' since that is the backend default.
const thresholdType = computed<'fixed' | 'percentage'>(() => {
  return authStore.user?.balance_notify_threshold_type ?? 'fixed'
})

const items = computed<AttentionItem[]>(() => {
  const result: AttentionItem[] = []

  // 1. Balance at threshold — only for fixed-type thresholds; percentage not yet implemented.
  // balance=0 is intentional for subscription users, skip.
  if (
    thresholdType.value === 'fixed' &&
    threshold.value > 0 &&
    balance.value > 0 &&
    balance.value < threshold.value
  ) {
    result.push({
      key: 'low-balance',
      title: t('dashboard.needsAttention.lowBalance'),
      description: t('dashboard.needsAttention.lowBalanceDesc', { balance: formatWallet(balance.value) }),
      actionLabel: t('dashboard.needsAttention.topUp'),
      action: () => router.push('/purchase'),
      urgent: false,
    })
  }

  // 2. Any API key used ≥ 80% of its quota (quota > 0 means a limit is set).
  // Only alert on active keys — inactive/expired/quota_exhausted keys are already
  // stopped so alerting adds noise without any actionable path for the user.
  for (const key of props.apiKeys) {
    if (key.status !== 'active') continue
    if (key.quota > 0 && key.quota_used >= 0) {
      const pct = key.quota_used / key.quota
      if (pct >= 0.8) {
        const isExhausted = pct >= 1
        result.push({
          key: `key-quota-${key.id}`,
          title: isExhausted
            ? t('dashboard.needsAttention.keyExhausted', { name: key.name })
            : t('dashboard.needsAttention.keyNearLimit', { name: key.name, pct: Math.round(pct * 100) }),
          description: t('dashboard.needsAttention.keyQuotaDesc', {
            used: formatWallet(key.quota_used),
            limit: formatWallet(key.quota),
          }),
          actionLabel: t('dashboard.needsAttention.manageKey'),
          action: () => router.push('/keys'),
          urgent: isExhausted,
        })
      }
    }
  }

  return result
})
</script>
