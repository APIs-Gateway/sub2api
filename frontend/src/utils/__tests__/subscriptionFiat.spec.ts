import { describe, expect, it } from 'vitest'

import { planFiatPerCredit, planValidityDays } from '../subscriptionFiat'

describe('planValidityDays', () => {
  it('按单位折成天数，月按 30 天', () => {
    expect(planValidityDays({ validity_days: 30, validity_unit: 'day' })).toBe(30)
    expect(planValidityDays({ validity_days: 2, validity_unit: 'weeks' })).toBe(14)
    expect(planValidityDays({ validity_days: 3, validity_unit: 'month' })).toBe(90)
    expect(planValidityDays({ validity_days: 7, validity_unit: '' })).toBe(7)
  })
})

describe('planFiatPerCredit', () => {
  it('实付金额 ÷（每日额度 × 天数）', () => {
    // 付 ¥40.5 买每日 30 额度 × 30 天 = 900 额度，单价 0.045。
    const rate = planFiatPerCredit({ validity_days: 30, validity_unit: 'day', daily_amount_usd: 30 }, 40.5)
    expect(rate).toBeCloseTo(0.045, 10)
  })

  it('缺每日额度、有效期或实付金额时无法折算', () => {
    expect(planFiatPerCredit({ validity_days: 30, validity_unit: 'day' }, 40)).toBeNull()
    expect(planFiatPerCredit({ validity_days: 0, validity_unit: 'day', daily_amount_usd: 30 }, 40)).toBeNull()
    expect(planFiatPerCredit({ validity_days: 30, validity_unit: 'day', daily_amount_usd: 30 }, 0)).toBeNull()
  })

  it('没有 daily_amount_usd 时退用 daily_limit_usd', () => {
    expect(planFiatPerCredit({ validity_days: 10, validity_unit: 'day', daily_limit_usd: 5 }, 5)).toBeCloseTo(0.1, 10)
  })
})
