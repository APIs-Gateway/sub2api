<template>
  <AppLayout>
    <div class="mx-auto max-w-4xl space-y-6">
      <div v-if="loading" class="flex items-center justify-center py-20">
        <div class="h-8 w-8 animate-spin rounded-full border-4 border-primary-500 border-t-transparent"></div>
      </div>
      <template v-else>
        <!-- Tab Switcher (hide during payment and subscription confirm) -->
        <div v-if="tabs.length > 1 && paymentPhase === 'select' && !selectedPlan" class="flex space-x-1 rounded-md bg-gray-100 p-1 dark:bg-dark-800">
          <button v-for="tab in tabs" :key="tab.key"
            class="flex-1 rounded-md px-4 py-2.5 text-sm font-medium transition-colors"
            :class="activeTab === tab.key ? 'bg-white text-gray-900 dark:bg-dark-700 dark:text-white' : 'text-gray-600 hover:text-gray-900 dark:text-gray-400 dark:hover:text-gray-200'"
            @click="activeTab = tab.key">{{ tab.label }}</button>
        </div>
        <!-- Payment in progress (shared by recharge and subscription) -->
        <template v-if="paymentPhase === 'paying'">
          <PaymentStatusPanel
            :order-id="paymentState.orderId"
            :qr-code="paymentState.qrCode"
            :expires-at="paymentState.expiresAt"
            :payment-type="paymentState.paymentType"
            :pay-url="paymentState.payUrl"
            :order-type="paymentState.orderType"
            :currency="paymentState.currency || selectedCurrency"
            @done="onPaymentDone"
            @success="onPaymentSuccess"
            @settled="onPaymentSettled"
          />
        </template>
        <!-- Tab content (select phase) -->
        <template v-else>
          <!-- Top-up Tab -->
          <template v-if="activeTab === 'recharge'">
            <!-- Recharge Account Card -->
            <div class="card p-5">
              <p class="text-xs font-medium text-gray-600 dark:text-gray-400">{{ t('payment.rechargeAccount') }}</p>
              <p class="mt-1 text-base font-semibold text-gray-900 dark:text-white">{{ user?.username || '' }}</p>
              <p class="mt-0.5 text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('payment.currentBalance') }}: <span class="font-mono tabular-nums text-gray-900 dark:text-white">{{ user?.balance?.toFixed(2) || '0.00' }}</span></p>
            </div>
            <div v-if="enabledMethods.length === 0" class="card py-16 text-center">
              <p class="text-gray-500 dark:text-gray-400">{{ t('payment.notAvailable') }}</p>
            </div>
            <template v-else>
            <div class="card p-6">
              <AmountInput
                v-model="amount"
                :amounts="[10, 20, 50, 100, 200, 500, 1000, 2000, 5000]"
                :min="globalMinAmount"
                :max="globalMaxAmount"
                :currency-label="selectedCurrency"
                :prefix="selectedCurrencySymbol"
              />
              <p v-if="balanceRechargeMultiplier !== 1" class="mt-3 text-xs font-medium text-gray-600 dark:text-gray-400">
                {{ t('payment.rechargeMultiplier', { currency: selectedCurrency, usd: balanceRechargeMultiplier.toFixed(2) }) }}
              </p>
              <p v-if="amountError" class="mt-2 text-xs text-primary-700 dark:text-primary-400">{{ amountError }}</p>
            </div>
            <div v-if="enabledMethods.length >= 1" class="card p-6">
              <PaymentMethodSelector
                :methods="methodOptions"
                :selected="selectedMethod"
                @select="selectedMethod = $event"
              />
              <CryptoNetworkSelector v-if="selectedMethod === 'crypto'" v-model="cryptoNetwork" />
            </div>
            <div v-if="validAmount > 0" class="card p-6">
              <div class="space-y-2 text-sm">
                <div class="flex justify-between">
                  <span class="text-gray-600 dark:text-gray-400">{{ t('payment.paymentAmountWithCurrency', { currency: selectedCurrency }) }}</span>
                  <span class="font-mono tabular-nums text-gray-900 dark:text-white">{{ formatSelectedPaymentAmount(validAmount) }}</span>
                </div>
                <div v-if="feeRate > 0" class="flex justify-between">
                  <span class="text-gray-600 dark:text-gray-400">{{ t('payment.fee') }} ({{ feeRate }}%)</span>
                  <span class="font-mono tabular-nums text-gray-900 dark:text-white">{{ formatSelectedPaymentAmount(feeAmount) }}</span>
                </div>
                <div v-if="feeRate > 0" class="flex justify-between border-t border-gray-200 pt-2 dark:border-dark-600">
                  <span class="font-medium text-gray-700 dark:text-gray-300">{{ t('payment.actualPay') }}</span>
                  <span class="font-mono tabular-nums text-lg font-bold text-gray-900 dark:text-white">{{ formatSelectedPaymentAmount(totalAmount) }}</span>
                </div>
                <div v-if="balanceRechargeMultiplier !== 1" class="flex justify-between" :class="{ 'border-t border-gray-200 pt-2 dark:border-dark-600': feeRate <= 0 }">
                  <span class="text-gray-600 dark:text-gray-400">{{ t('payment.creditedBalanceWithCurrency', { currency: 'USD' }) }}</span>
                  <span class="font-mono tabular-nums text-gray-900 dark:text-white">{{ formatUSDValue(creditedAmount) }}</span>
                </div>
                <p v-if="balanceRechargeMultiplier !== 1" class="border-t border-gray-200 pt-2 text-xs text-gray-600 dark:border-dark-600 dark:text-gray-400">
                  {{ t('payment.rechargeRatePreview', { currency: selectedCurrency, usd: balanceRechargeMultiplier.toFixed(2) }) }}
                </p>
              </div>
            </div>
            <button :class="['btn w-full py-3 text-base font-medium', paymentButtonClass]" :disabled="!canSubmit || submitting" @click="handleSubmitRecharge">
              <span v-if="submitting" class="flex items-center justify-center gap-2">
                <span class="h-4 w-4 animate-spin rounded-full border-2 border-white border-t-transparent"></span>
                {{ t('common.processing') }}
              </span>
              <span v-else>{{ t('payment.createOrder') }} {{ formatSelectedPaymentAmount(totalAmount) }}</span>
            </button>
            </template>
          </template>
          <!-- Subscribe Tab -->
          <template v-else-if="activeTab === 'subscription'">
            <!-- 续费/转套餐结账（per-day redesign §5/§7）：从「我的订阅」带 D/T+意图跳来，走法币网关下单。 -->
            <template v-if="lifecycleOrder">
              <div class="card p-5">
                <div class="mb-3 flex flex-wrap items-center gap-2">
                  <h3 class="text-lg font-bold text-gray-900 dark:text-white">
                    {{ lifecycleOrder.intent === 'renew' ? t('userSubscriptions.lifecycle.renewTitle') : t('userSubscriptions.lifecycle.changeTitle') }}
                  </h3>
                </div>
                <div class="flex items-baseline gap-2">
                  <span class="font-mono tabular-nums text-3xl font-bold text-gray-900 dark:text-white">{{ formatSelectedPaymentAmount(lifecyclePaymentAmount) }}</span>
                  <span class="text-sm text-gray-600 dark:text-gray-400">
                    {{ t('payment.paymentAmountWithCurrency', { currency: selectedCurrency }) }}
                  </span>
                </div>
                <div class="mt-3 grid grid-cols-2 gap-3">
                  <div>
                    <span class="text-xs text-gray-600 dark:text-gray-400">{{ lifecycleOrder.intent === 'renew' ? t('userSubscriptions.lifecycle.renewValue') : t('userSubscriptions.lifecycle.changeDiffValue') }}</span>
                    <div class="font-mono tabular-nums text-lg font-semibold text-gray-900 dark:text-white">{{ formatUSDValue(lifecycleOrder.amount) }}</div>
                  </div>
                  <div>
                    <span class="text-xs text-gray-600 dark:text-gray-400">{{ t('userSubscriptions.lifecycle.dailyAmount') }}</span>
                    <div class="font-mono tabular-nums text-lg font-semibold text-gray-900 dark:text-white">{{ formatUSDValue(lifecycleOrder.dailyAmountUsd) }}</div>
                  </div>
                  <div>
                    <span class="text-xs text-gray-600 dark:text-gray-400">{{ t('userSubscriptions.lifecycle.validity') }}</span>
                    <div class="font-mono tabular-nums text-lg font-semibold text-gray-900 dark:text-white">{{ lifecycleOrder.validityDays }} {{ t('userSubscriptions.lifecycle.days') }}</div>
                  </div>
                  <div>
                    <span class="text-xs text-gray-600 dark:text-gray-400">{{ t('payment.planCard.concurrency') }}</span>
                    <div class="font-mono tabular-nums text-lg font-semibold text-gray-900 dark:text-white">{{ lifecycleConcurrency }}</div>
                  </div>
                </div>
                <p class="mt-3 text-xs text-gray-500 dark:text-gray-400">{{ t('userSubscriptions.lifecycle.gatewayNote') }}</p>
              </div>
              <div v-if="enabledMethods.length >= 1" class="card p-6">
                <PaymentMethodSelector
                  :methods="lifecycleMethodOptions"
                  :selected="selectedMethod"
                  @select="selectedMethod = $event"
                />
                <CryptoNetworkSelector v-if="selectedMethod === 'crypto'" v-model="cryptoNetwork" />
              </div>
              <div v-if="feeRate > 0 && lifecyclePaymentAmount > 0" class="card p-6">
                <div class="space-y-2 text-sm">
                  <div class="flex justify-between">
                    <span class="text-gray-600 dark:text-gray-400">{{ t('payment.paymentAmountWithCurrency', { currency: selectedCurrency }) }}</span>
                    <span class="font-mono tabular-nums text-gray-900 dark:text-white">{{ formatSelectedPaymentAmount(lifecyclePaymentAmount) }}</span>
                  </div>
                  <div class="flex justify-between">
                    <span class="text-gray-600 dark:text-gray-400">{{ t('payment.fee') }} ({{ feeRate }}%)</span>
                    <span class="font-mono tabular-nums text-gray-900 dark:text-white">{{ formatSelectedPaymentAmount(lifecycleFeeAmount) }}</span>
                  </div>
                  <div class="flex justify-between border-t border-gray-200 pt-2 dark:border-dark-600">
                    <span class="font-medium text-gray-700 dark:text-gray-300">{{ t('payment.actualPay') }}</span>
                    <span class="font-mono tabular-nums text-lg font-bold text-gray-900 dark:text-white">{{ formatSelectedPaymentAmount(lifecycleTotalAmount) }}</span>
                  </div>
                </div>
              </div>
              <button :class="['btn w-full py-3 text-base font-medium', paymentButtonClass]" :disabled="!canSubmitLifecycle || submitting" @click="confirmLifecycle">
                <span v-if="submitting" class="flex items-center justify-center gap-2">
                  <span class="h-4 w-4 animate-spin rounded-full border-2 border-white border-t-transparent"></span>
                  {{ t('common.processing') }}
                </span>
                <span v-else>{{ t('payment.createOrder') }} {{ formatSelectedPaymentAmount(feeRate > 0 ? lifecycleTotalAmount : lifecyclePaymentAmount) }}</span>
              </button>
              <button class="btn btn-secondary w-full" @click="lifecycleOrder = null">{{ t('common.cancel') }}</button>
            </template>
            <!-- Subscription confirm (inline, replaces plan list) -->
            <template v-else-if="selectedPlan">
              <div class="card p-5">
                <!-- Header: platform badge + plan name -->
                <div class="mb-3 flex flex-wrap items-center gap-2">
                  <span :class="['rounded-md border px-2 py-0.5 text-xs font-medium', planBadgeClass]">
                    {{ platformLabel(selectedPlan.group_platform || '') }}
                  </span>
                  <h3 class="text-lg font-bold text-gray-900 dark:text-white">{{ selectedPlan.name }}</h3>
                </div>
                <!-- Price -->
                <div class="flex items-baseline gap-2">
                  <span v-if="selectedPlan.original_price" class="font-mono tabular-nums text-sm text-gray-500 line-through dark:text-gray-500">
                    {{ formatSelectedPaymentAmount(selectedPlanOriginalPaymentAmount) }}
                  </span>
                  <span class="font-mono tabular-nums text-3xl font-bold text-gray-900 dark:text-white">{{ formatSelectedPaymentAmount(selectedPlanPaymentAmount) }}</span>
                  <span class="text-sm text-gray-600 dark:text-gray-400">/ {{ planValiditySuffix }}</span>
                </div>
                <!-- Description -->
                <p v-if="selectedPlan.description" class="mt-2 text-sm leading-relaxed text-gray-600 dark:text-gray-400">
                  {{ selectedPlan.description }}
                </p>
                <!-- Rate + Limits grid -->
                <div class="mt-3 grid grid-cols-2 gap-3">
                  <div>
                    <span class="text-xs text-gray-600 dark:text-gray-400">{{ t('payment.planCard.rate') }}</span>
                    <div class="flex items-baseline">
                      <span class="font-mono tabular-nums text-lg font-bold text-gray-900 dark:text-white">×{{ selectedPlan.rate_multiplier ?? 1 }}</span>
                    </div>
                  </div>
                  <div v-if="selectedPlan.daily_limit_usd != null">
                    <span class="text-xs text-gray-600 dark:text-gray-400">{{ t('payment.planCard.dailyLimit') }}</span>
                    <div class="font-mono tabular-nums text-lg font-semibold text-gray-900 dark:text-white">{{ formatUSDValue(selectedPlan.daily_limit_usd) }}</div>
                  </div>
                  <div v-if="selectedPlan.weekly_limit_usd != null">
                    <span class="text-xs text-gray-600 dark:text-gray-400">{{ t('payment.planCard.weeklyLimit') }}</span>
                    <div class="font-mono tabular-nums text-lg font-semibold text-gray-900 dark:text-white">{{ formatUSDValue(selectedPlan.weekly_limit_usd) }}</div>
                  </div>
                  <div v-if="selectedPlan.monthly_limit_usd != null">
                    <span class="text-xs text-gray-600 dark:text-gray-400">{{ t('payment.planCard.monthlyLimit') }}</span>
                    <div class="font-mono tabular-nums text-lg font-semibold text-gray-900 dark:text-white">{{ formatUSDValue(selectedPlan.monthly_limit_usd) }}</div>
                  </div>
                  <div>
                    <span class="text-xs text-gray-600 dark:text-gray-400">{{ t('payment.subscriptionValueWithCurrency', { currency: 'USD' }) }}</span>
                    <div class="font-mono tabular-nums text-lg font-semibold text-gray-900 dark:text-white">{{ formatUSDValue(selectedPlan.price) }}</div>
                  </div>
                  <div v-if="selectedPlan.daily_limit_usd == null && selectedPlan.weekly_limit_usd == null && selectedPlan.monthly_limit_usd == null">
                    <span class="text-xs text-gray-600 dark:text-gray-400">{{ t('payment.planCard.quota') }}</span>
                    <div class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('payment.planCard.unlimited') }}</div>
                  </div>
                  <div v-if="selectedPlanConcurrency > 0">
                    <span class="text-xs text-gray-600 dark:text-gray-400">{{ t('payment.planCard.concurrency') }}</span>
                    <div class="font-mono tabular-nums text-lg font-semibold text-gray-900 dark:text-white">{{ selectedPlanConcurrency }}</div>
                  </div>
                </div>
              </div>
              <div v-if="enabledMethods.length >= 1" class="card p-6">
                <PaymentMethodSelector
                  :methods="subMethodOptions"
                  :selected="selectedMethod"
                  @select="selectedMethod = $event"
                />
                <CryptoNetworkSelector v-if="selectedMethod === 'crypto'" v-model="cryptoNetwork" />
              </div>
              <div v-if="feeRate > 0 && selectedPlanPaymentAmount > 0" class="card p-6">
                <div class="space-y-2 text-sm">
                  <div class="flex justify-between">
                    <span class="text-gray-600 dark:text-gray-400">{{ t('payment.paymentAmountWithCurrency', { currency: selectedCurrency }) }}</span>
                    <span class="font-mono tabular-nums text-gray-900 dark:text-white">{{ formatSelectedPaymentAmount(selectedPlanPaymentAmount) }}</span>
                  </div>
                  <div class="flex justify-between">
                    <span class="text-gray-600 dark:text-gray-400">{{ t('payment.fee') }} ({{ feeRate }}%)</span>
                    <span class="font-mono tabular-nums text-gray-900 dark:text-white">{{ formatSelectedPaymentAmount(subFeeAmount) }}</span>
                  </div>
                  <div class="flex justify-between border-t border-gray-200 pt-2 dark:border-dark-600">
                    <span class="font-medium text-gray-700 dark:text-gray-300">{{ t('payment.actualPay') }}</span>
                    <span class="font-mono tabular-nums text-lg font-bold text-gray-900 dark:text-white">{{ formatSelectedPaymentAmount(subTotalAmount) }}</span>
                  </div>
                </div>
              </div>
              <button :class="['btn w-full py-3 text-base font-medium', paymentButtonClass]" :disabled="!canSubmitSubscription || submitting" @click="confirmSubscribe">
                <span v-if="submitting" class="flex items-center justify-center gap-2">
                  <span class="h-4 w-4 animate-spin rounded-full border-2 border-white border-t-transparent"></span>
                  {{ t('common.processing') }}
                </span>
                <span v-else>{{ t('payment.createOrder') }} {{ formatSelectedPaymentAmount(feeRate > 0 ? subTotalAmount : selectedPlanPaymentAmount) }}</span>
              </button>
              <button class="btn btn-secondary w-full" @click="selectedPlan = null">{{ t('common.cancel') }}</button>
            </template>
            <!-- Plan list -->
            <template v-else>
              <div v-if="hasActiveSubscription" class="card p-6">
                <div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
                  <div>
                    <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('payment.existingSubscriptionTitle') }}</h3>
                    <p class="mt-1 text-sm text-gray-600 dark:text-gray-400">{{ t('payment.existingSubscriptionDesc') }}</p>
                  </div>
                  <button type="button" class="btn btn-primary shrink-0" @click="goToSubscriptions">
                    {{ t('payment.changeExistingPlan') }}
                  </button>
                </div>
              </div>
              <template v-else>
                <!-- 自定义购买（无固定套餐）：自填 D+T，实时报价。买按钮经 onCustomPurchase 下单。 -->
                <div v-if="enabledMethods.length >= 1" class="card p-6">
                  <PaymentMethodSelector
                    :methods="subscriptionMethodOptions"
                    :selected="selectedMethod"
                    @select="selectedMethod = $event"
                  />
                  <CryptoNetworkSelector v-if="selectedMethod === 'crypto'" v-model="cryptoNetwork" />
                </div>
                <div class="card p-6">
                  <SubscriptionPurchasePanel
                    :payment-currency="selectedCurrency"
                    :subscription-payment-multiplier="subscriptionPaymentMultiplier"
                    :locale="localeCode"
                    @purchase="onCustomPurchase"
                  />
                </div>
                <BillingRulesCard class="mb-4" />
              </template>
            </template>
          </template>
        </template>
        <div v-if="(checkout.help_text || checkout.help_image_url) && paymentPhase === 'select' && !selectedPlan" class="card p-4">
          <div class="flex flex-col items-center gap-3">
            <img v-if="checkout.help_image_url" :src="checkout.help_image_url" alt=""
              class="h-40 max-w-full cursor-pointer rounded-lg object-contain transition-opacity hover:opacity-80"
              @click="previewImage = checkout.help_image_url" />
            <p v-if="checkout.help_text" class="text-center text-sm text-gray-600 dark:text-gray-400">{{ checkout.help_text }}</p>
          </div>
        </div>
      </template>
    </div>
    <!-- Renewal Plan Selection Modal -->
    <Teleport to="body">
      <Transition name="modal">
        <div v-if="showRenewalModal" class="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4" @click.self="closeRenewalModal">
          <div class="relative w-full max-w-lg rounded-lg border border-gray-200 bg-white p-6 shadow-overlay dark:border-dark-700 dark:bg-dark-900">
            <!-- Close button -->
            <button class="absolute right-4 top-4 rounded-md p-1 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 dark:hover:bg-dark-800 dark:hover:text-gray-200" @click="closeRenewalModal">
              <svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12" /></svg>
            </button>
            <h3 class="mb-4 text-lg font-semibold text-gray-900 dark:text-white">{{ t('payment.selectPlan') }}</h3>
            <div class="space-y-4">
              <SubscriptionPlanCard v-for="plan in renewalPlans" :key="plan.id" :plan="plan" :active-subscriptions="activeSubscriptions" @select="selectPlanFromModal" />
            </div>
          </div>
        </div>
      </Transition>
    </Teleport>
    <!-- Image Preview Overlay -->
    <Teleport to="body">
      <Transition name="modal">
        <div v-if="previewImage" class="fixed inset-0 z-[60] flex items-center justify-center bg-black/70" @click="previewImage = ''">
          <img :src="previewImage" alt="" class="max-h-[85vh] max-w-[90vw] rounded-md object-contain shadow-overlay" />
        </div>
      </Transition>
    </Teleport>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { usePaymentStore } from '@/stores/payment'
import { useSubscriptionStore } from '@/stores/subscriptions'
import { useAppStore } from '@/stores'
import { paymentAPI } from '@/api/payment'
import { extractApiErrorMessage, extractI18nErrorMessage } from '@/utils/apiError'
import { isMobileDevice } from '@/utils/device'
import type { SubscriptionPlan, CheckoutInfoResponse, CreateOrderResult, OrderType } from '@/types/payment'
import AppLayout from '@/components/layout/AppLayout.vue'
import AmountInput from '@/components/payment/AmountInput.vue'
import PaymentMethodSelector from '@/components/payment/PaymentMethodSelector.vue'
import CryptoNetworkSelector from '@/components/payment/CryptoNetworkSelector.vue'
import SubscriptionPurchasePanel from '@/components/subscription/SubscriptionPurchasePanel.vue'
import { METHOD_ORDER, getPaymentPopupFeatures } from '@/components/payment/providerConfig'
import {
  PAYMENT_RECOVERY_STORAGE_KEY,
  buildCreateOrderPayload,
  clearPaymentRecoverySnapshot,
  decidePaymentLaunch,
  getVisibleMethods,
  normalizeVisibleMethod,
  readPaymentRecoverySnapshot,
  type PaymentRecoverySnapshot,
  writePaymentRecoverySnapshot,
} from '@/components/payment/paymentFlow'
import { platformBadgeClass, platformLabel } from '@/utils/platformColors'
import SubscriptionPlanCard from '@/components/payment/SubscriptionPlanCard.vue'
import BillingRulesCard from '@/components/common/BillingRulesCard.vue'
import PaymentStatusPanel from '@/components/payment/PaymentStatusPanel.vue'
import { ceilPaymentAmount, formatPaymentAmount, normalizePaymentCurrency, paymentCurrencySymbol } from '@/components/payment/currency'
import type { PaymentMethodOption } from '@/components/payment/PaymentMethodSelector.vue'
import { buildPaymentErrorToastMessage, describePaymentScenarioError } from './paymentUx'
import { hasWechatResumeQuery, parseWechatResumeRoute, stripWechatResumeQuery } from './paymentWechatResume'

const i18n = useI18n()
const { t } = i18n
const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()
const paymentStore = usePaymentStore()
const subscriptionStore = useSubscriptionStore()
const appStore = useAppStore()

const user = computed(() => authStore.user)
const activeSubscriptions = computed(() => subscriptionStore.activeSubscriptions)
const hasActiveSubscription = computed(() => activeSubscriptions.value.length > 0)

const loading = ref(true)
const submitting = ref(false)
const errorMessage = ref('')
const errorHintMessage = ref('')
const activeTab = ref<'recharge' | 'subscription'>('subscription')
const amount = ref<number | null>(null)
const selectedMethod = ref('')
const cryptoNetwork = ref('usdt.trc20')
const selectedPlan = ref<SubscriptionPlan | null>(null)
const previewImage = ref('')

const paymentPhase = ref<'select' | 'paying'>('select')

interface CreateOrderOptions {
  openid?: string
  wechatResumeToken?: string
  paymentType?: string
  isResume?: boolean
  mobileQrFallbackAttempted?: boolean
  // 自定义订阅购买（无固定套餐）：每日额度 D + 有效期 T，与 planId 互斥。
  groupId?: number
  dailyAmountUsd?: number
  validityDays?: number
  // 订阅生命周期意图：'renew' / 'change_plan'（购买留空）。
  subscriptionIntent?: string
}

interface WeixinJSBridgeLike {
  invoke(
    action: string,
    payload: Record<string, unknown>,
    callback: (result: Record<string, unknown>) => void,
  ): void
}

function emptyPaymentState(): PaymentRecoverySnapshot {
  return {
    orderId: 0,
    amount: 0,
    qrCode: '',
    expiresAt: '',
    paymentType: '',
    payUrl: '',
    outTradeNo: '',
    clientSecret: '',
    intentId: '',
    currency: '',
    countryCode: '',
    paymentEnv: '',
    payAmount: 0,
    orderType: '',
    paymentMode: '',
    resumeToken: '',
    createdAt: 0,
  }
}

function getWeixinJSBridge(): WeixinJSBridgeLike | undefined {
  return (window as Window & { WeixinJSBridge?: WeixinJSBridgeLike }).WeixinJSBridge
}

function waitForWeixinJSBridge(timeoutMs = 4000): Promise<WeixinJSBridgeLike | null> {
  const existing = getWeixinJSBridge()
  if (existing) return Promise.resolve(existing)

  return new Promise((resolve) => {
    let settled = false
    const finish = (bridge: WeixinJSBridgeLike | null) => {
      if (settled) return
      settled = true
      document.removeEventListener('WeixinJSBridgeReady', handleReady)
      document.removeEventListener('onWeixinJSBridgeReady', handleReady)
      window.clearTimeout(timer)
      resolve(bridge)
    }
    const handleReady = () => finish(getWeixinJSBridge() ?? null)
    const timer = window.setTimeout(() => finish(getWeixinJSBridge() ?? null), timeoutMs)
    document.addEventListener('WeixinJSBridgeReady', handleReady, false)
    document.addEventListener('onWeixinJSBridgeReady', handleReady, false)
  })
}

async function invokeWechatJsapiPayment(payload: Record<string, unknown>): Promise<Record<string, unknown>> {
  const bridge = await waitForWeixinJSBridge()
  if (!bridge) {
    throw new Error('WECHAT_JSAPI_UNAVAILABLE')
  }
  return new Promise((resolve) => {
    bridge.invoke('getBrandWCPayRequest', payload, (result) => resolve(result || {}))
  })
}

const paymentState = ref<PaymentRecoverySnapshot>(emptyPaymentState())

function persistRecoverySnapshot(snapshot: PaymentRecoverySnapshot) {
  if (typeof window === 'undefined' || !snapshot.orderId) return
  writePaymentRecoverySnapshot(window.localStorage, snapshot, PAYMENT_RECOVERY_STORAGE_KEY)
}

function removeRecoverySnapshot() {
  if (typeof window === 'undefined') return
  clearPaymentRecoverySnapshot(window.localStorage, PAYMENT_RECOVERY_STORAGE_KEY)
}

function resetPayment() {
  paymentPhase.value = 'select'
  paymentState.value = emptyPaymentState()
  removeRecoverySnapshot()
}

async function redirectToPaymentResult(state: PaymentRecoverySnapshot): Promise<void> {
  const query: Record<string, string | undefined> = {}
  if (state.orderId > 0) {
    query.order_id = String(state.orderId)
  }
  if (state.outTradeNo) {
    query.out_trade_no = state.outTradeNo
  }
  if (state.resumeToken) {
    query.resume_token = state.resumeToken
  }
  await router.push({
    path: '/payment/result',
    query,
  })
}

function buildWechatOAuthAuthorizeUrl(
  authorizeUrl: string,
  context: {
    paymentType: string
    orderType: OrderType
    planId?: number
    groupId?: number
    orderAmount: number
    dailyAmountUsd?: number
    validityDays?: number
  },
): string {
  const normalizedUrl = authorizeUrl.trim()
  if (!normalizedUrl || typeof window === 'undefined') {
    return normalizedUrl
  }

  try {
    const targetUrl = new URL(normalizedUrl, window.location.origin)
    const redirectPath = targetUrl.searchParams.get('redirect') || '/purchase'
    const redirectUrl = new URL(redirectPath, window.location.origin)
    const paymentType = normalizeVisibleMethod(context.paymentType) || context.paymentType.trim() || 'wxpay'

    redirectUrl.searchParams.set('payment_type', paymentType)
    redirectUrl.searchParams.set('order_type', context.orderType)

    if (context.planId) {
      redirectUrl.searchParams.set('plan_id', String(context.planId))
    } else {
      redirectUrl.searchParams.delete('plan_id')
    }
    if (context.groupId) {
      redirectUrl.searchParams.set('group_id', String(context.groupId))
    } else {
      redirectUrl.searchParams.delete('group_id')
    }
    if (context.dailyAmountUsd != null && context.validityDays != null) {
      redirectUrl.searchParams.set('daily_amount_usd', String(context.dailyAmountUsd))
      redirectUrl.searchParams.set('validity_days', String(context.validityDays))
    } else {
      redirectUrl.searchParams.delete('daily_amount_usd')
      redirectUrl.searchParams.delete('validity_days')
    }

    if (context.orderAmount > 0) {
      redirectUrl.searchParams.set('amount', String(context.orderAmount))
    } else {
      redirectUrl.searchParams.delete('amount')
    }

    targetUrl.searchParams.set('redirect', `${redirectUrl.pathname}${redirectUrl.search}`)
    return targetUrl.toString()
  } catch {
    return normalizedUrl
  }
}

function onPaymentDone() {
  const wasSubscription = paymentState.value.orderType === 'subscription'
  resetPayment()
  selectedPlan.value = null
  if (wasSubscription) {
    subscriptionStore.fetchActiveSubscriptions(true).catch(() => {})
  }
}

function onPaymentSuccess() {
  removeRecoverySnapshot()
  authStore.refreshUser()
  if (paymentState.value.orderType === 'subscription') {
    subscriptionStore.fetchActiveSubscriptions(true).catch(() => {})
  }
}

function onPaymentSettled() {
  removeRecoverySnapshot()
}

// All checkout data from single API call
const checkout = ref<CheckoutInfoResponse>({
  methods: {}, global_min: 0, global_max: 0,
  plans: [], subscription_groups: [], balance_disabled: false, balance_recharge_multiplier: 1, subscription_payment_multiplier: 1, recharge_fee_rate: 0, refund_fee_rate: 0, help_text: '', help_image_url: '', stripe_publishable_key: '',
})

const tabs = computed(() => {
  const result: { key: 'recharge' | 'subscription'; label: string }[] = []
  result.push({ key: 'subscription', label: t('payment.tabSubscribe') })
  if (!checkout.value.balance_disabled) result.push({ key: 'recharge', label: t('payment.tabTopUp') })
  return result
})

const visibleMethods = computed(() => getVisibleMethods(checkout.value.methods))
const enabledMethods = computed(() => Object.keys(visibleMethods.value))
const validAmount = computed(() => amount.value ?? 0)

const balanceRechargeMultiplier = computed(() => {
  const multiplier = checkout.value.balance_recharge_multiplier
  return multiplier > 0 ? multiplier : 1
})
const subscriptionPaymentMultiplier = computed(() => {
  const multiplier = checkout.value.subscription_payment_multiplier
  return multiplier > 0 ? multiplier : 1
})
const creditedAmount = computed(() => Math.round((validAmount.value * balanceRechargeMultiplier.value) * 100) / 100)

// Check if an amount fits a method's [min, max]. 0 = no limit.
function amountFitsMethod(amt: number, methodType: string): boolean {
  if (amt <= 0) return true
  const ml = visibleMethods.value[methodType]
  if (!ml) return false
  if (ml.single_min > 0 && amt < ml.single_min) return false
  if (ml.single_max > 0 && amt > ml.single_max) return false
  return true
}

function methodCanPayAmount(amt: number, methodType: string): boolean {
  if (!methodType) return false
  const ml = visibleMethods.value[methodType]
  return ml?.available !== false && amountFitsMethod(amt, methodType)
}

function firstMethodForAmount(amt: number): string {
  return enabledMethods.value.find((method) => methodCanPayAmount(amt, method)) ?? ''
}

// Visible methods decide the amount range shown to users.
const globalMinAmount = computed(() => {
  const limits = Object.values(visibleMethods.value)
  if (limits.length === 0) return 0
  if (limits.some(limit => limit.single_min <= 0)) return 0
  return Math.min(...limits.map(limit => limit.single_min))
})
const globalMaxAmount = computed(() => {
  const limits = Object.values(visibleMethods.value)
  if (limits.length === 0) return 0
  if (limits.some(limit => limit.single_max <= 0)) return 0
  return Math.max(...limits.map(limit => limit.single_max))
})

// Selected method's limits (for validation and error messages)
const selectedLimit = computed(() => visibleMethods.value[selectedMethod.value])
const selectedCurrency = computed(() => normalizePaymentCurrency(selectedLimit.value?.currency))
const localeCode = computed(() => {
  const raw = i18n.locale as unknown
  if (typeof raw === 'string') return raw
  if (raw && typeof raw === 'object' && 'value' in raw) {
    return String((raw as { value?: string }).value || '')
  }
  return undefined
})
const selectedCurrencySymbol = computed(() => paymentCurrencySymbol(selectedCurrency.value, localeCode.value))

function formatSelectedPaymentAmount(value: number): string {
  return formatPaymentAmount(value, selectedCurrency.value, localeCode.value)
}

function formatUSDValue(value: number): string {
  return `USD ${Number.isFinite(value) ? value.toFixed(2) : '0.00'}`
}

function subscriptionValueToPaymentAmount(value: number): number {
  if (value <= 0) return 0
  return ceilPaymentAmount(value / subscriptionPaymentMultiplier.value, selectedCurrency.value)
}

const methodOptions = computed<PaymentMethodOption[]>(() =>
  enabledMethods.value.map((type) => {
    const ml = visibleMethods.value[type]
    return {
      type,
      fee_rate: ml?.fee_rate ?? 0,
      available: ml?.available !== false && amountFitsMethod(validAmount.value, type),
    }
  })
)

// Crypto Pay applies its adjustment inside BEpusdt's exchange rate.  It must
// never display or inherit the ordinary recharge fee.
const feeRate = computed(() =>
  selectedMethod.value === 'crypto' ? 0 : (checkout.value?.recharge_fee_rate ?? 0),
)
const feeAmount = computed(() =>
  feeRate.value > 0 && validAmount.value > 0
    ? Math.ceil(((validAmount.value * feeRate.value) / 100) * 100) / 100
    : 0
)
const totalAmount = computed(() =>
  feeRate.value > 0 && validAmount.value > 0
    ? Math.round((validAmount.value + feeAmount.value) * 100) / 100
    : validAmount.value
)

const amountError = computed(() => {
  if (validAmount.value <= 0) return ''
  // No method can handle this amount
  if (!enabledMethods.value.some((m) => amountFitsMethod(validAmount.value, m))) {
    return t('payment.amountNoMethod')
  }
  // Selected method can't handle this amount (but others can)
  const ml = selectedLimit.value
  if (ml) {
    if (ml.single_min > 0 && validAmount.value < ml.single_min) return t('payment.amountTooLow', { min: formatSelectedPaymentAmount(ml.single_min) })
    if (ml.single_max > 0 && validAmount.value > ml.single_max) return t('payment.amountTooHigh', { max: formatSelectedPaymentAmount(ml.single_max) })
  }
  return ''
})

const canSubmit = computed(() =>
  validAmount.value > 0
    && amountFitsMethod(validAmount.value, selectedMethod.value)
    && selectedLimit.value?.available !== false
)

// Subscription-specific: method options based on plan price
const subMethodOptions = computed<PaymentMethodOption[]>(() => {
  const planPrice = selectedPlanPaymentAmount.value
  return enabledMethods.value.map((type) => {
    const ml = visibleMethods.value[type]
    return {
      type,
      fee_rate: ml?.fee_rate ?? 0,
      available: ml?.available !== false && amountFitsMethod(planPrice, type),
    }
  })
})

const subscriptionMethodOptions = computed<PaymentMethodOption[]>(() =>
  enabledMethods.value.map((type) => {
    const ml = visibleMethods.value[type]
    return {
      type,
      fee_rate: ml?.fee_rate ?? 0,
      available: ml?.available !== false,
    }
  })
)

const subFeeAmount = computed(() => {
  const price = selectedPlanPaymentAmount.value
  if (feeRate.value <= 0 || price <= 0) return 0
  return Math.ceil(((price * feeRate.value) / 100) * 100) / 100
})

const selectedPlanPaymentAmount = computed(() =>
  subscriptionValueToPaymentAmount(selectedPlan.value?.price ?? 0)
)
const selectedPlanOriginalPaymentAmount = computed(() =>
  subscriptionValueToPaymentAmount(selectedPlan.value?.original_price ?? 0)
)
function concurrencyForDailyAmount(dailyAmount: number): number {
  return dailyAmount > 0 ? Math.max(1, Math.ceil(dailyAmount / 10)) : 0
}
const selectedPlanConcurrency = computed(() =>
  concurrencyForDailyAmount(selectedPlan.value?.daily_amount_usd ?? selectedPlan.value?.daily_limit_usd ?? 0)
)

const subTotalAmount = computed(() => {
  const price = selectedPlanPaymentAmount.value
  if (feeRate.value <= 0 || price <= 0) return price
  return Math.round((price + subFeeAmount.value) * 100) / 100
})

const canSubmitSubscription = computed(() =>
  selectedPlan.value !== null
    && amountFitsMethod(selectedPlanPaymentAmount.value, selectedMethod.value)
    && selectedLimit.value?.available !== false
)

// 续费/转套餐结账（per-day redesign §5/§7）：从「我的订阅」弹窗带 D/T+意图+预估金额跳来，
// 走与购买同一条法币网关下单链路（金额由后端按订单快照权威重算，前端 amount 仅展示）。
const lifecycleOrder = ref<null | {
  intent: 'renew' | 'change_plan'
  dailyAmountUsd: number
  validityDays: number
  amount: number // 预估实收（续费=价，转套餐=差价）；后端权威重算。
}>(null)

const lifecycleFeeAmount = computed(() => {
  const amt = lifecyclePaymentAmount.value
  if (feeRate.value <= 0 || amt <= 0) return 0
  return Math.ceil(((amt * feeRate.value) / 100) * 100) / 100
})

const lifecyclePaymentAmount = computed(() =>
  subscriptionValueToPaymentAmount(lifecycleOrder.value?.amount ?? 0)
)
const lifecycleMethodOptions = computed<PaymentMethodOption[]>(() => {
  const lifecycleAmount = lifecyclePaymentAmount.value
  return enabledMethods.value.map((type) => {
    const ml = visibleMethods.value[type]
    return {
      type,
      fee_rate: ml?.fee_rate ?? 0,
      available: methodCanPayAmount(lifecycleAmount, type),
    }
  })
})
const lifecycleConcurrency = computed(() =>
  concurrencyForDailyAmount(lifecycleOrder.value?.dailyAmountUsd ?? 0)
)

const lifecycleTotalAmount = computed(() => {
  const amt = lifecyclePaymentAmount.value
  if (feeRate.value <= 0 || amt <= 0) return amt
  return Math.round((amt + lifecycleFeeAmount.value) * 100) / 100
})

const canSubmitLifecycle = computed(() =>
  lifecycleOrder.value !== null
    && lifecyclePaymentAmount.value > 0
    && selectedMethod.value !== ''
    && amountFitsMethod(lifecyclePaymentAmount.value, selectedMethod.value)
    && selectedLimit.value?.available !== false
)

async function confirmLifecycle() {
  const lo = lifecycleOrder.value
  if (!lo || submitting.value) return
  // order_type=subscription + subscription_intent；后端按用户唯一生效卡派生目标、权威算价并冻结快照。
  await createOrder(lo.amount, 'subscription', undefined, {
    dailyAmountUsd: lo.dailyAmountUsd,
    validityDays: lo.validityDays,
    subscriptionIntent: lo.intent,
  })
}

// Auto-switch to first available method when current selection can't handle the amount
watch(() => [validAmount.value, selectedMethod.value] as const, ([amt, method]) => {
  if (amt <= 0 || amountFitsMethod(amt, method)) return
  const available = firstMethodForAmount(amt)
  if (available) selectedMethod.value = available
})

watch(() => [lifecyclePaymentAmount.value, selectedMethod.value] as const, ([amt, method]) => {
  if (!lifecycleOrder.value || amt <= 0 || methodCanPayAmount(amt, method)) return
  const available = firstMethodForAmount(amt)
  if (available) selectedMethod.value = available
})

// Payment button class: follows selected payment method color
const paymentButtonClass = computed(() => {
  const m = selectedMethod.value
  if (!m) return 'btn-primary'
  if (m.includes('alipay')) return 'btn-alipay'
  if (m.includes('wxpay')) return 'btn-wxpay'
  if (m === 'stripe') return 'btn-stripe'
  if (m === 'airwallex') return 'btn-airwallex'
  return 'btn-primary'
})

// Subscription confirm: platform identity badge (clean card, no gradient)
const planBadgeClass = computed(() => platformBadgeClass(selectedPlan.value?.group_platform || ''))

// Renewal modal state
const showRenewalModal = ref(false)
const renewGroupId = ref<number | null>(null)
const renewalPlans = computed(() => {
  if (renewGroupId.value == null) return []
  return checkout.value.plans.filter(p => p.group_id === renewGroupId.value)
})

const planValiditySuffix = computed(() => {
  if (!selectedPlan.value) return ''
  const u = selectedPlan.value.validity_unit || 'day'
  if (u === 'month') return t('payment.perMonth')
  if (u === 'year') return t('payment.perYear')
  return `${selectedPlan.value.validity_days}${t('payment.days')}`
})

function selectPlanFromModal(plan: SubscriptionPlan) {
  showRenewalModal.value = false
  renewGroupId.value = null
  selectedPlan.value = plan
  errorMessage.value = ''
}

function closeRenewalModal() {
  showRenewalModal.value = false
  renewGroupId.value = null
}

async function handleSubmitRecharge() {
  if (!canSubmit.value || submitting.value) return
  await createOrder(validAmount.value, 'balance')
}

async function confirmSubscribe() {
  if (!selectedPlan.value || submitting.value) return
  if (hasActiveSubscription.value) {
    appStore.showError(t('payment.existingSubscriptionDesc'))
    return
  }
  await createOrder(selectedPlan.value.price, 'subscription', selectedPlan.value.id)
}

// 自定义订阅购买（无固定套餐）：面板 @purchase 带 D/T + 后端报价，按报价价下单（金额由后端公式再算、不信前端）。
async function onCustomPurchase(payload: {
  dailyAmountUsd: number
  validityDays: number
  quote: { price: number }
}) {
  if (submitting.value) return
  if (hasActiveSubscription.value) {
    appStore.showError(t('payment.existingSubscriptionDesc'))
    return
  }
  await createOrder(payload.quote.price, 'subscription', undefined, {
    dailyAmountUsd: payload.dailyAmountUsd,
    validityDays: payload.validityDays,
  })
}

function goToSubscriptions() {
  router.push('/subscriptions')
}

async function createOrder(orderAmount: number, orderType: OrderType, planId?: number, options: CreateOrderOptions = {}) {
  submitting.value = true
  errorMessage.value = ''
  errorHintMessage.value = ''
  const requestType = normalizeVisibleMethod(options.paymentType || selectedMethod.value) || options.paymentType || selectedMethod.value
  try {
    const payload = buildCreateOrderPayload({
      amount: orderAmount,
      paymentType: requestType,
      orderType,
      planId,
      groupId: options.groupId,
      dailyAmountUsd: options.dailyAmountUsd,
      validityDays: options.validityDays,
      subscriptionIntent: options.subscriptionIntent,
      origin: typeof window !== 'undefined' ? window.location.origin : '',
      isMobile: isMobileDevice(),
      isWechatBrowser: typeof window !== 'undefined' && /MicroMessenger/i.test(window.navigator.userAgent),
      forceQRCode: !!(checkout.value.alipay_force_qrcode && normalizeVisibleMethod(requestType) === 'alipay'),
      cryptoNetwork: cryptoNetwork.value,
    })
    if (options.openid) {
      payload.openid = options.openid
    }
    if (options.wechatResumeToken) {
      payload.wechat_resume_token = options.wechatResumeToken
    }

    const result = await paymentStore.createOrder(payload) as CreateOrderResult & { resume_token?: string }
    const openWindow = (url: string) => {
      const win = window.open(url, 'paymentPopup', getPaymentPopupFeatures())
      if (!win || win.closed) {
        window.location.href = url
      }
    }
    const visibleMethod = normalizeVisibleMethod(requestType) || requestType
    // When user clicks the dedicated Stripe button, leave method blank so the
    // landing page renders Stripe's full Payment Element (card/link/alipay/wxpay).
    const stripeMethod = visibleMethod === 'stripe'
      ? ''
      : visibleMethod === 'wxpay' ? 'wechat_pay' : 'alipay'
    const stripeRouteUrl = result.client_secret && visibleMethod !== 'airwallex'
      ? router.resolve({
        path: '/payment/stripe',
        query: {
          order_id: String(result.order_id),
          client_secret: result.client_secret,
          method: stripeMethod || undefined,
          resume_token: result.resume_token || undefined,
        },
      }).href
      : ''
    const airwallexRouteUrl = result.client_secret && result.intent_id
      ? router.resolve({
        path: '/payment/airwallex',
        query: {
          order_id: String(result.order_id),
          out_trade_no: result.out_trade_no || undefined,
          resume_token: result.resume_token || undefined,
        },
      }).href
      : ''
    const decision = decidePaymentLaunch(result, {
      visibleMethod,
      orderType,
      isMobile: isMobileDevice(),
      isWechatBrowser: typeof window !== 'undefined' && /MicroMessenger/i.test(window.navigator.userAgent),
      forceQRCode: !!(checkout.value.alipay_force_qrcode && visibleMethod === 'alipay'),
      stripePopupUrl: stripeRouteUrl,
      stripeRouteUrl,
      airwallexRouteUrl,
    })

    if (decision.kind === 'wechat_oauth' && decision.oauth?.authorize_url) {
      window.location.href = buildWechatOAuthAuthorizeUrl(decision.oauth.authorize_url, {
        paymentType: visibleMethod,
        orderType,
        planId,
        groupId: options.groupId,
        orderAmount,
        dailyAmountUsd: options.dailyAmountUsd,
        validityDays: options.validityDays,
      })
      return
    }

    if (decision.kind === 'unhandled') {
      applyScenarioError({ reason: 'UNHANDLED_PAYMENT_SCENARIO' }, visibleMethod)
      return
    }

    paymentState.value = decision.paymentState
    paymentPhase.value = 'paying'
    persistRecoverySnapshot(decision.recovery)

    if (decision.kind === 'stripe_popup') {
      openWindow(decision.paymentState.payUrl)
      return
    }
    if (decision.kind === 'stripe_route') {
      window.location.href = decision.paymentState.payUrl
      return
    }
    if (decision.kind === 'airwallex_route') {
      window.location.href = decision.paymentState.payUrl
      return
    }
    if (decision.kind === 'wechat_jsapi' && decision.jsapi) {
      try {
        const jsapiResult = await invokeWechatJsapiPayment(decision.jsapi as Record<string, unknown>)
        const errMsg = String(jsapiResult.err_msg || '').toLowerCase()
        if (errMsg.includes('cancel')) {
          appStore.showInfo(t('payment.qr.cancelled'))
          resetPayment()
        } else if (errMsg && !errMsg.includes('ok')) {
          resetPayment()
          const fallbackApplied = await attemptMobileQrFallback(
            { reason: 'WECHAT_JSAPI_FAILED', message: errMsg },
            {
              orderAmount,
              orderType,
              planId,
              groupId: options.groupId,
              dailyAmountUsd: options.dailyAmountUsd,
              validityDays: options.validityDays,
              subscriptionIntent: options.subscriptionIntent,
              paymentType: visibleMethod,
              attempted: options.mobileQrFallbackAttempted === true,
            },
          )
          if (!fallbackApplied) {
            applyScenarioError({ reason: 'WECHAT_JSAPI_FAILED', message: errMsg }, visibleMethod)
          }
        } else {
          const resultState = { ...decision.paymentState }
          resetPayment()
          await redirectToPaymentResult(resultState)
        }
      } catch (err: unknown) {
        resetPayment()
        const fallbackApplied = await attemptMobileQrFallback(err, {
          orderAmount,
          orderType,
          planId,
          groupId: options.groupId,
          dailyAmountUsd: options.dailyAmountUsd,
          validityDays: options.validityDays,
          subscriptionIntent: options.subscriptionIntent,
          paymentType: visibleMethod,
          attempted: options.mobileQrFallbackAttempted === true,
        })
        if (!fallbackApplied) {
          throw err
        }
      }
      return
    }
    if (decision.kind === 'redirect_waiting' && decision.paymentState.payUrl) {
      if (isMobileDevice()) {
        window.location.href = decision.paymentState.payUrl
        return
      }
      openWindow(decision.paymentState.payUrl)
    }
  } catch (err: unknown) {
    const apiErr = err as Record<string, unknown>
    if (apiErr.reason === 'TOO_MANY_PENDING') {
      const metadata = apiErr.metadata as Record<string, unknown> | undefined
      errorMessage.value = t('payment.errors.tooManyPending', { max: metadata?.max || '' })
      errorHintMessage.value = ''
    } else if (apiErr.reason === 'CANCEL_RATE_LIMITED') {
      errorMessage.value = t('payment.errors.cancelRateLimited')
      errorHintMessage.value = ''
    } else if (await attemptMobileQrFallback(err, {
      orderAmount,
      orderType,
      planId,
      groupId: options.groupId,
      dailyAmountUsd: options.dailyAmountUsd,
      validityDays: options.validityDays,
      subscriptionIntent: options.subscriptionIntent,
      paymentType: requestType,
      attempted: options.mobileQrFallbackAttempted === true,
    })) {
      return
    } else {
      const handled = applyScenarioError(
        err,
        normalizeVisibleMethod(options.paymentType || selectedMethod.value) || selectedMethod.value,
      )
      if (!handled) {
        errorMessage.value = extractI18nErrorMessage(err, t, 'payment.errors', extractApiErrorMessage(err, t('payment.result.failed')))
        errorHintMessage.value = ''
      }
      if (handled) {
        return
      }
    }
    appStore.showError(buildPaymentErrorToastMessage(errorMessage.value, errorHintMessage.value))
  } finally {
    submitting.value = false
  }
}

interface MobileQrFallbackContext {
  orderAmount: number
  orderType: OrderType
  planId?: number
  groupId?: number
  dailyAmountUsd?: number
  validityDays?: number
  subscriptionIntent?: string
  paymentType: string
  attempted: boolean
}

function shouldFallbackToDesktopQr(err: unknown, paymentMethod: string, attempted: boolean): boolean {
  if (attempted || !isMobileDevice()) {
    return false
  }

  const normalizedMethod = normalizeVisibleMethod(paymentMethod) || paymentMethod
  const reason = typeof err === 'object' && err && 'reason' in err && typeof err.reason === 'string'
    ? err.reason
    : ''
  const message = err instanceof Error
    ? err.message
    : (typeof err === 'object' && err && 'message' in err && typeof err.message === 'string'
      ? err.message
      : '')
  const normalizedMessage = message.toLowerCase()

  if (normalizedMethod === 'wxpay') {
    return reason === 'WECHAT_H5_NOT_AUTHORIZED'
      || reason === 'WECHAT_PAYMENT_MP_NOT_CONFIGURED'
      || reason === 'WECHAT_JSAPI_FAILED'
      || reason === 'PAYMENT_GATEWAY_ERROR'
      || reason === 'UNHANDLED_PAYMENT_SCENARIO'
      || normalizedMessage.includes('weixinjsbridge is unavailable')
      || normalizedMessage.includes('wechat_jsapi_unavailable')
  }

  if (normalizedMethod === 'alipay') {
    return reason === 'PAYMENT_GATEWAY_ERROR' || reason === 'UNHANDLED_PAYMENT_SCENARIO'
  }

  return false
}

async function attemptMobileQrFallback(err: unknown, context: MobileQrFallbackContext): Promise<boolean> {
  if (!shouldFallbackToDesktopQr(err, context.paymentType, context.attempted)) {
    return false
  }

  try {
    const visibleMethod = normalizeVisibleMethod(context.paymentType) || context.paymentType
    const payload = buildCreateOrderPayload({
      amount: context.orderAmount,
      paymentType: visibleMethod,
      orderType: context.orderType,
      planId: context.planId,
      groupId: context.groupId,
      dailyAmountUsd: context.dailyAmountUsd,
      validityDays: context.validityDays,
      subscriptionIntent: context.subscriptionIntent,
      origin: typeof window !== 'undefined' ? window.location.origin : '',
      isMobile: false,
      isWechatBrowser: false,
      cryptoNetwork: cryptoNetwork.value,
    })
    const result = await paymentStore.createOrder(payload) as CreateOrderResult & { resume_token?: string }
    const stripeMethod = visibleMethod === 'wxpay' ? 'wechat_pay' : 'alipay'
    const stripeRouteUrl = result.client_secret
      ? router.resolve({
        path: '/payment/stripe',
        query: {
          order_id: String(result.order_id),
          client_secret: result.client_secret,
          method: stripeMethod,
          resume_token: result.resume_token || undefined,
        },
      }).href
      : ''
    const decision = decidePaymentLaunch(result, {
      visibleMethod,
      orderType: context.orderType,
      isMobile: false,
      isWechatBrowser: false,
      stripePopupUrl: stripeRouteUrl,
      stripeRouteUrl,
    })

    if (decision.kind !== 'qr_waiting' || !decision.paymentState.qrCode) {
      return false
    }

    errorMessage.value = ''
    errorHintMessage.value = ''
    paymentState.value = decision.paymentState
    paymentPhase.value = 'paying'
    persistRecoverySnapshot(decision.recovery)
    appStore.showWarning(t('payment.errors.mobilePaymentFallbackToQr'))
    return true
  } catch {
    return false
  }
}

function applyScenarioError(err: unknown, paymentMethod: string): boolean {
  const descriptor = describePaymentScenarioError(err, {
    paymentMethod,
    isMobile: isMobileDevice(),
    isWechatBrowser: typeof window !== 'undefined' && /MicroMessenger/i.test(window.navigator.userAgent),
  })
  if (!descriptor) {
    errorMessage.value = ''
    errorHintMessage.value = ''
    return false
  }
  errorMessage.value = t(descriptor.messageKey)
  errorHintMessage.value = descriptor.hintKey ? t(descriptor.hintKey) : ''
  appStore.showError(buildPaymentErrorToastMessage(errorMessage.value, errorHintMessage.value))
  return true
}

async function resumeWechatPaymentFromQuery() {
  const resume = parseWechatResumeRoute(route.query, checkout.value.plans, validAmount.value)
  if (!resume) {
    return
  }

  selectedMethod.value = resume.paymentType
  if (resume.orderType === 'balance' && resume.orderAmount > 0) {
    amount.value = resume.orderAmount
  }
  if (resume.orderType === 'subscription' && resume.planId) {
    selectedPlan.value = checkout.value.plans.find(plan => plan.id === resume.planId) ?? null
  }

  await router.replace({ path: route.path, query: stripWechatResumeQuery(route.query) })

  if (resume.wechatResumeToken) {
    await createOrder(0, resume.orderType, resume.planId, {
      wechatResumeToken: resume.wechatResumeToken,
      paymentType: resume.paymentType,
      isResume: true,
      groupId: resume.groupId,
      dailyAmountUsd: resume.dailyAmountUsd,
      validityDays: resume.validityDays,
    })
    return
  }

  if (resume.orderAmount > 0 && resume.openid) {
    await createOrder(resume.orderAmount, resume.orderType, resume.planId, {
      openid: resume.openid,
      paymentType: resume.paymentType,
      isResume: true,
      groupId: resume.groupId,
      dailyAmountUsd: resume.dailyAmountUsd,
      validityDays: resume.validityDays,
    })
  }
}

onMounted(async () => {
  try {
    const res = await paymentAPI.getCheckoutInfo()
    checkout.value = res.data
    if (enabledMethods.value.length) {
      const order: readonly string[] = METHOD_ORDER
      const sorted = [...enabledMethods.value].sort((a, b) => {
        const ai = order.indexOf(a)
        const bi = order.indexOf(b)
        return (ai === -1 ? 999 : ai) - (bi === -1 ? 999 : bi)
      })
      selectedMethod.value = sorted[0]
    }
    if (typeof window !== 'undefined') {
      if (hasWechatResumeQuery(route.query)) {
        removeRecoverySnapshot()
      }
      const routeResumeToken = typeof route.query.resume_token === 'string'
        ? route.query.resume_token
        : typeof route.query.wechat_resume_token === 'string'
          ? route.query.wechat_resume_token
          : undefined
      const restored = readPaymentRecoverySnapshot(
        window.localStorage.getItem(PAYMENT_RECOVERY_STORAGE_KEY),
        { resumeToken: routeResumeToken },
      )
      if (restored) {
        paymentState.value = restored
        paymentPhase.value = 'paying'
        const restoredMethod = normalizeVisibleMethod(restored.paymentType)
        if (restoredMethod) {
          selectedMethod.value = restoredMethod
        }
      } else {
        removeRecoverySnapshot()
      }
    }
    await resumeWechatPaymentFromQuery()
    if (checkout.value.balance_disabled) {
      activeTab.value = 'subscription'
    }
    // 续费/转套餐结账导航（per-day redesign §5/§7）：
    // ?tab=subscription&intent=renew|change_plan&daily_amount_usd=&validity_days=&charge=
    const lifecycleIntent = typeof route.query.intent === 'string' ? route.query.intent : ''
    if (lifecycleIntent === 'renew' || lifecycleIntent === 'change_plan') {
      activeTab.value = 'subscription'
      const d = Number(route.query.daily_amount_usd)
      const tt = Number(route.query.validity_days)
      const charge = Number(route.query.charge)
      if (d > 0 && tt > 0 && Number.isFinite(charge) && charge > 0) {
        lifecycleOrder.value = { intent: lifecycleIntent, dailyAmountUsd: d, validityDays: tt, amount: Math.round((charge + Number.EPSILON) * 100) / 100 }
        // 清掉 query，避免刷新/返回重复进入结账。
        await router.replace({ path: route.path, query: { tab: 'subscription' } })
      }
    }
    // Handle renewal navigation: ?tab=subscription&group=123
    if (route.query.tab === 'subscription') {
      activeTab.value = 'subscription'
      if (route.query.group) {
        const groupId = Number(route.query.group)
        const groupPlans = checkout.value.plans.filter(p => p.group_id === groupId)
        if (groupPlans.length === 1) {
          selectedPlan.value = groupPlans[0]
        } else if (groupPlans.length > 1) {
          renewGroupId.value = groupId
          showRenewalModal.value = true
        }
      }
    }
  } catch (err: unknown) { appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error'))) }
  finally { loading.value = false }
  // Fetch active subscriptions (uses cache, non-blocking)
  subscriptionStore.fetchActiveSubscriptions().catch(() => {})
})
</script>
