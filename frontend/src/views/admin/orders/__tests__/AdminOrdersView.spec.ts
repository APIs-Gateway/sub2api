import { defineComponent, h } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AdminOrdersView from '../AdminOrdersView.vue'
import type { PaymentOrder } from '@/types/payment'

const mocks = vi.hoisted(() => ({
  route: { meta: { refundOverview: true as boolean | undefined } },
  getOrders: vi.fn(),
  getOrder: vi.fn(),
  cancelOrder: vi.fn(),
  retryRecharge: vi.fn(),
  refundOrder: vi.fn(),
  getSettings: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('vue-router', () => ({ useRoute: () => mocks.route }))
vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return { ...actual, useI18n: () => ({ t: (key: string, fallback?: string) => fallback ?? key }) }
})
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: mocks.showError, showSuccess: mocks.showSuccess }) }))
vi.mock('@/api/admin/payment', () => ({
  adminPaymentAPI: {
    getOrders: mocks.getOrders,
    getOrder: mocks.getOrder,
    cancelOrder: mocks.cancelOrder,
    retryRecharge: mocks.retryRecharge,
    refundOrder: mocks.refundOrder,
  },
  default: {
    getOrders: mocks.getOrders,
    getOrder: mocks.getOrder,
    cancelOrder: mocks.cancelOrder,
    retryRecharge: mocks.retryRecharge,
    refundOrder: mocks.refundOrder,
  },
}))
vi.mock('@/api/admin/settings', () => ({
  settingsAPI: { getSettings: mocks.getSettings },
  default: { getSettings: mocks.getSettings },
}))

function makeOrder(overrides: Partial<PaymentOrder> = {}): PaymentOrder {
  return {
    id: 41,
    user_id: 1761,
    user_email: 'smallwater2018@gmail.com',
    amount: 252,
    pay_amount: 252,
    fee_rate: 5,
    payment_type: 'alipay',
    out_trade_no: 'sub2_20260706BIBavUqq',
    status: 'PARTIALLY_REFUNDED',
    order_type: 'subscription',
    product_name: 'Subscription $210 daily / 30 days',
    created_at: '2026-07-06T12:24:42Z',
    expires_at: '2026-07-06T12:29:42Z',
    paid_at: '2026-07-06T12:25:22Z',
    refund_amount: 210,
    refund_reason: 'Slow and unstable',
    refund_requested_at: '2026-07-08T16:04:00Z',
    refund_requested_by: '1761',
    refund_request_reason: 'Slow and unstable',
    ...overrides,
  }
}

const AppLayoutStub = defineComponent({ setup: (_, { slots }) => () => h('div', slots.default?.()) })
const IconStub = defineComponent({ props: ['name', 'size'], setup: () => () => h('i') })
const OrderStatusBadgeStub = defineComponent({
  props: ['status'],
  setup: (props) => () => h('span', { 'data-test': 'status-badge' }, String(props.status)),
})
const BaseDialogStub = defineComponent({
  props: ['show', 'title', 'width'],
  emits: ['close'],
  setup: (props, { slots, emit }) => () =>
    props.show
      ? h('div', { 'data-test': 'dialog' }, [
          h('div', { 'data-test': 'dialog-title' }, String(props.title ?? '')),
          h('div', { 'data-test': 'dialog-body' }, slots.default?.()),
          h('button', { 'data-test': 'dialog-close', onClick: () => emit('close') }, 'x'),
        ])
      : null,
})
const PaginationStub = defineComponent({
  props: ['page', 'total', 'pageSize'],
  emits: ['update:page', 'update:pageSize'],
  setup: (_, { emit }) => () =>
    h('div', { 'data-test': 'pagination' }, [
      h('button', { 'data-test': 'next-page', onClick: () => emit('update:page', 2) }, 'next'),
      h('button', { 'data-test': 'change-size', onClick: () => emit('update:pageSize', 50) }, 'size'),
    ]),
})
const SelectStub = defineComponent({
  props: ['modelValue', 'options'],
  emits: ['update:modelValue', 'change'],
  setup: (props, { emit }) => () =>
    h(
      'select',
      {
        'data-test': 'select',
        value: props.modelValue,
        onChange: (event: Event) => {
          emit('update:modelValue', (event.target as HTMLSelectElement).value)
          emit('change')
        },
      },
      ((props.options as Array<{ value: string; label: string }>) ?? []).map((option) =>
        h('option', { value: option.value }, option.label),
      ),
    ),
})
const OrderTableStub = defineComponent({
  props: ['orders', 'loading', 'showUser'],
  setup: (props, { slots }) => () =>
    h(
      'div',
      { 'data-test': 'order-table' },
      ((props.orders as PaymentOrder[]) ?? []).map((row) =>
        h('div', { 'data-test': `order-row-${row.id}` }, slots.actions?.({ row })),
      ),
    ),
})
const AdminRefundDialogStub = defineComponent({
  props: ['show', 'order', 'submitting', 'requireForce', 'warning'],
  emits: ['confirm', 'cancel'],
  setup: (props) => () => (props.show ? h('div', { 'data-test': 'refund-dialog' }, String(props.order?.id ?? '')) : null),
})

function mountView() {
  return mount(AdminOrdersView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        Icon: IconStub,
        Select: SelectStub,
        OrderTable: OrderTableStub,
        Pagination: PaginationStub,
        BaseDialog: BaseDialogStub,
        AdminRefundDialog: AdminRefundDialogStub,
        OrderStatusBadge: OrderStatusBadgeStub,
      },
    },
  })
}

describe('AdminOrdersView refund management', () => {
  beforeEach(() => {
    mocks.route.meta.refundOverview = true
    mocks.getOrders.mockReset()
    mocks.getOrder.mockReset()
    mocks.cancelOrder.mockReset()
    mocks.retryRecharge.mockReset()
    mocks.refundOrder.mockReset()
    mocks.getSettings.mockReset()
    mocks.showError.mockReset()
    mocks.showSuccess.mockReset()
    mocks.getSettings.mockResolvedValue({ payment_refund_fee_rate: 5 })
    mocks.getOrders.mockResolvedValue({ data: { items: [], total: 0 } })
    mocks.getOrder.mockResolvedValue({ data: { order: makeOrder(), auditLogs: [] } })
  })

  it('loads the refund overview with refund statuses and no refund button for settled refunds', async () => {
    const partial = makeOrder()
    const requested = makeOrder({ id: 42, status: 'REFUND_REQUESTED', refund_amount: 88 })
    const failed = makeOrder({ id: 43, status: 'REFUND_FAILED', refund_amount: 66 })
    mocks.getOrders.mockResolvedValue({ data: { items: [partial, requested, failed], total: 3 } })

    const wrapper = mountView()
    await flushPromises()

    expect(mocks.getOrders).toHaveBeenCalledWith({
      page: 1,
      page_size: 20,
      keyword: undefined,
      status: 'REFUND_REQUESTED,REFUNDING,PARTIALLY_REFUNDED,REFUNDED,REFUND_FAILED',
      payment_type: undefined,
      order_type: undefined,
    })
    expect(wrapper.find('select').text()).toContain('payment.admin.allRefundStatuses')
    expect(wrapper.find('[data-test="order-row-41"]').text()).toContain('payment.admin.alreadyRefunded ¥210.00')
    expect(wrapper.find('[data-test="order-row-41"]').text()).not.toContain('payment.admin.refund')
    expect(wrapper.find('[data-test="order-row-42"]').text()).toContain('payment.admin.approveRefund')
    expect(wrapper.find('[data-test="order-row-43"]').text()).toContain('payment.admin.retryRefund')

    mocks.getOrders.mockClear()
    await wrapper.find('select').setValue('REFUND_FAILED')
    await flushPromises()
    expect(mocks.getOrders).toHaveBeenLastCalledWith({
      page: 1,
      page_size: 20,
      keyword: undefined,
      status: 'REFUND_FAILED',
      payment_type: undefined,
      order_type: undefined,
    })
  })

  it('shows the refund settlement detail using the configured refund fee rate', async () => {
    const order = makeOrder()
    mocks.getOrders.mockResolvedValue({ data: { items: [order], total: 1 } })
    mocks.getOrder.mockResolvedValue({
      data: {
        order,
        auditLogs: [{ id: 1, action: 'ORDER_CREATED', detail: null, operator: null, created_at: '2026-07-06T12:24:42Z' }],
      },
    })

    const wrapper = mountView()
    await flushPromises()
    await wrapper.find('[data-test="order-row-41"] button').trigger('click')
    await flushPromises()

    const text = wrapper.find('[data-test="dialog"]').text()
    expect(mocks.getOrder).toHaveBeenCalledWith(41)
    expect(text).toContain('payment.admin.paymentFeeRate')
    expect(text).toContain('5%')
    expect(text).toContain('payment.orders.fee')
    expect(text).toContain('¥12.00')
    expect(text).toContain('payment.admin.refundSettlementTitle')
    expect(text).toContain('payment.admin.refundGatewayBase')
    expect(text).toContain('¥210.00')
    expect(text).toContain('payment.admin.refundFee (5.00%)')
    expect(text).toContain('¥10.50')
    expect(text).toContain('payment.admin.refundUserReceives')
    expect(text).toContain('¥199.50')
    expect(text).toContain('ORDER_CREATED')
  })

  it('keeps the normal orders tab unfiltered and opens refunds only for completed orders', async () => {
    mocks.route.meta.refundOverview = false
    const completed = makeOrder({ id: 44, status: 'COMPLETED', refund_amount: 0 })
    const refunded = makeOrder({ id: 45, status: 'REFUNDED', refund_amount: 252 })
    mocks.getOrders.mockResolvedValue({ data: { items: [completed, refunded], total: 2 } })

    const wrapper = mountView()
    await flushPromises()

    expect(mocks.getOrders).toHaveBeenCalledWith({
      page: 1,
      page_size: 20,
      keyword: undefined,
      status: undefined,
      payment_type: undefined,
      order_type: undefined,
    })
    expect(wrapper.find('select').text()).toContain('payment.admin.allStatuses')
    expect(wrapper.find('[data-test="order-row-44"]').text()).toContain('payment.admin.refund')
    expect(wrapper.find('[data-test="order-row-45"]').text()).toContain('payment.admin.alreadyRefunded ¥252.00')
    expect(wrapper.find('[data-test="order-row-45"]').text()).not.toContain('payment.admin.refund')

    const refundButton = wrapper.findAll('[data-test="order-row-44"] button').find((button) => button.text().includes('payment.admin.refund'))
    await refundButton?.trigger('click')
    expect(wrapper.find('[data-test="refund-dialog"]').text()).toBe('44')
  })
})
