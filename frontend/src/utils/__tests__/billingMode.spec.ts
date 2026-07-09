import { describe, expect, it } from 'vitest'

import {
  BILLING_MODE_IMAGE,
  BILLING_MODE_PER_REQUEST,
  BILLING_MODE_TOKEN,
  getDisplayBillingMode,
  isImageUsage,
} from '../billingMode'

describe('billingMode helpers', () => {
  it('treats historical image usage rows as image billing for display', () => {
    const row = { billing_mode: null, image_count: 1 }

    expect(getDisplayBillingMode(row)).toBe(BILLING_MODE_IMAGE)
    expect(isImageUsage(row)).toBe(true)
  })

  it('preserves explicit billing modes', () => {
    expect(getDisplayBillingMode({ billing_mode: BILLING_MODE_TOKEN, image_count: 3 })).toBe(BILLING_MODE_TOKEN)
    expect(isImageUsage({ billing_mode: BILLING_MODE_TOKEN, image_count: 3 })).toBe(false)
    expect(getDisplayBillingMode({ billing_mode: BILLING_MODE_PER_REQUEST, image_count: 0 })).toBe(BILLING_MODE_PER_REQUEST)
  })
})
