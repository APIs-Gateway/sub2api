import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { useCurrencyDisplay } from '@/composables/useCurrencyDisplay'
import OrderTable from '../OrderTable.vue'
import type { PaymentOrder } from '@/types/payment'

const settings = vi.hoisted(() => ({ value: { balance_recharge_multiplier: 13 } as Record<string, unknown> | null }))

vi.mock('@/stores/app', () => ({ useAppStore: () => ({ cachedPublicSettings: settings.value }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const order = {
  id: 1,
  out_trade_no: 'T1',
  order_type: 'balance',
  amount: 1300,
  pay_amount: 100,
  fee_rate: 0,
  payment_type: 'alipay',
  status: 'COMPLETED',
  created_at: '2026-10-01T00:00:00Z',
  user_id: 7,
  user_email: 'a@b.c',
} as unknown as PaymentOrder

const DataTableStub = {
  props: ['columns', 'data'],
  template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-pay_amount" :value="row.pay_amount" :row="row" /></div></div>',
}

function mountTable(showUser: boolean) {
  return mount(OrderTable, {
    props: { orders: [order], loading: false, showUser },
    global: { stubs: { DataTable: DataTableStub, OrderStatusBadge: true } },
  })
}

describe('OrderTable credited amount', () => {
  beforeEach(() => {
    settings.value = { balance_recharge_multiplier: 13 }
    useCurrencyDisplay().setMode('fiat')
  })

  it('admin (showUser) keeps the USD ledger amount even in fiat mode', () => {
    const text = mountTable(true).text()
    // 实付列（¥100.00）改动前后一致；到账金额保持美元账本口径
    expect(text).toBe('¥100.00payment.orders.creditedAmount: $1,300.00')
  })

  it('user side shows the fiat amount in fiat mode', () => {
    const text = mountTable(false).text()
    expect(text).toBe('¥100.00payment.orders.creditedAmount: ¥100.00')
  })
})
