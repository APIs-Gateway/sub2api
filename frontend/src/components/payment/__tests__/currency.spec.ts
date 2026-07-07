import { describe, expect, it } from 'vitest'
import { formatPaymentAmount, paymentCurrencySymbol } from '../currency'

describe('formatPaymentAmount', () => {
  it('uses the currency default fraction digits', () => {
    expect(formatPaymentAmount(100, 'JPY', 'en-US')).not.toContain('.00')
    expect(formatPaymentAmount(100, 'KRW', 'en-US')).not.toContain('.00')
    expect(formatPaymentAmount(100, 'HKD', 'en-US')).toContain('.00')
  })
})

describe('paymentCurrencySymbol', () => {
  it('maps common payment currencies and falls back safely', () => {
    expect(paymentCurrencySymbol('USD', 'en-US')).toBe('$')
    expect(paymentCurrencySymbol('cny', 'zh-CN')).toBe('¥')
    expect(paymentCurrencySymbol('EUR', 'en-US')).toBe('€')
    expect(paymentCurrencySymbol('', 'zh-CN')).toBe('¥')
    expect(paymentCurrencySymbol('XYZ', 'en-US')).toBe('XYZ')
  })
})
