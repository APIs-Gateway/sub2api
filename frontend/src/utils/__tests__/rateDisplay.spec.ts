import { describe, expect, it } from 'vitest'

import {
  activeCardUnit,
  balanceRate,
  buildRateView,
  formatRate,
  formatRawRate,
  planLowestRate,
  planRateAvailable,
  planRateForUnit,
  planUnitUsable,
  resolvePlanUnitMin,
  usageRowRate,
  type RateContext
} from '../rateDisplay'

/** 生产 codex 站：m = 13，u_min = 0.04（D ≥ 210 时的单价），订阅可买，人民币模式，无生效卡。 */
const prod: RateContext = {
  isFiat: true,
  rechargeMultiplier: 13,
  planUnitMin: 0.04,
  paymentEnabled: true,
  cardUnit: null
}
const withCard: RateContext = { ...prod, cardUnit: 0.0467 }
const usdMode: RateContext = { ...prod, isFiat: false }
/** free 站：m = 1，u_min = 1，美元计价。 */
const free: RateContext = {
  isFiat: false,
  rechargeMultiplier: 1,
  planUnitMin: 1,
  paymentEnabled: true,
  cardUnit: null
}

/** 方案 v2 里出现过的分组倍率：专属 0.585、1、1.35、codex特惠 1.4、kiro 2、pro+plus 3、luna 4、满血 5。 */
const RATES = [0.585, 1, 1.35, 1.4, 2, 3, 4, 5]

describe('formatRate（R6：10 位有效数字去噪 → 3 位有效数字 half up → 去末尾 0）', () => {
  const cases: Array<[string, number, string]> = [
    ['1/13', 1 / 13, '0.0769'],
    ['1.4/13', 1.4 / 13, '0.108'],
    ['0.585/13', 0.585 / 13, '0.045'],
    ['0.585 × 0.04', 0.585 * 0.04, '0.0234'],
    ['5 × 0.0467 = 0.2335，必须进位成 0.234 而不是 0.233', 5 * 0.0467, '0.234'],
    ['0.2335 字面量', 0.2335, '0.234'],
    ['1.005 在二进制里略小于 1.005，也要按十进制进位', 1.005, '1.01'],
    ['2.675 同理', 2.675, '2.68'],
    ['12.55 → 3 位有效数字', 12.55, '12.6'],
    ['7.6923 → 7.69', 7.6923, '7.69'],
    ['整百不丢零', 100, '100'],
    ['整数', 3, '3'],
    ['末尾 0 去掉：0.5', 0.5, '0.5'],
    ['末尾 0 去掉：0.040', 0.04, '0.04'],
    ['浮点噪声 1.4 × 0.04 = 0.056000000000000001', 1.4 * 0.04, '0.056'],
    ['浮点噪声 0.1 + 0.2', 0.1 + 0.2, '0.3'],
    ['进位溢出 0.9996 → 1', 0.9996, '1'],
    ['进位溢出 999.5 → 1000', 999.5, '1000'],
    ['整数部分超过 3 位保留全部整数位', 1234.5, '1235'],
    ['很小的数不用科学计数法', 0.00001234, '0.0000123'],
    ['很小的数：1e-7', 1e-7, '0.0000001'],
    ['0', 0, '0'],
    ['负数保留符号', -0.5, '-0.5'],
    ['超大数不做有效数字取整', 1e21, '1e+21']
  ]
  it.each(cases)('%s', (_name, input, expected) => {
    expect(formatRate(input)).toBe(expected)
  })

  it.each([Number.NaN, Number.POSITIVE_INFINITY, null, undefined])('非有限数 %s 显示 -', (value) => {
    expect(formatRate(value)).toBe('-')
  })
})

describe('formatRawRate（美元模式 / free 站：只去噪声，不取有效数字）', () => {
  it.each([
    [1.4, '1.4'],
    [3, '3'],
    [0.585, '0.585'],
    [1.2345, '1.2345'],
    [0.1 + 0.2, '0.3']
  ])('%s → %s', (input, expected) => {
    expect(formatRawRate(input)).toBe(expected)
  })

  it.each([Number.NaN, Number.POSITIVE_INFINITY, null, undefined])('非有限数 %s 显示 -', (value) => {
    expect(formatRawRate(value)).toBe('-')
  })
})

describe('resolvePlanUnitMin（u_min 取 /subscriptions/pricing 的 u_min，R2 条件 b）', () => {
  it.each([
    ['生产：u_min 0.04、u_max 0.05', { u_min: 0.04, u_max: 0.05 }, 0.04],
    ['配置写反时后端按较小的夹，这里同样取较小的', { u_min: 0.05, u_max: 0.04 }, 0.04],
    ['没有 u_max 就用 u_min', { u_min: 0.04 }, 0.04],
    ['u_max 非正忽略', { u_min: 0.04, u_max: 0 }, 0.04],
    ['u_min = 0 不出现（不退回 u_max）', { u_min: 0, u_max: 0.05 }, null],
    ['u_min 为负', { u_min: -1, u_max: 0.05 }, null],
    ['u_min 是 NaN', { u_min: Number.NaN, u_max: 0.05 }, null],
    ['u_min 缺失', { u_max: 0.05 }, null],
    ['u_min 为 null', { u_min: null, u_max: 0.05 }, null]
  ])('%s', (_name, bounds, expected) => {
    expect(resolvePlanUnitMin(bounds)).toBe(expected)
  })

  it('取不到区间（null / undefined）为 null', () => {
    expect(resolvePlanUnitMin(null)).toBeNull()
    expect(resolvePlanUnitMin(undefined)).toBeNull()
  })
})

describe('activeCardUnit（有卡 / 无卡）', () => {
  it('取第一张生效且带单价的卡', () => {
    expect(
      activeCardUnit([
        { status: 'expired', fiat_per_credit: 0.09 },
        { status: 'active' },
        { status: 'active', fiat_per_credit: 0.0467 },
        { status: 'active', fiat_per_credit: 0.05 }
      ])
    ).toBe(0.0467)
  })

  it.each([
    ['无卡', []],
    ['过期卡', [{ status: 'expired', fiat_per_credit: 0.0467 }]],
    ['单价为 0', [{ status: 'active', fiat_per_credit: 0 }]],
    ['单价非法', [{ status: 'active', fiat_per_credit: Number.NaN }]],
    ['单价为 null', [{ status: 'active', fiat_per_credit: null }]]
  ])('%s → null', (_name, subs) => {
    expect(activeCardUnit(subs)).toBeNull()
  })

  it('列表缺失为 null', () => {
    expect(activeCardUnit(null)).toBeNull()
    expect(activeCardUnit(undefined)).toBeNull()
  })
})

describe('balanceRate（R1 余额倍率）', () => {
  it('m = 13、人民币模式：r ÷ 13', () => {
    const expected = ['0.045', '0.0769', '0.104', '0.108', '0.154', '0.231', '0.308', '0.385']
    expect(RATES.map((r) => formatRate(balanceRate(r, prod)))).toEqual(expected)
    expect(balanceRate(1.4, prod)).toBeCloseTo(0.1076923, 6)
  })

  it('美元模式：原始 r', () => {
    expect(RATES.map((r) => balanceRate(r, usdMode))).toEqual(RATES)
  })

  it('free 站（m = 1）：原始 r，哪怕上下文里误标成人民币模式', () => {
    expect(RATES.map((r) => balanceRate(r, free))).toEqual(RATES)
    expect(RATES.map((r) => balanceRate(r, { ...free, isFiat: true }))).toEqual(RATES)
  })

  it('充值倍率缺失或非法按 1，绝不除以 0', () => {
    for (const m of [0, -13, Number.NaN]) {
      expect(balanceRate(1.4, { ...prod, rechargeMultiplier: m })).toBe(1.4)
    }
  })

  it('r 非法返回 NaN', () => {
    expect(balanceRate(Number.NaN, prod)).toBeNaN()
    expect(balanceRate(-1, prod)).toBeNaN()
    expect(balanceRate(Number.POSITIVE_INFINITY, prod)).toBeNaN()
  })

  it('r = 0 就是 0（免费分组）', () => {
    expect(balanceRate(0, prod)).toBe(0)
  })
})

describe('planLowestRate（R2 套餐低至 r × u_min）', () => {
  it('m = 13、u_min = 0.04：方案里的整张表', () => {
    const expected = ['0.0234', '0.04', '0.054', '0.056', '0.08', '0.12', '0.16', '0.2']
    expect(RATES.map((r) => formatRate(planLowestRate(r, prod)))).toEqual(expected)
  })

  it('数值就是 r × u_min', () => {
    expect(planLowestRate(1.4, prod)).toBe(1.4 * 0.04)
  })

  it('美元模式恒为 null', () => {
    expect(RATES.map((r) => planLowestRate(r, usdMode))).toEqual(RATES.map(() => null))
  })

  it('free 站恒为 null（m = 1、u_min = 1，即使误标成人民币模式、u_min 很小也一样）', () => {
    expect(RATES.map((r) => planLowestRate(r, free))).toEqual(RATES.map(() => null))
    expect(planLowestRate(1.4, { ...free, isFiat: true, planUnitMin: 0.04 })).toBeNull()
  })

  describe('R2 抑制条件逐条覆盖（任一成立整段不出现）', () => {
    const suppressed: Array<[string, RateContext, number]> = [
      ['(a) 美元模式', usdMode, 1.4],
      ['(b) /subscriptions/pricing 取不到', { ...prod, planUnitMin: null }, 1.4],
      ['(b) u_min = 0', { ...prod, planUnitMin: resolvePlanUnitMin({ u_min: 0, u_max: 0.05 }) }, 1.4],
      ['(c) payment_enabled = false', { ...prod, paymentEnabled: false }, 1.4],
      ['(d) m × u_min > 1（13 × 0.08 = 1.04）', { ...prod, planUnitMin: 0.08 }, 1.4],
      ['(d) m × u_min = 1（13 × 1/13，不是严格更低）', { ...prod, planUnitMin: 1 / 13 }, 1.4],
      // 49 × (1/49) 在浮点里是 0.9999999999999999，不去噪声就会被当成「严格更低」。
      ['(d) m × u_min 浮点噪声下仍然等于 1', { ...prod, rechargeMultiplier: 49, planUnitMin: 1 / 49 }, 1.4],
      ['(d) free 站 m × u_min = 1 × 1', { ...prod, rechargeMultiplier: 1, planUnitMin: 1 }, 1.4],
      ['r = 0（套餐倍率与主倍率相等）', prod, 0],
      ['r 非法', prod, Number.NaN]
    ]
    it.each(suppressed)('%s', (_name, ctx, r) => {
      expect(planLowestRate(r, ctx)).toBeNull()
    })

    it('刚好严格更低（13 × 0.0769 = 0.9997）仍然出现', () => {
      expect(planLowestRate(1, { ...prod, planUnitMin: 0.0769 })).toBe(0.0769)
    })

    it('planRateAvailable 与 planLowestRate 同一套条件（价格页整列显示 / 隐藏）', () => {
      expect(planRateAvailable(prod)).toBe(true)
      expect(planRateAvailable(usdMode)).toBe(false)
      expect(planRateAvailable(free)).toBe(false)
      expect(planRateAvailable({ ...prod, planUnitMin: null })).toBe(false)
      expect(planRateAvailable({ ...prod, paymentEnabled: false })).toBe(false)
      expect(planRateAvailable({ ...prod, planUnitMin: 0.08 })).toBe(false)
    })
  })
})

describe('planRateForUnit（卡的精确单价、报价的 unit_price）', () => {
  it('有卡：u_card = 0.0467 → 0.0654 / 0.14 / 0.187 / 0.234', () => {
    // 5 × 0.0467 = 0.2335，half up 写 0.234，不是 0.233。
    const rates = [1.4, 3, 4, 5]
    expect(rates.map((r) => formatRate(planRateForUnit(r, 0.0467, withCard)))).toEqual([
      '0.0654',
      '0.14',
      '0.187',
      '0.234'
    ])
  })

  it('购买面板：D = 210 时 u = 0.04 → 0.056；D = 30 时 u = 0.05 → 0.07，余额倍率不变', () => {
    expect(formatRate(planRateForUnit(1.4, 0.04, prod))).toBe('0.056')
    expect(formatRate(planRateForUnit(1.4, 0.05, prod))).toBe('0.07')
    expect(formatRate(balanceRate(1.4, prod))).toBe('0.108')
  })

  it.each([
    ['u = 0', 0],
    ['u 为负', -0.04],
    ['u 是 NaN', Number.NaN],
    ['u 为 null', null],
    ['u 缺失', undefined],
    ['m × u > 1', 0.08],
    ['m × u = 1', 1 / 13]
  ])('不出现：%s', (_name, u) => {
    expect(planRateForUnit(1.4, u, prod)).toBeNull()
  })

  it('不出现：美元模式 / free / 支付关闭 / r = 0', () => {
    expect(planRateForUnit(1.4, 0.04, usdMode)).toBeNull()
    expect(planRateForUnit(1.4, 0.04, free)).toBeNull()
    expect(planRateForUnit(1.4, 0.04, { ...prod, paymentEnabled: false })).toBeNull()
    expect(planRateForUnit(0, 0.04, prod)).toBeNull()
  })

  it('planUnitUsable 是 R2 的 a、c、d', () => {
    expect(planUnitUsable(0.04, prod)).toBe(true)
    expect(planUnitUsable(0.04, usdMode)).toBe(false)
    expect(planUnitUsable(0.04, { ...prod, paymentEnabled: false })).toBe(false)
    expect(planUnitUsable(0.08, prod)).toBe(false)
    expect(planUnitUsable(null, prod)).toBe(false)
  })
})

describe('usageRowRate（R7 用量行 = fiat_cost ÷ total_cost）', () => {
  // 输入 60,000 + 输出 10,000 token，官方价 $0.60，分组倍率 1.4：额度 $0.84。
  const walletRow = { total_cost: 0.6, rate_multiplier: 1.4, fiat_cost: 0.84 / 13 }
  const planRow = { total_cost: 0.6, rate_multiplier: 1.4, fiat_cost: 0.84 * 0.0467 }

  it('余额扣费 → 0.108（r ÷ 13）', () => {
    expect(formatRate(usageRowRate(walletRow, prod))).toBe('0.108')
  })

  it('套餐扣费（卡 D=90，u = 0.0467）→ 0.0654（r × u_card），不依赖 rate_multiplier 的含义', () => {
    expect(formatRate(usageRowRate(planRow, prod))).toBe('0.0654')
    // rate_multiplier 变成别的含义也不影响结果。
    expect(formatRate(usageRowRate({ ...planRow, rate_multiplier: 1 }, prod))).toBe('0.0654')
  })

  it.each([
    ['缺 fiat_cost', { total_cost: 0.6, rate_multiplier: 1.4 }],
    ['fiat_cost 为 null', { total_cost: 0.6, rate_multiplier: 1.4, fiat_cost: null }],
    ['fiat_cost 为 0（后端折算不可用）', { total_cost: 0.6, rate_multiplier: 1.4, fiat_cost: 0 }],
    ['fiat_cost 非法', { total_cost: 0.6, rate_multiplier: 1.4, fiat_cost: Number.NaN }],
    ['total_cost 缺失', { rate_multiplier: 1.4, fiat_cost: 0.0646 }]
  ])('老行回落 r ÷ m：%s', (_name, row) => {
    expect(formatRate(usageRowRate(row, prod))).toBe('0.108')
  })

  it('total_cost = 0 → null（显示 -），不论有没有 fiat_cost', () => {
    expect(usageRowRate({ total_cost: 0, rate_multiplier: 1.4, fiat_cost: 0 }, prod)).toBeNull()
    expect(usageRowRate({ total_cost: 0, rate_multiplier: 1.4, fiat_cost: 0.5 }, prod)).toBeNull()
  })

  it('既没有 fiat_cost 也没有可用的 rate_multiplier → null', () => {
    expect(usageRowRate({ total_cost: 0.6 }, prod)).toBeNull()
    expect(usageRowRate({ total_cost: 0.6, rate_multiplier: Number.NaN }, prod)).toBeNull()
    expect(usageRowRate({ total_cost: 0.6, rate_multiplier: -1 }, prod)).toBeNull()
  })

  it('免费分组（倍率 0、花费 0）显示 0', () => {
    expect(formatRate(usageRowRate({ total_cost: 0.6, rate_multiplier: 0, fiat_cost: 0 }, prod))).toBe('0')
  })

  it('美元模式与 free 站：原样返回 rate_multiplier，取不到为 null', () => {
    expect(usageRowRate(walletRow, usdMode)).toBe(1.4)
    expect(usageRowRate(walletRow, free)).toBe(1.4)
    expect(usageRowRate({ total_cost: 0, rate_multiplier: 1.4 }, usdMode)).toBe(1.4)
    expect(usageRowRate({ total_cost: 0.6 }, usdMode)).toBeNull()
  })
})

describe('buildRateView（徽标 / 下拉 / 价格页用的分组倍率视图）', () => {
  it('m = 13、人民币、无卡：codex特惠分组 1.4 → 0.108x + 套餐低至 0.056x', () => {
    expect(buildRateView(1.4, null, prod)).toEqual({
      main: '0.108',
      mainValue: 1.4 / 13,
      plan: '0.056',
      planValue: 1.4 * 0.04
    })
  })

  it('专属倍率：两个数都换成等效值，默认值留给划线（0.585 ÷ 13、0.585 × 0.04）', () => {
    expect(buildRateView(1.4, 0.585, prod)).toEqual({
      main: '0.045',
      mainValue: 0.585 / 13,
      mainStruck: '0.108',
      plan: '0.0234',
      planValue: 0.585 * 0.04,
      planStruck: '0.056'
    })
  })

  it('专属倍率与默认倍率相同、为空或非法时不算专属（与 GroupBadge 的判定一致）', () => {
    for (const userRate of [1.4, null, undefined, Number.NaN]) {
      const view = buildRateView(1.4, userRate, prod)
      expect(view.main).toBe('0.108')
      expect(view.mainStruck).toBeUndefined()
      expect(view.planStruck).toBeUndefined()
    }
  })

  it('有生效卡：多一个 yourPlan（r × u_card），低至仍用 u_min，数字不随用户状态变', () => {
    const view = buildRateView(5, null, withCard)
    expect(view.main).toBe('0.385')
    expect(view.plan).toBe('0.2')
    expect(view.yourPlan).toBe('0.234')
    expect(view.yourPlanValue).toBe(5 * 0.0467)
    // 无卡的同一分组，主倍率和套餐低至完全一样。
    const noCard = buildRateView(5, null, prod)
    expect(noCard.main).toBe(view.main)
    expect(noCard.plan).toBe(view.plan)
    expect(noCard.yourPlan).toBeUndefined()
  })

  it('有卡 + 专属倍率：yourPlan 用专属 r', () => {
    expect(buildRateView(1.4, 0.585, withCard).yourPlan).toBe('0.0273')
  })

  it('有卡但套餐倍率被抑制时整段不出现，yourPlan 也不出现', () => {
    for (const ctx of [
      { ...withCard, planUnitMin: null },
      { ...withCard, paymentEnabled: false },
      { ...withCard, planUnitMin: 0.08 },
      { ...withCard, isFiat: false }
    ]) {
      const view = buildRateView(1.4, null, ctx)
      expect(view.plan).toBeUndefined()
      expect(view.yourPlan).toBeUndefined()
    }
  })

  it('r = 0：只剩主倍率 0', () => {
    expect(buildRateView(0, null, prod)).toEqual({ main: '0', mainValue: 0 })
  })

  it('美元模式：原始 r，没有套餐倍率；专属倍率照旧划线', () => {
    expect(buildRateView(1.4, null, usdMode)).toEqual({ main: '1.4', mainValue: 1.4 })
    expect(buildRateView(1.4, 0.585, usdMode)).toEqual({ main: '0.585', mainValue: 0.585, mainStruck: '1.4' })
  })

  it('free 站：与美元模式逐字相同，哪怕上下文里有 u_min、误标成人民币', () => {
    expect(buildRateView(1.4, null, free)).toEqual({ main: '1.4', mainValue: 1.4 })
    expect(buildRateView(1.4, null, { ...free, isFiat: true, planUnitMin: 0.04 })).toEqual({
      main: '1.4',
      mainValue: 1.4
    })
  })

  it('美元模式不取有效数字：1.2345 照原样', () => {
    expect(buildRateView(1.2345, null, usdMode).main).toBe('1.2345')
  })

  it('方案里 5 个分组的全部显示值', () => {
    // codex特惠 1.4、pro+plus 3、kiro 2、满血 5、luna 4
    const groups: Array<[number, string, string]> = [
      [1.4, '0.108', '0.056'],
      [3, '0.231', '0.12'],
      [2, '0.154', '0.08'],
      [5, '0.385', '0.2'],
      [4, '0.308', '0.16']
    ]
    for (const [r, main, plan] of groups) {
      const view = buildRateView(r, null, prod)
      expect([view.main, view.plan]).toEqual([main, plan])
    }
  })
})
