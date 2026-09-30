import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import PaymentQRDialog from '../PaymentQRDialog.vue'
import PaymentStatusPanel from '../PaymentStatusPanel.vue'
import PaymentQRCodeView from '@/views/user/PaymentQRCodeView.vue'

const { pollOrderStatus } = vi.hoisted(() => ({ pollOrderStatus: vi.fn() }))

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
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError: vi.fn() }) }))
vi.mock('@/api/payment', () => ({ paymentAPI: {} }))
vi.mock('qrcode', () => ({ default: { toCanvas: vi.fn() } }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: { order_id: '42', expires_at: new Date(Date.now() + 120_000).toISOString() } }),
  useRouter: () => ({ push: vi.fn() }),
}))

enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(new Date('2026-09-30T12:00:00Z'))
  pollOrderStatus.mockReset().mockResolvedValue(null)
})
afterEach(() => vi.useRealTimers())

async function open(kind: 'dialog' | 'panel' | 'page') {
  const props = {
    orderId: 42,
    qrCode: '',
    expiresAt: new Date(Date.now() + 120_000).toISOString(),
    paymentType: 'custom',
  }
  const global = { stubs: {
    Icon: true,
    AppLayout: { template: '<div><slot /></div>' },
    BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
  } }
  if (kind === 'page') return mount(PaymentQRCodeView, { global })
  if (kind === 'panel') return mount(PaymentStatusPanel, { props, global })
  const wrapper = mount(PaymentQRDialog, { props: { ...props, show: false }, global })
  await wrapper.setProps({ show: true })
  return wrapper
}

describe.each(['dialog', 'panel', 'page'] as const)('payment countdown: %s', kind => {
  it('catches up on the first timer callback after suspension', async () => {
    const wrapper = await open(kind)
    expect(wrapper.text()).toContain('02:00')

    vi.setSystemTime(Date.now() + 65_000)
    await vi.advanceTimersByTimeAsync(1000)

    expect(wrapper.text()).toContain('00:54')
    expect(wrapper.text()).not.toContain('payment.qr.expired')
  })

  it('expires and stops order polling on the first overdue callback', async () => {
    const wrapper = await open(kind)
    vi.setSystemTime(Date.now() + 121_000)
    await vi.advanceTimersByTimeAsync(1000)

    expect(wrapper.text()).toContain('payment.qr.expired')
    expect(vi.getTimerCount()).toBe(0)
    await vi.advanceTimersByTimeAsync(6000)
    expect(pollOrderStatus).not.toHaveBeenCalled()
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
