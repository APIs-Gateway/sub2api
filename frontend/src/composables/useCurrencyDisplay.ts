import { computed, ref } from 'vue'

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

/**
 * 金额格式化用的语言。读 <html lang>（i18n 切换语言时会同步设置），而不是 import
 * '@/i18n'：后者在模块加载时就执行 createI18n，会让所有间接引用本 composable 的
 * 模块（接口层、GroupBadge 等）都背上整个 i18n 实例。
 */
function currentLocale(): string {
  if (typeof document === 'undefined') return 'zh-CN'
  return document.documentElement.getAttribute('lang') || 'zh-CN'
}

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

/**
 * 后端是否缺少混合金额的人民币值（*_fiat 字段）。
 *
 * 余额这类钱包金额前端能精确折算，但今日/累计花费这类混合金额只能用后端分桶值。
 * 后端缺字段时（旧版本、查询失败），如果只把缺的那几项回落成 $，同一页上就会
 * ¥ 和 $ 混排。所以一旦发现缺失，整站退回按美元展示，并隐藏切换器。
 */
const fiatDataMissing = ref(false)

/**
 * 接口层拿到混合金额后调用：额度非 0 却没有人民币值，说明后端不支持，整站退回美元。
 * 额度为 0 时后端按 omitempty 省略人民币字段，属于正常情况。
 */
export function reportMixedFiat(credits: number | null | undefined, fiat: number | null | undefined) {
  if (fiatDataMissing.value) return
  if (typeof fiat === 'number' && Number.isFinite(fiat)) return
  if (typeof credits === 'number' && credits !== 0) fiatDataMissing.value = true
}

/** 仅供测试复位。 */
export function resetFiatDataMissingForTest() {
  fiatDataMissing.value = false
}

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

  /**
   * 倍率为 1 时两个币种是同一个数，展示切换没有意义，隐藏切换器；
   * 后端缺人民币数据时只能按美元展示，同样隐藏。
   */
  const canSwitch = computed(() => rechargeMultiplier.value !== 1 && !fiatDataMissing.value)

  /**
   * 实际生效的展示口径。倍率为 1（free 站，没有充值）时额度就按美元计价，
   * 没有「付了多少人民币」可言，强制按美元展示——否则会把美元数字直接
   * 套上 ¥ 符号显示出来。
   */
  const effectiveMode = computed<CurrencyMode>(() => (canSwitch.value ? mode.value : 'usd'))

  const isFiat = computed(() => effectiveMode.value === 'fiat')

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

    return new Intl.NumberFormat(currentLocale(), {
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

  /**
   * 官方价展示汇率（后台设置 OFFICIAL_PRICE_CNY_RATE）：只用来把模型官方美元价
   * 换算成人民币给用户对照，与扣费无关。缺省 / 无效时为 0，表示不提供。
   */
  const officialCnyRate = computed(() => {
    const raw = appStore.cachedPublicSettings?.official_price_cny_rate
    return typeof raw === 'number' && Number.isFinite(raw) && raw > 0 ? raw : 0
  })

  /**
   * 官方价（美元）的展示。人民币模式下按官方价展示汇率换成 ¥，让全站只有一种货币；
   * 后端未提供汇率时返回 null，调用方应隐藏这一项，而不是混排一个 $。
   * 美元模式下保持原样。
   */
  function formatOfficial(usd: number | null | undefined, fractionDigits = 4): string | null {
    if (!isFiat.value) return formatUsd(usd, fractionDigits)
    if (!officialCnyRate.value) return null
    const value = typeof usd === 'number' && Number.isFinite(usd) ? usd : 0
    return formatFiat(value * officialCnyRate.value)
  }

  /** 钱包里 1 个额度值多少人民币：充值时 1 元买 m 个额度。 */
  const walletFiatPerCredit = computed(() => 1 / rechargeMultiplier.value)

  function isPositiveNumber(value: unknown): value is number {
    return typeof value === 'number' && Number.isFinite(value) && value > 0
  }

  /**
   * 钱包类金额：余额、充值到账、兑换码、签到奖励、余额提醒阈值等。
   * 这些额度全部是按充值倍率买来的，÷ m 是精确值。
   */
  function formatWallet(credits: number | null | undefined, fractionDigits = 2): string {
    if (!isFiat.value) return formatUsd(credits, fractionDigits)
    return formatFiat(usdToFiat(credits))
  }

  /**
   * 订阅卡金额：日/周/月额度与已用量。按该卡的单价 u(D)（后端 fiat_per_credit）折算。
   * 拿不到单价时宁可按美元展示，也不要按钱包单价猜——那会高估将近一倍。
   */
  function formatSubscription(
    credits: number | null | undefined,
    fiatPerCredit: number | null | undefined,
    fractionDigits = 2
  ): string {
    if (!isFiat.value || !isPositiveNumber(fiatPerCredit)) return formatUsd(credits, fractionDigits)
    const value = typeof credits === 'number' && Number.isFinite(credits) ? credits : 0
    return formatFiat(value * fiatPerCredit)
  }

  /**
   * 混合来源的花费汇总（今日/累计、按 Key、按模型、趋势）。钱包和订阅卡扣的额度
   * 单价不同，只能用服务端分桶折算好的人民币值（*_fiat 字段）。
   *
   * 后端的 *_fiat 字段带 omitempty：额度为 0 时人民币字段缺省，按 ¥0 处理；
   * 额度非 0 却没有人民币值，说明后端没有提供（旧版本或查询失败），回落到美元，
   * 不按充值倍率估算。
   */
  function formatMixed(
    credits: number | null | undefined,
    fiatValue: number | null | undefined,
    fractionDigits = 4
  ): string {
    if (!isFiat.value) return formatUsd(credits, fractionDigits)
    if (typeof fiatValue === 'number' && Number.isFinite(fiatValue)) return formatFiat(fiatValue)
    if (!credits) return formatFiat(0)
    return formatUsd(credits, fractionDigits)
  }

  /** 混合金额在当前口径下是否能精确给出人民币值（图表等需要纯数字的地方用）。 */
  function hasMixedFiat(credits: number | null | undefined, fiatValue: number | null | undefined): boolean {
    if (!isFiat.value) return false
    return (typeof fiatValue === 'number' && Number.isFinite(fiatValue)) || !credits
  }

  /**
   * 把用户填的人民币换算回额度（输入框提交前用）。
   * @param fiatPerCredit 1 个额度值多少人民币；缺省时按钱包单价 1/m。
   */
  function creditsFromFiat(fiat: number, fiatPerCredit?: number | null): number {
    if (!Number.isFinite(fiat)) return 0
    const rate = isPositiveNumber(fiatPerCredit) ? fiatPerCredit : walletFiatPerCredit.value
    return fiat / rate
  }

  /** creditsFromFiat 的反向：额度 × 单价。 */
  function fiatFromCredits(credits: number | null | undefined, fiatPerCredit?: number | null): number {
    if (typeof credits !== 'number' || !Number.isFinite(credits)) return 0
    const rate = isPositiveNumber(fiatPerCredit) ? fiatPerCredit : walletFiatPerCredit.value
    return credits * rate
  }

  return {
    mode,
    effectiveMode,
    isFiat,
    canSwitch,
    fiatCurrency: FIAT_CURRENCY,
    rechargeMultiplier,
    setMode,
    toggle,
    usdToFiat,
    formatFiat,
    formatUsd,
    formatAmount,
    walletFiatPerCredit,
    officialCnyRate,
    formatOfficial,
    formatWallet,
    formatSubscription,
    formatMixed,
    hasMixedFiat,
    creditsFromFiat,
    fiatFromCredits
  }
}
