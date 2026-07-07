<template>
  <AppLayout>
    <div class="space-y-5">
      <!-- Filters -->
      <div class="rounded-md border border-stone-200 bg-white/80 p-3 dark:border-dark-700 dark:bg-dark-900">
        <div class="flex flex-wrap items-center gap-3">
          <Select v-model="currentFilter" :options="statusFilters" class="w-36" @change="fetchOrders" />
          <div class="flex flex-1 items-center justify-end gap-2">
            <button
              type="button"
              @click="fetchOrders"
              :disabled="loading"
              class="inline-flex h-10 w-10 items-center justify-center rounded-md border border-stone-200 text-stone-600 transition-colors hover:bg-stone-50 disabled:cursor-not-allowed disabled:opacity-60 dark:border-dark-700 dark:text-gray-300 dark:hover:bg-dark-800"
              :title="t('common.refresh')"
            >
              <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
            </button>
            <button
              type="button"
              class="inline-flex h-10 items-center rounded-md border border-stone-900 bg-stone-900 px-4 text-sm font-medium text-white transition-colors hover:bg-stone-800 dark:border-gray-100 dark:bg-gray-100 dark:text-dark-900 dark:hover:bg-white"
              @click="router.push('/purchase')"
            >
              {{ t('payment.result.backToRecharge') }}
            </button>
          </div>
        </div>
      </div>

      <!-- Table -->
      <OrderTable :orders="orders" :loading="loading">
        <template #actions="{ row }">
          <div class="flex items-center gap-1">
            <button
              v-if="row.status === 'PENDING'"
              type="button"
              @click="handleCancel(row.id)"
              class="inline-flex items-center gap-1 rounded-md border border-transparent px-2 py-1 text-xs font-medium text-stone-600 transition-colors hover:border-stone-200 hover:bg-stone-50 dark:text-gray-300 dark:hover:border-dark-700 dark:hover:bg-dark-800"
            >
              <Icon name="x" size="xs" />
              <span>{{ t('payment.orders.cancel') }}</span>
            </button>
            <button
              v-if="canRequestRefund(row)"
              type="button"
              @click="openRefundDialog(row)"
              class="inline-flex items-center gap-1 rounded-md border border-transparent px-2 py-1 text-xs font-medium text-stone-600 transition-colors hover:border-stone-200 hover:bg-stone-50 dark:text-gray-300 dark:hover:border-dark-700 dark:hover:bg-dark-800"
            >
              <Icon name="dollar" size="xs" />
              <span>{{ t('payment.orders.requestRefund') }}</span>
            </button>
          </div>
        </template>
      </OrderTable>

      <!-- Pagination -->
      <Pagination
        v-if="pagination.total > 0"
        :page="pagination.page"
        :total="pagination.total"
        :page-size="pagination.page_size"
        @update:page="handlePageChange"
        @update:pageSize="handlePageSizeChange"
      />
    </div>

    <!-- Cancel Confirm Dialog -->
    <BaseDialog :show="!!cancelTargetId" :title="t('payment.orders.cancel')" width="narrow" @close="cancelTargetId = null">
      <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('payment.confirmCancel') }}</p>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button class="btn btn-secondary" @click="cancelTargetId = null">{{ t('common.cancel') }}</button>
          <button class="btn btn-danger" :disabled="actionLoading" @click="confirmCancel">{{ actionLoading ? t('common.processing') : t('payment.orders.cancel') }}</button>
        </div>
      </template>
    </BaseDialog>

    <!-- Refund Dialog -->
    <BaseDialog :show="!!refundTarget" :title="t('payment.orders.requestRefund')" @close="refundTarget = null">
      <div v-if="refundTarget" class="space-y-4">
        <div class="rounded-md bg-gray-50 p-4 dark:bg-dark-800">
          <div class="flex justify-between text-sm">
            <span class="text-gray-600 dark:text-gray-400">{{ t('payment.orders.orderId') }}</span>
            <span class="font-mono tabular-nums text-gray-900 dark:text-white">#{{ refundTarget.id }}</span>
          </div>
          <div class="mt-2 flex justify-between text-sm">
            <span class="text-gray-600 dark:text-gray-400">{{ t('payment.orders.amount') }}</span>
            <span class="font-mono tabular-nums text-gray-900 dark:text-white">{{ refundOrderAmountText }}</span>
          </div>
        </div>
        <div class="rounded-md border border-gray-200 bg-white p-4 text-sm dark:border-dark-700 dark:bg-dark-900">
          <div class="flex justify-between">
            <span class="text-gray-600 dark:text-gray-400">{{ t('payment.refundGatewayBase') }}</span>
            <span class="font-mono tabular-nums text-gray-900 dark:text-white">{{ refundGatewayBaseText }}</span>
          </div>
          <div class="mt-2 flex justify-between">
            <span class="text-gray-600 dark:text-gray-400">{{ t('payment.refundFee') }} ({{ refundFeeRate.toFixed(2) }}%)</span>
            <span class="font-mono tabular-nums text-amber-700 dark:text-amber-300">{{ refundFeeText }}</span>
          </div>
          <div class="mt-2 flex justify-between font-medium">
            <span class="text-gray-700 dark:text-gray-300">{{ t('payment.refundUserReceives') }}</span>
            <span class="font-mono tabular-nums text-gray-900 dark:text-white">{{ refundUserReceivesText }}</span>
          </div>
          <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">
            {{ refundTarget.order_type === 'subscription' ? t('payment.subscriptionRefundNote') : t('payment.balanceRefundNote') }}
          </p>
        </div>
        <div>
          <label class="input-label">{{ t('payment.refundReason') }}</label>
          <textarea v-model="refundReason" rows="3" class="input mt-1 w-full" :placeholder="t('payment.refundReasonPlaceholder')" />
        </div>
      </div>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button class="btn btn-secondary" @click="refundTarget = null">{{ t('common.cancel') }}</button>
          <button class="btn btn-primary" :disabled="actionLoading || !refundReason.trim()" @click="confirmRefund">{{ actionLoading ? t('common.processing') : t('payment.orders.requestRefund') }}</button>
        </div>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { useAppStore } from '@/stores'
import { paymentAPI } from '@/api/payment'
import { extractI18nErrorMessage } from '@/utils/apiError'
import type { PaymentOrder } from '@/types/payment'
import AppLayout from '@/components/layout/AppLayout.vue'
import Pagination from '@/components/common/Pagination.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import OrderTable from '@/components/payment/OrderTable.vue'

const { t } = useI18n()
const router = useRouter()
const appStore = useAppStore()

const loading = ref(false)
const actionLoading = ref(false)
const orders = ref<PaymentOrder[]>([])
const refundEligibleProviders = ref<Set<string>>(new Set())
const refundFeeRate = ref(0)
const currentFilter = ref('')
const cancelTargetId = ref<number | null>(null)
const refundTarget = ref<PaymentOrder | null>(null)
const refundReason = ref('')
const pagination = reactive({ page: 1, page_size: 20, total: 0 })

const statusFilters = computed(() => [
  { value: '', label: t('common.all') },
  { value: 'PENDING', label: t('payment.status.pending') },
  { value: 'COMPLETED', label: t('payment.status.completed') },
  { value: 'FAILED', label: t('payment.status.failed') },
  { value: 'REFUNDED', label: t('payment.status.refunded') },
  { value: 'REFUND_PENDING', label: t('payment.status.refund_pending') },
])

const refundOrderAmountText = computed(() => {
  if (!refundTarget.value) return ''
  const symbol = refundTarget.value.order_type === 'balance' ? '$' : '¥'
  return `${symbol}${refundTarget.value.amount.toFixed(2)}`
})

const refundGatewayBase = computed(() => roundCurrency(refundTarget.value?.pay_amount || refundTarget.value?.amount || 0))
const refundFee = computed(() => roundCurrencyUp(refundGatewayBase.value * Math.min(Math.max(refundFeeRate.value, 0), 100) / 100))
const refundUserReceives = computed(() => Math.max(0, roundCurrency(refundGatewayBase.value - refundFee.value)))
const refundGatewayBaseText = computed(() => `¥${refundGatewayBase.value.toFixed(2)}`)
const refundFeeText = computed(() => `¥${refundFee.value.toFixed(2)}`)
const refundUserReceivesText = computed(() => `¥${refundUserReceives.value.toFixed(2)}`)

async function fetchOrders() {
  loading.value = true
  try {
    const res = await paymentAPI.getMyOrders({
      page: pagination.page,
      page_size: pagination.page_size,
      status: currentFilter.value || undefined,
    })
    orders.value = res.data.items || []
    pagination.total = res.data.total || 0
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
  } finally {
    loading.value = false
  }
}

function handlePageChange(page: number) { pagination.page = page; fetchOrders() }
function handlePageSizeChange(size: number) { pagination.page_size = size; pagination.page = 1; fetchOrders() }

function handleCancel(orderId: number) { cancelTargetId.value = orderId }

async function confirmCancel() {
  if (!cancelTargetId.value) return
  actionLoading.value = true
  try {
    await paymentAPI.cancelOrder(cancelTargetId.value)
    appStore.showSuccess(t('common.success'))
    cancelTargetId.value = null
    await fetchOrders()
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
  } finally {
    actionLoading.value = false
  }
}

function openRefundDialog(order: PaymentOrder) { refundTarget.value = order; refundReason.value = '' }

async function confirmRefund() {
  if (!refundTarget.value || !refundReason.value.trim()) return
  actionLoading.value = true
  try {
    await paymentAPI.requestRefund(refundTarget.value.id, { reason: refundReason.value.trim() })
    appStore.showSuccess(t('common.success'))
    refundTarget.value = null
    refundReason.value = ''
    await fetchOrders()
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
  } finally {
    actionLoading.value = false
  }
}

function canRequestRefund(order: PaymentOrder): boolean {
  if (order.status !== 'COMPLETED') return false
  if (!order.provider_instance_id) return false
  return refundEligibleProviders.value.has(order.provider_instance_id)
}

async function loadRefundEligibility() {
  try {
    const res = await paymentAPI.getRefundEligibleProviders()
    refundEligibleProviders.value = new Set(res.data.provider_instance_ids || [])
  } catch { /* ignore — default to hiding refund button */ }
}

async function loadRefundFeeRate() {
  try {
    const res = await paymentAPI.getCheckoutInfo()
    refundFeeRate.value = Number(res.data.refund_fee_rate) || 0
  } catch {
    refundFeeRate.value = 0
  }
}

function roundCurrency(value: number): number {
  if (!Number.isFinite(value)) return 0
  return Math.round(value * 100) / 100
}

function roundCurrencyUp(value: number): number {
  if (!Number.isFinite(value)) return 0
  return Math.ceil(value * 100) / 100
}

onMounted(() => { fetchOrders(); loadRefundEligibility(); loadRefundFeeRate() })
</script>
