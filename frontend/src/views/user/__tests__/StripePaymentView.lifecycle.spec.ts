import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, shallowMount } from '@vue/test-utils'

const routeState = vi.hoisted(() => ({
  query: {} as Record<string, unknown>,
}))
const routerPush = vi.hoisted(() => vi.fn())
const getOrder = vi.hoisted(() => vi.fn())
const paymentStore = vi.hoisted(() => ({
  config: { stripe_publishable_key: 'pk_test' } as { stripe_publishable_key?: string },
  fetchConfig: vi.fn(),
  pollOrderStatus: vi.fn(),
}))
const loadStripe = vi.hoisted(() => vi.fn())
const stripeElements = vi.hoisted(() => ({
  create: vi.fn(),
}))
const stripePaymentElement = vi.hoisted(() => ({
  mount: vi.fn(),
  on: vi.fn(),
}))
const stripeInstance = vi.hoisted(() => ({
  elements: vi.fn(),
  confirmPayment: vi.fn(),
  confirmAlipayPayment: vi.fn(),
  confirmWechatPayPayment: vi.fn(),
}))

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
    useI18n: () => ({
      t: (key: string) => key,
      locale: { value: 'zh-CN' },
    }),
  }
})

vi.mock('@/stores/payment', () => ({
  usePaymentStore: () => paymentStore,
}))

vi.mock('@/api/payment', () => ({
  paymentAPI: {
    getOrder,
  },
}))

vi.mock('@stripe/stripe-js/pure', () => ({
  loadStripe,
}))

import StripePaymentView from '../StripePaymentView.vue'
import { formatPaymentAmount } from '@/components/payment/currency'
import type { PaymentOrder } from '@/types/payment'

function orderFactory(overrides: Partial<PaymentOrder> = {}): PaymentOrder {
  return {
    id: 42,
    user_id: 7,
    amount: 100,
    pay_amount: 103,
    currency: 'CNY',
    fee_rate: 0.03,
    payment_type: 'stripe',
    out_trade_no: 'sub2_stripe_42',
    status: 'PENDING',
    order_type: 'balance',
    created_at: '2026-04-20T12:00:00Z',
    expires_at: '2026-04-20T12:30:00Z',
    refund_amount: 0,
    ...overrides,
  }
}

function mountView() {
  return shallowMount(StripePaymentView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        Icon: true,
      },
    },
  })
}


enableAutoUnmount(afterEach)
afterEach(() => { vi.clearAllTimers(); vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks() })

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
function state(wrapper: ReturnType<typeof mountView>) {
  return wrapper.vm as unknown as Record<string, unknown>
}
async function settle() { await flushPromises(); await flushPromises() }
const qr = { paymentIntent: { status: 'requires_action', next_action: {
  wechat_pay_display_qr_code: { image_data_url: 'data:image/png;base64,fixture' }
} } }

describe('Stripe page owns asynchronous callbacks only while mounted', () => {
  beforeEach(() => {
    vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: false })))
    routeState.query = {
      order_id: '42',
      client_secret: 'pi_secret_42',
    }
    routerPush.mockReset()
    getOrder.mockReset().mockResolvedValue({ data: orderFactory() })
    paymentStore.config = { stripe_publishable_key: 'pk_test' }
    paymentStore.fetchConfig.mockReset().mockResolvedValue(undefined)
    paymentStore.pollOrderStatus.mockReset()
    loadStripe.mockReset().mockResolvedValue(stripeInstance)
    stripeElements.create.mockReset().mockReturnValue(stripePaymentElement)
    stripePaymentElement.mount.mockReset()
    stripePaymentElement.on.mockReset().mockImplementation((event: string, callback: () => void) => {
      if (event === 'ready') callback()
    })
    stripeInstance.elements.mockReset().mockReturnValue(stripeElements)
    stripeInstance.confirmPayment.mockReset()
    stripeInstance.confirmAlipayPayment.mockReset()
    stripeInstance.confirmWechatPayPayment.mockReset()
    window.localStorage.clear()
  })


  it.each(['alipay', 'wechat_pay', ''])('does not initiate %s payment after unmount during SDK loading', async method => {
    routeState.query.method = method
    const pending = deferred<typeof stripeInstance>()
    loadStripe.mockReturnValueOnce(pending.promise)
    const wrapper = mountView()
    await settle()
    expect(loadStripe).toHaveBeenCalledTimes(1)
    wrapper.unmount()
    pending.resolve(stripeInstance)
    await settle()
    expect(stripeInstance.confirmAlipayPayment).not.toHaveBeenCalled()
    expect(stripeInstance.confirmWechatPayPayment).not.toHaveBeenCalled()
    expect(stripeInstance.elements).not.toHaveBeenCalled()
  })

  it.each(['order', 'config'])('does not load SDK when late %s returns after unmount', async stage => {
    const pending = deferred<unknown>()
    if (stage === 'order') getOrder.mockReturnValueOnce(pending.promise)
    else paymentStore.fetchConfig.mockReturnValueOnce(pending.promise)
    const wrapper = mountView()
    await settle()
    expect(stage === 'order' ? getOrder : paymentStore.fetchConfig).toHaveBeenCalledTimes(1)
    const view = state(wrapper)
    const previousOrder = view.order
    wrapper.unmount()
    pending.resolve(stage === 'order' ? { data: orderFactory() } : undefined)
    await settle()
    expect(loadStripe).not.toHaveBeenCalled()
    expect(view.loading).toBe(true)
    expect(view.order).toBe(previousOrder)
    if (stage === 'order') expect(paymentStore.fetchConfig).not.toHaveBeenCalled()
  })

  it.each(['order', 'config', 'sdk'])('does not apply late %s rejection or finally to disposed page', async stage => {
    const pending = deferred<never>()
    if (stage === 'order') getOrder.mockReturnValueOnce(pending.promise)
    else if (stage === 'config') paymentStore.fetchConfig.mockReturnValueOnce(pending.promise)
    else loadStripe.mockReturnValueOnce(pending.promise)
    const wrapper = mountView()
    await settle()
    expect(stage === 'order' ? getOrder : stage === 'config' ? paymentStore.fetchConfig : loadStripe).toHaveBeenCalledTimes(1)
    const view = state(wrapper)
    wrapper.unmount()
    pending.reject(new Error('late fixture failure'))
    await settle()
    expect(view.initError).toBe('')
    expect(view.loading).toBe(true)
  })

  it.each(['qr', 'succeeded', 'error', 'rejection'])('does not apply late WeChat %s or restart timers', async outcome => {
    vi.useFakeTimers()
    routeState.query.method = 'wechat_pay'
    const pending = deferred<unknown>()
    stripeInstance.confirmWechatPayPayment.mockReturnValueOnce(pending.promise)
    const wrapper = mountView()
    await settle()
    expect(stripeInstance.confirmWechatPayPayment).toHaveBeenCalledTimes(1)
    const view = state(wrapper)
    wrapper.unmount()
    if (outcome === 'rejection') pending.reject(new Error('late QR failure'))
    else pending.resolve(outcome === 'qr' ? qr : outcome === 'error' ? { error: { message: 'late decline' } } : { paymentIntent: { status: 'succeeded' } })
    await settle()
    await vi.advanceTimersByTimeAsync(5000)
    expect(view.wechatQrUrl).toBe('')
    expect(view.stripeSuccess).toBe(false)
    expect(view.stripeError).toBe('')
    expect(view.initError).toBe('')
    expect(paymentStore.pollOrderStatus).not.toHaveBeenCalled()
    expect(routerPush).not.toHaveBeenCalled()
    expect(vi.getTimerCount()).toBe(0)
  })

  it.each(['error', 'rejection'])('does not apply late Alipay %s', async outcome => {
    routeState.query.method = 'alipay'
    const pending = deferred<unknown>()
    stripeInstance.confirmAlipayPayment.mockReturnValueOnce(pending.promise)
    const wrapper = mountView()
    await settle()
    expect(stripeInstance.confirmAlipayPayment).toHaveBeenCalledWith('pi_secret_42', { return_url: window.location.origin + '/payment/result?order_id=42&status=success' })
    const view = state(wrapper)
    wrapper.unmount()
    if (outcome === 'error') pending.resolve({ error: { message: 'late decline' } })
    else pending.reject(new Error('late rejection'))
    await settle()
    expect(view.stripeError).toBe('')
    expect(view.initError).toBe('')
    expect(view.redirecting).toBe(true)
  })

  it('ignores Payment Element ready callback after unmount', async () => {
    let ready!: () => void
    stripePaymentElement.on.mockImplementation((event: string, callback: () => void) => { if (event === 'ready') ready = callback })
    const wrapper = mountView()
    await settle()
    expect(stripePaymentElement.mount).toHaveBeenCalledWith('#stripe-payment-element')
    const view = state(wrapper)
    expect(view.stripeReady).toBe(false)
    wrapper.unmount()
    ready()
    expect(view.stripeReady).toBe(false)
  })

  it.each(['success', 'error', 'rejection'])('does not apply generic payment %s after unmount', async outcome => {
    vi.useFakeTimers()
    const pending = deferred<unknown>()
    stripeInstance.confirmPayment.mockReturnValueOnce(pending.promise)
    const wrapper = mountView()
    await settle()
    const pay = wrapper.findAll('button').find(button => button.text() === 'payment.stripePay')!
    expect(pay.exists()).toBe(true)
    await pay.trigger('click')
    expect(stripeInstance.confirmPayment).toHaveBeenCalledTimes(1)
    const view = state(wrapper)
    expect(view.stripeSubmitting).toBe(true)
    wrapper.unmount()
    if (outcome === 'rejection') pending.reject(new Error('late payment failure'))
    else pending.resolve(outcome === 'success' ? {} : { error: { message: 'late decline' } })
    await settle()
    await vi.advanceTimersByTimeAsync(2000)
    expect(view.stripeSubmitting).toBe(true)
    expect(view.stripeError).toBe('')
    expect(view.stripeSuccess).toBe(false)
    expect(routerPush).not.toHaveBeenCalled()
    expect(vi.getTimerCount()).toBe(0)
  })

  it.each([false, true])('handles poll completion when unmounted=%s', async unmounted => {
    vi.useFakeTimers()
    routeState.query.method = 'wechat_pay'
    stripeInstance.confirmWechatPayPayment.mockResolvedValue(qr)
    const pending = deferred<PaymentOrder>()
    paymentStore.pollOrderStatus.mockReturnValueOnce(pending.promise)
    const wrapper = mountView()
    await settle()
    await vi.advanceTimersByTimeAsync(3000)
    expect(paymentStore.pollOrderStatus).toHaveBeenCalledWith(42)
    const view = state(wrapper)
    if (unmounted) wrapper.unmount()
    pending.resolve(orderFactory({ status: 'PAID' }))
    await settle()
    await vi.advanceTimersByTimeAsync(2000)
    expect(view.stripeSuccess).toBe(!unmounted)
    expect(routerPush).toHaveBeenCalledTimes(unmounted ? 0 : 1)
    if (!unmounted) expect(routerPush).toHaveBeenCalledWith({ path: '/payment/result', query: { order_id: '42', status: 'success' } })
    expect(vi.getTimerCount()).toBe(0)
  })

  it('clears already scheduled popup close on unmount', async () => {
    vi.useFakeTimers()
    const close = vi.spyOn(window, 'close').mockImplementation(() => {})
    vi.stubGlobal('opener', {})
    routeState.query.method = 'wechat_pay'
    stripeInstance.confirmWechatPayPayment.mockResolvedValue({ paymentIntent: { status: 'succeeded' } })
    const wrapper = mountView()
    await settle()
    expect(vi.getTimerCount()).toBe(1)
    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(2000)
    expect(close).not.toHaveBeenCalled()
    expect(vi.getTimerCount()).toBe(0)
    close.mockRestore()
    vi.unstubAllGlobals()
  })

  it('preserves mounted generic success, exact confirmation options and amount/currency', async () => {
    vi.useFakeTimers()
    getOrder.mockResolvedValue({ data: orderFactory({ currency: 'HKD', pay_amount: 103 }) })
    stripeInstance.confirmPayment.mockResolvedValue({})
    const wrapper = mountView()
    await settle()
    expect(wrapper.text()).toContain(formatPaymentAmount(103, 'HKD', 'zh-CN'))
    await wrapper.findAll('button').find(button => button.text() === 'payment.stripePay')!.trigger('click')
    await settle()
    expect(stripeInstance.confirmPayment).toHaveBeenCalledWith({ elements: stripeElements, confirmParams: {
      return_url: window.location.origin + '/payment/result?order_id=42&status=success'
    }, redirect: 'if_required' })
    expect(state(wrapper).stripeSuccess).toBe(true)
    expect(state(wrapper).stripeSubmitting).toBe(false)
    await vi.advanceTimersByTimeAsync(2000)
    expect(routerPush).toHaveBeenCalledTimes(1)
  })

  it('does not let an old QR result affect a new mounted page', async () => {
    vi.useFakeTimers()
    routeState.query.method = 'wechat_pay'
    const pending = deferred<unknown>()
    stripeInstance.confirmWechatPayPayment.mockReturnValueOnce(pending.promise)
    const old = mountView()
    await settle()
    expect(stripeInstance.confirmWechatPayPayment).toHaveBeenCalledTimes(1)
    old.unmount()
    routeState.query = { order_id: '43', client_secret: 'pi_secret_43' }
    const current = mountView()
    await settle()
    expect(state(current).showPaymentElement).toBe(true)
    pending.resolve(qr)
    await settle()
    await vi.advanceTimersByTimeAsync(3000)
    expect(paymentStore.pollOrderStatus).not.toHaveBeenCalled()
    expect(state(current).stripeSuccess).toBe(false)
    expect(state(current).stripeError).toBe('')
    expect(vi.getTimerCount()).toBe(0)
  })

  it.each(['order', 'config', 'sdk'])('preserves mounted %s errors and ends initial loading', async stage => {
    const failure = new Error('mounted fixture failure')
    if (stage === 'order') getOrder.mockRejectedValueOnce(failure)
    else if (stage === 'config') paymentStore.fetchConfig.mockRejectedValueOnce(failure)
    else loadStripe.mockRejectedValueOnce(failure)
    const wrapper = mountView()
    await settle()
    expect(state(wrapper).initError).toBe('mounted fixture failure')
    expect(state(wrapper).loading).toBe(false)
    expect(wrapper.text()).toContain('mounted fixture failure')
    expect(stripeInstance.confirmPayment).not.toHaveBeenCalled()
  })

  it.each(['success', 'error', 'rejection'])('preserves mounted Alipay %s behavior', async outcome => {
    routeState.query.method = 'alipay'
    if (outcome === 'rejection') stripeInstance.confirmAlipayPayment.mockRejectedValueOnce(new Error('mounted Alipay failure'))
    else stripeInstance.confirmAlipayPayment.mockResolvedValueOnce(outcome === 'success' ? {} : { error: { message: 'mounted decline' } })
    const wrapper = mountView()
    await settle()
    expect(stripeInstance.confirmAlipayPayment).toHaveBeenCalledWith('pi_secret_42', { return_url: window.location.origin + '/payment/result?order_id=42&status=success' })
    expect(state(wrapper).loading).toBe(false)
    expect(state(wrapper).redirecting).toBe(outcome !== 'error')
    expect(state(wrapper).stripeError).toBe(outcome === 'error' ? 'mounted decline' : '')
    expect(state(wrapper).initError).toBe(outcome === 'rejection' ? 'mounted Alipay failure' : '')
  })

  it.each(['succeeded', 'error', 'unknown'])('preserves mounted WeChat %s behavior and exact request options', async outcome => {
    vi.useFakeTimers()
    routeState.query.method = 'wechat_pay'
    stripeInstance.confirmWechatPayPayment.mockResolvedValueOnce(outcome === 'error' ? { error: { message: 'mounted WeChat decline' } } : { paymentIntent: { status: outcome } })
    const wrapper = mountView()
    await settle()
    expect(stripeInstance.confirmWechatPayPayment).toHaveBeenCalledWith('pi_secret_42', { payment_method_options: { wechat_pay: { client: 'web' } } })
    expect(state(wrapper).stripeSuccess).toBe(outcome === 'succeeded')
    expect(state(wrapper).stripeError).toBe(outcome === 'error' ? 'mounted WeChat decline' : outcome === 'unknown' ? 'payment.result.failed' : '')
    await vi.advanceTimersByTimeAsync(2000)
    expect(routerPush).toHaveBeenCalledTimes(outcome === 'succeeded' ? 1 : 0)
  })

  it.each(['error', 'rejection'])('preserves mounted generic %s and releases submitting state', async outcome => {
    if (outcome === 'rejection') stripeInstance.confirmPayment.mockRejectedValueOnce(new Error('mounted generic failure'))
    else stripeInstance.confirmPayment.mockResolvedValueOnce({ error: { message: 'mounted generic decline' } })
    const wrapper = mountView()
    await settle()
    await wrapper.findAll('button').find(button => button.text() === 'payment.stripePay')!.trigger('click')
    await settle()
    expect(stripeInstance.confirmPayment).toHaveBeenCalledTimes(1)
    expect(state(wrapper).stripeError).toBe(outcome === 'error' ? 'mounted generic decline' : 'mounted generic failure')
    expect(state(wrapper).stripeSubmitting).toBe(false)
    expect(state(wrapper).stripeSuccess).toBe(false)
  })

  it('does not mount an Element when unmounted during the real DOM nextTick', async () => {
    const pending = deferred<typeof stripeInstance>()
    loadStripe.mockReturnValueOnce(pending.promise)
    const wrapper = mountView()
    await settle()
    expect(loadStripe).toHaveBeenCalledTimes(1)
    pending.resolve(stripeInstance)
    queueMicrotask(() => wrapper.unmount())
    await settle()
    expect(stripeInstance.elements).not.toHaveBeenCalled()
    expect(stripePaymentElement.mount).not.toHaveBeenCalled()
  })

  it('does not load SDK when disposed during the actual dynamic-import await', async () => {
    const pending = deferred<void>()
    paymentStore.fetchConfig.mockReturnValueOnce(pending.promise)
    const wrapper = mountView()
    await settle()
    expect(paymentStore.fetchConfig).toHaveBeenCalledTimes(1)
    pending.resolve()
    queueMicrotask(() => wrapper.unmount())
    await settle()
    expect(loadStripe).not.toHaveBeenCalled()
  })

  it('a retained real payment button cannot submit after page disposal', async () => {
    stripeInstance.confirmPayment.mockResolvedValueOnce({})
    const wrapper = mountView()
    await settle()
    const button = wrapper.findAll('button').find(element => element.text() === 'payment.stripePay')!.element as HTMLButtonElement
    expect(button.disabled).toBe(false)
    wrapper.unmount()
    button.click()
    await settle()
    expect(stripeInstance.confirmPayment).not.toHaveBeenCalled()
  })

  it.each([false, true])('suppresses controlled late completion callback delivery after unmount (popup=%s)', async popup => {
    vi.useFakeTimers()
    const scheduled = vi.spyOn(globalThis, 'setTimeout')
    const close = vi.spyOn(window, 'close').mockImplementation(() => {})
    if (popup) vi.stubGlobal('opener', {})
    routeState.query.method = 'wechat_pay'
    stripeInstance.confirmWechatPayPayment.mockResolvedValueOnce({ paymentIntent: { status: 'succeeded' } })
    const wrapper = mountView()
    await settle()
    const completion = scheduled.mock.calls.find(([, delay]) => delay === 2000)?.[0]
    expect(completion).toBeTypeOf('function')
    expect(state(wrapper).stripeSuccess).toBe(true)
    expect(vi.getTimerCount()).toBe(1)
    wrapper.unmount()
    expect(vi.getTimerCount()).toBe(0)
    // Deliberately deliver the captured callback; do not infer browser clearTimeout behavior.
    ;(completion as () => void)()
    expect(close).not.toHaveBeenCalled()
    expect(routerPush).not.toHaveBeenCalled()
  })

  it('preserves the mounted popup completion delay and closes it once', async () => {
    vi.useFakeTimers()
    const close = vi.spyOn(window, 'close').mockImplementation(() => {})
    vi.stubGlobal('opener', {})
    routeState.query.method = 'wechat_pay'
    stripeInstance.confirmWechatPayPayment.mockResolvedValueOnce({ paymentIntent: { status: 'succeeded' } })
    const wrapper = mountView()
    await settle()
    expect(state(wrapper).stripeSuccess).toBe(true)
    expect(close).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(1999)
    expect(close).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(1)
    expect(close).toHaveBeenCalledTimes(1)
    expect(routerPush).not.toHaveBeenCalled()
    expect(vi.getTimerCount()).toBe(0)
  })

})
