<template>
  <AppLayout>
    <div class="space-y-8">
      <!-- Loading State -->
      <div v-if="loading" class="flex justify-center py-12">
        <div
          class="h-8 w-8 animate-spin rounded-full border-2 border-primary-500 border-t-transparent"
        ></div>
      </div>

      <!-- Empty State -->
      <div v-else-if="subscriptions.length === 0" class="card p-12 text-center">
        <div
          class="mx-auto mb-4 flex h-16 w-16 items-center justify-center rounded-full bg-gray-100 dark:bg-dark-700"
        >
          <Icon name="creditCard" size="xl" class="text-gray-500 dark:text-gray-400" />
        </div>
        <h3 class="mb-2 text-lg font-semibold text-gray-900 dark:text-white">
          {{ t('userSubscriptions.noActiveSubscriptions') }}
        </h3>
        <p class="text-gray-600 dark:text-gray-400">
          {{ t('userSubscriptions.noActiveSubscriptionsDesc') }}
        </p>
      </div>

      <template v-else>
        <!-- 生效中 -->
        <section class="space-y-4">
          <div class="flex items-center gap-2">
            <span class="h-2 w-2 rounded-full bg-primary-500" />
            <h2 class="text-sm font-semibold text-gray-900 dark:text-white">
              {{ t('userSubscriptions.sectionActive') }}
            </h2>
            <span class="text-xs text-gray-500 dark:text-gray-400">{{
              activeSubscriptions.length
            }}</span>
          </div>

          <div v-if="activeSubscriptions.length" :class="activeSubscriptionsGridClass">
            <UserSubscriptionCard
              v-for="subscription in activeSubscriptions"
              :key="subscription.id"
              :subscription="subscription"
              :payment-currency="paymentCurrency"
              :subscription-payment-multiplier="subscriptionPaymentMultiplier"
              :locale="localeCode"
              @saved="loadSubscriptions"
            />
          </div>
          <p
            v-else
            class="rounded-md border border-dashed border-gray-200 px-4 py-6 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-gray-400"
          >
            {{ t('userSubscriptions.noActiveNow') }}
          </p>
        </section>

        <!-- 已结束（已过期 / 已撤销）：默认折叠 -->
        <section v-if="endedSubscriptions.length" class="space-y-4">
          <button
            type="button"
            class="flex w-full items-center gap-2 text-left"
            @click="showEnded = !showEnded"
          >
            <span class="h-2 w-2 rounded-full bg-gray-300 dark:bg-dark-500" />
            <h2 class="text-sm font-semibold text-gray-600 dark:text-gray-400">
              {{ t('userSubscriptions.sectionEnded') }}
            </h2>
            <span class="text-xs text-gray-500 dark:text-gray-400">{{
              endedSubscriptions.length
            }}</span>
            <Icon
              name="chevronDown"
              size="sm"
              class="text-gray-400 transition-transform"
              :class="{ 'rotate-180': showEnded }"
            />
          </button>

          <div v-show="showEnded" :class="endedSubscriptionsGridClass">
            <UserSubscriptionCard
              v-for="subscription in endedSubscriptions"
              :key="subscription.id"
              :subscription="subscription"
              :payment-currency="paymentCurrency"
              :subscription-payment-multiplier="subscriptionPaymentMultiplier"
              :locale="localeCode"
              @saved="loadSubscriptions"
            />
          </div>
        </section>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import subscriptionsAPI from '@/api/subscriptions'
import { paymentAPI } from '@/api/payment'
import { getVisibleMethods } from '@/components/payment/paymentFlow'
import { normalizePaymentCurrency } from '@/components/payment/currency'
import type { UserSubscription } from '@/types'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import UserSubscriptionCard from '@/components/subscription/UserSubscriptionCard.vue'

const { t, locale } = useI18n()
const appStore = useAppStore()

const subscriptions = ref<UserSubscription[]>([])
const loading = ref(true)
const showEnded = ref(false)
const paymentCurrency = ref('CNY')
const subscriptionPaymentMultiplier = ref(1)

const localeCode = computed(() => {
  const raw = locale as unknown
  if (typeof raw === 'string') return raw
  if (raw && typeof raw === 'object' && 'value' in raw) return String((raw as { value?: string }).value || '')
  return undefined
})

// 生效中：仅 status === 'active'。
const activeSubscriptions = computed(() =>
  subscriptions.value.filter((s) => s.status === 'active')
)

// 已结束：已过期 / 已撤销，归到折叠分区。
const endedSubscriptions = computed(() =>
  subscriptions.value.filter((s) => s.status === 'expired' || s.status === 'revoked')
)

const activeSubscriptionsGridClass = computed(() =>
  activeSubscriptions.value.length === 1 ? 'grid gap-6' : 'grid gap-6 lg:grid-cols-2'
)

const endedSubscriptionsGridClass = computed(() =>
  endedSubscriptions.value.length === 1
    ? 'grid gap-6 opacity-80'
    : 'grid gap-6 opacity-80 lg:grid-cols-2'
)

async function loadSubscriptions() {
  try {
    loading.value = true
    const [items, checkout] = await Promise.all([
      subscriptionsAPI.getMySubscriptions(),
      paymentAPI.getCheckoutInfo().then((res) => res.data).catch(() => null),
    ])
    subscriptions.value = items
    if (checkout) {
      const visibleMethods = getVisibleMethods(checkout.methods || {})
      const firstMethod = Object.values(visibleMethods)[0]
      paymentCurrency.value = normalizePaymentCurrency(firstMethod?.currency)
      subscriptionPaymentMultiplier.value = checkout.subscription_payment_multiplier > 0
        ? checkout.subscription_payment_multiplier
        : 1
    }
    // 没有生效中订阅时，默认展开历史，避免页面显得空白。
    showEnded.value = activeSubscriptions.value.length === 0 && endedSubscriptions.value.length > 0
  } catch (error) {
    console.error('Failed to load subscriptions:', error)
    appStore.showError(t('userSubscriptions.failedToLoad'))
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  loadSubscriptions()
})
</script>
