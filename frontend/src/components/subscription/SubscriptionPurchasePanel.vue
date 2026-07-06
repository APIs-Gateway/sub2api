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
            :min="dailyAmountMin"
            :max="dailyAmountMax"
            :step="dailyAmountStep"
            class="h-2 flex-1 accent-gray-900 dark:accent-gray-100"
          />
          <input
            v-model.number="dailyAmount"
            type="number"
            :min="dailyAmountMin"
            :max="dailyAmountMax"
            :step="dailyAmountStep"
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
        <div class="grid grid-cols-2 gap-2 sm:grid-cols-4">
          <button
            v-for="option in validityOptions"
            :key="option.days"
            type="button"
            :disabled="option.disabled"
            class="rounded-md border px-3 py-2 text-sm font-medium transition-colors"
            :class="[
              validityDays === option.days
                ? 'border-gray-900 bg-gray-900 text-white dark:border-gray-100 dark:bg-gray-100 dark:text-gray-950'
                : 'border-gray-200 bg-white text-gray-700 hover:border-gray-400 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-300 dark:hover:border-dark-500',
              option.disabled ? 'cursor-not-allowed opacity-40' : ''
            ]"
            @click="validityDays = option.days"
          >
            {{ option.label }}
          </button>
        </div>
      </div>

      <p class="input-hint">
        {{
          t('subscriptionPurchase.rangeHint', {
            dMin: dailyAmountMin,
            dMax: dailyAmountMax,
            tMin: pricing.t_min,
            tMax: pricing.t_max,
            tStep: tStep
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
          <div class="flex min-w-0 flex-col gap-1 sm:flex-row sm:items-end sm:justify-between">
            <span class="min-w-0 text-sm text-gray-600 dark:text-gray-400">
              {{ t('subscriptionPurchase.priceWithCurrency', { currency: paymentCurrency }) }}
            </span>
            <span
              data-testid="subscription-purchase-price-value"
              class="block w-full max-w-full min-w-0 break-all text-right font-mono text-xl font-semibold leading-tight tabular-nums text-gray-900 dark:text-white sm:w-auto sm:flex-1 sm:text-2xl sm:text-right"
            >
              <span v-if="quoting" class="text-base text-gray-400">{{ t('subscriptionPurchase.quoting') }}</span>
              <span v-else>{{ formattedPayableAmount }}</span>
            </span>
          </div>
          <dl class="mt-3 grid grid-cols-2 gap-2 border-t border-gray-200 pt-3 text-center dark:border-dark-700 sm:grid-cols-4">
            <div>
              <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('subscriptionPurchase.unitPrice') }}</dt>
              <dd class="font-mono text-sm tabular-nums text-gray-900 dark:text-white">×{{ (quote?.unit_price ?? 0).toFixed(4) }}</dd>
            </div>
            <div>
              <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('subscriptionPurchase.concurrency') }}</dt>
              <dd class="font-mono text-sm tabular-nums text-gray-900 dark:text-white">{{ subscriptionConcurrency }}</dd>
            </div>
            <div>
              <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('subscriptionPurchase.weeklyCap') }}</dt>
              <dd class="font-mono text-sm tabular-nums text-gray-900 dark:text-white">{{ formatUSDValue(quote?.weekly_cap_usd ?? 0) }}</dd>
            </div>
            <div>
              <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('subscriptionPurchase.monthlyCap') }}</dt>
              <dd class="font-mono text-sm tabular-nums text-gray-900 dark:text-white">{{ formatUSDValue(quote?.monthly_cap_usd ?? 0) }}</dd>
            </div>
          </dl>
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
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import subscriptionsAPI, {
  type SubscriptionPricingBounds,
  type SubscriptionQuote
} from '@/api/subscriptions'
import { ceilPaymentAmount, formatPaymentAmount, normalizePaymentCurrency } from '@/components/payment/currency'

const emit = defineEmits<{
  // 购买意向：把校验过的 D/T 与当前报价交给父组件去走下单流程（订单创建/支付）。
  (e: 'purchase', payload: { dailyAmountUsd: number; validityDays: number; quote: SubscriptionQuote }): void
}>()

const props = withDefaults(defineProps<{
  paymentCurrency?: string
  subscriptionPaymentMultiplier?: number
  locale?: string
}>(), {
  paymentCurrency: 'CNY',
  subscriptionPaymentMultiplier: 1,
  locale: undefined,
})

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
const dailyAmountStep = 30
const concurrencyUnitUSD = 10

// 有效期步长：T 必须为该值整数倍（按整月购买）；后端缺省/旧响应回退 30。
const tStep = computed(() => {
  const s = pricing.value?.t_step
  return s && s > 0 ? s : 30
})

const paymentCurrency = computed(() => normalizePaymentCurrency(props.paymentCurrency))
const subscriptionPaymentMultiplier = computed(() =>
  props.subscriptionPaymentMultiplier > 0 ? props.subscriptionPaymentMultiplier : 1
)
const payableAmount = computed(() =>
  ceilPaymentAmount((quote.value?.price ?? 0) / subscriptionPaymentMultiplier.value, paymentCurrency.value)
)
const formattedPayableAmount = computed(() =>
  formatPaymentAmount(payableAmount.value, paymentCurrency.value, props.locale)
)

function formatUSDValue(value: number): string {
  return `USD ${Number.isFinite(value) ? value.toFixed(2) : '0.00'}`
}

const validityOptions = computed(() => {
  const options = [
    { days: 30, label: t('subscriptionPurchase.validityMonth') },
    { days: 90, label: t('subscriptionPurchase.validityQuarter') },
    { days: 180, label: t('subscriptionPurchase.validityHalfYear') },
    { days: 360, label: t('subscriptionPurchase.validityYear') },
  ]
  if (!pricing.value) return options.map(option => ({ ...option, disabled: true }))
  return options.map(option => ({
    ...option,
    disabled: option.days < pricing.value!.t_min || option.days > pricing.value!.t_max || option.days % tStep.value !== 0,
  }))
})

const dailyAmountMin = computed(() => {
  if (!pricing.value) return dailyAmountStep
  return Math.max(dailyAmountStep, Math.ceil(pricing.value.d_min / dailyAmountStep) * dailyAmountStep)
})
const dailyAmountMax = computed(() => {
  if (!pricing.value) return dailyAmountStep
  return Math.max(dailyAmountMin.value, Math.floor(pricing.value.d_max / dailyAmountStep) * dailyAmountStep)
})
const subscriptionConcurrency = computed(() => Math.max(1, Math.ceil(dailyAmount.value / concurrencyUnitUSD)))

function clamp(v: number, lo: number, hi: number): number {
  if (Number.isNaN(v)) return lo
  return Math.min(Math.max(v, lo), hi)
}

// 把 T 吸附到最近的整月（tStep 整数倍）后再夹回 [t_min, t_max]，与后端 ValidateCustom 的整月约束一致，
// 避免提交 31/45 这类非整月值被后端 INVALID_SUBSCRIPTION_PARAMS 拒。
function snapValidity(v: number): number {
  if (!pricing.value) return v
  const step = tStep.value
  const snapped = step > 0 ? Math.round(v / step) * step : Math.round(v)
  return clamp(snapped, pricing.value.t_min, pricing.value.t_max)
}

// 输入框失焦/回车时把越界值夹回允许范围（滑块本身已受 min/max/step 约束）。
function clampInputs() {
  if (!pricing.value) return
  dailyAmount.value = snapDailyAmount(dailyAmount.value)
  validityDays.value = snapValidity(validityDays.value)
}

function snapDailyAmount(v: number): number {
  if (!pricing.value) return v
  const snapped = Math.round(v / dailyAmountStep) * dailyAmountStep
  return clamp(snapped, dailyAmountMin.value, dailyAmountMax.value)
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
    // 合理默认：每日额度从 30 刀档起，有效期默认单月；若后端区间不含 30 天则取可用的第一档。
    dailyAmount.value = snapDailyAmount(dailyAmountMin.value)
    validityDays.value = validityOptions.value.find(option => !option.disabled)?.days ?? snapValidity(bounds.t_min)
    await refreshQuote()
  } catch {
    loadError.value = true
  }
})
</script>
