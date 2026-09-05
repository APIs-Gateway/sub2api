import { computed, ref } from 'vue'

import { getLocale } from '@/i18n'
import { useAppStore } from '@/stores/app'

/**
 * 用量金额的展示口径。
 *
 * 背景：站内扣的那个数是「模型官方价 × 分组倍率」，以美元计价——产品里一直叫
 * 余额（USD）或套餐额度，充值时 1 元能买到十几美元。于是「本次扣 $5」会被读成
 * 「花了 5 美元 ≈ 36 元」，实际只有几毛钱，差出两个数量级。
 *
 * - fiat：按人民币展示（默认），直接给出用户真正付出的钱。
 * - usd：按美元展示，即历史行为，方便和官方价对照、逐条对账。
 */
export type CurrencyMode = 'fiat' | 'usd'

const STORAGE_KEY = 'currency-display-mode'

/** 默认按人民币展示：直接消除「扣了 5 就是 5 美元」这个最常见的误读。 */
const DEFAULT_MODE: CurrencyMode = 'fiat'

/**
 * 站点结算法币。与后端 payment.DefaultPaymentCurrency 一致。
 * 充值倍率 BALANCE_RECHARGE_MULTIPLIER 就是以这个币种计价的，
 * 所以折算结果的单位必然是它，不能跟着支付时选的币种走。
 */
const FIAT_CURRENCY = 'CNY'

function readPersistedMode(): CurrencyMode {
  if (typeof window === 'undefined') return DEFAULT_MODE
  try {
    const stored = window.localStorage.getItem(STORAGE_KEY)
    if (stored === 'fiat' || stored === 'usd') return stored
  } catch (error) {
    console.warn('Failed to read currency display mode:', error)
  }
  return DEFAULT_MODE
}

/**
 * 模块级单例：切换器改一次，全站所有引用这个 composable 的组件同步更新。
 * 放在 composable 函数外面是刻意的——每个组件各持一份 ref 会让切换只影响局部。
 */
const mode = ref<CurrencyMode>(readPersistedMode())

export function useCurrencyDisplay() {
  const appStore = useAppStore()

  /**
   * 1 元能买到多少美元余额。缺省/损坏时为 1，此时折算退化为恒等（即不折算），
   * 与后端 normalizeBalanceRechargeMultiplier 的兜底口径一致。
   */
  const rechargeMultiplier = computed(() => {
    const raw = appStore.cachedPublicSettings?.balance_recharge_multiplier
    return typeof raw === 'number' && Number.isFinite(raw) && raw > 0 ? raw : 1
  })

  /** 倍率为 1 时两个币种是同一个数，展示切换没有意义，隐藏切换器。 */
  const canSwitch = computed(() => rechargeMultiplier.value !== 1)

  const isFiat = computed(() => mode.value === 'fiat')

  function setMode(next: CurrencyMode) {
    mode.value = next
    if (typeof window === 'undefined') return
    try {
      window.localStorage.setItem(STORAGE_KEY, next)
    } catch (error) {
      console.warn('Failed to persist currency display mode:', error)
    }
  }

  function toggle() {
    setMode(mode.value === 'fiat' ? 'usd' : 'fiat')
  }

  /**
   * 按充值倍率把美元金额折算成人民币。
   *
   * 这对钱包余额、充值到账这类「就是按充值倍率买来的」金额是精确的。
   * 用量记录不要用它——那些扣费可能来自套餐额度（单价便宜得多），必须用服务端
   * 随每条记录下发的 fiat_cost，否则会把订阅用户的花费高估近一倍。
   */
  function usdToFiat(usd: number | null | undefined): number {
    if (typeof usd !== 'number' || !Number.isFinite(usd)) return 0
    return usd / rechargeMultiplier.value
  }

  /**
   * 格式化人民币金额。小额消费低至 0.003 元，固定两位小数会把它们全部显示成
   * ¥0.00，所以按量级动态调整小数位。
   */
  function formatFiat(amount: number | null | undefined): string {
    const value = typeof amount === 'number' && Number.isFinite(amount) ? amount : 0
    const abs = Math.abs(value)
    let fractionDigits = 2
    if (abs > 0 && abs < 0.01) fractionDigits = 4
    else if (abs > 0 && abs < 1) fractionDigits = 3

    return new Intl.NumberFormat(getLocale(), {
      style: 'currency',
      currency: FIAT_CURRENCY,
      minimumFractionDigits: fractionDigits,
      maximumFractionDigits: fractionDigits
    }).format(value)
  }

  /**
   * 格式化美元金额。保持既有的定宽小数方便逐条对账，并显式带上 $——
   * 这个数就是以美元计价的，去掉符号反而会让人以为是另一种单位。
   */
  function formatUsd(usd: number | null | undefined, fractionDigits = 4): string {
    const value = typeof usd === 'number' && Number.isFinite(usd) ? usd : 0
    return `$${value.toFixed(fractionDigits)}`
  }

  /**
   * 按当前模式展示一笔金额。
   *
   * @param usd       美元金额（站内扣费口径：官方价 × 分组倍率）
   * @param fiatValue 服务端算好的人民币金额。缺省时回落到按充值倍率折算——
   *                  对钱包余额精确，对套餐额度会偏高，所以有精确值就一定要传。
   */
  function formatAmount(
    usd: number | null | undefined,
    fiatValue?: number | null,
    fractionDigits = 4
  ): string {
    if (!isFiat.value) return formatUsd(usd, fractionDigits)
    const fiat =
      typeof fiatValue === 'number' && Number.isFinite(fiatValue) && fiatValue !== 0
        ? fiatValue
        : usdToFiat(usd)
    return formatFiat(fiat)
  }

  return {
    mode,
    isFiat,
    canSwitch,
    fiatCurrency: FIAT_CURRENCY,
    rechargeMultiplier,
    setMode,
    toggle,
    usdToFiat,
    formatFiat,
    formatUsd,
    formatAmount
  }
}
