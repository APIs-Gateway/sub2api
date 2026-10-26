/**
 * 价格配置页（模型页、开放与价格矩阵页）共用的纯逻辑：
 * 把接口返回的目录、分组、报价整理成页面要显示的行和格子。不碰网络、不碰界面。
 */
import type { GroupPlatform } from '@/types'
import type {
  CatalogStatus,
  GroupDeriveView,
  ModelCatalogEntry,
  OfficialReference,
  PricingStage,
  QuoteBatchCell,
  QuoteSource
} from '@/api/admin/pricing'

/** 页签与筛选里平台的固定顺序；不在表里的平台排在最后。 */
export const PLATFORM_ORDER = ['openai', 'anthropic', 'gemini', 'antigravity', 'grok']

export const PLATFORM_LABEL: Record<string, string> = {
  openai: 'OpenAI',
  anthropic: 'Anthropic',
  gemini: 'Gemini',
  antigravity: 'Antigravity',
  grok: 'Grok'
}

export function platformLabel(platform: string): string {
  return PLATFORM_LABEL[platform] ?? platform
}

/** 平台图标只认已知平台，未知平台它会回落成通用图标。 */
export function asGroupPlatform(platform: string): GroupPlatform {
  return platform as GroupPlatform
}

export function comparePlatforms(a: string, b: string): number {
  const ia = PLATFORM_ORDER.indexOf(a)
  const ib = PLATFORM_ORDER.indexOf(b)
  if (ia !== ib) return (ia === -1 ? 99 : ia) - (ib === -1 ? 99 : ib)
  return a.localeCompare(b)
}

export interface PricingGroup {
  id: number
  name: string
  platform: string
  /** 分组倍率 */
  rate: number
  /** 灰度阶段；没读到派生结果时按 legacy（渠道配置）处理 */
  stage: PricingStage
  /** 开放方式：open 默认开放，allowlist 白名单；没读到时为 null */
  accessMode: 'open' | 'allowlist' | null
}

/** 分组还在用渠道配置（尚未切换到新配置）。 */
export function isGroupUnswitched(group: PricingGroup): boolean {
  return group.stage !== 'v2'
}

export function buildGroup(
  base: { id: number; name: string; platform: string; rate_multiplier: number },
  derive: GroupDeriveView | null | undefined
): PricingGroup {
  const stage = derive?.stored_config?.pricing_stage ?? 'legacy'
  // v2 分组生效的是库里的准入模式；其余分组本来不可写，只展示按渠道推出的值
  const accessMode =
    (stage === 'v2' ? derive?.stored_config?.access_mode : undefined) ?? derive?.derived?.config?.access_mode ?? null
  return {
    id: base.id,
    name: base.name,
    platform: base.platform,
    rate: base.rate_multiplier,
    stage,
    accessMode
  }
}

export interface ModelRow {
  /** 平台 + 模型名，页面内唯一 */
  id: string
  key: string
  platform: string
  displayName: string
  aliases: string[]
  referenceModel: string | null
  /** 目录状态；null 表示没有登记在目录里，只在分组配置里出现过 */
  status: CatalogStatus | null
  registered: boolean
}

/**
 * 模型行 = 目录里的模型 + 分组配置里出现过、但目录里没有的模型（未登记）。
 * 分组配置里的名字已经是规范写法；目录的别名也算登记过。
 */
export function buildModelRows(catalog: ModelCatalogEntry[], groupDerives: GroupDeriveView[]): ModelRow[] {
  const rows = new Map<string, ModelRow>()
  const known = new Set<string>()
  for (const entry of catalog) {
    const id = `${entry.platform}/${entry.model_key}`
    known.add(id)
    for (const alias of entry.aliases ?? []) known.add(`${entry.platform}/${alias}`)
    rows.set(id, {
      id,
      key: entry.model_key,
      platform: entry.platform,
      displayName: entry.display_name || entry.model_key,
      aliases: entry.aliases ?? [],
      referenceModel: entry.reference_model,
      status: entry.status,
      registered: true
    })
  }
  for (const view of groupDerives) {
    if (view.deleted) continue
    for (const cell of view.derived?.cells ?? []) {
      if (cell.is_pattern || !cell.model_key) continue
      const id = `${view.platform}/${cell.model_key}`
      if (known.has(id) || rows.has(id)) continue
      rows.set(id, {
        id,
        key: cell.model_key,
        platform: view.platform,
        displayName: cell.model_key,
        aliases: [],
        referenceModel: null,
        status: null,
        registered: false
      })
    }
  }
  return [...rows.values()].sort(
    (a, b) =>
      comparePlatforms(a.platform, b.platform) ||
      Number(b.registered) - Number(a.registered) ||
      a.key.localeCompare(b.key)
  )
}

export function cellKey(groupId: number, model: string): string {
  return `${groupId}|${model}`
}

export type CellKind = 'open' | 'extra' | 'custom' | 'unpriced' | 'closed' | 'error'

/** 用户实付单价（美元 / 百万 Token）；缓存读写只有报价或配置里有时才带。 */
export interface CellUsd {
  input: number
  output: number
  cache_write?: number
  cache_read?: number
}

export interface CellView {
  kind: CellKind
  /** 分组还没切换，这个格子是由渠道配置推出的结果 */
  unswitched: boolean
  /** 用户实付单价（美元 / 百万 Token）；开放且有价时才有 */
  usd: CellUsd | null
  /** 按次计费的用户实付单价（美元 / 次） */
  perRequestUsd: number | null
  /** 按次计费只有区间价时的价格范围（美元 / 次） */
  perRequestRange: { min: number; max: number } | null
  extra: number | null
  /** 关闭原因：closed_in_group / not_in_allowlist / catalog_draft / catalog_retired */
  reason: string | null
}

/**
 * 把一个报价翻成格子状态。优先级：没报出价 > 关闭 > 未定价 > 自定义价 > 额外倍率 > 开放。
 * 报价没取到（undefined）返回 null，页面显示占位。
 */
export function cellView(quote: QuoteBatchCell | undefined, group: PricingGroup): CellView | null {
  if (!quote) return null
  const base = { unswitched: isGroupUnswitched(group), usd: null, perRequestUsd: null, perRequestRange: null, extra: null, reason: null }
  if (quote.error) return { ...base, kind: 'error' }
  if (!quote.access?.ok) return { ...base, kind: 'closed', reason: quote.access?.reason ?? null }

  const f = quote.final_per_mtok
  const usd: CellUsd | null = f
    ? {
        input: f.input,
        output: f.output,
        ...(typeof f.cache_write === 'number' ? { cache_write: f.cache_write } : {}),
        ...(typeof f.cache_read === 'number' ? { cache_read: f.cache_read } : {})
      }
    : null
  const perRequestUsd = typeof quote.per_request_price === 'number' ? quote.per_request_price : null
  const perRequestRange =
    typeof quote.per_request_min === 'number' && typeof quote.per_request_max === 'number'
      ? { min: quote.per_request_min, max: quote.per_request_max }
      : null
  const extra = typeof quote.extra_multiplier === 'number' && quote.extra_multiplier !== 1 ? quote.extra_multiplier : null
  const priced = quote.priced && (usd !== null || perRequestUsd !== null || perRequestRange !== null)
  const shown = { ...base, usd, perRequestUsd, perRequestRange, extra }

  if (!priced) return { ...shown, kind: 'unpriced', usd: null, perRequestUsd: null, perRequestRange: null }
  if (quote.source === 'channel') return { ...shown, kind: 'custom' }
  if (extra !== null) return { ...shown, kind: 'extra' }
  return { ...shown, kind: 'open' }
}

export function isOpenCell(view: CellView | null): boolean {
  return !!view && view.kind !== 'closed' && view.kind !== 'error'
}

/** 价格来源的 i18n 键（admin.pricingConfig.source.*）。 */
export function sourceKey(source: QuoteSource | undefined): string {
  switch (source) {
    case 'litellm':
      return 'litellm'
    case 'fallback':
      return 'fallback'
    case 'card_policy':
      return 'cardPolicy'
    case 'channel':
      return 'channel'
    default:
      return 'none'
  }
}

export interface ModelRowStats {
  /** 同平台的分组数 */
  groupCount: number
  /** 其中开放这个模型的分组数 */
  openCount: number
  /** 读到报价的分组数；小于 groupCount 说明有格子没取到 */
  quotedCount: number
}

export function modelStats(
  row: ModelRow,
  groups: PricingGroup[],
  cells: Record<string, QuoteBatchCell>
): ModelRowStats {
  const same = groups.filter((g) => g.platform === row.platform)
  let openCount = 0
  let quotedCount = 0
  for (const g of same) {
    const view = cellView(cells[cellKey(g.id, row.key)], g)
    if (!view) continue
    quotedCount++
    if (isOpenCell(view)) openCount++
  }
  return { groupCount: same.length, openCount, quotedCount }
}

export function isUnpricedModel(ref: OfficialReference | undefined): boolean {
  return !!ref && !ref.priced
}
