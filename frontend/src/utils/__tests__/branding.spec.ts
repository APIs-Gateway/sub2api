import { beforeEach, describe, expect, it } from 'vitest'
import { updateFavicon } from '@/utils/branding'

describe('updateFavicon', () => {
  beforeEach(() => {
    document.head.innerHTML = '<link rel="icon" href="/logo.png">'
  })

  it('replaces the default favicon with the configured logo', () => {
    updateFavicon('https://example.com/custom-logo.png')

    const link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
    expect(link?.href).toBe('https://example.com/custom-logo.png')
  })

  it('ignores unsafe logo URLs', () => {
    updateFavicon('javascript:alert(1)')

    const link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
    expect(link?.getAttribute('href')).toBe('/logo.png')
  })

  it('creates an SVG favicon from a safe relative URL', () => {
    document.head.innerHTML = ''

    updateFavicon('/custom-logo.svg')

    const link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
    expect(link?.getAttribute('href')).toBe('/custom-logo.svg')
    expect(link?.type).toBe('image/svg+xml')
  })

  it('accepts image data URLs for configured branding', () => {
    const dataUrl = 'data:image/png;base64,AAAA'

    updateFavicon(dataUrl)

    const link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
    expect(link?.getAttribute('href')).toBe(dataUrl)
    expect(link?.type).toBe('image/x-icon')
  })
})
