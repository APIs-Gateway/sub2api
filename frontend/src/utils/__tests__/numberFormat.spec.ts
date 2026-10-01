import { describe, expect, it } from 'vitest'
import {
  formatCnyAmount,
  formatCompactCount,
  formatCount,
  formatCurrencyAmount,
  formatDurationMs,
  formatFixed,
  formatMoneyNumber,
  formatUsdAmount,
  splitNumeric
} from '../numberFormat'

describe('formatMoneyNumber：金额统一规则', () => {
  it('0 显示 0.00', () => {
    expect(formatMoneyNumber(0)).toBe('0.00')
    expect(formatMoneyNumber(-0)).toBe('0.00')
  })

  it('≥ 1 固定两位小数并带千分位', () => {
    expect(formatMoneyNumber(1)).toBe('1.00')
    expect(formatMoneyNumber(1234.5)).toBe('1,234.50')
    expect(formatMoneyNumber(17717.09)).toBe('17,717.09')
    expect(formatMoneyNumber(56948.1518)).toBe('56,948.15')
    expect(formatMoneyNumber(1234567.891)).toBe('1,234,567.89')
  })

  it('0 < 金额 < 1 保留 4 位有效数字，不补尾随 0', () => {
    expect(formatMoneyNumber(0.0004)).toBe('0.0004')
    expect(formatMoneyNumber(0.0012)).toBe('0.0012')
    expect(formatMoneyNumber(0.00123456)).toBe('0.001235')
    expect(formatMoneyNumber(0.12346)).toBe('0.1235')
    expect(formatMoneyNumber(0.0000123456)).toBe('0.00001235')
  })

  it('< 1 的金额至少显示两位小数：0.5 读作 0.50 而不是 0.5', () => {
    expect(formatMoneyNumber(0.5)).toBe('0.50')
    expect(formatMoneyNumber(0.1)).toBe('0.10')
    expect(formatMoneyNumber(0.05)).toBe('0.05')
  })

  it('取整进位到 1 时按 ≥ 1 处理，不会写成 1.0000', () => {
    expect(formatMoneyNumber(0.99996)).toBe('1.00')
    expect(formatMoneyNumber(0.99995)).toBe('1.00')
    // 4 位有效数字内不进位：仍按 < 1 的规则
    expect(formatMoneyNumber(0.9999)).toBe('0.9999')
  })

  it('负数：符号在数字前，规则与正数一致', () => {
    expect(formatMoneyNumber(-3.2)).toBe('-3.20')
    expect(formatMoneyNumber(-1234.567)).toBe('-1,234.57')
    expect(formatMoneyNumber(-0.0004)).toBe('-0.0004')
  })

  it('非法输入按 0 处理', () => {
    expect(formatMoneyNumber(null)).toBe('0.00')
    expect(formatMoneyNumber(undefined)).toBe('0.00')
    expect(formatMoneyNumber(Number.NaN)).toBe('0.00')
    expect(formatMoneyNumber(Number.POSITIVE_INFINITY)).toBe('0.00')
  })

  it('显式指定小数位：精确值场景，仍带千分位', () => {
    expect(formatMoneyNumber(52.4361, { fractionDigits: 4 })).toBe('52.4361')
    expect(formatMoneyNumber(56948.1518, { fractionDigits: 4 })).toBe('56,948.1518')
    expect(formatMoneyNumber(5, { fractionDigits: 6 })).toBe('5.000000')
    // 取整后全是 0 不带负号
    expect(formatMoneyNumber(-0.00001, { fractionDigits: 2 })).toBe('0.00')
  })

  it('精确值（exact）：放宽小数位上限，信息量不低于旧版 4 位小数', () => {
    expect(formatMoneyNumber(52.4361, { exact: true })).toBe('52.4361')
    expect(formatMoneyNumber(5, { exact: true })).toBe('5.00')
    expect(formatMoneyNumber(0.5, { exact: true })).toBe('0.50')
    expect(formatMoneyNumber(0.001234, { exact: true })).toBe('0.001234')
    expect(formatMoneyNumber(0.000123456, { exact: true })).toBe('0.0001235')
    expect(formatMoneyNumber(17717.0912346, { exact: true })).toBe('17,717.091235')
  })

  it('单价（unitPrice）：不论 ≥ 1 还是 < 1，最多 6 位小数、去尾零、至少两位，不丢精度', () => {
    expect(formatMoneyNumber(0.015625, { unitPrice: true })).toBe('0.015625')
    expect(formatMoneyNumber(0.0390625, { unitPrice: true })).toBe('0.039063')
    expect(formatMoneyNumber(12.34567, { unitPrice: true })).toBe('12.34567')
    expect(formatMoneyNumber(3, { unitPrice: true })).toBe('3.00')
    expect(formatMoneyNumber(0.5, { unitPrice: true })).toBe('0.50')
    expect(formatMoneyNumber(1234.5, { unitPrice: true })).toBe('1,234.50')
    expect(formatMoneyNumber(2.769230769, { unitPrice: true })).toBe('2.769231')
    expect(formatUsdAmount(0.015625, { unitPrice: true })).toBe('$0.015625')
  })

  it('单价（unitPrice）：1.875 不被写成 1.88', () => {
    expect(formatMoneyNumber(1.875, { unitPrice: true })).toBe('1.875')
    expect(formatMoneyNumber(2.5, { unitPrice: true })).toBe('2.50')
    expect(formatMoneyNumber(18.75, { unitPrice: true })).toBe('18.75')
    expect(formatMoneyNumber(0.0375, { unitPrice: true })).toBe('0.0375')
    expect(formatMoneyNumber(0, { unitPrice: true })).toBe('0.00')
  })
})

describe('formatCurrencyAmount', () => {
  it('USD / CNY 走统一规则，大小写不敏感', () => {
    expect(formatCurrencyAmount(1234.5, 'usd')).toBe('$1,234.50')
    expect(formatCurrencyAmount(0.0004, 'CNY')).toBe('¥0.0004')
    expect(formatCurrencyAmount(null)).toBe('$0.00')
  })
})

describe('formatUsdAmount / formatCnyAmount', () => {
  it('美元：符号在前，负号在符号之前', () => {
    expect(formatUsdAmount(17717.09)).toBe('$17,717.09')
    expect(formatUsdAmount(0.0004)).toBe('$0.0004')
    expect(formatUsdAmount(0)).toBe('$0.00')
    expect(formatUsdAmount(-12.5)).toBe('-$12.50')
    expect(formatUsdAmount(52.4361, { exact: true })).toBe('$52.4361')
  })

  it('人民币：规则与美元完全一致，只换符号', () => {
    expect(formatCnyAmount(17717.09)).toBe('¥17,717.09')
    expect(formatCnyAmount(0.99996)).toBe('¥1.00')
    expect(formatCnyAmount(0)).toBe('¥0.00')
    expect(formatCnyAmount(-0.0004)).toBe('-¥0.0004')
    expect(formatCnyAmount(1234.5)).toBe('¥1,234.50')
  })
})

describe('formatCount / formatFixed', () => {
  it('计数带千分位', () => {
    expect(formatCount(0)).toBe('0')
    expect(formatCount(2271)).toBe('2,271')
    expect(formatCount(937241)).toBe('937,241')
    expect(formatCount(null)).toBe('0')
  })

  it('固定小数位带千分位', () => {
    expect(formatFixed(1234.5, 2)).toBe('1,234.50')
    expect(formatFixed(undefined, 2)).toBe('0.00')
  })
})

describe('formatCompactCount：K / M 缩写', () => {
  it('小于 1000 不缩写', () => {
    expect(formatCompactCount(0)).toBe('0')
    expect(formatCompactCount(999)).toBe('999')
  })

  it('K / M 保留 1 位小数', () => {
    expect(formatCompactCount(1000)).toBe('1.0K')
    expect(formatCompactCount(1234)).toBe('1.2K')
    expect(formatCompactCount(227_800_000)).toBe('227.8M')
  })

  it('缩写后的数带千分位：74935.4M → 74,935.4M', () => {
    expect(formatCompactCount(74_935_400_000, { allowBillions: false })).toBe('74,935.4M')
  })

  it('取整进位时升到下一档，不出现 1000.0K', () => {
    expect(formatCompactCount(999_999)).toBe('1.0M')
    expect(formatCompactCount(999_950)).toBe('1.0M')
    expect(formatCompactCount(999_949)).toBe('999.9K')
  })

  it('B 档默认开启，requests 口径可以关掉', () => {
    expect(formatCompactCount(1_000_000_000)).toBe('1.0B')
    expect(formatCompactCount(1_000_000_000, { allowBillions: false })).toBe('1,000.0M')
  })

  it('非法输入按 0 处理，刻度里的小数最多保留 1 位', () => {
    expect(formatCompactCount(null)).toBe('0')
    expect(formatCompactCount(0.5)).toBe('0.5')
  })
})

describe('formatDurationMs', () => {
  it('小于 1 秒显示毫秒，否则显示秒（两位小数）', () => {
    expect(formatDurationMs(0)).toBe('0ms')
    expect(formatDurationMs(320)).toBe('320ms')
    expect(formatDurationMs(25_700)).toBe('25.70s')
    expect(formatDurationMs(null)).toBe('-')
  })
})

describe('splitNumeric：拆出符号 / 数字 / 单位', () => {
  it('货币符号在前', () => {
    expect(splitNumeric('¥17,717.09')).toEqual({ sign: '', prefix: '¥', digits: '17,717.09', suffix: '' })
    expect(splitNumeric('-$12.50')).toEqual({ sign: '-', prefix: '$', digits: '12.50', suffix: '' })
    expect(splitNumeric('≈¥0.0012')).toEqual({ sign: '', prefix: '≈¥', digits: '0.0012', suffix: '' })
  })

  it('单位在后', () => {
    expect(splitNumeric('74,935.4M')).toEqual({ sign: '', prefix: '', digits: '74,935.4', suffix: 'M' })
    expect(splitNumeric('25.70s')).toEqual({ sign: '', prefix: '', digits: '25.70', suffix: 's' })
    expect(splitNumeric('13 RPM')).toEqual({ sign: '', prefix: '', digits: '13', suffix: 'RPM' })
    expect(splitNumeric('12.5%')).toEqual({ sign: '', prefix: '', digits: '12.5', suffix: '%' })
  })

  it('前缀可以带空格：USD 72.60、/ $40.25', () => {
    expect(splitNumeric('USD 72.60')).toEqual({ sign: '', prefix: 'USD', digits: '72.60', suffix: '' })
    expect(splitNumeric('/ $40.25')).toEqual({ sign: '', prefix: '/ $', digits: '40.25', suffix: '' })
  })

  it('不是「符号 + 数字 + 单位」的形状时返回 null，原样渲染', () => {
    expect(splitNumeric('-')).toBeNull()
    expect(splitNumeric('∞')).toBeNull()
    expect(splitNumeric('')).toBeNull()
    expect(splitNumeric('$1.00 / $2.00')).toBeNull()
  })
})
