import { describe, expect, it } from 'vitest'
import { planValiditySuffix } from '../validity'

const t = (key: string) => ({
  'payment.days': ' days',
  'payment.weeks': ' weeks',
  'payment.months': ' months',
  'payment.perMonth': 'month',
}[key] ?? key)

describe('planValiditySuffix', () => {
  it('normalizes plural months and weeks', () => {
    expect(planValiditySuffix({ validity_days: 1, validity_unit: 'months' }, t)).toBe('month')
    expect(planValiditySuffix({ validity_days: 2, validity_unit: 'months' }, t)).toBe('2 months')
    expect(planValiditySuffix({ validity_days: 3, validity_unit: 'weeks' }, t)).toBe('3 weeks')
  })

  it('keeps day fallback for legacy and unknown units', () => {
    expect(planValiditySuffix({ validity_days: 30, validity_unit: 'day' }, t)).toBe('30 days')
    expect(planValiditySuffix({ validity_days: 9, validity_unit: 'unknown' }, t)).toBe('9 days')
  })
})
