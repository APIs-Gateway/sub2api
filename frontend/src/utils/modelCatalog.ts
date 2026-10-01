/**
 * 价格目录的纯函数：把「渠道 → 平台 section → 分组 + 模型」聚合成以模型为中心的目录，
 * 并按分组算出余额价 / 套餐价 / 官方价。组件只负责展示。
 *
 * 口径：
 * - 扣费额度 = 官方 USD 价 × 分组倍率（用户专属倍率优先于分组默认倍率）。
 * - 余额价（人民币模式）= 额度价 ÷ m，m 为公开设置里的充值倍率；美元模式直接展示额度价。
 * - 套餐价 = 额度价 × u，u 为套餐卡单价（有生效卡时精确，否则为可购买套餐的区间）。
 */
import type {
  UserAvailableChannel,
  UserPricingInterval,
  UserSupportedModelPricing,
} from '@/api/channels'
import { BILLING_MODE_TOKEN, type BillingMode } from '@/constants/channel'

export const TOKEN_SCALE = 1_000_000

/** token 计费按每百万 token 展示，按次 / 按图按每次展示。 */
export type PriceKind = 'token' | 'request'

/** 官方价（USD，未乘分组倍率，未缩放）。按次模型只用 unit。 */
export interface PriceSet {
  input: number | null
  output: number | null
  cacheRead: number | null
  cacheWrite: number | null
  /** 图片输出（按 token）。只出现在只配了图片输出价的模型上。 */
  imageOutput: number | null
  unit: number | null
}

export interface TierRange {
  /** 展示用标签（后端给的 tier_label），缺省时用 min / max 组织文案。 */
  label?: string
  min: number
  max: number | null
}

export interface ModelTier {
  range: TierRange
  prices: PriceSet
}

export interface ModelPricing {
  kind: PriceKind
  /** 后端的计费方式，阶梯表首列表头按它区分。 */
  mode: BillingMode
  /** 首档（无阶梯时就是基础价）。 */
  first: PriceSet
  /** 完整阶梯；只有一档或没有阶梯时为空数组。 */
  tiers: ModelTier[]
}

export interface CatalogGroup {
  id: number
  name: string
  platform: string
  subscriptionType: string
  /** 分组默认倍率。 */
  baseRate: number
  /** 实际生效倍率（专属倍率优先）。 */
  rate: number
  hasCustomRate: boolean
}

export interface GroupPrice {
  group: CatalogGroup
  pricing: ModelPricing
}

export interface CatalogModel {
  key: string
  name: string
  platform: string
  kind: PriceKind | null
  /** 按起价从低到高排序，只含有定价的分组。 */
  entries: GroupPrice[]
  /** 起价所在分组；没有任何定价时为 null。 */
  cheapest: GroupPrice | null
}

export interface SubscriptionUnitRange {
  min: number
  max: number
  exact: boolean
}

export interface PricingContext {
  isFiat: boolean
  rechargeMultiplier: number
  subscriptionUnit: SubscriptionUnitRange | null
}

export interface PlanPrice {
  min: number
  max: number
  exact: boolean
}

/** 0 表示免费，是有效价格；只有 null / 非法值才算未配置。 */
function pick(v: number | null | undefined): number | null {
  return typeof v === 'number' && Number.isFinite(v) && v >= 0 ? v : null
}

function clean(n: number): number {
  return Number(n.toPrecision(10))
}

function tokenSet(iv: UserPricingInterval | undefined, p: UserSupportedModelPricing): PriceSet {
  return {
    input: pick(iv?.input_price ?? p.input_price),
    output: pick(iv?.output_price ?? p.output_price),
    cacheRead: pick(iv?.cache_read_price ?? p.cache_read_price),
    cacheWrite: pick(iv?.cache_write_price ?? p.cache_write_price),
    imageOutput: null,
    unit: null,
  }
}

function unitSet(unit: number | null): PriceSet {
  return { input: null, output: null, cacheRead: null, cacheWrite: null, imageOutput: null, unit }
}

/** 把接口里的定价规整成「首档 + 阶梯」。没有任何有效价格时返回 null。 */
export function normalizePricing(p: UserSupportedModelPricing | null | undefined): ModelPricing | null {
  if (!p) return null
  const intervals = p.intervals ?? []

  if (p.billing_mode === BILLING_MODE_TOKEN) {
    const first = tokenSet(intervals[0], p)
    if (first.input == null && first.output == null) return null
    const tiers: ModelTier[] =
      intervals.length > 1
        ? intervals.map((iv) => ({
            range: { label: iv.tier_label || undefined, min: iv.min_tokens, max: iv.max_tokens },
            prices: tokenSet(iv, p),
          }))
        : []
    return { kind: 'token', mode: p.billing_mode, first, tiers }
  }

  // 按次 / 按图：后端只看阶梯价和 per_request_price（calculatePerRequestCost）。
  // 首档和 token 模型一样取 intervals[0]，缺省再回退到基础价；
  // image_output_price 是按 token 的单价，不能当每次价格。
  const perRequest = (iv: UserPricingInterval | undefined) => pick(iv?.per_request_price ?? p.per_request_price)
  const unit = perRequest(intervals[0])
  if (unit == null) {
    // 没有每次价格、只配了图片输出价：按 token 计费的图片输出价展示。
    const imageOutput = pick(p.image_output_price)
    if (imageOutput == null) return null
    return { kind: 'token', mode: p.billing_mode, first: { ...unitSet(null), imageOutput }, tiers: [] }
  }
  const tiers: ModelTier[] =
    intervals.length > 1
      ? intervals.map((iv) => ({
          range: { label: iv.tier_label || undefined, min: iv.min_tokens, max: iv.max_tokens },
          prices: unitSet(perRequest(iv)),
        }))
      : []
  return { kind: 'request', mode: p.billing_mode, first: unitSet(unit), tiers }
}

/** 排序 / 比较用的官方价基准：输入价优先，其次按次价，再次输出价、图片输出价。 */
function rankBase(set: PriceSet): number {
  return set.input ?? set.unit ?? set.output ?? set.imageOutput ?? Number.POSITIVE_INFINITY
}

export function creditPriceOf(entry: GroupPrice): number {
  return rankBase(entry.pricing.first) * entry.group.rate
}

/**
 * 聚合成模型目录。同一模型在不同 section 下定价可能不同：
 * 按「分组所在 section」取该分组的价格；同一分组在多个 section 都有该模型时，优先取有定价的。
 */
export function buildCatalog(
  channels: UserAvailableChannel[],
  userGroupRates: Record<number, number> = {},
): CatalogModel[] {
  const models = new Map<string, { name: string; platform: string; groups: Map<number, GroupPrice> }>()

  for (const ch of channels) {
    for (const sec of ch.platforms) {
      for (const g of sec.groups) {
        const custom = userGroupRates[g.id]
        const hasCustomRate = typeof custom === 'number' && Number.isFinite(custom) && custom !== g.rate_multiplier
        const group: CatalogGroup = {
          id: g.id,
          name: g.name,
          platform: g.platform || sec.platform,
          subscriptionType: g.subscription_type,
          baseRate: g.rate_multiplier,
          rate: typeof custom === 'number' && Number.isFinite(custom) ? custom : g.rate_multiplier,
          hasCustomRate,
        }
        for (const m of sec.supported_models) {
          const platform = m.platform || sec.platform
          const key = `${platform}::${m.name}`
          let e = models.get(key)
          if (!e) {
            e = { name: m.name, platform, groups: new Map() }
            models.set(key, e)
          }
          const pricing = normalizePricing(m.pricing)
          if (!pricing) continue
          if (!e.groups.has(g.id)) e.groups.set(g.id, { group, pricing })
        }
      }
    }
  }

  return Array.from(models.entries())
    .map(([key, e]): CatalogModel => {
      const entries = Array.from(e.groups.values()).sort(
        (a, b) => creditPriceOf(a) - creditPriceOf(b) || a.group.name.localeCompare(b.group.name),
      )
      return {
        key,
        name: e.name,
        platform: e.platform,
        kind: entries[0]?.pricing.kind ?? null,
        entries,
        cheapest: entries[0] ?? null,
      }
    })
    .sort((a, b) => a.name.localeCompare(b.name))
}

export function scaleOf(kind: PriceKind): number {
  return kind === 'token' ? TOKEN_SCALE : 1
}

/** a 是否高于 b。先按 10 位有效数字去掉浮点噪声，避免 7.000000000001 > 7 这类误判。 */
export function exceeds(a: number, b: number): boolean {
  return clean(a) > clean(b)
}

/** 官方价（USD），按展示单位缩放。 */
export function officialPrice(usd: number, kind: PriceKind): number {
  return clean(usd * scaleOf(kind))
}

/** 额度价：官方价 × 倍率。美元模式的展示价。 */
export function creditPrice(usd: number, rate: number, kind: PriceKind): number {
  return clean(usd * rate * scaleOf(kind))
}

/** 余额价：人民币模式 = 额度价 ÷ m；美元模式 = 额度价。 */
export function balancePrice(usd: number, rate: number, kind: PriceKind, ctx: PricingContext): number {
  const credit = usd * rate * scaleOf(kind)
  return clean(ctx.isFiat ? credit / ctx.rechargeMultiplier : credit)
}

/** 套餐价：仅人民币模式且拿得到卡单价时有值。 */
export function planPrice(usd: number, rate: number, kind: PriceKind, ctx: PricingContext): PlanPrice | null {
  const u = ctx.subscriptionUnit
  if (!ctx.isFiat || !u || !(u.min > 0) || !(u.max > 0)) return null
  const credit = usd * rate * scaleOf(kind)
  const min = clean(credit * u.min)
  const max = u.exact ? min : clean(credit * u.max)
  return { min, max, exact: u.exact || Math.abs(u.max - u.min) < 1e-12 }
}

/**
 * 套餐单价 u：有生效套餐卡时用那张卡的 fiat_per_credit（精确）；
 * 否则用可购买套餐的区间。
 */
export function resolveSubscriptionUnit(
  activeSubscriptions: Array<{ status?: string; fiat_per_credit?: number | null }>,
  bounds: { u_min: number; u_max: number } | null | undefined,
): SubscriptionUnitRange | null {
  for (const sub of activeSubscriptions) {
    const rate = sub.fiat_per_credit
    if (sub.status === 'active' && typeof rate === 'number' && Number.isFinite(rate) && rate > 0) {
      return { min: rate, max: rate, exact: true }
    }
  }
  if (!bounds || !(bounds.u_min > 0) || !(bounds.u_max > 0)) return null
  return { min: Math.min(bounds.u_min, bounds.u_max), max: Math.max(bounds.u_min, bounds.u_max), exact: false }
}

/** 200000 → "200K"，1000000 → "1M"。 */
export function formatTokenCount(n: number): string {
  if (n >= 1_000_000 && n % 1_000_000 === 0) return `${n / 1_000_000}M`
  if (n >= 1000 && n % 1000 === 0) return `${n / 1000}K`
  return String(n)
}
