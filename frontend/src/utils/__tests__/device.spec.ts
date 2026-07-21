import { describe, expect, it } from 'vitest'
import { applyIOSViewportZoomFix, detectIOSDevice, detectMobileDevice, isIOSDevice } from '../device'

describe('detectMobileDevice', () => {
  it('prefers userAgentData.mobile when available', () => {
    expect(detectMobileDevice({
      navigator: {
        userAgent: 'Mozilla/5.0',
        userAgentData: { mobile: true },
      },
    })).toBe(true)
  })

  it('recognizes handheld browsers from the mobile UA token', () => {
    expect(detectMobileDevice({
      navigator: {
        userAgent: 'Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/136.0 Mobile Safari/537.36',
        maxTouchPoints: 5,
      },
    })).toBe(true)
  })

  it('recognizes iPadOS desktop mode via touch capability', () => {
    expect(detectMobileDevice({
      navigator: {
        userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15) AppleWebKit/605.1.15 Version/17.0 Safari/605.1.15',
        platform: 'MacIntel',
        maxTouchPoints: 5,
      },
    })).toBe(true)
  })

  it('falls back to input capability detection for touch-first devices', () => {
    expect(detectMobileDevice({
      navigator: {
        userAgent: 'Mozilla/5.0',
        maxTouchPoints: 10,
      },
      matchMedia: (query) => ({
        matches: query === '(pointer: coarse)' || query === '(hover: none)',
      }),
    })).toBe(true)
  })

  it('keeps desktop environments as non-mobile', () => {
    expect(detectMobileDevice({
      navigator: {
        userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/136.0 Safari/537.36',
        platform: 'MacIntel',
        maxTouchPoints: 0,
      },
      matchMedia: () => ({ matches: false }),
    })).toBe(false)
  })
})

describe('detectIOSDevice', () => {
  it('recognizes iPhone from the user agent', () => {
    expect(detectIOSDevice({
      navigator: {
        userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 Version/17.5 Mobile/15E148 Safari/604.1',
        maxTouchPoints: 5,
      },
    })).toBe(true)
  })

  it('recognizes iPadOS desktop mode via touch capability', () => {
    expect(detectIOSDevice({
      navigator: {
        userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15) AppleWebKit/605.1.15 Version/17.0 Safari/605.1.15',
        platform: 'MacIntel',
        maxTouchPoints: 5,
      },
    })).toBe(true)
  })

  it('keeps Android devices as non-iOS', () => {
    expect(detectIOSDevice({
      navigator: {
        userAgent: 'Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/136.0 Mobile Safari/537.36',
        maxTouchPoints: 5,
      },
    })).toBe(false)
  })

  it('keeps desktop macOS without touch as non-iOS', () => {
    expect(detectIOSDevice({
      navigator: {
        userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/136.0 Safari/537.36',
        platform: 'MacIntel',
        maxTouchPoints: 0,
      },
    })).toBe(false)
  })
})

describe('applyIOSViewportZoomFix', () => {
  const createViewport = (content: string | null) => {
    const attributes = { content }
    return {
      getAttribute: (name: string) => (name === 'content' ? attributes.content : null),
      setAttribute: (name: string, value: string) => {
        if (name === 'content') attributes.content = value
      },
      readContent: () => attributes.content,
    }
  }

  it('adds maximum-scale for an iOS viewport without the setting', () => {
    const viewport = createViewport('width=device-width, initial-scale=1.0')
    applyIOSViewportZoomFix({ querySelector: () => viewport }, true)
    expect(viewport.readContent()).toBe('width=device-width, initial-scale=1.0, maximum-scale=1.0')
  })

  it('keeps an existing maximum-scale setting unchanged', () => {
    const viewport = createViewport('width=device-width, maximum-scale=2.0')
    applyIOSViewportZoomFix({ querySelector: () => viewport }, true)
    expect(viewport.readContent()).toBe('width=device-width, maximum-scale=2.0')
  })

  it('does nothing for non-iOS devices or a missing viewport', () => {
    const viewport = createViewport('width=device-width')
    applyIOSViewportZoomFix({ querySelector: () => viewport }, false)
    expect(viewport.readContent()).toBe('width=device-width')
    applyIOSViewportZoomFix({ querySelector: () => null }, true)
  })

  it('uses runtime device detection when the iOS flag is omitted', () => {
    const viewport = createViewport('width=device-width')
    applyIOSViewportZoomFix({ querySelector: () => viewport })
    expect(viewport.readContent()).toBe('width=device-width')
  })
})

it('reports the test runtime as non-iOS', () => {
  expect(isIOSDevice()).toBe(false)
})
