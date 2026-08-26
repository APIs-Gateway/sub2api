import { beforeEach, describe, expect, it, vi } from 'vitest'

// 可变的假设置：测试里改它就能模拟不同的充值倍率。
const publicSettings: { value: Record<string, unknown> | null } = {
  value: { balance_recharge_multiplier: 13 }
}

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    get cachedPublicSettings() {
      return publicSettings.value
    }
  })
}))

vi.mock('@/i18n', () => ({
  getLocale: () => 'zh-CN'
}))

import { useCurrencyDisplay } from '../useCurrencyDisplay'

/** 去掉 Intl 可能插入的不间断空格，断言只关心数字和币种符号。 */
function normalize(text: string): string {
  return text.replace(/ | /g, ' ')
}

describe('useCurrencyDisplay', () => {
  beforeEach(() => {
    publicSettings.value = { balance_recharge_multiplier: 13 }
    window.localStorage.clear()
    // mode 是模块级单例，逐个用例显式复位，避免相互串味。
    useCurrencyDisplay().setMode('fiat')
  })

  it('默认按法币展示', () => {
    expect(useCurrencyDisplay().isFiat.value).toBe(true)
  })

  it('按充值倍率把额度折算成法币', () => {
    const { creditToFiat } = useCurrencyDisplay()

    // 这条就是整个功能要解决的误读：看到「扣了 5」，其实只花了三毛八。
    expect(creditToFiat(5)).toBeCloseTo(5 / 13, 10)
    expect(creditToFiat(0)).toBe(0)
  })

  it('倍率缺失或损坏时折算退化为恒等，绝不除以 0', () => {
    for (const broken of [undefined, 0, -13, Number.NaN]) {
      publicSettings.value = { balance_recharge_multiplier: broken }
      const { creditToFiat, rechargeMultiplier } = useCurrencyDisplay()

      expect(rechargeMultiplier.value).toBe(1)
      expect(creditToFiat(5)).toBe(5)
    }

    publicSettings.value = null
    expect(useCurrencyDisplay().creditToFiat(5)).toBe(5)
  })

  it('倍率为 1 时隐藏切换器——两个口径数字相同，切换没有意义', () => {
    expect(useCurrencyDisplay().canSwitch.value).toBe(true)

    publicSettings.value = { balance_recharge_multiplier: 1 }
    expect(useCurrencyDisplay().canSwitch.value).toBe(false)
  })

  it('小额消费按量级提高小数位，不会被截成 ¥0.00', () => {
    const { formatFiat } = useCurrencyDisplay()

    // 0.003 元这种量级如果固定两位小数就全变成 0 了。
    expect(normalize(formatFiat(0.003))).toContain('0.0030')
    expect(normalize(formatFiat(0.25))).toContain('0.250')
    expect(normalize(formatFiat(12.5))).toContain('12.50')
    expect(normalize(formatFiat(null))).toContain('0.00')
  })

  it('额度保持定宽小数，便于逐条对账', () => {
    const { formatCredit } = useCurrencyDisplay()

    expect(formatCredit(5)).toBe('5.0000')
    expect(formatCredit(5, 6)).toBe('5.000000')
    expect(formatCredit(null)).toBe('0.0000')
    expect(formatCredit(Number.NaN)).toBe('0.0000')
  })

  it('法币模式优先用服务端算好的精确值，而不是按充值倍率估算', () => {
    const { formatAmount } = useCurrencyDisplay()

    // 订阅扣费：服务端给的 0.25 才是真实花费，按 1/13 估算会得到 0.385。
    const withExact = normalize(formatAmount(5, 0.25))
    expect(withExact).toContain('0.250')
    expect(withExact).not.toContain('0.385')

    // 缺精确值时回落到按充值倍率估算。
    expect(normalize(formatAmount(5))).toContain('0.385')
  })

  it('额度模式下展示原始额度，不做任何折算', () => {
    const { formatAmount, setMode } = useCurrencyDisplay()
    setMode('credit')

    expect(formatAmount(5, 0.25, 6)).toBe('5.000000')
  })

  it('切换会持久化，且 toggle 在两个口径间往返', () => {
    const { setMode, toggle, mode } = useCurrencyDisplay()

    setMode('credit')
    expect(mode.value).toBe('credit')
    expect(window.localStorage.getItem('currency-display-mode')).toBe('credit')

    toggle()
    expect(mode.value).toBe('fiat')
    expect(window.localStorage.getItem('currency-display-mode')).toBe('fiat')

    toggle()
    expect(mode.value).toBe('credit')
  })

  it('多个调用方共享同一份状态——切换器改一次要全站生效', () => {
    const a = useCurrencyDisplay()
    const b = useCurrencyDisplay()

    a.setMode('credit')

    expect(b.isFiat.value).toBe(false)
    expect(b.mode.value).toBe('credit')
  })
})
