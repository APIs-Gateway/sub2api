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

import { EXACT_DIGITS, reportMixedFiat, resetFiatDataMissingForTest, useCurrencyDisplay } from '../useCurrencyDisplay'

/** 去掉 Intl 可能插入的不间断空格，断言只关心数字和币种符号。 */
function normalize(text: string): string {
  return text.replace(/[\u00a0\u202f]/g, ' ')
}

describe('useCurrencyDisplay', () => {
  beforeEach(() => {
    publicSettings.value = { balance_recharge_multiplier: 13 }
    window.localStorage.clear()
    resetFiatDataMissingForTest()
    // mode 是模块级单例，逐个用例显式复位，避免相互串味。
    useCurrencyDisplay().setMode('fiat')
  })

  it('默认按法币展示', () => {
    expect(useCurrencyDisplay().isFiat.value).toBe(true)
  })

  it('按充值倍率把美元金额折算成人民币', () => {
    const { usdToFiat } = useCurrencyDisplay()

    // 这条就是整个功能要解决的误读：看到「扣了 5」，其实只花了三毛八。
    expect(usdToFiat(5)).toBeCloseTo(5 / 13, 10)
    expect(usdToFiat(0)).toBe(0)
  })

  it('倍率缺失或损坏时折算退化为恒等，绝不除以 0', () => {
    for (const broken of [undefined, 0, -13, Number.NaN]) {
      publicSettings.value = { balance_recharge_multiplier: broken }
      const { usdToFiat, rechargeMultiplier } = useCurrencyDisplay()

      expect(rechargeMultiplier.value).toBe(1)
      expect(usdToFiat(5)).toBe(5)
    }

    publicSettings.value = null
    expect(useCurrencyDisplay().usdToFiat(5)).toBe(5)
  })

  it('倍率为 1 时隐藏切换器——两个口径数字相同，切换没有意义', () => {
    expect(useCurrencyDisplay().canSwitch.value).toBe(true)

    publicSettings.value = { balance_recharge_multiplier: 1 }
    expect(useCurrencyDisplay().canSwitch.value).toBe(false)
  })

  it('小额消费按 4 位有效数字展示，不会被截成 ¥0.00', () => {
    const { formatFiat } = useCurrencyDisplay()

    // 0.003 元这种量级如果固定两位小数就全变成 0 了。
    expect(normalize(formatFiat(0.003))).toContain('0.003')
    expect(normalize(formatFiat(0.25))).toContain('0.25')
    expect(normalize(formatFiat(12.5))).toContain('12.50')
    expect(normalize(formatFiat(null))).toContain('0.00')
  })

  it('美元按统一规则（≥ 1 两位小数）并显式带 $；指定小数位时仍可定宽对账', () => {
    const { formatUsd } = useCurrencyDisplay()

    expect(formatUsd(5)).toBe('$5.00')
    // 千分位照常，符号永远是 $，不会写成 USD1,300.00。
    expect(formatUsd(1300)).toBe('$1,300.00')
    expect(formatUsd(5, 6)).toBe('$5.000000')
    expect(formatUsd(52.4361, EXACT_DIGITS)).toBe('$52.4361')
    expect(formatUsd(null)).toBe('$0.00')
    expect(formatUsd(Number.NaN)).toBe('$0.00')
  })

  it('法币模式优先用服务端算好的精确值，而不是按充值倍率估算', () => {
    const { formatAmount } = useCurrencyDisplay()

    // 订阅扣费：服务端给的 0.25 才是真实花费，按 1/13 估算会得到 0.385。
    const withExact = normalize(formatAmount(5, 0.25))
    expect(withExact).toContain('0.25')
    expect(withExact).not.toContain('0.3846')

    // 缺精确值时回落到按充值倍率估算。
    expect(normalize(formatAmount(5))).toContain('0.3846')
  })

  it('美元模式下展示原始美元金额，不做任何折算', () => {
    const { formatAmount, setMode } = useCurrencyDisplay()
    setMode('usd')

    expect(formatAmount(5, 0.25, 6)).toBe('$5.000000')
  })

  it('切换会持久化，且 toggle 在两个口径间往返', () => {
    const { setMode, toggle, mode } = useCurrencyDisplay()

    setMode('usd')
    expect(mode.value).toBe('usd')
    expect(window.localStorage.getItem('currency-display-mode')).toBe('usd')

    toggle()
    expect(mode.value).toBe('fiat')
    expect(window.localStorage.getItem('currency-display-mode')).toBe('fiat')

    toggle()
    expect(mode.value).toBe('usd')
  })

  it('多个调用方共享同一份状态——切换器改一次要全站生效', () => {
    const a = useCurrencyDisplay()
    const b = useCurrencyDisplay()

    a.setMode('usd')

    expect(b.isFiat.value).toBe(false)
    expect(b.mode.value).toBe('usd')
  })

  it('倍率为 1 时强制按美元展示，不会把美元数字套上 ¥', () => {
    publicSettings.value = { balance_recharge_multiplier: 1 }
    const { isFiat, effectiveMode, mode, formatWallet, formatMixed, formatAmount } = useCurrencyDisplay()

    // 用户偏好仍然是 fiat，只是在这个站点上不生效。
    expect(mode.value).toBe('fiat')
    expect(effectiveMode.value).toBe('usd')
    expect(isFiat.value).toBe(false)
    expect(formatWallet(5)).toBe('$5.00')
    expect(formatMixed(5, 0.25)).toBe('$5.00')
    expect(formatAmount(5)).toBe('$5.00')
  })

  it('钱包金额按 1/m 精确折算', () => {
    const { formatWallet, setMode } = useCurrencyDisplay()

    expect(normalize(formatWallet(130))).toContain('10.00')
    setMode('usd')
    expect(formatWallet(130)).toBe('$130.00')
    // 美元模式的钱包金额（充值到账、余额）都是 $ + 千分位，同一页上只有一种写法。
    expect(formatWallet(1300)).toBe('$1,300.00')
    expect(formatWallet(159.61)).toBe('$159.61')
  })

  it('订阅金额按卡单价折算；缺单价时回落到美元，绝不按钱包单价猜', () => {
    const { formatSubscription } = useCurrencyDisplay()

    expect(normalize(formatSubscription(100, 0.045))).toContain('4.50')
    // 按钱包单价会得到 ¥7.69，高估将近一倍。
    expect(formatSubscription(100, undefined)).toBe('$100.00')
    expect(formatSubscription(100, 0)).toBe('$100.00')
  })

  it('混合金额只用服务端分桶值；额度为 0 缺字段按 ¥0，非 0 缺字段回落美元', () => {
    const { formatMixed, hasMixedFiat } = useCurrencyDisplay()

    expect(normalize(formatMixed(5, 0.25))).toContain('0.25')
    expect(normalize(formatMixed(0, undefined))).toContain('0.00')
    expect(formatMixed(5, undefined)).toBe('$5.00')
    expect(hasMixedFiat(5, 0.25)).toBe(true)
    expect(hasMixedFiat(0, undefined)).toBe(true)
    expect(hasMixedFiat(5, undefined)).toBe(false)
  })

  it('人民币与额度互换：缺单价按钱包 1/m，有单价按单价', () => {
    const { creditsFromFiat, fiatFromCredits } = useCurrencyDisplay()

    expect(creditsFromFiat(10)).toBeCloseTo(130, 10)
    expect(creditsFromFiat(4.5, 0.045)).toBeCloseTo(100, 10)
    expect(fiatFromCredits(130)).toBeCloseTo(10, 10)
    expect(fiatFromCredits(100, 0.045)).toBeCloseTo(4.5, 10)
    expect(creditsFromFiat(Number.NaN)).toBe(0)
  })

  it('后端缺混合金额的人民币值时整站退回美元并隐藏切换器，避免 ¥ $ 混排', () => {
    const { isFiat, canSwitch, formatWallet } = useCurrencyDisplay()

    // 额度为 0 时人民币字段按 omitempty 省略，属于正常情况
    reportMixedFiat(0, undefined)
    reportMixedFiat(5, 0.25)
    expect(isFiat.value).toBe(true)

    reportMixedFiat(5, undefined)
    expect(isFiat.value).toBe(false)
    expect(canSwitch.value).toBe(false)
    expect(formatWallet(130)).toBe('$130.00')
  })

  it('官方价人民币模式按展示汇率换成 ¥；没有汇率时返回 null 由调用方隐藏', () => {
    publicSettings.value = { balance_recharge_multiplier: 13, official_price_cny_rate: 7.2 }
    const { formatOfficial, setMode } = useCurrencyDisplay()

    expect(normalize(formatOfficial(10) ?? '')).toContain('72.00')
    setMode('usd')
    expect(formatOfficial(10)).toBe('$10.00')
    setMode('fiat')

    publicSettings.value = { balance_recharge_multiplier: 13 }
    expect(useCurrencyDisplay().formatOfficial(10)).toBeNull()
  })
})
