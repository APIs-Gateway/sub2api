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
  queryRefund: vi.fn(),
  resolveRefund: vi.fn(),
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
    queryRefund: mocks.queryRefund,
    resolveRefund: mocks.resolveRefund,
  },
  default: {
    getOrders: mocks.getOrders,
    getOrder: mocks.getOrder,
    cancelOrder: mocks.cancelOrder,
    retryRecharge: mocks.retryRecharge,
    refundOrder: mocks.refundOrder,
    queryRefund: mocks.queryRefund,
    resolveRefund: mocks.resolveRefund,
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
const ConfirmDialogStub = defineComponent({
  props: ['show', 'title', 'message', 'confirmText', 'danger'],
  emits: ['confirm', 'cancel'],
  setup: (props, { emit }) => () =>
    props.show
      ? h('div', { 'data-test': 'confirm-dialog', 'data-danger': String(Boolean(props.danger)) }, [
          h('span', { 'data-test': 'confirm-message' }, String(props.message ?? '')),
          h('button', { 'data-test': 'confirm-ok', onClick: () => emit('confirm') }, String(props.confirmText ?? '')),
          h('button', { 'data-test': 'confirm-cancel', onClick: () => emit('cancel') }, 'cancel'),
        ])
      : null,
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
        ConfirmDialog: ConfirmDialogStub,
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
    mocks.queryRefund.mockReset()
    mocks.resolveRefund.mockReset()
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
      status: 'REFUND_REQUESTED,REFUNDING,REFUND_PENDING,PARTIALLY_REFUNDED,REFUNDED,REFUND_FAILED',
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

  it('queries a pending refund and reports each gateway outcome', async () => {
    const pending = makeOrder({ id: 46, status: 'REFUND_PENDING', refund_amount: 100 })
    mocks.getOrders.mockResolvedValue({ data: { items: [pending], total: 1 } })

    const wrapper = mountView()
    await flushPromises()
    const button = () => wrapper.findAll('[data-test="order-row-46"] button').find((b) => b.text().includes('payment.admin.queryRefundStatus'))!
    expect(button().exists()).toBe(true)

    mocks.queryRefund.mockResolvedValueOnce({ data: { success: true } })
    mocks.getOrders.mockClear()
    await button().trigger('click')
    await flushPromises()
    expect(mocks.queryRefund).toHaveBeenLastCalledWith(46)
    expect(mocks.showSuccess).toHaveBeenLastCalledWith('payment.admin.refundSuccess')
    expect(mocks.getOrders).toHaveBeenCalled()

    mocks.queryRefund.mockResolvedValueOnce({ data: { success: false, refund_pending: true } })
    await button().trigger('click')
    await flushPromises()
    expect(mocks.showSuccess).toHaveBeenLastCalledWith('payment.admin.refundPending')

    mocks.queryRefund.mockResolvedValueOnce({ data: { success: false, warning: 'gateway refund failed: closed' } })
    await button().trigger('click')
    await flushPromises()
    expect(mocks.showError).toHaveBeenLastCalledWith('gateway refund failed: closed')

    mocks.queryRefund.mockRejectedValueOnce(new Error('network'))
    await button().trigger('click')
    await flushPromises()
    expect(mocks.showError).toHaveBeenCalledTimes(2)
    expect(button().attributes('disabled')).toBeUndefined()
  })

  it('closes the refund dialog with a pending notice when the gateway only accepted the refund', async () => {
    mocks.route.meta.refundOverview = false
    const completed = makeOrder({ id: 47, status: 'COMPLETED', refund_amount: 0 })
    mocks.getOrders.mockResolvedValue({ data: { items: [completed], total: 1 } })
    mocks.refundOrder.mockResolvedValue({ data: { success: false, refund_pending: true, warning: 'gateway refund is pending confirmation' } })

    const wrapper = mountView()
    await flushPromises()
    const refundButton = wrapper.findAll('[data-test="order-row-47"] button').find((b) => b.text().includes('payment.admin.refund'))
    await refundButton?.trigger('click')
    expect(wrapper.find('[data-test="refund-dialog"]').exists()).toBe(true)

    wrapper.findComponent(AdminRefundDialogStub).vm.$emit('confirm', { amount: 10, reason: 'r', deduct_balance: true, force: false })
    await flushPromises()
    expect(mocks.refundOrder).toHaveBeenCalledWith(47, { amount: 10, reason: 'r', deduct_balance: true, force: false })
    expect(mocks.showSuccess).toHaveBeenLastCalledWith('payment.admin.refundPending')
    expect(wrapper.find('[data-test="refund-dialog"]').exists()).toBe(false)
  })

  it('lets an admin resolve a pending refund by hand after confirmation', async () => {
    const pending = makeOrder({ id: 48, status: 'REFUND_PENDING', refund_amount: 100 })
    mocks.getOrders.mockResolvedValue({ data: { items: [pending], total: 1 } })

    const wrapper = mountView()
    await flushPromises()
    const row = () => wrapper.find('[data-test="order-row-48"]')
    expect(row().find('[data-test="resolve-refund-succeeded"]').exists()).toBe(true)
    expect(row().find('[data-test="resolve-refund-failed"]').exists()).toBe(true)

    // Cancel does not call the API.
    await row().find('[data-test="resolve-refund-failed"]').trigger('click')
    expect(wrapper.find('[data-test="confirm-dialog"]').attributes('data-danger')).toBe('true')
    expect(wrapper.find('[data-test="confirm-message"]').text()).toBe('payment.admin.resolveRefundFailedConfirm')
    await wrapper.find('[data-test="confirm-cancel"]').trigger('click')
    expect(wrapper.find('[data-test="confirm-dialog"]').exists()).toBe(false)
    expect(mocks.resolveRefund).not.toHaveBeenCalled()

    mocks.resolveRefund.mockResolvedValueOnce({ data: { success: false } })
    await row().find('[data-test="resolve-refund-failed"]').trigger('click')
    mocks.getOrders.mockClear()
    await wrapper.find('[data-test="confirm-ok"]').trigger('click')
    await flushPromises()
    expect(mocks.resolveRefund).toHaveBeenLastCalledWith(48, { outcome: 'failed' })
    expect(mocks.showSuccess).toHaveBeenLastCalledWith('payment.admin.refundMarkedFailed')
    expect(mocks.getOrders).toHaveBeenCalled()
    expect(wrapper.find('[data-test="confirm-dialog"]').exists()).toBe(false)

    mocks.resolveRefund.mockResolvedValueOnce({ data: { success: true } })
    await row().find('[data-test="resolve-refund-succeeded"]').trigger('click')
    expect(wrapper.find('[data-test="confirm-message"]').text()).toBe('payment.admin.resolveRefundSucceededConfirm')
    await wrapper.find('[data-test="confirm-ok"]').trigger('click')
    await flushPromises()
    expect(mocks.resolveRefund).toHaveBeenLastCalledWith(48, { outcome: 'succeeded' })
    expect(mocks.showSuccess).toHaveBeenLastCalledWith('payment.admin.refundSuccess')

    mocks.resolveRefund.mockRejectedValueOnce(new Error('conflict'))
    await row().find('[data-test="resolve-refund-succeeded"]').trigger('click')
    await wrapper.find('[data-test="confirm-ok"]').trigger('click')
    await flushPromises()
    expect(mocks.showError).toHaveBeenCalled()
    expect(wrapper.find('[data-test="confirm-dialog"]').exists()).toBe(true)
  })
})
