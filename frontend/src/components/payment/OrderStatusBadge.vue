<template>
  <span
    class="inline-flex items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs font-medium"
    :class="toneClass"
  >
    <span class="h-1.5 w-1.5 rounded-full" :class="dotClass" />
    {{ statusLabel }}
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { OrderStatus } from '@/types/payment'

const props = defineProps<{
  status: OrderStatus
}>()

const { t } = useI18n()

type StatusTone = 'settled' | 'waiting' | 'quiet' | 'failed' | 'refunding'

const statusMap: Record<OrderStatus, { key: string; tone: StatusTone }> = {
  PENDING: { key: 'payment.status.pending', tone: 'waiting' },
  PAID: { key: 'payment.status.paid', tone: 'settled' },
  RECHARGING: { key: 'payment.status.recharging', tone: 'waiting' },
  COMPLETED: { key: 'payment.status.completed', tone: 'settled' },
  EXPIRED: { key: 'payment.status.expired', tone: 'quiet' },
  CANCELLED: { key: 'payment.status.cancelled', tone: 'quiet' },
  FAILED: { key: 'payment.status.failed', tone: 'failed' },
  REFUND_REQUESTED: { key: 'payment.status.refund_requested', tone: 'refunding' },
  REFUNDING: { key: 'payment.status.refunding', tone: 'refunding' },
  REFUNDED: { key: 'payment.status.refunded', tone: 'quiet' },
  PARTIALLY_REFUNDED: { key: 'payment.status.partially_refunded', tone: 'quiet' },
  REFUND_FAILED: { key: 'payment.status.refund_failed', tone: 'failed' },
}

const toneMap: Record<StatusTone, { badge: string; dot: string }> = {
  settled: {
    badge: 'border-stone-200 bg-white text-stone-700 dark:border-dark-700 dark:bg-dark-900 dark:text-gray-300',
    dot: 'bg-[#6f7d68] dark:bg-[#9aa88f]',
  },
  waiting: {
    badge: 'border-[#e7ded1] bg-[#fbfaf7] text-[#6f6252] dark:border-dark-700 dark:bg-dark-900 dark:text-gray-300',
    dot: 'bg-[#b08a5b] dark:bg-[#c9a77d]',
  },
  quiet: {
    badge: 'border-stone-200 bg-white text-stone-500 dark:border-dark-700 dark:bg-dark-900 dark:text-gray-400',
    dot: 'bg-[#c9c1b4] dark:bg-dark-500',
  },
  failed: {
    badge: 'border-[#e7d8d4] bg-[#fffaf8] text-[#7f4c43] dark:border-dark-700 dark:bg-dark-900 dark:text-gray-300',
    dot: 'bg-[#b56a5f] dark:bg-[#c9867a]',
  },
  refunding: {
    badge: 'border-[#e2d8c8] bg-[#fbfaf7] text-[#6f6252] dark:border-dark-700 dark:bg-dark-900 dark:text-gray-300',
    dot: 'bg-[#a98d66] dark:bg-[#c0a27a]',
  },
}

const statusLabel = computed(() => {
  const entry = statusMap[props.status]
  return entry ? t(entry.key) : props.status
})

const toneClass = computed(() => {
  const entry = statusMap[props.status]
  return toneMap[entry?.tone ?? 'quiet'].badge
})

const dotClass = computed(() => {
  const entry = statusMap[props.status]
  return toneMap[entry?.tone ?? 'quiet'].dot
})
</script>
