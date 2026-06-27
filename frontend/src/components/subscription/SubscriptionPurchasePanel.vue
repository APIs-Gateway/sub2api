<template>
  <div class="space-y-5">
    <div>
      <h3 class="text-base font-semibold text-gray-900 dark:text-white">
        {{ t('subscriptionPurchase.title') }}
      </h3>
      <p class="mt-0.5 text-sm text-gray-600 dark:text-gray-400">
        {{ t('subscriptionPurchase.desc') }}
      </p>
    </div>

    <div v-if="loadError" class="rounded-md border border-gray-200 bg-gray-50 p-4 text-sm text-gray-600 dark:border-dark-700 dark:bg-dark-800/40 dark:text-gray-400">
      {{ t('subscriptionPurchase.loadFailed') }}
    </div>

    <template v-else-if="pricing">
      <!-- 每日额度 D -->
      <div>
        <label class="input-label">
          {{ t('subscriptionPurchase.dailyAmount') }}
          <span class="font-normal text-gray-500 dark:text-dark-400">({{ t('subscriptionPurchase.dailyAmountUnit') }})</span>
        </label>
        <div class="flex items-center gap-3">
          <input
            v-model.number="dailyAmount"
            type="range"
            :min="pricing.d_min"
            :max="pricing.d_max"
            step="0.5"
            class="h-2 flex-1 accent-gray-900 dark:accent-gray-100"
          />
          <input
            v-model.number="dailyAmount"
            type="number"
            :min="pricing.d_min"
            :max="pricing.d_max"
            step="0.5"
            class="input w-24 text-right font-mono tabular-nums"
            @change="clampInputs"
          />
        </div>
      </div>

      <!-- 有效期 T -->
      <div>
        <label class="input-label">
          {{ t('subscriptionPurchase.validityDays') }}
          <span class="font-normal text-gray-500 dark:text-dark-400">({{ t('subscriptionPurchase.days') }})</span>
        </label>
        <div class="flex items-center gap-3">
          <input
            v-model.number="validityDays"
            type="range"
            :min="pricing.t_min"
            :max="pricing.t_max"
            step="1"
            class="h-2 flex-1 accent-gray-900 dark:accent-gray-100"
          />
          <input
            v-model.number="validityDays"
            type="number"
            :min="pricing.t_min"
            :max="pricing.t_max"
            step="1"
            class="input w-24 text-right font-mono tabular-nums"
            @change="clampInputs"
          />
        </div>
      </div>

      <p class="input-hint">
        {{
          t('subscriptionPurchase.rangeHint', {
            dMin: pricing.d_min,
            dMax: pricing.d_max,
            tMin: pricing.t_min,
            tMax: pricing.t_max
          })
        }}
      </p>

      <!-- 报价 -->
      <div class="rounded-md border border-gray-200 bg-gray-50 p-4 dark:border-dark-700 dark:bg-dark-800/40">
        <div v-if="quoteError" class="text-sm text-primary-700 dark:text-primary-400">
          {{ t('subscriptionPurchase.quoteFailed') }}
        </div>
        <!-- quoteError 用 primary（Signal）表错误，符合设计系统语义。 -->
        <template v-else>
          <div class="flex items-end justify-between">
            <span class="text-sm text-gray-600 dark:text-gray-400">{{ t('subscriptionPurchase.price') }}</span>
            <span class="font-mono text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">
              <span v-if="quoting" class="text-base text-gray-400">{{ t('subscriptionPurchase.quoting') }}</span>
              <span v-else>${{ (quote?.price ?? 0).toFixed(2) }}</span>
            </span>
          </div>
          <dl class="mt-3 grid grid-cols-3 gap-2 border-t border-gray-200 pt-3 text-center dark:border-dark-700">
            <div>
              <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('subscriptionPurchase.unitPrice') }}</dt>
              <dd class="font-mono text-sm tabular-nums text-gray-900 dark:text-white">×{{ (quote?.unit_price ?? 0).toFixed(2) }}</dd>
            </div>
            <div>
              <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('subscriptionPurchase.weeklyCap') }}</dt>
              <dd class="font-mono text-sm tabular-nums text-gray-900 dark:text-white">${{ (quote?.weekly_cap_usd ?? 0).toFixed(2) }}</dd>
            </div>
            <div>
              <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('subscriptionPurchase.monthlyCap') }}</dt>
              <dd class="font-mono text-sm tabular-nums text-gray-900 dark:text-white">${{ (quote?.monthly_cap_usd ?? 0).toFixed(2) }}</dd>
            </div>
          </dl>
          <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">{{ t('subscriptionPurchase.capHint') }}</p>
        </template>
      </div>

      <button
        type="button"
        class="btn btn-primary w-full"
        :disabled="quoting || quoteError || !quote"
        @click="onBuy"
      >
        {{ t('subscriptionPurchase.buy') }}
      </button>
    </template>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import subscriptionsAPI, {
  type SubscriptionPricingBounds,
  type SubscriptionQuote
} from '@/api/subscriptions'

const emit = defineEmits<{
  // 购买意向：把校验过的 D/T 与当前报价交给父组件去走下单流程（订单创建/支付）。
  (e: 'purchase', payload: { dailyAmountUsd: number; validityDays: number; quote: SubscriptionQuote }): void
}>()

const { t } = useI18n()

const pricing = ref<SubscriptionPricingBounds | null>(null)
const loadError = ref(false)
const dailyAmount = ref(0)
const validityDays = ref(0)
const quote = ref<SubscriptionQuote | null>(null)
const quoting = ref(false)
const quoteError = ref(false)

let quoteTimer: ReturnType<typeof setTimeout> | null = null
let quoteSeq = 0 // 防抖 + 乱序保护：只采用最新一次请求的结果

function clamp(v: number, lo: number, hi: number): number {
  if (Number.isNaN(v)) return lo
  return Math.min(Math.max(v, lo), hi)
}

// 输入框失焦/回车时把越界值夹回允许范围（滑块本身已受 min/max 约束）。
function clampInputs() {
  if (!pricing.value) return
  dailyAmount.value = clamp(dailyAmount.value, pricing.value.d_min, pricing.value.d_max)
  validityDays.value = clamp(Math.round(validityDays.value), pricing.value.t_min, pricing.value.t_max)
}

async function refreshQuote() {
  if (!pricing.value) return
  const seq = ++quoteSeq
  quoting.value = true
  quoteError.value = false
  try {
    const q = await subscriptionsAPI.quoteSubscription(dailyAmount.value, validityDays.value)
    if (seq !== quoteSeq) return // 已有更新的请求，丢弃过期结果
    quote.value = q
  } catch {
    if (seq !== quoteSeq) return
    quote.value = null
    quoteError.value = true
  } finally {
    if (seq === quoteSeq) quoting.value = false
  }
}

watch([dailyAmount, validityDays], () => {
  if (quoteTimer) clearTimeout(quoteTimer)
  quoteTimer = setTimeout(refreshQuote, 300)
})

function onBuy() {
  if (!quote.value) return
  emit('purchase', {
    dailyAmountUsd: dailyAmount.value,
    validityDays: validityDays.value,
    quote: quote.value
  })
}

onMounted(async () => {
  try {
    const bounds = await subscriptionsAPI.getSubscriptionPricing()
    pricing.value = bounds
    // 合理默认：每日额度取区间内偏低档（贴近 d_min 的整数），有效期取最短可买（t_min）。
    dailyAmount.value = clamp(Math.max(bounds.d_min, 2), bounds.d_min, bounds.d_max)
    validityDays.value = bounds.t_min
    await refreshQuote()
  } catch {
    loadError.value = true
  }
})
</script>
