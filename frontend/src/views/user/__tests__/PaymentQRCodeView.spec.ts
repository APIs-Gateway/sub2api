import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const routeState = vi.hoisted(() => ({
  query: {} as Record<string, unknown>,
}))
const routerPush = vi.hoisted(() => vi.fn())
const pollOrderStatus = vi.hoisted(() => vi.fn())
const showError = vi.hoisted(() => vi.fn())
const toCanvas = vi.hoisted(() => vi.fn())

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => {
    resolve = done
  })
  return { promise, resolve }
}

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return {
    ...actual,
    useRoute: () => routeState,
    useRouter: () => ({ push: routerPush }),
  }
})

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

vi.mock('@/stores/payment', () => ({
  usePaymentStore: () => ({ pollOrderStatus }),
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError }),
}))

vi.mock('@/api/payment', () => ({
  paymentAPI: { cancelOrder: vi.fn() },
}))

vi.mock('qrcode', () => ({
  default: { toCanvas },
}))

import PaymentQRCodeView from '../PaymentQRCodeView.vue'

describe('PaymentQRCodeView', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    routeState.query = {
      order_id: '42',
      qr: 'https://pay.example.test/qr/42',
      payment_type: 'alipay',
      expires_at: '2099-01-01T00:30:00.000Z',
    }
    routerPush.mockReset()
    pollOrderStatus.mockReset()
    showError.mockReset()
    toCanvas.mockReset().mockResolvedValue(undefined)
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('does not overlap QR polling while the previous request is pending', async () => {
    const pending = deferred<{ status: string }>()
    pollOrderStatus.mockReturnValue(pending.promise)

    const wrapper = mount(PaymentQRCodeView, {
      global: {
        stubs: { AppLayout: { template: '<div><slot /></div>' } },
      },
    })

    await flushPromises()
    vi.advanceTimersByTime(9000)

    expect(pollOrderStatus).toHaveBeenCalledTimes(1)
    expect(pollOrderStatus).toHaveBeenCalledWith(42)

    pending.resolve({ status: 'PENDING' })
    await flushPromises()
    wrapper.unmount()
  })

  it('does not poll when the page has no order id', async () => {
    routeState.query.order_id = undefined
    pollOrderStatus.mockResolvedValue({ status: 'PENDING' })

    const wrapper = mount(PaymentQRCodeView, {
      global: {
        stubs: { AppLayout: { template: '<div><slot /></div>' } },
      },
    })

    await vi.advanceTimersByTimeAsync(6000)

    expect(pollOrderStatus).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('ignores a terminal result that arrives after the page is unmounted', async () => {
    const pending = deferred<{ status: string }>()
    pollOrderStatus.mockReturnValue(pending.promise)

    const wrapper = mount(PaymentQRCodeView, {
      global: {
        stubs: { AppLayout: { template: '<div><slot /></div>' } },
      },
    })

    await flushPromises()
    vi.advanceTimersByTime(3000)
    expect(pollOrderStatus).toHaveBeenCalledTimes(1)

    wrapper.unmount()
    pending.resolve({ status: 'PAID' })
    await flushPromises()
    vi.advanceTimersByTime(9000)

    expect(routerPush).not.toHaveBeenCalled()
    expect(pollOrderStatus).toHaveBeenCalledTimes(1)
  })

  it('shows an expired result and stops polling for a terminal expired order', async () => {
    pollOrderStatus.mockResolvedValue({ status: 'EXPIRED' })

    const wrapper = mount(PaymentQRCodeView, {
      global: {
        stubs: { AppLayout: { template: '<div><slot /></div>' } },
      },
    })

    await vi.advanceTimersByTimeAsync(3000)

    expect(wrapper.text()).toContain('payment.qr.expired')
    await vi.advanceTimersByTimeAsync(9000)
    expect(pollOrderStatus).toHaveBeenCalledTimes(1)
  })

  it('redirects after a completed QR payment and stops polling', async () => {
    pollOrderStatus.mockResolvedValue({ status: 'PAID' })

    const wrapper = mount(PaymentQRCodeView, {
      global: {
        stubs: { AppLayout: { template: '<div><slot /></div>' } },
      },
    })

    await vi.advanceTimersByTimeAsync(3000)

    expect(routerPush).toHaveBeenCalledWith({
      path: '/payment/result',
      query: { order_id: '42', status: 'success' },
    })
    await vi.advanceTimersByTimeAsync(6000)
    expect(pollOrderStatus).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('keeps polling after an empty QR status response', async () => {
    pollOrderStatus.mockResolvedValue(undefined)

    const wrapper = mount(PaymentQRCodeView, {
      global: {
        stubs: { AppLayout: { template: '<div><slot /></div>' } },
      },
    })

    await vi.advanceTimersByTimeAsync(6000)

    expect(pollOrderStatus).toHaveBeenCalledTimes(2)
    expect(routerPush).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('expires at countdown zero and clears the pending poll timer', async () => {
    const now = new Date('2026-04-20T12:00:00.000Z')
    vi.setSystemTime(now)
    routeState.query.expires_at = new Date(now.getTime() + 1000).toISOString()

    const wrapper = mount(PaymentQRCodeView, {
      global: {
        stubs: { AppLayout: { template: '<div><slot /></div>' } },
      },
    })

    await vi.advanceTimersByTimeAsync(1000)

    expect(wrapper.text()).toContain('payment.qr.expired')
    await vi.advanceTimersByTimeAsync(6000)
    expect(pollOrderStatus).not.toHaveBeenCalled()
  })
})
