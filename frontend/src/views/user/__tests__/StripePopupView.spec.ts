import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const routeState = vi.hoisted(() => ({
  query: {} as Record<string, unknown>,
}))
const getAPIBaseURL = vi.hoisted(() => vi.fn())
const loadStripe = vi.hoisted(() => vi.fn())
const confirmWechatPayPayment = vi.hoisted(() => vi.fn())

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
  }
})

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

vi.mock('@/api/client', () => ({ getAPIBaseURL }))

vi.mock('@/utils/device', () => ({ isMobileDevice: () => false }))

vi.mock('@stripe/stripe-js/pure', () => ({ loadStripe }))

import StripePopupView from '../StripePopupView.vue'

async function initializeWechatPolling() {
  const wrapper = mount(StripePopupView)
  window.dispatchEvent(new MessageEvent('message', {
    origin: window.location.origin,
    data: {
      type: 'STRIPE_POPUP_INIT',
      clientSecret: 'pi_secret_42',
      publishableKey: 'pk_test_42',
    },
  }))
  await flushPromises()
  return wrapper
}

describe('StripePopupView', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    routeState.query = {
      order_id: '42',
      method: 'wechat_pay',
      amount: '88',
    }
    getAPIBaseURL.mockReset().mockReturnValue('https://gateway.example.test/api/v1')
    confirmWechatPayPayment.mockReset().mockResolvedValue({
      paymentIntent: { status: 'processing' },
    })
    loadStripe.mockReset().mockResolvedValue({ confirmWechatPayPayment })
    window.localStorage.clear()
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('uses the configured API base and auth_token without overlapping popup polls', async () => {
    const pendingFetch = deferred<Response>()
    const fetchMock = vi.fn().mockReturnValue(pendingFetch.promise)
    vi.stubGlobal('fetch', fetchMock)
    window.localStorage.setItem('auth_token', 'popup-access-token')

    const wrapper = await initializeWechatPolling()
    vi.advanceTimersByTime(9000)

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock).toHaveBeenCalledWith(
      'https://gateway.example.test/api/v1/payment/orders/42',
      {
        headers: { Authorization: 'Bearer popup-access-token' },
        credentials: 'include',
      },
    )

    pendingFetch.resolve({ ok: true, json: vi.fn().mockResolvedValue({ data: { status: 'PENDING' } }) } as unknown as Response)
    await flushPromises()
    wrapper.unmount()
  })

  it('cleans up popup polling and its init listener on unmount', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: vi.fn() })
    vi.stubGlobal('fetch', fetchMock)

    const wrapper = await initializeWechatPolling()
    wrapper.unmount()
    vi.advanceTimersByTime(9000)
    window.dispatchEvent(new MessageEvent('message', {
      origin: window.location.origin,
      data: {
        type: 'STRIPE_POPUP_INIT',
        clientSecret: 'pi_second',
        publishableKey: 'pk_second',
      },
    }))
    await flushPromises()

    expect(fetchMock).not.toHaveBeenCalled()
    expect(loadStripe).toHaveBeenCalledTimes(1)
  })

  it('ignores a completed polling response that arrives after unmount', async () => {
    const pendingBody = deferred<{ data: { status: string } }>()
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: vi.fn().mockReturnValue(pendingBody.promise),
    })
    const closeWindow = vi.spyOn(window, 'close').mockImplementation(() => undefined)
    vi.stubGlobal('fetch', fetchMock)

    const wrapper = await initializeWechatPolling()
    vi.advanceTimersByTime(3000)
    await flushPromises()
    wrapper.unmount()

    pendingBody.resolve({ data: { status: 'COMPLETED' } })
    await flushPromises()
    vi.advanceTimersByTime(2000)

    expect(closeWindow).not.toHaveBeenCalled()
  })

  it('shows an initialization timeout when the opener never sends Stripe details', () => {
    const wrapper = mount(StripePopupView)

    vi.advanceTimersByTime(15000)

    expect(wrapper.text()).toContain('payment.stripePopup.timeout')
    wrapper.unmount()
  })

  it('ignores unrelated messages before accepting the popup initialization message', async () => {
    const wrapper = mount(StripePopupView)
    window.dispatchEvent(new MessageEvent('message', {
      origin: 'https://untrusted.example.test',
      data: { type: 'STRIPE_POPUP_INIT', clientSecret: 'ignored', publishableKey: 'ignored' },
    }))
    window.dispatchEvent(new MessageEvent('message', {
      origin: window.location.origin,
      data: { type: 'UNRELATED_EVENT' },
    }))
    await flushPromises()

    expect(loadStripe).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('notifies its opener that the popup is ready', () => {
    const postMessage = vi.fn()
    Object.defineProperty(window, 'opener', { configurable: true, value: { postMessage } })

    const wrapper = mount(StripePopupView)

    expect(postMessage).toHaveBeenCalledWith({ type: 'STRIPE_POPUP_READY' }, window.location.origin)
    wrapper.unmount()
    Object.defineProperty(window, 'opener', { configurable: true, value: null })
  })

  it('keeps polling after a non-OK payment status response', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: false })
    vi.stubGlobal('fetch', fetchMock)

    const wrapper = await initializeWechatPolling()
    vi.advanceTimersByTime(3000)
    await flushPromises()
    vi.advanceTimersByTime(3000)
    await flushPromises()

    expect(fetchMock).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('keeps polling after a payment status request fails', async () => {
    const fetchMock = vi.fn().mockRejectedValue(new Error('network unavailable'))
    vi.stubGlobal('fetch', fetchMock)

    const wrapper = await initializeWechatPolling()
    vi.advanceTimersByTime(3000)
    await flushPromises()
    vi.advanceTimersByTime(3000)
    await flushPromises()

    expect(fetchMock).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('stops polling and closes after a completed payment status', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      json: vi.fn().mockResolvedValue({ data: { status: 'COMPLETED' } }),
    })
    const closeWindow = vi.spyOn(window, 'close').mockImplementation(() => undefined)
    vi.stubGlobal('fetch', fetchMock)

    const wrapper = await initializeWechatPolling()
    vi.advanceTimersByTime(3000)
    await flushPromises()

    expect(wrapper.text()).toContain('payment.result.success')
    vi.advanceTimersByTime(3000)
    await flushPromises()
    expect(fetchMock).toHaveBeenCalledTimes(1)

    vi.advanceTimersByTime(1999)
    expect(closeWindow).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)
    expect(closeWindow).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })
})
