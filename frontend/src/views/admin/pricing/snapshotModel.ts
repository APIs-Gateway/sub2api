/**
 * 价格快照审批页的纯函数：LiteLLM 单价（美元 / Token）换成每百万 Token、涨跌方向、筛选。
 */
import type { EffectiveChange, LiteLLMPricing, SnapshotDiffEntry } from '@/api/admin/pricingOps'

/** 每 Token 的美元价换成每百万 Token；去掉浮点误差（1.25e-6 × 1e6 不应显示成 1.2500000000000002）。 */
export function perMtok(perToken: number | undefined | null): number | null {
  if (perToken === undefined || perToken === null || Number.isNaN(perToken)) return null
  return Number((perToken * 1e6).toPrecision(10))
}

export interface EntryPrices {
  oldIn: number | null
  oldOut: number | null
  newIn: number | null
  newOut: number | null
}

export function entryPrices(e: SnapshotDiffEntry): EntryPrices {
  const side = (p?: LiteLLMPricing) => ({ i: perMtok(p?.input_cost_per_token), o: perMtok(p?.output_cost_per_token) })
  const o = side(e.old)
  const n = side(e.new)
  return { oldIn: o.i, oldOut: o.o, newIn: n.i, newOut: n.o }
}

/** up 涨价；down 降价；mixed 有涨有跌（按涨价处理）；flat 价格字段都没变。 */
export type Direction = 'up' | 'down' | 'mixed' | 'flat'

/** 涨价或有涨有跌：筛选与「搁置所有涨价项」都按涨价处理，宁可多搁置。 */
export function isUpward(d: Direction | null): boolean {
  return d === 'up' || d === 'mixed'
}

/** 快照条目里会影响计费的数值字段：后端 LiteLLM 价格记录里键名带 cost 的都是（输入、输出、缓存读写、长上下文、priority、图片 token 价、按次价等）。 */
const THRESHOLD_FIELD = 'long_context_input_token_threshold'

function isPriceField(key: string): boolean {
  return key.includes('cost')
}

function priceValue(p: LiteLLMPricing | undefined, key: string): number {
  const v = (p as unknown as Record<string, unknown> | undefined)?.[key]
  return typeof v === 'number' && !Number.isNaN(v) ? v : 0
}

/** 变化类型为 changed 时的涨跌：比较新旧记录里所有价格字段，任一项涨价就算涨价。其余类型没有方向。 */
export function entryDirection(e: SnapshotDiffEntry): Direction | null {
  if (e.change_type !== 'changed') return null
  const keys = new Set<string>()
  for (const side of [e.old, e.new]) for (const k of Object.keys(side ?? {})) if (isPriceField(k)) keys.add(k)
  for (const k of e.changed_fields ?? []) if (isPriceField(k)) keys.add(k)
  let up = false
  let down = false
  for (const k of keys) {
    const before = priceValue(e.old, k)
    const after = priceValue(e.new, k)
    if (after > before) up = true
    else if (after < before) down = true
  }
  // 长上下文门槛不带 cost 但影响计费：调低（更多请求按长上下文价计）= 涨价，调高 = 降价；
  // 从无到有、从有到无都从严按涨价处理
  const t0 = priceValue(e.old, THRESHOLD_FIELD)
  const t1 = priceValue(e.new, THRESHOLD_FIELD)
  if (t0 !== t1) {
    if (t0 === 0 || t1 === 0 || t1 < t0) up = true
    else down = true
  }
  return combine(up, down)
}

function combine(up: boolean, down: boolean): Direction {
  if (up && down) return 'mixed'
  if (up) return 'up'
  return down ? 'down' : 'flat'
}

function pairDirection(p: EntryPrices): Direction {
  let up = false
  let down = false
  for (const [a, b] of [[p.oldIn, p.newIn], [p.oldOut, p.newOut]] as [number | null, number | null][]) {
    const before = a ?? 0
    const after = b ?? 0
    if (after > before) up = true
    else if (after < before) down = true
  }
  return combine(up, down)
}

/** 输入价的涨跌幅（百分比）；输入价为 0 时看输出价，都为 0 返回 null。 */
export function entryPercent(e: SnapshotDiffEntry): number | null {
  const p = entryPrices(e)
  const [a, b] = p.oldIn ? [p.oldIn, p.newIn ?? 0] : [p.oldOut ?? 0, p.newOut ?? 0]
  if (!a || a === b) return null
  return ((b - a) / a) * 100
}

/** 价格记录里除输入、输出之外还有几项字段变了（缓存价、按图价等）。 */
export function otherChangedFieldCount(e: SnapshotDiffEntry): number {
  return (e.changed_fields ?? []).filter((f) => f !== 'input_cost_per_token' && f !== 'output_cost_per_token').length
}

/**
 * 实际变价模型的涨跌。后端只给输入、输出两个价；这两个没变而别的价格字段变了（比如缓存价）时，
 * 用同一模型在差异条目里的方向，找不到条目才算没变。
 */
export function effectiveDirection(c: EffectiveChange, entries: SnapshotDiffEntry[] = []): Direction {
  const d = pairDirection({ oldIn: c.old_input_per_mtok, oldOut: c.old_output_per_mtok, newIn: c.new_input_per_mtok, newOut: c.new_output_per_mtok })
  const entry = entries.find((e) => e.model_key === c.model)
  const ed = entry ? entryDirection(entry) : null
  if (!ed || ed === 'flat') return d
  // 输入输出和其它价格字段合起来看：输入降、缓存涨 = 有涨有跌
  const up = isUpward(d) || isUpward(ed)
  const down = d === 'down' || d === 'mixed' || ed === 'down' || ed === 'mixed'
  return combine(up, down)
}

export type DiffFilter = 'all' | DiffTypeFilter
export type DiffTypeFilter = 'added' | 'removed' | 'changed' | 'up'

export function filterEntries(entries: SnapshotDiffEntry[], filter: DiffFilter, search: string): SnapshotDiffEntry[] {
  const q = search.trim().toLowerCase()
  return entries.filter((e) => {
    if (q && !e.model_key.toLowerCase().includes(q)) return false
    switch (filter) {
      case 'all':
        return true
      case 'up':
        return isUpward(entryDirection(e))
      default:
        return e.change_type === filter
    }
  })
}
