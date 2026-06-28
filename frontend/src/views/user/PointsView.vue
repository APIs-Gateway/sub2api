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
            <p class="mt-2 text-2xl font-semibold font-mono tabular-nums text-gray-900 dark:text-white">{{ formatPercent(overview.effective_rate) }}</p>
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
              <div v-else class="space-y-1">
                <label class="input-label">{{ t('points.withdraw.usdtAddress') }}</label>
                <input v-model.trim="usdtAddress" type="text" maxlength="128" class="input" />
              </div>
              <div class="rounded-md bg-gray-50 px-3 py-2 text-xs text-gray-600 dark:bg-dark-900 dark:text-gray-400 space-y-1">
                <div class="flex justify-between"><span>{{ t('points.withdraw.gross') }}</span><span class="font-mono">{{ formatCurrency(withdrawGross) }}</span></div>
                <div class="flex justify-between"><span>{{ t('points.withdraw.fee') }} ({{ overview.config.withdraw_fee_percent }}%)</span><span class="font-mono">-{{ formatCurrency(withdrawFee) }}</span></div>
                <div class="flex justify-between font-semibold text-gray-900 dark:text-white"><span>{{ t('points.withdraw.net') }}</span><span class="font-mono">{{ formatCurrency(withdrawNet) }}</span></div>
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
            <div v-else class="space-y-2">
              <div v-for="plan in plans" :key="plan.group_id" class="flex items-center justify-between rounded-md border border-gray-200 px-3 py-2 dark:border-dark-700">
                <div class="min-w-0">
                  <p class="truncate text-sm font-medium text-gray-900 dark:text-white">{{ plan.name }}</p>
                  <p class="text-xs text-gray-500 dark:text-gray-500">{{ t('points.redeemPlan.validity', { n: plan.validity_days }) }} · {{ plan.points_price.toLocaleString() }} {{ t('points.unit') }}</p>
                </div>
                <button class="btn btn-secondary btn-sm shrink-0" :disabled="busy || overview.account.available < plan.points_price" @click="onRedeemPlan(plan)">{{ t('points.redeemPlan.submit') }}</button>
              </div>
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
import { computed, onMounted, ref } from 'vue'
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
} from '@/api/points'
import { useAppStore } from '@/stores/app'
import { useClipboard } from '@/composables/useClipboard'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatCurrency, formatDateTime } from '@/utils/format'

const { t } = useI18n()
const appStore = useAppStore()
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
const usdtAddress = ref('')

const peg = computed(() => overview.value?.config.peg ?? 0)
const feePercent = computed(() => overview.value?.config.withdraw_fee_percent ?? 0)
const redeemBalanceEstimate = computed(() => (redeemBalancePoints.value || 0) * peg.value)
const withdrawGross = computed(() => (withdrawPoints.value || 0) * peg.value)
const withdrawFee = computed(() => withdrawGross.value * (feePercent.value / 100))
const withdrawNet = computed(() => withdrawGross.value - withdrawFee.value)

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
      const [page, planList] = await Promise.all([listPointsLedger(1, 20), listPointsPlans()])
      ledger.value = page.items
      plans.value = planList
    }
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('points.loadFailed')))
  } finally {
    loading.value = false
  }
}

async function refresh(): Promise<void> {
  overview.value = await getPointsOverview()
  const page = await listPointsLedger(1, 20)
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
      payout_usdt_address: withdrawMethod.value === 'usdt' ? usdtAddress.value : undefined,
    })
    appStore.showSuccess(t('points.withdraw.success'))
    withdrawPoints.value = null
    alipayAccount.value = ''
    alipayName.value = ''
    usdtAddress.value = ''
    await refresh()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('points.actionFailed')))
  } finally {
    busy.value = false
  }
}

async function onRedeemPlan(plan: PointsPlanOption): Promise<void> {
  if (!window.confirm(t('points.redeemPlan.confirm', { points: plan.points_price.toLocaleString(), plan: plan.name }))) return
  busy.value = true
  // 幂等键：本次兑换生成一个，网络重发/重试复用同一 key → 后端按 (user, key) 去重，绝不二次扣分。
  const idempotencyKey = newExchangeId()
  try {
    await redeemPointsToPlan(plan.group_id, plan.validity_days, idempotencyKey)
    appStore.showSuccess(t('points.redeemPlan.success'))
    await refresh()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('points.actionFailed')))
  } finally {
    busy.value = false
  }
}

onMounted(() => {
  void loadAll()
})
</script>
