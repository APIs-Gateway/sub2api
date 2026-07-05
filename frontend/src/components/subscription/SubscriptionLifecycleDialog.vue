<template>
  <BaseDialog :show="show" :title="dialogTitle" width="normal" @close="emit('close')">
    <div class="space-y-4">
      <p class="text-sm text-gray-600 dark:text-gray-400">
        {{ mode === 'renew' ? t('userSubscriptions.lifecycle.renewHint') : t('userSubscriptions.lifecycle.changeHint') }}
      </p>

      <!-- Loading bounds -->
      <div v-if="loading" class="flex justify-center py-8">
        <div class="h-6 w-6 animate-spin rounded-full border-2 border-gray-400 border-t-transparent" />
      </div>

      <div v-else-if="loadError" class="rounded-md border border-dashed border-gray-200 px-4 py-6 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-gray-400">
        {{ t('userSubscriptions.lifecycle.loadFailed') }}
      </div>

      <template v-else-if="bounds">
        <!-- 每日额度 D -->
        <div>
          <label class="input-label">{{ t('userSubscriptions.lifecycle.dailyAmount') }}</label>
          <!-- 续费：D 固定为当前卡，只读展示 -->
          <div v-if="mode === 'renew'" class="input flex items-center justify-between bg-gray-50 dark:bg-dark-800/40">
            <span class="font-mono tabular-nums">${{ dailyAmount }}</span>
            <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('userSubscriptions.lifecycle.perDay') }}（{{ t('userSubscriptions.lifecycle.dFixed') }}）</span>
          </div>
          <!-- 转套餐：D 可改 -->
          <div v-else class="flex items-center gap-3">
            <input
              v-model.number="dailyAmount"
              type="range"
              :min="dailyAmountMin"
              :max="dailyAmountMax"
              :step="dailyAmountStep"
              class="h-2 flex-1 accent-gray-900 dark:accent-gray-100"
              @change="onParamChange"
            />
            <input
              v-model.number="dailyAmount"
              type="number"
              :min="dailyAmountMin"
              :max="dailyAmountMax"
              :step="dailyAmountStep"
              class="input w-24 text-right font-mono tabular-nums"
              @change="clampDailyAndQuote"
            />
          </div>
        </div>

        <!-- 有效期 T（整月：30/60/90） -->
        <div>
          <label class="input-label">{{ t('userSubscriptions.lifecycle.validity') }}</label>
          <div class="grid grid-cols-3 gap-2">
            <button
              v-for="opt in tOptions"
              :key="opt"
              type="button"
              class="rounded-lg border px-3 py-2 text-sm transition-colors"
              :class="
                validityDays === opt
                  ? 'border-gray-900 bg-gray-50 font-semibold text-gray-900 dark:border-gray-100 dark:bg-dark-800/60 dark:text-white'
                  : 'border-gray-200 text-gray-600 hover:border-gray-300 dark:border-dark-700 dark:text-gray-300 dark:hover:border-dark-600'
              "
              @click="selectValidity(opt)"
            >
              {{ opt }} {{ t('userSubscriptions.lifecycle.days') }}
            </button>
          </div>
        </div>

        <!-- 报价 -->
        <div class="rounded-md border border-gray-200 bg-gray-50 p-4 dark:border-dark-700 dark:bg-dark-800/40">
          <div v-if="quoting" class="text-sm text-gray-500 dark:text-gray-400">{{ t('common.loading') }}</div>
          <div v-else-if="quoteErrorMsg" class="text-sm text-primary-700 dark:text-primary-400">
            {{ quoteErrorMsg }}
          </div>
          <template v-else-if="mode === 'renew' && renewQuoteData">
            <div class="flex items-baseline justify-between">
              <span class="text-sm text-gray-600 dark:text-gray-400">{{ t('userSubscriptions.lifecycle.renewPrice') }}</span>
              <span class="text-lg font-bold text-gray-900 dark:text-white">{{ formatUSDValue(renewQuoteData.price) }}</span>
            </div>
          </template>
          <template v-else-if="mode === 'change' && changeQuoteData">
            <div class="flex items-baseline justify-between">
              <span class="text-sm text-gray-600 dark:text-gray-400">{{ t('userSubscriptions.lifecycle.changeDiff') }}</span>
              <span class="text-lg font-bold text-gray-900 dark:text-white">{{ formatUSDValue(changeQuoteData.diff) }}</span>
            </div>
            <div class="mt-1 space-y-0.5 text-xs text-gray-500 dark:text-gray-400">
              <div>{{ t('userSubscriptions.lifecycle.newPlanPrice') }}: {{ formatUSDValue(changeQuoteData.new_plan_price) }}</div>
              <div>{{ t('userSubscriptions.lifecycle.oldRemainingValue') }}: {{ formatUSDValue(changeQuoteData.old_remaining_value) }}</div>
              <div>{{ t('userSubscriptions.lifecycle.caps', { weekly: formatUSDValue(changeQuoteData.weekly_cap_usd), monthly: formatUSDValue(changeQuoteData.monthly_cap_usd) }) }}</div>
            </div>
          </template>
        </div>

        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('userSubscriptions.lifecycle.gatewayNote') }}</p>
      </template>
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
        <button
          type="button"
          class="btn btn-primary"
          :disabled="!canConfirm"
          @click="handleConfirm"
        >
          {{ t('userSubscriptions.lifecycle.goPay') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import subscriptionsAPI, {
  type SubscriptionPricingBounds,
  type RenewOrderQuote,
  type ChangePlanOrderQuote
} from '@/api/subscriptions'
import { extractApiErrorMessage, extractI18nErrorMessage } from '@/utils/apiError'
import type { UserSubscription } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{
  show: boolean
  mode: 'renew' | 'change'
  subscription: UserSubscription
}>()

const emit = defineEmits<{
  close: []
  // 前往法币支付网关结账：父组件据此跳转 /purchase（intent + D/T + 预估金额 charge）。
  purchase: [payload: { intent: 'renew' | 'change_plan'; dailyAmountUsd: number; validityDays: number; charge: number }]
}>()

const { t } = useI18n()
const appStore = useAppStore()

const bounds = ref<SubscriptionPricingBounds | null>(null)
const loading = ref(false)
const loadError = ref(false)
const dailyAmount = ref(0)
const validityDays = ref(0)
const renewQuoteData = ref<RenewOrderQuote | null>(null)
const changeQuoteData = ref<ChangePlanOrderQuote | null>(null)
const quoting = ref(false)
const quoteErrorMsg = ref('')

let quoteTimer: ReturnType<typeof setTimeout> | null = null
let quoteSeq = 0
const dailyAmountStep = 30
const MONEY_CENTS = 100

const dialogTitle = computed(() =>
  props.mode === 'renew'
    ? t('userSubscriptions.lifecycle.renewTitle')
    : t('userSubscriptions.lifecycle.changeTitle')
)

// 有效期可选项 = [t_min, t_min+t_step, …, t_max]（默认 30/60/90，整月）。
const tOptions = computed<number[]>(() => {
  const b = bounds.value
  if (!b) return []
  const step = b.t_step > 0 ? b.t_step : 30
  const out: number[] = []
  for (let v = b.t_min; v <= b.t_max; v += step) out.push(v)
  return out
})

const dailyAmountMin = computed(() => {
  if (!bounds.value) return dailyAmountStep
  return Math.max(dailyAmountStep, Math.ceil(bounds.value.d_min / dailyAmountStep) * dailyAmountStep)
})

const dailyAmountMax = computed(() => {
  if (!bounds.value) return dailyAmountStep
  return Math.max(dailyAmountMin.value, Math.floor(bounds.value.d_max / dailyAmountStep) * dailyAmountStep)
})

const renewCharge = computed(() => roundMoney(renewQuoteData.value?.price ?? 0))
const changeCharge = computed(() => roundMoney(changeQuoteData.value?.diff ?? 0))

// 续费：报价成功(price>0)即可去支付；转套餐：必须 diff>0（diff≤0 后端报价已拒，按钮禁用）。
const canConfirm = computed(() => {
  if (quoting.value || validityDays.value <= 0) return false
  if (props.mode === 'renew') return renewQuoteData.value != null && renewCharge.value > 0
  return changeQuoteData.value != null && changeCharge.value > 0
})

function roundMoney(value: number): number {
  if (!Number.isFinite(value)) return 0
  return Math.round((value + Number.EPSILON) * MONEY_CENTS) / MONEY_CENTS
}

function formatUSD(value: number): string {
  return roundMoney(value).toFixed(2)
}

function formatUSDValue(value: number): string {
  return `USD ${formatUSD(value)}`
}

function clamp(v: number, lo: number, hi: number): number {
  if (Number.isNaN(v)) return lo
  return Math.min(Math.max(v, lo), hi)
}

function clampDailyAndQuote() {
  if (!bounds.value) return
  dailyAmount.value = snapDailyAmount(dailyAmount.value)
  scheduleQuote()
}

function selectValidity(v: number) {
  validityDays.value = v
  scheduleQuote()
}

function onParamChange() {
  if (props.mode === 'change') {
    dailyAmount.value = snapDailyAmount(dailyAmount.value)
  }
  scheduleQuote()
}

function snapDailyAmount(v: number): number {
  const snapped = Math.round(v / dailyAmountStep) * dailyAmountStep
  return clamp(snapped, dailyAmountMin.value, dailyAmountMax.value)
}

function scheduleQuote() {
  if (quoteTimer) clearTimeout(quoteTimer)
  quoteTimer = setTimeout(refreshQuote, 250)
}

async function refreshQuote() {
  if (validityDays.value <= 0) return
  if (props.mode === 'change' && dailyAmount.value <= 0) return
  const seq = ++quoteSeq
  quoting.value = true
  quoteErrorMsg.value = ''
  renewQuoteData.value = null
  changeQuoteData.value = null
  try {
    if (props.mode === 'renew') {
      const q = await subscriptionsAPI.renewQuote(validityDays.value)
      if (seq !== quoteSeq) return
      renewQuoteData.value = q
    } else {
      const q = await subscriptionsAPI.changePlanQuote(dailyAmount.value, validityDays.value)
      if (seq !== quoteSeq) return
      changeQuoteData.value = q
    }
  } catch (err: unknown) {
    if (seq !== quoteSeq) return
    // 把后端语义错误码（CHANGE_PLAN_DOWNGRADE_NOT_ALLOWED / CHANGE_PLAN_DAILY_LIMIT /
    // NO_ACTIVE_SUBSCRIPTION / INVALID_SUBSCRIPTION_PARAMS 等）映射为友好本地化文案。
    quoteErrorMsg.value = extractI18nErrorMessage(err, t, 'userSubscriptions.lifecycle.errors', t('userSubscriptions.lifecycle.quoteFailed'))
  } finally {
    if (seq === quoteSeq) quoting.value = false
  }
}

watch(
  () => props.show,
  async (visible) => {
    if (!visible) return
    renewQuoteData.value = null
    changeQuoteData.value = null
    quoteErrorMsg.value = ''
    try {
      loading.value = true
      loadError.value = false
      bounds.value = await subscriptionsAPI.getSubscriptionPricing()
    } catch (err: unknown) {
      loadError.value = true
      appStore.showError(extractApiErrorMessage(err, t('common.error')))
      loading.value = false
      return
    } finally {
      loading.value = false
    }
    const b = bounds.value
    if (!b) return
    // 续费：D 固定为当前卡；转套餐：默认取当前卡 D（夹到区间）便于在其上调整。
    const cardD = props.subscription.daily_amount_usd ?? b.d_min
    dailyAmount.value = props.mode === 'renew' ? cardD : snapDailyAmount(cardD)
    validityDays.value = b.t_min
    await refreshQuote()
  },
  { immediate: true }
)

// 确认 → 不在此扣费/换卡，而是带「意图 + D/T + 预估金额」交给父组件跳转法币支付网关结账。
function handleConfirm() {
  if (!canConfirm.value) return
  if (props.mode === 'renew') {
    emit('purchase', { intent: 'renew', dailyAmountUsd: dailyAmount.value, validityDays: validityDays.value, charge: renewCharge.value })
  } else {
    emit('purchase', { intent: 'change_plan', dailyAmountUsd: dailyAmount.value, validityDays: validityDays.value, charge: changeCharge.value })
  }
  emit('close')
}
</script>
