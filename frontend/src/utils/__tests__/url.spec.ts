import { describe, expect, it } from 'vitest'

import { sanitizeUrl } from '@/utils/url'

describe('sanitizeUrl', () => {
  it('keeps http and https URLs', () => {
    expect(sanitizeUrl('https://docs.example.com/path')).toBe('https://docs.example.com/path')
    expect(sanitizeUrl('http://docs.example.com/path')).toBe('http://docs.example.com/path')
  })

  it('rejects script-like and relative URLs by default', () => {
    expect(sanitizeUrl('javascript:alert(1)')).toBe('')
    expect(sanitizeUrl('data:text/html,<script>alert(1)</script>')).toBe('')
    expect(sanitizeUrl('/docs')).toBe('')
  })

  it('allows explicit relative and image data URL modes', () => {
    expect(sanitizeUrl('/docs', { allowRelative: true })).toBe('/docs')
    expect(sanitizeUrl('data:image/png;base64,abc', { allowDataUrl: true })).toBe('data:image/png;base64,abc')
  })
})
