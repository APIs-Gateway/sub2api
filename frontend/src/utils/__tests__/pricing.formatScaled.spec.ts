import { describe, expect, it } from 'vitest'
import { formatScaled } from '../pricing'

describe('formatScaled', () => {
  it.each([
    [1e-10, 1, '$1e-10'],
    [1.25e-10, 1, '$1.25e-10'],
    [1e-20, 1, '$1e-20'],
    [1e-16, 1_000_000, '$1e-10'],
    [1e10, 1, '$10000000000'],
  ])('preserves magnitude for %s scaled by %s', (value, scale, expected) => {
    expect(formatScaled(value, scale)).toBe(expected)
  })

  it.each([
    [0.000003, 1_000_000, '$3'],
    [0.5, 1, '$0.5'],
    [0.0125, 1, '$0.0125'],
    [0, 1, '$0'],
    [null, 1_000_000, '-'],
  ])('keeps ordinary prices and absence for %s', (value, scale, expected) => {
    expect(formatScaled(value, scale)).toBe(expected)
  })
})
