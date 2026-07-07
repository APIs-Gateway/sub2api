<template>
  <DataTable :columns="columns" :data="orders" :loading="loading">
    <template #cell-id="{ value }">
      <span class="font-mono text-xs text-stone-500 dark:text-gray-400">#{{ value }}</span>
    </template>
    <template #cell-out_trade_no="{ value }">
      <span class="block max-w-[18rem] truncate font-mono text-xs text-stone-700 dark:text-gray-300" :title="value">
        {{ value }}
      </span>
    </template>
    <template #cell-product_name="{ value, row }">
      <div class="max-w-[16rem] truncate text-sm font-medium text-stone-900 dark:text-gray-100" :title="value || fallbackProductName(row)">
        {{ value || fallbackProductName(row) }}
      </div>
    </template>
    <template v-if="showUser" #cell-user_email="{ value, row }">
      <div class="text-sm">
        <span class="text-gray-900 dark:text-white">{{ value || row.user_name || '#' + row.user_id }}</span>
        <span v-if="row.user_notes" class="ml-1 text-xs text-gray-400">({{ row.user_notes }})</span>
      </div>
    </template>
    <template #cell-pay_amount="{ value, row }">
      <div class="text-sm leading-snug">
        <span class="font-mono font-medium tabular-nums text-stone-900 dark:text-gray-100">{{ formatOrderCurrencyAmount(value, row) }}</span>
        <span v-if="row.fee_rate > 0" class="ml-1 text-xs text-stone-400" :title="t('payment.orders.fee') + ': ' + row.fee_rate + '%'">
          ({{ t('payment.orders.fee') }} {{ row.fee_rate }}%)
        </span>
        <div v-if="row.amount !== row.pay_amount" class="mt-0.5 text-xs text-stone-500 dark:text-gray-400">
          {{ t('payment.orders.creditedAmount') }}: {{ formatCreditAmount(row.amount) }}
        </div>
      </div>
    </template>
    <template #cell-payment_type="{ value }">
      <span class="text-sm text-stone-600 dark:text-gray-300">{{ t('payment.methods.' + value, value) }}</span>
    </template>
    <template #cell-status="{ value }">
      <OrderStatusBadge :status="value" />
    </template>
    <template #cell-created_at="{ value }">
      <span class="text-xs text-gray-500 dark:text-gray-400">{{ formatDate(value) }}</span>
    </template>
    <template #cell-actions="{ row }">
      <slot name="actions" :row="row" />
    </template>
  </DataTable>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PaymentOrder } from '@/types/payment'
import type { Column } from '@/components/common/types'
import DataTable from '@/components/common/DataTable.vue'
import OrderStatusBadge from '@/components/payment/OrderStatusBadge.vue'
import { formatPaymentAmount } from '@/components/payment/currency'

const { t } = useI18n()

const props = defineProps<{
  orders: PaymentOrder[]
  loading: boolean
  showUser?: boolean
}>()

function formatDate(dateStr: string) { return new Date(dateStr).toLocaleString() }

function fallbackProductName(order: PaymentOrder): string {
  return order.order_type === 'subscription' ? t('payment.admin.subscriptionOrder') : t('payment.admin.balanceOrder')
}

function formatOrderCurrencyAmount(amount: number, order: PaymentOrder): string {
  return formatPaymentAmount(amount, order.currency)
}

function formatCreditAmount(amount: number): string {
  return formatPaymentAmount(amount, 'USD')
}

const columns = computed((): Column[] => {
  const cols: Column[] = [
    { key: 'id', label: t('payment.orders.orderId') },
    { key: 'out_trade_no', label: t('payment.orders.orderNo') },
    { key: 'product_name', label: t('payment.orders.productName') },
  ]
  if (props.showUser) {
    cols.push({ key: 'user_email', label: t('payment.admin.colUser') })
  }
  cols.push(
    { key: 'pay_amount', label: t('payment.orders.payAmount') },
    { key: 'payment_type', label: t('payment.orders.paymentMethod') },
    { key: 'status', label: t('payment.orders.status') },
    { key: 'created_at', label: t('payment.orders.createdAt') },
    { key: 'actions', label: t('common.actions') },
  )
  return cols
})
</script>
