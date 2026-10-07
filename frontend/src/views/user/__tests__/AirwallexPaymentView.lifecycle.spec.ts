import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, shallowMount } from '@vue/test-utils'
import AirwallexPaymentView from '../AirwallexPaymentView.vue'
import { PAYMENT_RECOVERY_STORAGE_KEY } from '@/components/payment/paymentFlow'

const mocks = vi.hoisted(() => ({ init: vi.fn(), redirectToCheckout: vi.fn() }))
vi.mock('@airwallex/components-sdk', () => ({ init: mocks.init }))
vi.mock('vue-router', () => ({ useRoute: () => ({ query: {} }), useRouter: () => ({ push: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key, locale: { value: 'zh-CN' } })
}))
enableAutoUnmount(afterEach)
afterEach(() => window.localStorage.clear())

beforeEach(() => {
  vi.resetAllMocks()
  window.localStorage.setItem(PAYMENT_RECOVERY_STORAGE_KEY, JSON.stringify({
    orderId: 101, amount: 88, qrCode: '', expiresAt: '2099-01-01T00:10:00.000Z',
    paymentType: 'airwallex', payUrl: '/payment/airwallex', outTradeNo: 'sub2_awx_101',
    clientSecret: 'awx_client_secret', intentId: 'int_awx_101', currency: 'CNY',
    countryCode: 'CN', paymentEnv: 'demo', payAmount: 88, orderType: 'balance',
    paymentMode: '', resumeToken: 'resume-awx', createdAt: Date.UTC(2099, 0, 1)
  }))
})

function mountView() {
  return shallowMount(AirwallexPaymentView, {
    global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, Icon: true } }
  })
}

describe('Airwallex checkout lifecycle', () => {
  it('does not initialize checkout after leaving during the SDK import', async () => {
    mocks.init.mockResolvedValue({ payments: { redirectToCheckout: mocks.redirectToCheckout } })
    const wrapper = mountView()
    wrapper.unmount()
    await flushPromises()
    expect(mocks.init).not.toHaveBeenCalled()
    expect(mocks.redirectToCheckout).not.toHaveBeenCalled()
  })

  it.each([true, false])('redirects after SDK initialization only while mounted (leave=%s)', async (leave) => {
    let finish!: (result: unknown) => void
    mocks.init.mockReturnValue(new Promise(resolve => { finish = resolve }))
    const wrapper = mountView()
    await flushPromises()
    expect(mocks.init).toHaveBeenCalledExactlyOnceWith({
      env: 'demo', enabledElements: ['payments'], locale: 'zh'
    })
    if (leave) wrapper.unmount()
    finish({ payments: { redirectToCheckout: mocks.redirectToCheckout } })
    await flushPromises()
    expect(mocks.redirectToCheckout).toHaveBeenCalledTimes(leave ? 0 : 1)
    if (!leave) {
      expect(mocks.redirectToCheckout).toHaveBeenCalledWith(expect.objectContaining({
        intent_id: 'int_awx_101', client_secret: 'awx_client_secret',
        currency: 'CNY', country_code: 'CN'
      }))
      const url = new URL(mocks.redirectToCheckout.mock.calls[0][0].successUrl)
      expect(url.searchParams.get('order_id')).toBe('101')
      expect(url.searchParams.get('out_trade_no')).toBe('sub2_awx_101')
      expect(url.searchParams.get('resume_token')).toBe('resume-awx')
    }
  })

  it.each([true, false])('handles initialization rejection without checkout (leave=%s)', async (leave) => {
    let reject!: (error: Error) => void
    mocks.init.mockReturnValue(new Promise((_resolve, fail) => { reject = fail }))
    const wrapper = mountView()
    await flushPromises()
    expect(mocks.init).toHaveBeenCalledOnce()
    if (leave) wrapper.unmount()
    reject(new Error('SDK initialization failed'))
    await flushPromises()
    expect(mocks.redirectToCheckout).not.toHaveBeenCalled()
    if (!leave) expect(wrapper.text()).toContain('SDK initialization failed')
  })

  it('shows the existing failure when the active SDK has no payments element', async () => {
    mocks.init.mockResolvedValue({})
    const wrapper = mountView()
    await flushPromises()
    expect(mocks.redirectToCheckout).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('payment.airwallexLoadFailed')
  })

  it('lets a fresh mount checkout once while ignoring the older initialization', async () => {
    let finishOld!: (result: unknown) => void
    const payments = { redirectToCheckout: mocks.redirectToCheckout }
    mocks.init.mockReturnValueOnce(new Promise(resolve => { finishOld = resolve }))
      .mockResolvedValueOnce({ payments })
    const old = mountView()
    await flushPromises()
    expect(mocks.init).toHaveBeenCalledOnce()
    old.unmount()
    mountView()
    await flushPromises()
    expect(mocks.init).toHaveBeenCalledTimes(2)
    expect(mocks.redirectToCheckout).toHaveBeenCalledOnce()
    finishOld({ payments })
    await flushPromises()
    expect(mocks.redirectToCheckout).toHaveBeenCalledOnce()
  })
})
