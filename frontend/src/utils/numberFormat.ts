/**
 * 数字展示规则的唯一出处（纯函数，不依赖 Vue / i18n / store）。
 *
 * 全站的数字都走这里，规则只有三条：
 * 1. 金额：绝对值 ≥ 1 固定两位小数；0 < 绝对值 < 1 保留 4 位有效数字、不补尾随 0
 *    （至少显示两位小数，保证 0.5 读作 0.50）；0 显示 0.00；一律带千分位。
 * 2. 计数（请求数等）：整数千分位。
 * 3. 大计数（Token）：K / M 缩写保留 1 位小数，缩写后的数同样带千分位。
 *
 * 千分位和小数点固定用 en-US：站点只有中英两种语言，两者写法一致；
 * 固定下来后截图、测试和跨语言切换都不会因 <html lang> 变化而抖动。
 */

const LOCALE = 'en-US'

/** 4 位有效数字。 */
const SIGNIFICANT_DIGITS = 4
/** 小数位上限：再小的数没有展示意义，同时避免 Intl 取到非法位数。 */
const MAX_FRACTION_DIGITS = 12
/** 金额至少显示的小数位。 */
const MIN_MONEY_FRACTION_DIGITS = 2
/** 单价的小数位上限：足以还原常见报价（如 0.015625），再多没有展示意义。 */
const UNIT_PRICE_MAX_FRACTION_DIGITS = 6
/** 精确值（title / tooltip）的小数位上限。 */
const EXACT_MAX_FRACTION_DIGITS = 6

function isFiniteNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value)
}

const formatterCache = new Map<string, Intl.NumberFormat>()

function groupedFormatter(minimumFractionDigits: number, maximumFractionDigits: number): Intl.NumberFormat {
  const key = `${minimumFractionDigits}:${maximumFractionDigits}`
  let formatter = formatterCache.get(key)
  if (!formatter) {
    formatter = new Intl.NumberFormat(LOCALE, {
      useGrouping: true,
      minimumFractionDigits,
      maximumFractionDigits
    })
    formatterCache.set(key, formatter)
  }
  return formatter
}

/** 按固定小数位格式化，带千分位。非法输入按 0 处理。 */
export function formatFixed(value: number | null | undefined, fractionDigits: number): string {
  const n = isFiniteNumber(value) ? value : 0
  const digits = Math.min(Math.max(0, Math.trunc(fractionDigits)), MAX_FRACTION_DIGITS)
  return groupedFormatter(digits, digits).format(n)
}

/** 计数：整数千分位（请求数、次数、积分等）。小数会四舍五入。 */
export function formatCount(value: number | null | undefined): string {
  return formatFixed(value, 0)
}

export interface CompactCountOptions {
  /** 为 false 时最高只缩写到 M（请求数口径）；默认 true，≥ 10 亿显示 B。 */
  allowBillions?: boolean
}

/**
 * 大计数缩写：K / M（/ B），缩写后保留 1 位小数并带千分位，如 `74,935.4M`。
 * 小于 1000 的数不缩写，原样输出。
 */
export function formatCompactCount(value: number | null | undefined, options?: CompactCountOptions): string {
  const n = isFiniteNumber(value) ? value : 0
  const abs = Math.abs(n)
  const allowBillions = options?.allowBillions !== false

  const units: Array<[number, string]> = [
    [1_000_000_000, 'B'],
    [1_000_000, 'M'],
    [1_000, 'K']
  ]
  const candidates = allowBillions ? units : units.slice(1)

  for (let i = 0; i < candidates.length; i++) {
    const [base, suffix] = candidates[i]
    if (abs < base) continue
    const sign = n < 0 ? '-' : ''
    const text = formatFixed(abs / base, 1)
    // 取整后进位（999,950 → 1,000.0K）要升到上一档，写成 1.0M。
    const upper = candidates[i - 1]
    if (upper && Number(text.replace(/,/g, '')) * base >= upper[0]) {
      return `${sign}${formatFixed(abs / upper[0], 1)}${upper[1]}`
    }
    return `${sign}${text}${suffix}`
  }
  // 小于 1000 不缩写：整数原样，偶尔出现的小数（图表刻度）最多保留 1 位。
  return groupedFormatter(0, 1).format(n)
}

export interface MoneyDigitsOptions {
  /**
   * 显式指定小数位（精确值场景：title / tooltip / 对账）。
   * 指定后不再按量级自适应，仍然带千分位。
   */
  fractionDigits?: number
  /**
   * 精确值（title / tooltip）：把界面上收口掉的位数放回来，保留旧版 4 位小数的信息量。
   * 在统一规则的基础上放宽小数位上限（≥ 1 最多 6 位，极小的数按 4 位有效数字再多给几位），
   * 末尾多余的 0 不显示，至少两位小数。52.4361 → "52.4361"，0.5 → "0.50"，0.000123456 → "0.0001235"。
   */
  exact?: boolean
  /**
   * 单价（每百万 Token / 每次请求）：价目表上的数字是报价，不能被四舍五入吞掉信息。
   * 不分大小，一律保留到 6 位小数、去掉多余尾随 0（至少两位小数）：
   * 1.875 → "1.875"，0.015625 → "0.015625"，3 → "3.00"。
   */
  unitPrice?: boolean
}

/**
 * 金额的数字部分（不含货币符号），带符号和千分位。
 *
 *   17717.09 → "17,717.09"      1234.5 → "1,234.50"
 *   0.0004   → "0.0004"         0.5    → "0.50"
 *   0.99996  → "1.00"           0      → "0.00"
 *   -3.2     → "-3.20"
 */
export function formatMoneyNumber(value: number | null | undefined, options: MoneyDigitsOptions = {}): string {
  const n = isFiniteNumber(value) ? value : 0
  const abs = Math.abs(n)
  const text = formatMoneyDigits(abs, options)
  // 取整后全是 0（如 -0.00001 按两位小数）时不带负号，避免出现 -0.00。
  return n < 0 && /[1-9]/.test(text) ? `-${text}` : text
}

function formatMoneyDigits(abs: number, options: MoneyDigitsOptions): string {
  if (isFiniteNumber(options.fractionDigits)) return formatFixed(abs, options.fractionDigits)
  if (abs === 0) return '0.00'
  if (options.unitPrice) return groupedFormatter(MIN_MONEY_FRACTION_DIGITS, UNIT_PRICE_MAX_FRACTION_DIGITS).format(abs)

  // 用 toExponential 取 4 位有效数字后的指数：它已经处理了进位，
  // 0.99996 会得到 1.000e+0，直接落进「≥ 1」分支，不会写成 1.0000。
  const exponent = Number(abs.toExponential(SIGNIFICANT_DIGITS - 1).split('e')[1])

  if (options.exact) {
    const maxDigits = Math.min(Math.max(EXACT_MAX_FRACTION_DIGITS, -exponent + SIGNIFICANT_DIGITS - 1), MAX_FRACTION_DIGITS)
    return groupedFormatter(MIN_MONEY_FRACTION_DIGITS, maxDigits).format(abs)
  }

  if (exponent >= 0) {
    return groupedFormatter(MIN_MONEY_FRACTION_DIGITS, MIN_MONEY_FRACTION_DIGITS).format(abs)
  }

  const maxDigits = Math.min(-exponent + SIGNIFICANT_DIGITS - 1, MAX_FRACTION_DIGITS)
  return groupedFormatter(MIN_MONEY_FRACTION_DIGITS, maxDigits).format(abs)
}

/** 站点结算法币符号。与 useCurrencyDisplay 的 FIAT_CURRENCY（CNY）对应。 */
export const FIAT_SYMBOL = '¥'
export const USD_SYMBOL = '$'

function withSymbol(symbol: string, numberText: string): string {
  // 负号放在符号前：-$12.50，而不是 $-12.50。
  return numberText.startsWith('-') ? `-${symbol}${numberText.slice(1)}` : `${symbol}${numberText}`
}

/** 美元金额：`$17,717.09`。 */
export function formatUsdAmount(value: number | null | undefined, options?: MoneyDigitsOptions): string {
  return withSymbol(USD_SYMBOL, formatMoneyNumber(value, options))
}

/** 人民币金额：`¥17,717.09`。 */
export function formatCnyAmount(value: number | null | undefined, options?: MoneyDigitsOptions): string {
  return withSymbol(FIAT_SYMBOL, formatMoneyNumber(value, options))
}

/**
 * 按币种代码展示金额：USD / CNY 走统一金额规则，其他币种按 Intl 的币种习惯（en-US）。
 * 供用户端使用；后台沿用 utils/format 的 formatCurrency。
 */
export function formatCurrencyAmount(value: number | null | undefined, currency: string = 'USD'): string {
  const code = currency.toUpperCase()
  if (code === 'USD') return formatUsdAmount(value)
  if (code === 'CNY') return formatCnyAmount(value)
  return new Intl.NumberFormat(LOCALE, { style: 'currency', currency: code }).format(isFiniteNumber(value) ? value : 0)
}

/** 耗时：< 1s 显示毫秒，否则显示秒（两位小数）。 */
export function formatDurationMs(ms: number | null | undefined): string {
  if (!isFiniteNumber(ms)) return '-'
  if (Math.abs(ms) < 1000) return `${Math.round(ms)}ms`
  return `${formatFixed(ms / 1000, 2)}s`
}

export interface NumericParts {
  /** 前导负号（或正号）。 */
  sign: string
  /** 数字前的货币符号 / 前缀，如 `$`、`¥`、`≈¥`、`USD`。 */
  prefix: string
  /** 数字本体，含千分位和小数点。 */
  digits: string
  /** 数字后的单位，如 `M`、`s`、`RPM`、`%`。 */
  suffix: string
}

const NUMERIC_PATTERN = /^([+\-−]?)\s*([^\d.,+\-−]*?)\s*(\d[\d,]*(?:\.\d+)?)\s*(\S.*)?$/

/**
 * 把「符号 + 数字 + 单位」的展示串拆开，供 NumText 给货币符号和单位单独上样式。
 * 不是这种形状（如 `-`、`∞`、`1,200 / 5,000`）时返回 null，调用方原样渲染。
 */
export function splitNumeric(text: string): NumericParts | null {
  const match = NUMERIC_PATTERN.exec(text.trim())
  if (!match) return null
  const suffix = match[4] ?? ''
  // 后缀里还有数字说明是复合串（区间、比值），拆开只会弄乱，原样渲染。
  if (/\d/.test(suffix)) return null
  return { sign: match[1], prefix: match[2].trim(), digits: match[3], suffix }
}
