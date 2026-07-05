<template>
  <AppLayout>
    <div class="space-y-6">
      <div v-if="loading" class="flex justify-center py-12">
        <div class="h-8 w-8 animate-spin rounded-full border-2 border-gray-400 border-t-transparent dark:border-dark-500"></div>
      </div>

      <div v-else-if="!overview || !overview.config.enabled" class="card p-8 text-center text-sm text-gray-600 dark:text-gray-400">
        {{ t('points.disabled') }}
      </div>

      <template v-else>
        <!-- 统计 -->
        <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <div class="card p-5">
            <p class="text-sm text-gray-600 dark:text-gray-400">{{ t('points.stats.available') }}</p>
            <p class="mt-2 text-2xl font-semibold font-mono tabular-nums text-gray-900 dark:text-white">{{ overview.account.available.toLocaleString() }}</p>
          </div>
          <div class="card p-5">
            <p class="text-sm text-gray-600 dark:text-gray-400">{{ t('points.stats.frozen') }}</p>
            <p class="mt-2 text-2xl font-semibold font-mono tabular-nums text-gray-900 dark:text-white">{{ overview.account.frozen.toLocaleString() }}</p>
          </div>
          <div class="card p-5">
            <p class="text-sm text-gray-600 dark:text-gray-400">{{ t('points.stats.lifetime') }}</p>
            <p class="mt-2 text-2xl font-semibold font-mono tabular-nums text-gray-900 dark:text-white">{{ overview.account.lifetime_earned.toLocaleString() }}</p>
          </div>
          <div class="card p-5">
            <p class="text-sm text-gray-600 dark:text-gray-400">{{ t('points.stats.effectiveRate') }}</p>
            <p class="mt-2 text-2xl font-semibold font-mono tabular-nums text-gray-900 dark:text-white">{{ formatPercent(firstPaymentRate) }}</p>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-500">{{ t('points.stats.firstPaymentRate', { rate: formatPercent(firstPaymentRate) }) }}</p>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-500">{{ t('points.stats.repeatPaymentRate', { rate: formatPercent(repeatPaymentRate) }) }}</p>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-500">{{ t('points.stats.pegValue', { value: formatCurrency(overview.config.peg) }) }}</p>
          </div>
        </div>

        <!-- 邀请码 / 链接 -->
        <div class="card p-6">
          <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('points.invite.invitees') }}</h3>
          <div class="mt-5 grid gap-4 md:grid-cols-2">
            <div class="space-y-2">
              <p class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('points.invite.code') }}</p>
              <div class="flex items-center gap-2 rounded-md border border-gray-200 bg-gray-50 px-3 py-2 dark:border-dark-700 dark:bg-dark-900">
                <code class="flex-1 truncate text-sm font-semibold font-mono text-gray-900 dark:text-white">{{ overview.affiliate.aff_code }}</code>
                <button class="btn btn-secondary btn-sm" @click="copy(overview.affiliate.aff_code)">
                  <Icon name="copy" size="sm" /><span>{{ t('points.invite.copy') }}</span>
                </button>
              </div>
            </div>
            <div class="space-y-2">
              <p class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('points.invite.link') }}</p>
              <div class="flex items-center gap-2 rounded-md border border-gray-200 bg-gray-50 px-3 py-2 dark:border-dark-700 dark:bg-dark-900">
                <code class="flex-1 truncate text-sm font-mono text-gray-700 dark:text-gray-300">{{ inviteLink }}</code>
                <button class="btn btn-secondary btn-sm" @click="copy(inviteLink)">
                  <Icon name="copy" size="sm" /><span>{{ t('points.invite.copy') }}</span>
                </button>
              </div>
            </div>
          </div>
          <p class="mt-4 text-sm text-gray-600 dark:text-gray-400">
            {{ t('points.invite.count') }}: <span class="font-mono tabular-nums">{{ overview.affiliate.aff_count.toLocaleString() }}</span>
          </p>
          <div class="mt-4 rounded-md bg-gray-50 px-4 py-3 text-sm text-gray-700 dark:bg-dark-900 dark:text-gray-300">
            <template v-if="repeatPaymentRate > 0">
              <p class="font-medium text-gray-900 dark:text-white">{{ t('points.invite.rewardTitle') }}</p>
              <p class="mt-1">
                {{ t('points.invite.rewardExample', {
                  amount: formatCurrency(inviteExampleAmount, 'CNY'),
                  firstPoints: inviteFirstExamplePoints.toLocaleString(),
                  repeatPoints: inviteRepeatExamplePoints.toLocaleString(),
                }) }}
              </p>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-500">
                {{ t('points.invite.rewardFormula', {
                  firstRate: formatPercent(firstPaymentRate),
                  repeatRate: formatPercent(repeatPaymentRate),
                  peg: formatCurrency(peg),
                }) }}
              </p>
            </template>
            <template v-else>
              {{ t('points.invite.rewardDisabled') }}
            </template>
          </div>
        </div>

        <!-- 三个动作 -->
        <div class="grid gap-4 lg:grid-cols-3">
          <!-- 换余额 -->
          <div v-if="overview.config.redeem_balance_on" class="card p-6 space-y-4">
            <div>
              <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('points.redeemBalance.title') }}</h3>
              <p class="mt-1 text-sm text-gray-600 dark:text-gray-400">{{ t('points.redeemBalance.desc') }}</p>
            </div>
            <div class="space-y-2">
              <label class="input-label">{{ t('points.redeemBalance.points') }}</label>
              <input v-model.number="redeemBalancePoints" type="number" min="1" class="input" />
              <p class="text-xs text-gray-500 dark:text-gray-500">{{ t('points.redeemBalance.estimate', { amount: formatCurrency(redeemBalanceEstimate) }) }}</p>
            </div>
            <button class="btn btn-primary w-full" :disabled="busy || !redeemBalancePoints" @click="onRedeemBalance">{{ t('points.redeemBalance.submit') }}</button>
          </div>

          <!-- 提现 -->
          <div v-if="overview.config.withdraw_enabled" class="card p-6 space-y-4">
            <div>
              <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('points.withdraw.title') }}</h3>
              <p class="mt-1 text-sm text-gray-600 dark:text-gray-400">{{ t('points.withdraw.desc') }}</p>
            </div>
            <div class="space-y-3">
              <div class="space-y-1">
                <label class="input-label">{{ t('points.withdraw.points') }}</label>
                <input v-model.number="withdrawPoints" type="number" :min="overview.config.withdraw_min_points || 1" class="input" />
                <p v-if="overview.config.withdraw_min_points > 0" class="text-xs text-gray-500 dark:text-gray-500">{{ t('points.withdraw.min', { n: overview.config.withdraw_min_points }) }}</p>
              </div>
              <div class="space-y-1">
                <label class="input-label">{{ t('points.withdraw.method') }}</label>
                <select v-model="withdrawMethod" class="input">
                  <option value="alipay">{{ t('points.withdraw.alipay') }}</option>
                  <option value="usdt">{{ t('points.withdraw.usdt') }}</option>
                </select>
              </div>
              <template v-if="withdrawMethod === 'alipay'">
                <div class="space-y-1">
                  <label class="input-label">{{ t('points.withdraw.alipayAccount') }}</label>
                  <input v-model.trim="alipayAccount" type="text" maxlength="128" class="input" />
                </div>
                <div class="space-y-1">
                  <label class="input-label">{{ t('points.withdraw.alipayName') }}</label>
                  <input v-model.trim="alipayName" type="text" maxlength="64" class="input" />
                </div>
              </template>
              <template v-else>
                <div class="space-y-1">
                  <label class="input-label">{{ t('points.withdraw.usdtChain') }}</label>
                  <select v-model="usdtChain" class="input">
                    <option value="TRC20">{{ t('points.withdraw.usdtChains.trc20') }}</option>
                    <option value="ERC20">{{ t('points.withdraw.usdtChains.erc20') }}</option>
                    <option value="BEP20">{{ t('points.withdraw.usdtChains.bep20') }}</option>
                  </select>
                </div>
                <div class="space-y-1">
                  <label class="input-label">{{ t('points.withdraw.usdtAddress') }}</label>
                  <input v-model.trim="usdtAddress" type="text" maxlength="128" class="input" />
                </div>
              </template>
              <div class="rounded-md bg-gray-50 px-3 py-2 text-xs text-gray-600 dark:bg-dark-900 dark:text-gray-400 space-y-1">
                <div v-if="withdrawMethod === 'usdt'" class="flex justify-between"><span>{{ t('points.withdraw.usdtRate') }}</span><span class="font-mono">{{ withdrawUSDCNYEffectiveRate.toFixed(2) }}</span></div>
                <div class="flex justify-between"><span>{{ t('points.withdraw.gross') }}</span><span class="font-mono">{{ formatWithdrawCurrency(withdrawGross) }}</span></div>
                <div class="flex justify-between"><span>{{ t('points.withdraw.fee') }} ({{ overview.config.withdraw_fee_percent }}%)</span><span class="font-mono">-{{ formatWithdrawCurrency(withdrawFee) }}</span></div>
                <div class="flex justify-between font-semibold text-gray-900 dark:text-white"><span>{{ t('points.withdraw.net') }}</span><span class="font-mono">{{ formatWithdrawCurrency(withdrawNet) }}</span></div>
              </div>
            </div>
            <button class="btn btn-primary w-full" :disabled="busy || !withdrawPoints" @click="onWithdraw">{{ t('points.withdraw.submit') }}</button>
          </div>

          <!-- 换套餐 -->
          <div v-if="overview.config.redeem_plan_on" class="card p-6 space-y-4">
            <div>
              <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('points.redeemPlan.title') }}</h3>
              <p class="mt-1 text-sm text-gray-600 dark:text-gray-400">{{ t('points.redeemPlan.desc') }}</p>
            </div>
            <div v-if="plans.length === 0" class="rounded-md border border-dashed border-gray-300 p-4 text-center text-sm text-gray-500 dark:border-dark-700">
              {{ t('points.redeemPlan.empty') }}
            </div>
            <div v-else class="space-y-4">
              <div class="space-y-2">
                <label class="input-label">{{ t('points.redeemPlan.dailyAmount') }}</label>
                <div class="grid grid-cols-3 gap-2">
                  <button
                    v-for="amount in planDailyOptions"
                    :key="amount"
                    type="button"
                    class="btn justify-center"
                    :class="selectedPlanDaily === amount ? 'btn-primary' : 'btn-secondary'"
                    :aria-pressed="selectedPlanDaily === amount"
                    @click="selectedPlanDaily = amount"
                  >
                    {{ t('points.redeemPlan.dailyOption', { d: amount }) }}
                  </button>
                </div>
              </div>
              <div class="space-y-2">
                <label class="input-label">{{ t('points.redeemPlan.validityDays') }}</label>
                <div class="grid grid-cols-2 gap-2">
                  <button
                    v-for="days in planValidityOptions"
                    :key="days"
                    type="button"
                    class="btn justify-center"
                    :class="selectedPlanDays === days ? 'btn-primary' : 'btn-secondary'"
                    :aria-pressed="selectedPlanDays === days"
                    @click="selectedPlanDays = days"
                  >
                    {{ t('points.redeemPlan.validity', { n: days }) }}
                  </button>
                </div>
              </div>
              <div v-if="selectedPlan" class="rounded-md bg-gray-50 px-3 py-3 text-sm text-gray-700 dark:bg-dark-900 dark:text-gray-300">
                <div class="flex items-center justify-between gap-3">
                  <span>{{ selectedPlanActionLabel }}</span>
                  <span class="font-mono tabular-nums text-gray-900 dark:text-white">
                    <template v-if="planQuoteLoading">{{ t('points.redeemPlan.quoteLoading') }}</template>
                    <template v-else>{{ selectedPlanPointsPrice.toLocaleString() }} {{ t('points.unit') }}</template>
                  </span>
                </div>
                <p v-if="planQuoteError" class="mt-1 text-xs text-red-600 dark:text-red-400">{{ planQuoteError }}</p>
                <div class="mt-1 flex items-center justify-between gap-3 text-xs text-gray-500 dark:text-gray-500">
                  <span>{{ t('points.redeemPlan.capSummary') }}</span>
                  <span class="font-mono tabular-nums">{{ formatCurrency(selectedPlan.weekly_cap_usd) }} / {{ formatCurrency(selectedPlan.monthly_cap_usd) }}</span>
                </div>
              </div>
              <button
                class="btn btn-primary w-full"
                :disabled="redeemPlanDisabled"
                @click="selectedPlan && onRedeemPlan(selectedPlan)"
              >
                {{ selectedPlanSubmitLabel }}
              </button>
            </div>
          </div>
        </div>

        <!-- 积分明细 -->
        <div class="card p-6">
          <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('points.ledger.title') }}</h3>
          <div v-if="ledger.length === 0" class="mt-4 rounded-md border border-dashed border-gray-300 p-6 text-center text-sm text-gray-600 dark:border-dark-700 dark:text-gray-400">
            {{ t('points.ledger.empty') }}
          </div>
          <div v-else class="mt-4 overflow-x-auto">
            <table class="table w-full text-sm">
              <thead>
                <tr>
                  <th class="text-left">{{ t('points.ledger.time') }}</th>
                  <th class="text-left">{{ t('points.ledger.kind') }}</th>
                  <th class="text-right">{{ t('points.ledger.points') }}</th>
                  <th class="text-right">{{ t('points.ledger.balanceAfter') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="row in ledger" :key="row.id">
                  <td class="text-gray-600 dark:text-gray-400">{{ formatDateTime(row.created_at) }}</td>
                  <td>{{ kindLabel(row.kind) }}</td>
                  <td class="text-right font-mono tabular-nums" :class="row.points >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-gray-900 dark:text-white'">{{ row.points >= 0 ? '+' : '' }}{{ row.points.toLocaleString() }}</td>
                  <td class="text-right font-mono tabular-nums text-gray-500">{{ row.available_after != null ? row.available_after.toLocaleString() : '—' }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import {
  getPointsOverview,
  listPointsLedger,
  listPointsPlans,
  redeemPointsToBalance,
  redeemPointsToPlan,
  createWithdrawal,
  type PointsOverview,
  type PointsLedgerEntry,
  type PointsPlanOption,
  type PointsPayoutMethod,
  type PointsUSDTChain,
} from '@/api/points'
import { changePlanQuote, renewQuote } from '@/api/subscriptions'
import { useAppStore } from '@/stores/app'
import { useSubscriptionStore } from '@/stores/subscriptions'
import { useClipboard } from '@/composables/useClipboard'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatCurrency, formatDateTime } from '@/utils/format'
import type { UserSubscription } from '@/types'

const { t } = useI18n()
const appStore = useAppStore()
const subscriptionStore = useSubscriptionStore()
const { copyToClipboard } = useClipboard()

const loading = ref(true)
const busy = ref(false)
const overview = ref<PointsOverview | null>(null)
const ledger = ref<PointsLedgerEntry[]>([])
const plans = ref<PointsPlanOption[]>([])

const redeemBalancePoints = ref<number | null>(null)
const withdrawPoints = ref<number | null>(null)
const withdrawMethod = ref<PointsPayoutMethod>('alipay')
const alipayAccount = ref('')
const alipayName = ref('')
const usdtChain = ref<PointsUSDTChain>('TRC20')
const usdtAddress = ref('')
const selectedPlanDaily = ref<number | null>(null)
const selectedPlanDays = ref<number | null>(null)
const selectedPlanCharge = ref<number | null>(null)
const planQuoteLoading = ref(false)
const planQuoteError = ref('')
let planQuoteSeq = 0

const peg = computed(() => overview.value?.config.peg ?? 0)
const repeatPaymentRate = computed(() => overview.value?.effective_rate ?? 0)
const firstPaymentRate = computed(() => repeatPaymentRate.value * 2)
const inviteExampleAmount = 100
const inviteRepeatExamplePoints = computed(() => computeEarnPoints(inviteExampleAmount, repeatPaymentRate.value, peg.value))
const inviteFirstExamplePoints = computed(() => computeEarnPoints(inviteExampleAmount, firstPaymentRate.value, peg.value))
const feePercent = computed(() => overview.value?.config.withdraw_fee_percent ?? 0)
const redeemBalanceEstimate = computed(() => (redeemBalancePoints.value || 0) * peg.value)
const withdrawUSDCNYBaseRate = computed(() => {
  const rate = overview.value?.config.withdraw_usd_cny_rate ?? 0
  return rate > 0 ? rate : 7.2
})
const withdrawUSDCNYEffectiveRate = computed(() => withdrawUSDCNYBaseRate.value + 0.1)
const withdrawCurrency = computed<'CNY' | 'USD'>(() => (withdrawMethod.value === 'usdt' ? 'USD' : 'CNY'))
const withdrawGrossCNY = computed(() => (withdrawPoints.value || 0) * peg.value)
const withdrawGross = computed(() => (
  withdrawMethod.value === 'usdt'
    ? withdrawGrossCNY.value / withdrawUSDCNYEffectiveRate.value
    : withdrawGrossCNY.value
))
const withdrawFee = computed(() => withdrawGross.value * (feePercent.value / 100))
const withdrawNet = computed(() => withdrawGross.value - withdrawFee.value)
const sortedPlans = computed(() => [...plans.value].sort((a, b) => a.daily_amount_usd - b.daily_amount_usd || a.validity_days - b.validity_days))
const planDailyOptions = computed(() => Array.from(new Set(sortedPlans.value.map((plan) => plan.daily_amount_usd))))
const planValidityOptions = computed(() => {
  const filtered = selectedPlanDaily.value == null ? sortedPlans.value : sortedPlans.value.filter((plan) => plan.daily_amount_usd === selectedPlanDaily.value)
  return Array.from(new Set(filtered.map((plan) => plan.validity_days)))
})
const selectedPlan = computed(() => sortedPlans.value.find((plan) => plan.daily_amount_usd === selectedPlanDaily.value && plan.validity_days === selectedPlanDays.value) ?? null)
const activeSubscription = computed<UserSubscription | null>(() =>
  (subscriptionStore.activeSubscriptions || []).find((sub) => sub.status === 'active') ?? null
)
const selectedPlanMode = computed<'purchase' | 'renew' | 'change_plan' | 'downgrade'>(() => {
  const plan = selectedPlan.value
  const sub = activeSubscription.value
  if (!plan || !sub) return 'purchase'
  if ((sub.daily_amount_usd || 0) > plan.daily_amount_usd + 1e-9) return 'downgrade'
  return Math.abs((sub.daily_amount_usd || 0) - plan.daily_amount_usd) <= 1e-9 ? 'renew' : 'change_plan'
})
const selectedPlanPointsPrice = computed(() => {
  const plan = selectedPlan.value
  if (!plan) return 0
  if (selectedPlanCharge.value == null) return selectedPlanMode.value === 'purchase' ? plan.points_price : 0
  return computePlanPoints(selectedPlanCharge.value, peg.value)
})
const selectedPlanActionLabel = computed(() => t(`points.redeemPlan.actionLabels.${selectedPlanMode.value}`))
const selectedPlanSubmitLabel = computed(() => t(`points.redeemPlan.submitLabels.${selectedPlanMode.value}`))
const redeemPlanDisabled = computed(() =>
  busy.value ||
  !selectedPlan.value ||
  planQuoteLoading.value ||
  overview.value == null ||
  (!planQuoteError.value && overview.value.account.available < selectedPlanPointsPrice.value)
)

const inviteLink = computed(() => {
  const code = overview.value?.affiliate.aff_code || ''
  if (!code) return ''
  if (typeof window === 'undefined') return `/register?aff=${encodeURIComponent(code)}`
  return `${window.location.origin}/register?aff=${encodeURIComponent(code)}`
})

function formatPercent(value: number): string {
  const rounded = Math.round(Number(value || 0) * 100) / 100
  return `${rounded}%`
}

function computePlanPoints(amount: number, pegValue: number): number {
  if (amount <= 0 || pegValue <= 0) return 0
  return Math.ceil(amount / pegValue)
}

function computeEarnPoints(amount: number, ratePercent: number, pegValue: number): number {
  if (amount <= 0 || ratePercent <= 0 || pegValue <= 0) return 0
  return Math.floor((amount * ratePercent / 100) / pegValue)
}

function formatWithdrawCurrency(amount: number): string {
  return formatCurrency(amount, withdrawCurrency.value)
}

function kindLabel(kind: string): string {
  const key = `points.ledger.kinds.${kind}`
  const label = t(key)
  return label === key ? kind : label
}

// 生成一次性兑换幂等键（exchange_id）；优先 crypto.randomUUID，环境不支持时回退。
function newExchangeId(): string {
  const c = typeof crypto !== 'undefined' ? crypto : undefined
  if (c && typeof c.randomUUID === 'function') return c.randomUUID()
  return `xid-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`
}

async function copy(text: string): Promise<void> {
  if (text) await copyToClipboard(text, t('points.invite.copied'))
}

async function loadAll(): Promise<void> {
  loading.value = true
  try {
    overview.value = await getPointsOverview()
    if (overview.value.config.enabled) {
      const [page, planList] = await Promise.all([
        listPointsLedger(1, 20),
        listPointsPlans(),
        subscriptionStore.fetchActiveSubscriptions(true).catch(() => []),
      ])
      ledger.value = page.items
      plans.value = planList
      syncSelectedPlan()
    }
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('points.loadFailed')))
  } finally {
    loading.value = false
  }
}

async function refresh(): Promise<void> {
  overview.value = await getPointsOverview()
  const [page] = await Promise.all([
    listPointsLedger(1, 20),
    subscriptionStore.fetchActiveSubscriptions(true).catch(() => []),
  ])
  ledger.value = page.items
}

async function onRedeemBalance(): Promise<void> {
  if (!redeemBalancePoints.value || redeemBalancePoints.value <= 0) return
  busy.value = true
  try {
    await redeemPointsToBalance(redeemBalancePoints.value)
    appStore.showSuccess(t('points.redeemBalance.success'))
    redeemBalancePoints.value = null
    await refresh()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('points.actionFailed')))
  } finally {
    busy.value = false
  }
}

async function onWithdraw(): Promise<void> {
  if (!withdrawPoints.value || withdrawPoints.value <= 0) return
  busy.value = true
  try {
    await createWithdrawal({
      points: withdrawPoints.value,
      payout_method: withdrawMethod.value,
      payout_alipay_account: withdrawMethod.value === 'alipay' ? alipayAccount.value : undefined,
      payout_alipay_name: withdrawMethod.value === 'alipay' ? alipayName.value : undefined,
      payout_usdt_chain: withdrawMethod.value === 'usdt' ? usdtChain.value : undefined,
      payout_usdt_address: withdrawMethod.value === 'usdt' ? usdtAddress.value : undefined,
    })
    appStore.showSuccess(t('points.withdraw.success'))
    withdrawPoints.value = null
    alipayAccount.value = ''
    alipayName.value = ''
    usdtChain.value = 'TRC20'
    usdtAddress.value = ''
    await refresh()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('points.actionFailed')))
  } finally {
    busy.value = false
  }
}

async function onRedeemPlan(plan: PointsPlanOption): Promise<void> {
  if (planQuoteError.value) {
    appStore.showError(planQuoteError.value)
    return
  }
  const planName = t('points.redeemPlan.planTitle', { d: plan.daily_amount_usd })
  if (!window.confirm(t('points.redeemPlan.confirm', {
    action: selectedPlanSubmitLabel.value,
    points: selectedPlanPointsPrice.value.toLocaleString(),
    plan: planName,
  }))) return
  busy.value = true
  // 幂等键：本次兑换生成一个，网络重发/重试复用同一 key → 后端按 (user, key) 去重，绝不二次扣分。
  const idempotencyKey = newExchangeId()
  try {
    await redeemPointsToPlan(plan.daily_amount_usd, plan.validity_days, idempotencyKey)
    appStore.showSuccess(t('points.redeemPlan.success'))
    await refresh()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('points.actionFailed')))
  } finally {
    busy.value = false
  }
}

async function refreshSelectedPlanQuote(): Promise<void> {
  const seq = ++planQuoteSeq
  const plan = selectedPlan.value
  planQuoteError.value = ''
  selectedPlanCharge.value = null
  if (!plan || !overview.value) return
  if (selectedPlanMode.value === 'purchase') {
    selectedPlanCharge.value = plan.price
    return
  }
  if (selectedPlanMode.value === 'downgrade') {
    planQuoteError.value = t('points.redeemPlan.downgradeBlocked')
    return
  }
  planQuoteLoading.value = true
  try {
    if (selectedPlanMode.value === 'renew') {
      const quote = await renewQuote(plan.validity_days)
      if (seq === planQuoteSeq) selectedPlanCharge.value = quote.price
    } else {
      const quote = await changePlanQuote(plan.daily_amount_usd, plan.validity_days)
      if (seq === planQuoteSeq) selectedPlanCharge.value = quote.diff
    }
  } catch (error) {
    if (seq === planQuoteSeq) {
      planQuoteError.value = extractApiErrorMessage(error, t('points.redeemPlan.quoteFailed'))
    }
  } finally {
    if (seq === planQuoteSeq) planQuoteLoading.value = false
  }
}

function syncSelectedPlan(): void {
  const firstDaily = planDailyOptions.value[0]
  if (firstDaily == null) {
    selectedPlanDaily.value = null
    selectedPlanDays.value = null
    return
  }
  if (selectedPlanDaily.value == null || !planDailyOptions.value.includes(selectedPlanDaily.value)) {
    selectedPlanDaily.value = firstDaily
  }
  const firstDays = planValidityOptions.value[0] ?? null
  if (selectedPlanDays.value == null || !planValidityOptions.value.includes(selectedPlanDays.value)) {
    selectedPlanDays.value = firstDays
  }
}

watch(selectedPlanDaily, () => {
  const firstDays = planValidityOptions.value[0] ?? null
  if (selectedPlanDays.value == null || !planValidityOptions.value.includes(selectedPlanDays.value)) {
    selectedPlanDays.value = firstDays
  }
})

watch([selectedPlan, activeSubscription, peg], () => {
  void refreshSelectedPlanQuote()
}, { immediate: true })

onMounted(() => {
  void loadAll()
})
</script>
