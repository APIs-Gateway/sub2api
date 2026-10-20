import { describe, expect, it } from 'vitest'
import { formatPaymentAmount } from '../currency'

describe('formatPaymentAmount', () => {
  it('uses the currency default fraction digits', () => {
    expect(formatPaymentAmount(100, 'JPY', 'en-US')).not.toContain('.00')
    expect(formatPaymentAmount(100, 'KRW', 'en-US')).not.toContain('.00')
    expect(formatPaymentAmount(100, 'HKD', 'en-US')).toContain('.00')
  })

  it('writes USD with the $ symbol in every locale, never a USD code prefix', () => {
    for (const locale of ['zh-CN', 'zh-HK', 'en']) {
      expect(formatPaymentAmount(1300, 'USD', locale)).toBe('$1,300.00')
    }
  })
})
