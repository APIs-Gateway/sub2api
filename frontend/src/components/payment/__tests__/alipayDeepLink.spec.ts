import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  ALIPAY_DEEP_LINK_FALLBACK_DELAY_MS,
  ALIPAY_EMBEDDED_BROWSER_FALLBACK_DELAY_MS,
  buildAlipayDeepLink,
  createAlipayDeepLinkLauncher,
  isAlipaySchemeRestrictedBrowser,
} from '../alipayDeepLink'

class FakeEventTarget {
  private readonly listeners = new Map<string, Set<EventListener>>()

  addEventListener(type: string, listener: EventListener) {
    const listeners = this.listeners.get(type) ?? new Set<EventListener>()
    listeners.add(listener)
    this.listeners.set(type, listeners)
  }

  removeEventListener(type: string, listener: EventListener) {
    this.listeners.get(type)?.delete(listener)
  }

  dispatch(type: string) {
    for (const listener of this.listeners.get(type) ?? []) {
      listener(new Event(type))
    }
  }
}

class FakeVisibilityDocument extends FakeEventTarget {
  hidden = false
}

describe('Alipay deep link', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('URL-encodes the dynamic qr_code exactly once', () => {
    const qrCode = 'https://qr.alipay.com/bax123?subject=A B&return=https%3A%2F%2Fexample.com%2Fpaid'
    const deepLink = buildAlipayDeepLink(qrCode)

    expect(deepLink).toBe(`alipays://platformapi/startapp?saId=10000007&qrcode=${encodeURIComponent(qrCode)}`)
    expect(decodeURIComponent(deepLink.split('&qrcode=')[1])).toBe(qrCode)
  })

  it('rejects blank QR code payloads and recognises restricted in-app browsers', () => {
    expect(buildAlipayDeepLink(' \n ')).toBe('')
    expect(isAlipaySchemeRestrictedBrowser('Mozilla/5.0 MicroMessenger')).toBe(true)
    expect(isAlipaySchemeRestrictedBrowser('MQQBrowser/13.0')).toBe(true)
    expect(isAlipaySchemeRestrictedBrowser('QQ/8.9')).toBe(true)
    expect(isAlipaySchemeRestrictedBrowser('Mozilla/5.0 Mobile Safari')).toBe(false)
  })

  it('shows fallback after the visible-page timeout', async () => {
    const visibility = new FakeVisibilityDocument()
    const onStateChange = vi.fn()
    const launcher = createAlipayDeepLinkLauncher({
      qrCode: 'https://qr.alipay.com/dynamic-order-1',
      document: visibility,
      lifecycleTarget: new FakeEventTarget(),
      userAgent: 'Mozilla/5.0 Mobile Safari',
      assignLocation: vi.fn(),
      onStateChange,
    })

    launcher.launch()
    await vi.advanceTimersByTimeAsync(ALIPAY_DEEP_LINK_FALLBACK_DELAY_MS)
    expect(onStateChange).toHaveBeenLastCalledWith('fallback')
  })

  it('keeps fallback hidden after a successful pagehide handoff', async () => {
    const visibility = new FakeVisibilityDocument()
    const lifecycle = new FakeEventTarget()
    const onStateChange = vi.fn()
    const launcher = createAlipayDeepLinkLauncher({
      qrCode: 'https://qr.alipay.com/dynamic-order-2',
      document: visibility,
      lifecycleTarget: lifecycle,
      userAgent: 'Mozilla/5.0 Android',
      assignLocation: vi.fn(),
      onStateChange,
    })

    launcher.launch()
    lifecycle.dispatch('pagehide')
    await vi.advanceTimersByTimeAsync(ALIPAY_EMBEDDED_BROWSER_FALLBACK_DELAY_MS)
    expect(onStateChange).toHaveBeenLastCalledWith('backgrounded')
    expect(onStateChange).not.toHaveBeenCalledWith('fallback')
  })

  it('falls back immediately when the QR code is blank or assigning the deep link fails', () => {
    const blankStateChange = vi.fn()
    const blankLauncher = createAlipayDeepLinkLauncher({
      qrCode: '  ',
      document: new FakeVisibilityDocument(),
      lifecycleTarget: new FakeEventTarget(),
      userAgent: 'Mozilla/5.0',
      assignLocation: vi.fn(),
      onStateChange: blankStateChange,
    })

    blankLauncher.launch()
    expect(blankStateChange).toHaveBeenLastCalledWith('fallback')

    const failedStateChange = vi.fn()
    const failedLauncher = createAlipayDeepLinkLauncher({
      qrCode: 'https://qr.alipay.com/dynamic-order-3',
      document: new FakeVisibilityDocument(),
      lifecycleTarget: new FakeEventTarget(),
      userAgent: 'Mozilla/5.0',
      assignLocation: () => { throw new Error('navigation blocked') },
      onStateChange: failedStateChange,
    })

    failedLauncher.launch()
    expect(failedStateChange).toHaveBeenNthCalledWith(1, 'launching')
    expect(failedStateChange).toHaveBeenLastCalledWith('fallback')
  })

  it('marks the handoff as backgrounded when visibility changes and ignores events after disposal', () => {
    const visibility = new FakeVisibilityDocument()
    const lifecycle = new FakeEventTarget()
    const onStateChange = vi.fn()
    const launcher = createAlipayDeepLinkLauncher({
      qrCode: 'https://qr.alipay.com/dynamic-order-4',
      document: visibility,
      lifecycleTarget: lifecycle,
      userAgent: 'Mozilla/5.0',
      assignLocation: vi.fn(),
      onStateChange,
    })

    launcher.launch()
    visibility.hidden = true
    visibility.dispatch('visibilitychange')
    expect(onStateChange).toHaveBeenLastCalledWith('backgrounded')

    launcher.dispose()
    lifecycle.dispatch('pagehide')
    expect(onStateChange).toHaveBeenCalledTimes(2)
  })

  it('uses the short fallback delay in restricted browsers and recognises hidden pages at timeout', async () => {
    const visibility = new FakeVisibilityDocument()
    const onStateChange = vi.fn()
    const launcher = createAlipayDeepLinkLauncher({
      qrCode: 'https://qr.alipay.com/dynamic-order-5',
      document: visibility,
      lifecycleTarget: new FakeEventTarget(),
      userAgent: 'Mozilla/5.0 MicroMessenger',
      assignLocation: vi.fn(),
      onStateChange,
    })

    launcher.launch()
    visibility.hidden = true
    await vi.advanceTimersByTimeAsync(ALIPAY_EMBEDDED_BROWSER_FALLBACK_DELAY_MS)
    expect(onStateChange).toHaveBeenLastCalledWith('backgrounded')
  })
})
