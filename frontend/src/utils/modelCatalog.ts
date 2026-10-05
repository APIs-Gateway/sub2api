/**
 * 价格目录的纯函数：把后端给的「模型 → 分组 → 价格」整理成以模型为中心的目录，并做币种换算。组件只负责展示。
 *
 * 口径：
 * - 后端（PriceQuoter）已经按各分组的价格阶段取好价，并乘好倍率（分组倍率，有专属倍率用专属倍率，
 *   v2 分组再含额外倍率）：`prices` 是乘完倍率的单价（美元），`official` 是乘倍率之前的单价，前端不再自己乘倍率。
 * - 余额价（人民币模式）= 额度价 ÷ m，m 为公开设置里的充值倍率；美元模式直接展示额度价。
 * - 套餐价 = 额度价 × u，u 为套餐卡单价（有生效卡时精确，否则为可购买套餐的区间）。
 */
import type { UserPriceCatalog, UserPriceEntry, UserPriceSet, UserPriceTier } from '@/api/channels'
import type { BillingMode } from '@/constants/channel'

export const TOKEN_SCALE = 1_000_000

/** token 计费按每百万 token 展示，按次 / 按图按每次展示。 */
export type PriceKind = 'token' | 'request'

/** 单价（USD，未按展示单位缩放）。按次模型只用 unit。 */
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
  /** 首档（无阶梯时就是基础价），已乘倍率。 */
  first: PriceSet
  /** 首档乘倍率之前的单价，官方价对照用。 */
  official: PriceSet
  /** 完整阶梯（已乘倍率）；只有一档或没有阶梯时为空数组。 */
  tiers: ModelTier[]
}

export interface CatalogGroup {
  id: number
  name: string
  platform: string
  subscriptionType: string
  /** 分组默认倍率。 */
  baseRate: number
  /** 这个模型在这个分组实际乘上的倍率（专属倍率优先，v2 分组含额外倍率）。 */
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

function toPriceSet(s: UserPriceSet): PriceSet {
  return {
    input: pick(s.input),
    output: pick(s.output),
    cacheRead: pick(s.cache_read),
    cacheWrite: pick(s.cache_write),
    imageOutput: pick(s.image_output),
    unit: pick(s.unit),
  }
}

function toTier(t: UserPriceTier): ModelTier {
  return {
    range: { label: t.label || undefined, min: t.min_tokens, max: t.max_tokens },
    prices: toPriceSet(t.prices),
  }
}

/** 把后端的一条价格整理成「首档 + 阶梯」。 */
function toPricing(e: UserPriceEntry): ModelPricing {
  return {
    kind: e.kind,
    mode: e.billing_mode as BillingMode,
    first: toPriceSet(e.prices),
    official: toPriceSet(e.official),
    tiers: (e.tiers ?? []).map(toTier),
  }
}

/** 排序 / 比较用的额度价基准：输入价优先，其次按次价，再次输出价、图片输出价。 */
function rankBase(set: PriceSet): number {
  return set.input ?? set.unit ?? set.output ?? set.imageOutput ?? Number.POSITIVE_INFINITY
}

/** 额度价（已含倍率），用来给分组排序。 */
export function creditPriceOf(entry: GroupPrice): number {
  return rankBase(entry.pricing.first)
}

/**
 * 把后端的价格数据整理成模型目录。后端已经按分组所在的价格阶段取好价、乘好倍率，这里只做分组 join 与排序；
 * 分组按额度价从低到高，同价按名字。没有任何分组有价的模型保留，页面显示「暂无价格」。
 */
export function buildPriceCatalog(data: UserPriceCatalog | null | undefined): CatalogModel[] {
  if (!data) return []
  const groups = new Map(data.groups.map((g) => [g.id, g]))
  return data.models
    .map((m): CatalogModel => {
      const entries: GroupPrice[] = []
      for (const e of m.entries) {
        const g = groups.get(e.group_id)
        if (!g) continue
        entries.push({
          group: {
            id: g.id,
            name: g.name,
            platform: g.platform || m.platform,
            subscriptionType: g.subscription_type,
            baseRate: e.base_rate,
            rate: e.rate,
            hasCustomRate: e.has_custom_rate,
          },
          pricing: toPricing(e),
        })
      }
      entries.sort((a, b) => creditPriceOf(a) - creditPriceOf(b) || a.group.name.localeCompare(b.group.name))
      return {
        key: `${m.platform}::${m.name}`,
        name: m.name,
        platform: m.platform,
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

/** 官方价（USD，乘倍率之前的单价），按展示单位缩放。 */
export function officialPrice(usd: number, kind: PriceKind): number {
  return clean(usd * scaleOf(kind))
}

/** 额度价：后端已乘好倍率的单价，按展示单位缩放。美元模式的展示价。 */
export function creditPrice(credit: number, kind: PriceKind): number {
  return clean(credit * scaleOf(kind))
}

/** 余额价：人民币模式 = 额度价 ÷ m；美元模式 = 额度价。 */
export function balancePrice(credit: number, kind: PriceKind, ctx: PricingContext): number {
  const scaled = credit * scaleOf(kind)
  return clean(ctx.isFiat ? scaled / ctx.rechargeMultiplier : scaled)
}

/** 套餐价：仅人民币模式且拿得到卡单价时有值。 */
export function planPrice(credit: number, kind: PriceKind, ctx: PricingContext): PlanPrice | null {
  const u = ctx.subscriptionUnit
  if (!ctx.isFiat || !u || !(u.min > 0) || !(u.max > 0)) return null
  const scaled = credit * scaleOf(kind)
  const min = clean(scaled * u.min)
  const max = u.exact ? min : clean(scaled * u.max)
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
