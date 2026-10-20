import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import PaymentQRDialog from '../PaymentQRDialog.vue'
import PaymentStatusPanel from '../PaymentStatusPanel.vue'

const { pollOrderStatus, verifyOrder, cancelOrder, routerPush, showError } = vi.hoisted(() => ({
  pollOrderStatus: vi.fn(), verifyOrder: vi.fn(), cancelOrder: vi.fn(), routerPush: vi.fn(),
  showError: vi.fn(),
}))

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}

function order(status: string, id = 42) {
  return { id, status, out_trade_no: `payment-${id}`, order_type: 'balance', amount: 10, pay_amount: 10 }
}

vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))
vi.mock('@/components/layout/AppLayout.vue', () => ({
  default: { template: '<div><slot /></div>' },
}))
vi.mock('@/stores/payment', () => ({
  usePaymentStore: () => ({ pollOrderStatus }),
}))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError }) }))
vi.mock('@/api/payment', () => ({ paymentAPI: { verifyOrder, cancelOrder } }))
vi.mock('qrcode', () => ({ default: { toCanvas: vi.fn() } }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ push: routerPush }),
}))

enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-09-30T12:00:00Z'))
  pollOrderStatus.mockReset().mockResolvedValue(null)
  verifyOrder.mockReset()
  cancelOrder.mockReset().mockResolvedValue(undefined)
  routerPush.mockReset()
  showError.mockReset()
})
afterEach(() => vi.useRealTimers())

async function open(kind: 'dialog' | 'panel', paymentType = 'custom', expiresAt = new Date(Date.now() + 120_000).toISOString()) {
  const props = {
    orderId: 42,
    qrCode: '',
    expiresAt,
    paymentType,
  }
  const global = { stubs: {
    Icon: true,
    BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
  } }
  if (kind === 'panel') return mount(PaymentStatusPanel, { props, global })
  const wrapper = mount(PaymentQRDialog, { props: { ...props, show: false }, global })
  await wrapper.setProps({ show: true })
  return wrapper
}

describe.each(['dialog', 'panel'] as const)('payment countdown: %s', kind => {
  it('catches up on the first timer callback after suspension', async () => {
    const wrapper = await open(kind)
    expect(wrapper.text()).toContain('02:00')

    vi.setSystemTime(Date.now() + 65_000)
    await vi.advanceTimersByTimeAsync(1000)

    expect(wrapper.text()).toContain('00:54')
    expect(wrapper.text()).not.toContain('payment.qr.expired')
  })

  it('checks the order once before declaring expiry after suspension', async () => {
    pollOrderStatus.mockResolvedValue(order('PENDING'))
    const wrapper = await open(kind)
    vi.setSystemTime(Date.now() + 121_000)
    await vi.advanceTimersByTimeAsync(1000)

    expect(wrapper.text()).toContain('payment.qr.expired')
    expect(pollOrderStatus).toHaveBeenCalledTimes(1)
    expect(vi.getTimerCount()).toBe(0)
    await vi.advanceTimersByTimeAsync(6000)
    expect(pollOrderStatus).toHaveBeenCalledTimes(1)
  })

  it('still counts down normally', async () => {
    const wrapper = await open(kind)
    await vi.advanceTimersByTimeAsync(1000)
    expect(wrapper.text()).toContain('01:59')
  })

  it('does not skip a second when the callback is slightly late', async () => {
    const wrapper = await open(kind)
    vi.setSystemTime(Date.now() + 1)
    await vi.advanceTimersByTimeAsync(1000)

    expect(wrapper.text()).toContain('01:59')
    expect(wrapper.text()).not.toContain('payment.qr.expired')
  })
})

describe.each(['dialog', 'panel'] as const)('payment expiry reconciliation: %s', kind => {
  it('waits for the final order query and accepts a payment whose notification was lost', async () => {
    const pending = deferred<ReturnType<typeof order>>()
    pollOrderStatus.mockReturnValue(pending.promise)
    const wrapper = await open(kind)

    vi.setSystemTime(Date.now() + 121_000)
    await vi.advanceTimersByTimeAsync(1000)
    expect(pollOrderStatus).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).not.toContain('payment.qr.expired')

    pending.resolve(order('PAID'))
    await flushPromises()
    expect(wrapper.text()).toContain('payment.result.success')
    expect(wrapper.emitted('success')).toHaveLength(1)
    expect(wrapper.text()).not.toContain('payment.qr.expired')
  })

  it('waits for an existing poll, then makes a fresh query after the deadline', async () => {
    const previous = deferred<ReturnType<typeof order>>()
    pollOrderStatus.mockReturnValueOnce(previous.promise).mockResolvedValue(order('PAID'))
    const wrapper = await open(kind)
    await vi.advanceTimersByTimeAsync(3000)
    expect(pollOrderStatus).toHaveBeenCalledTimes(1)

    vi.setSystemTime(Date.now() + 121_000)
    await vi.advanceTimersByTimeAsync(1000)
    expect(pollOrderStatus).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).not.toContain('payment.qr.expired')

    previous.resolve(order('PENDING'))
    await flushPromises()
    expect(pollOrderStatus).toHaveBeenCalledTimes(2)
    expect(wrapper.emitted('success')).toHaveLength(1)
  })

  it('ignores the final paid response after the user cancels', async () => {
    const pending = deferred<ReturnType<typeof order>>()
    pollOrderStatus.mockReturnValue(pending.promise)
    const wrapper = await open(kind)
    vi.setSystemTime(Date.now() + 121_000)
    await vi.advanceTimersByTimeAsync(1000)
    await wrapper.find('button.btn-secondary').trigger('click')
    await flushPromises()
    pending.resolve(order('PAID'))
    await flushPromises()

    expect(wrapper.emitted('success')).toBeUndefined()
    if (kind === 'panel') {
      expect(wrapper.text()).toContain('payment.qr.cancelled')
    } else {
      expect(wrapper.emitted('close')).toHaveLength(1)
    }
  })

  it('keeps the order active after cancellation fails and accepts a later paid retry', async () => {
    pollOrderStatus.mockResolvedValueOnce(null).mockResolvedValueOnce(order('PAID'))
    cancelOrder.mockRejectedValueOnce(new Error('cancel unavailable'))
    const wrapper = await open(kind)
    vi.setSystemTime(Date.now() + 121_000)
    await vi.advanceTimersByTimeAsync(1000)
    expect(pollOrderStatus).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).not.toContain('payment.qr.expired')

    await wrapper.find('button.btn-secondary').trigger('click')
    await flushPromises()
    expect(showError).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.emitted('settled')).toBeUndefined()
    expect(routerPush).not.toHaveBeenCalledWith('/purchase')

    await vi.advanceTimersByTimeAsync(3000)
    expect(pollOrderStatus).toHaveBeenCalledTimes(2)
    expect(wrapper.emitted('success')).toHaveLength(1)
  })
})

it.each(['dialog', 'panel'] as const)('%s verifies a pending built-in QR payment at expiry despite the retry interval', async kind => {
  pollOrderStatus.mockResolvedValue(order('PENDING'))
  const recovered = deferred<{ data: ReturnType<typeof order> }>()
  verifyOrder.mockResolvedValueOnce({ data: order('PENDING') })
    .mockResolvedValueOnce({ data: order('PENDING') })
    .mockReturnValue(recovered.promise)
  const startedAt = Date.now()
  const wrapper = await open(kind, 'wxpay')
  await vi.advanceTimersByTimeAsync(3000)
  vi.setSystemTime(startedAt + 116_000)
  await vi.advanceTimersByTimeAsync(3000)
  expect(verifyOrder).toHaveBeenCalledTimes(2)

  vi.setSystemTime(startedAt + 121_000)
  await vi.advanceTimersByTimeAsync(1000)
  expect(verifyOrder).toHaveBeenCalledTimes(3)
  expect(verifyOrder).toHaveBeenLastCalledWith('payment-42')
  expect(wrapper.text()).not.toContain('payment.qr.expired')

  recovered.resolve({ data: order('PAID') })
  await flushPromises()
  expect(wrapper.emitted('success')).toHaveLength(1)
  expect(wrapper.text()).not.toContain('payment.qr.expired')
})

it.each(['dialog', 'panel'] as const)('%s restores a database-expired QR payment after a missed notification', async kind => {
  pollOrderStatus.mockResolvedValue(order('EXPIRED'))
  verifyOrder.mockResolvedValue({ data: order('PAID') })
  const wrapper = await open(kind, 'wxpay', new Date(Date.now() - 1000).toISOString())
  await flushPromises()

  expect(pollOrderStatus).toHaveBeenCalledTimes(1)
  expect(verifyOrder).toHaveBeenCalledWith('payment-42')
  expect(wrapper.emitted('success')).toHaveLength(1)
  expect(wrapper.text()).not.toContain('payment.qr.expired')
})

it.each([
  ['dialog', 'PENDING'], ['dialog', 'EXPIRED'],
  ['panel', 'PENDING'], ['panel', 'EXPIRED'],
] as const)('%s retries a failed verification of a %s QR payment', async (kind, status) => {
  pollOrderStatus.mockResolvedValue(order(status))
  verifyOrder.mockRejectedValueOnce(new Error('provider unavailable'))
    .mockResolvedValueOnce({ data: order('PAID') })
  const wrapper = await open(kind, 'wxpay', new Date(Date.now() - 1000).toISOString())
  await flushPromises()

  expect(pollOrderStatus).toHaveBeenCalledTimes(1)
  expect(verifyOrder).toHaveBeenCalledTimes(1)
  expect(wrapper.text()).not.toContain('payment.qr.expired')
  expect(vi.getTimerCount()).toBe(1)

  await vi.advanceTimersByTimeAsync(3000)
  expect(pollOrderStatus).toHaveBeenCalledTimes(2)
  expect(verifyOrder).toHaveBeenCalledTimes(2)
  expect(wrapper.emitted('success')).toHaveLength(1)
  expect(wrapper.text()).not.toContain('payment.qr.expired')
})

it('does not let the old dialog final query settle a newly opened order', async () => {
  const oldQuery = deferred<ReturnType<typeof order>>()
  pollOrderStatus.mockReturnValueOnce(oldQuery.promise).mockResolvedValue(order('PENDING', 43))
  const wrapper = await open('dialog')
  vi.setSystemTime(Date.now() + 121_000)
  await vi.advanceTimersByTimeAsync(1000)

  await wrapper.setProps({ orderId: 43, expiresAt: new Date(Date.now() + 120_000).toISOString() })
  oldQuery.resolve(order('PAID'))
  await flushPromises()
  expect(wrapper.emitted('success')).toBeUndefined()
  expect(wrapper.text()).toContain('02:00')
  expect(wrapper.text()).not.toContain('payment.qr.expired')
})

it('ignores an old cancellation response after the dialog switches to a new order', async () => {
  const oldCancel = deferred<undefined>()
  cancelOrder.mockReturnValue(oldCancel.promise)
  const wrapper = await open('dialog')
  await wrapper.find('button.btn-secondary').trigger('click')
  expect(cancelOrder).toHaveBeenCalledWith(42)

  await wrapper.setProps({ orderId: 43, expiresAt: new Date(Date.now() + 120_000).toISOString() })
  oldCancel.resolve(undefined)
  await flushPromises()

  expect(wrapper.emitted('close')).toBeUndefined()
  expect(wrapper.text()).toContain('02:00')
  expect(wrapper.find('button.btn-secondary').attributes('disabled')).toBeUndefined()
})

// 组件里用到的金额口径依赖 app store；未配置充值倍率时按美元展示（旧行为）。
beforeEach(() => {
  setActivePinia(createPinia())
})
