import { computed, ref } from 'vue'

import { getLocale } from '@/i18n'
import { useAppStore } from '@/stores/app'

/**
 * 站内额度的展示口径。
 *
 * 背景：站内额度长期以 "$" 呈现，但它既不是美元也不是官方价美元——结算时把
 * 「模型官方价 × 分组倍率」累加进钱包和订阅额度，而 1 单位法币能换到十几个额度。
 * 于是「本次扣 5」会被读成「花了 5 美元」，实际只有几毛钱，差出两个数量级。
 *
 * - fiat：按法币展示（默认）。额度换算回用户真正付出的钱。
 * - credit：按站内额度展示，即历史行为，方便对账和排查。
 */
export type CurrencyMode = 'fiat' | 'credit'

const STORAGE_KEY = 'currency-display-mode'

/** 默认按法币展示：直接消除「扣了 5 就是 5 美元」这个最常见的误读。 */
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
    if (stored === 'fiat' || stored === 'credit') return stored
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
   * 1 单位法币能兑多少额度。缺省/损坏时为 1，此时折算退化为恒等（即不折算），
   * 与后端 normalizeBalanceRechargeMultiplier 的兜底口径一致。
   */
  const rechargeMultiplier = computed(() => {
    const raw = appStore.cachedPublicSettings?.balance_recharge_multiplier
    return typeof raw === 'number' && Number.isFinite(raw) && raw > 0 ? raw : 1
  })

  /** 倍率为 1 时法币和额度是同一个数，展示切换没有意义，隐藏切换器。 */
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
    setMode(mode.value === 'fiat' ? 'credit' : 'fiat')
  }

  /**
   * 按充值倍率把额度折算成法币。
   *
   * 这对钱包余额、充值到账这类「就是按充值倍率买来的」额度是精确的。
   * 用量记录不要用它——那些额度可能来自订阅卡（单价便宜得多），必须用服务端
   * 随每条记录下发的 fiat_cost，否则会把订阅用户的花费高估近一倍。
   */
  function creditToFiat(credits: number | null | undefined): number {
    if (typeof credits !== 'number' || !Number.isFinite(credits)) return 0
    return credits / rechargeMultiplier.value
  }

  /**
   * 格式化法币金额。小额消费低至 0.003 元，固定两位小数会把它们全部显示成
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

  /** 格式化站内额度。保持既有的定宽小数，方便逐条对账。 */
  function formatCredit(credits: number | null | undefined, fractionDigits = 4): string {
    const value = typeof credits === 'number' && Number.isFinite(credits) ? credits : 0
    return value.toFixed(fractionDigits)
  }

  /**
   * 按当前模式展示一笔金额。
   *
   * @param credits   站内额度金额
   * @param fiatValue 服务端算好的法币金额。缺省时回落到按充值倍率折算——
   *                  对钱包额度精确，对订阅额度会偏高，所以有精确值就一定要传。
   */
  function formatAmount(
    credits: number | null | undefined,
    fiatValue?: number | null,
    fractionDigits = 4
  ): string {
    if (!isFiat.value) return formatCredit(credits, fractionDigits)
    const fiat =
      typeof fiatValue === 'number' && Number.isFinite(fiatValue) && fiatValue !== 0
        ? fiatValue
        : creditToFiat(credits)
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
    creditToFiat,
    formatFiat,
    formatCredit,
    formatAmount
  }
}
