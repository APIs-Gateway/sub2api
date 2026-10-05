/**
 * 价格配置页的金额格式：¥ 为主，$ 是小字。
 * 切到美元口径（或站点没有充值倍率）时只显示 $，不重复。
 */
import { useCurrencyDisplay } from '@/composables/useCurrencyDisplay'

const UNIT = { unitPrice: true } as const

export interface MoneyPair {
  main: string
  /** 小字：对应的美元金额；已经是美元口径时为 null */
  sub: string | null
}

export function usePricingFormat() {
  const { isFiat, formatWallet, formatUsd, formatOfficial } = useCurrencyDisplay()

  /**
   * 用户实付的单价：美元额度按充值倍率折成人民币。
   * 默认是格子里用的紧凑写法（小于 1 的数保留 4 位有效数字）；exact 给悬停提示用，保留完整的单价精度。
   */
  function paid(usd: number | null | undefined, exact = false): MoneyPair {
    const digits = exact ? UNIT : undefined
    const dollars = formatUsd(usd, digits)
    if (!isFiat.value) return { main: dollars, sub: null }
    return { main: formatWallet(usd, digits), sub: dollars }
  }

  /** 官方参考价：人民币口径按官方价展示汇率换算；没有配置汇率时直接显示美元，不混排。 */
  function official(usd: number | null | undefined): MoneyPair {
    const dollars = formatUsd(usd, UNIT)
    if (!isFiat.value) return { main: dollars, sub: null }
    const fiat = formatOfficial(usd, UNIT)
    return fiat === null ? { main: dollars, sub: null } : { main: fiat, sub: dollars }
  }

  return { isFiat, paid, official }
}
