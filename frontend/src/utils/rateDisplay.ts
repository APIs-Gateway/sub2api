/**
 * 用户端「展示倍率」的纯函数（等效倍率口径）。
 *
 * 站内扣的额度 = 官方价 × 分组倍率 r，额度以美元计价。钱包额度是按充值倍率 m 买来的
 * （¥1 = m 个额度），套餐额度按卡的单价 u 算（¥u = 1 个额度）。主流站点的倍率是
 * 「¥1 兑 $1」口径，所以人民币模式下展示的倍率要折成每 $1 官方价实付多少元：
 *
 * - 余额倍率（主倍率）= r ÷ m
 * - 套餐倍率（次倍率）= r × u；「套餐低至」用最便宜的单价 u_min
 * - 美元模式、m = 1 的站点：原样显示 r，不出现套餐倍率
 *
 * 组件不要自己除 m / 乘 u，统一走这里；响应式封装见 composables/useRateDisplay.ts。
 * 这里不依赖 Vue / i18n / store，方便用表驱动测试把数字钉死。
 */

/** 去浮点噪声：保留 10 位有效数字（与 utils/modelCatalog.ts 的 clean 同口径）。 */
function clean(n: number): number {
  return Number(n.toPrecision(10))
}

function isFiniteNumber(v: unknown): v is number {
  return typeof v === 'number' && Number.isFinite(v)
}

function isPositive(v: unknown): v is number {
  return isFiniteNumber(v) && v > 0
}

/** 倍率展示的有效数字位数。 */
const RATE_SIGNIFICANT_DIGITS = 3

/** 超过这个量级不再做有效数字取整，原样输出（现实里不会出现，只为不产生奇怪的科学计数法）。 */
const RATE_FORMAT_MAX = 1e15

/**
 * 格式化倍率数值，不带 `x` 后缀（`x` 在 i18n 模板里，如 `{rate}x 倍率`）。
 *
 * 规则：先按 10 位有效数字去浮点噪声，再取 3 位有效数字、四舍五入（half up，
 * 在十进制数字上进位，不吃二进制浮点的误差）、去小数部分的末尾 0。
 * 1/13 → `0.0769`，1.4/13 → `0.108`，5 × 0.0467 = 0.2335 → `0.234`（不是 0.233）。
 * 整数部分超过 3 位时保留全部整数位（1234.5 → `1235`），不会把整数位截成 0。
 * 非有限数返回 `-`。
 */
export function formatRate(x: number | null | undefined): string {
  if (!isFiniteNumber(x)) return '-'
  if (x === 0) return '0'
  if (Math.abs(x) >= RATE_FORMAT_MAX) return String(clean(x))

  // toExponential(9) 恰好是 10 位有效数字，同时完成「去浮点噪声」。
  const [mantissa, exponent] = Math.abs(x).toExponential(9).split('e')
  const digits = mantissa.replace('.', '')
  let exp = Number(exponent)

  const sig = Math.max(RATE_SIGNIFICANT_DIGITS, exp + 1)
  const kept = digits.slice(0, sig).padEnd(sig, '0')
  // 第 sig+1 位 ≥ 5 即进位；已经没有后续数字时（sig ≥ 10）不进位。
  const roundUp = digits.charAt(sig) >= '5'

  let rounded = String(Number(kept) + (roundUp ? 1 : 0))
  if (rounded.length > sig) {
    // 999 → 1000：多出一位，指数加一，保持 sig 位。
    exp += 1
    rounded = rounded.slice(0, sig)
  }

  const intLen = exp + 1
  let intPart: string
  let fraction: string
  if (intLen <= 0) {
    // 0.0769：整数部分只有 0，小数点后先补 -exp-1 个 0。
    intPart = '0'
    fraction = '0'.repeat(-intLen) + rounded
  } else {
    const padded = rounded.padEnd(intLen, '0')
    intPart = padded.slice(0, intLen)
    fraction = padded.slice(intLen)
  }
  // 只去小数部分末尾的 0，整数部分的 0（100）保留。
  fraction = fraction.replace(/0+$/, '')
  const text = fraction ? `${intPart}.${fraction}` : intPart
  return x < 0 ? `-${text}` : text
}

/**
 * 原始倍率（美元模式、m = 1 的站点）的展示：只去浮点噪声，不取有效数字，
 * 与改动前 `${rate}x` / ModelCatalogRow 的写法逐字一致。非有限数返回 `-`。
 */
export function formatRawRate(r: number | null | undefined): string {
  if (!isFiniteNumber(r)) return '-'
  return String(clean(r))
}

/** 展示倍率需要的上下文。组件里用 useRateDisplay().ctx 取，不要手填。 */
export interface RateContext {
  /** 人民币模式（useCurrencyDisplay().isFiat；美元模式、m = 1 的站点为 false）。 */
  isFiat: boolean
  /** 充值倍率 m（公开设置 balance_recharge_multiplier；缺省或非法按 1）。 */
  rechargeMultiplier: number
  /** 套餐最低单价 u_min（/subscriptions/pricing 的 u_min，见 resolvePlanUnitMin）；没取到为 null。 */
  planUnitMin: number | null
  /** 订阅可买：公开设置 payment_enabled !== false。 */
  paymentEnabled: boolean
  /** 生效套餐卡的精确单价 u_card（卡上的 fiat_per_credit）；没有生效卡为 null / 缺省。 */
  cardUnit?: number | null
}

function rechargeMultiplierOf(ctx: RateContext): number {
  return isPositive(ctx.rechargeMultiplier) ? ctx.rechargeMultiplier : 1
}

/** 是否按等效倍率展示：人民币模式，且 m ≠ 1（m = 1 时两个口径是同一个数，没有换算可言）。 */
function isEquivalentMode(ctx: RateContext): boolean {
  return ctx.isFiat && rechargeMultiplierOf(ctx) !== 1
}

/**
 * 从 /subscriptions/pricing 取套餐最低单价 u_min。
 *
 * 响应里的 u_min 是 SUBSCRIPTION_MAX_PLAN_RATIO（每日额度达到 d_floor 之后的单价，最便宜），
 * u_max 是 SUBSCRIPTION_MIN_PLAN_RATIO（最小档，最贵）。后端算单价时会把结果夹在
 * [min(u_min, u_max), max(u_min, u_max)] 里（配置写反时也成立），所以这里也取两者较小的。u_min 非正 / 缺失返回 null（R2 条件 b）。
 */
export function resolvePlanUnitMin(
  bounds: { u_min?: number | null; u_max?: number | null } | null | undefined
): number | null {
  if (!bounds || !isPositive(bounds.u_min)) return null
  return isPositive(bounds.u_max) ? Math.min(bounds.u_min, bounds.u_max) : bounds.u_min
}

/** 生效套餐卡的精确单价：取第一张生效且带 fiat_per_credit 的卡（同一时间只有一张）。 */
export function activeCardUnit(
  subscriptions: ReadonlyArray<{ status?: string; fiat_per_credit?: number | null }> | null | undefined
): number | null {
  for (const sub of subscriptions ?? []) {
    if (sub.status === 'active' && isPositive(sub.fiat_per_credit)) return sub.fiat_per_credit
  }
  return null
}

/**
 * 单价 u 能不能用来展示套餐倍率（R2 的 a、c、d）：
 * (a) 人民币模式且 m ≠ 1；(c) 订阅可买；(d) 严格更低：m × u < 1。
 * 条件 (b)（u_min 取得到）由 resolvePlanUnitMin 保证。
 */
export function planUnitUsable(u: number | null | undefined, ctx: RateContext): u is number {
  return (
    isEquivalentMode(ctx) &&
    ctx.paymentEnabled &&
    isPositive(u) &&
    clean(rechargeMultiplierOf(ctx) * u) < 1
  )
}

/** 套餐倍率是否出现：u_min 取得到且通过 planUnitUsable。价格页的「套餐倍率」列据此整列显示 / 隐藏。 */
export function planRateAvailable(ctx: RateContext): boolean {
  return planUnitUsable(ctx.planUnitMin, ctx)
}

/**
 * R1 余额倍率（主倍率）：人民币模式 r ÷ m；美元模式、m = 1 原样返回 r。
 * r 不是有限的非负数时返回 NaN（formatRate 会显示 `-`）。
 */
export function balanceRate(r: number, ctx: RateContext): number {
  if (!isFiniteNumber(r) || r < 0) return NaN
  return isEquivalentMode(ctx) ? r / rechargeMultiplierOf(ctx) : r
}

/**
 * R2 套餐倍率 r × u（u 由调用方给：卡的精确单价、报价里的 unit_price 等）。
 * 不满足 planUnitUsable，或 r = 0（与主倍率相等）时返回 null。
 */
export function planRateForUnit(r: number, u: number | null | undefined, ctx: RateContext): number | null {
  if (!isPositive(r) || !planUnitUsable(u, ctx)) return null
  return r * u
}

/** R2 套餐低至：r × u_min。任一抑制条件成立返回 null（整段不出现，只剩主倍率）。 */
export function planLowestRate(r: number, ctx: RateContext): number | null {
  return planRateForUnit(r, ctx.planUnitMin, ctx)
}

/** usageRowRate 需要的用量字段（types 里的 UsageLog 满足）。 */
export interface UsageRateRow {
  /** 官方美元价。 */
  total_cost?: number | null
  /** 服务端按扣费来源折好的人民币花费。 */
  fiat_cost?: number | null
  /** 这行用量的倍率（分组倍率 / 专属倍率）。 */
  rate_multiplier?: number | null
}

/**
 * R7 用量行的展示倍率：
 * - 人民币模式：fiat_cost ÷ total_cost。钱包扣费得 r ÷ m，套餐扣费得 r × u_card，
 *   不依赖 rate_multiplier 的含义。没有 fiat_cost（老行）时回落 r ÷ m；
 *   total_cost 为 0 返回 null（调用方显示 `-`）。
 * - 美元模式 / m = 1：原样返回 rate_multiplier，取不到返回 null。
 */
export function usageRowRate(row: UsageRateRow, ctx: RateContext): number | null {
  const raw = isFiniteNumber(row.rate_multiplier) && row.rate_multiplier >= 0 ? row.rate_multiplier : null
  if (!isEquivalentMode(ctx)) return raw

  const total = row.total_cost
  if (isFiniteNumber(total) && total === 0) return null
  if (isPositive(total) && isPositive(row.fiat_cost)) return clean(row.fiat_cost / total)
  return raw === null ? null : balanceRate(raw, ctx)
}

/** 一个分组在界面上要展示的倍率，字符串已格式化、不带 `x`（`x` 在 i18n 模板里）。 */
export interface RateView {
  /** 主倍率：人民币模式是余额倍率 r ÷ m，美元模式 / m = 1 是原始 r。 */
  main: string
  mainValue: number
  /** 有专属倍率时，被划掉的默认主倍率。 */
  mainStruck?: string
  /** 套餐低至 r × u_min；R2 任一抑制条件成立时缺省。 */
  plan?: string
  planValue?: number
  /** 有专属倍率时，被划掉的默认套餐低至。 */
  planStruck?: string
  /** 有生效套餐卡时，这张卡的精确倍率 r × u_card；只在 plan 出现时才有。 */
  yourPlan?: string
  yourPlanValue?: number
}

/**
 * 一个分组的倍率视图（R1~R3）。baseRate 是分组默认倍率，userRate 是用户专属倍率；
 * 有专属倍率的判定与 GroupBadge 一致：userRate 非空且不等于 baseRate。
 * 专属倍率时主倍率 / 套餐低至都用专属 r 算，并带上默认值（mainStruck / planStruck）供划线。
 */
export function buildRateView(
  baseRate: number,
  userRate: number | null | undefined,
  ctx: RateContext
): RateView {
  const custom = isFiniteNumber(userRate) && userRate !== baseRate
  const effective = custom ? userRate : baseRate
  const formatMain = isEquivalentMode(ctx) ? formatRate : formatRawRate

  const mainValue = balanceRate(effective, ctx)
  const view: RateView = { main: formatMain(mainValue), mainValue }
  if (custom) view.mainStruck = formatMain(balanceRate(baseRate, ctx))

  const plan = planLowestRate(effective, ctx)
  if (plan === null) return view

  view.plan = formatRate(plan)
  view.planValue = plan
  if (custom) {
    const struck = planLowestRate(baseRate, ctx)
    if (struck !== null) view.planStruck = formatRate(struck)
  }

  const yours = planRateForUnit(effective, ctx.cardUnit, ctx)
  if (yours !== null) {
    view.yourPlan = formatRate(yours)
    view.yourPlanValue = yours
  }
  return view
}
