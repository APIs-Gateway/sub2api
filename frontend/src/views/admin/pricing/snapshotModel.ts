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

export type Direction = 'up' | 'down' | 'flat'

/** 变化类型为 changed 时的涨跌：输入或输出任一涨价就算涨价。其余类型没有方向。 */
export function entryDirection(e: SnapshotDiffEntry): Direction | null {
  if (e.change_type !== 'changed') return null
  return direction(entryPrices(e))
}

function direction(p: EntryPrices): Direction {
  const pairs: [number | null, number | null][] = [
    [p.oldIn, p.newIn],
    [p.oldOut, p.newOut]
  ]
  let up = false
  let down = false
  for (const [a, b] of pairs) {
    const before = a ?? 0
    const after = b ?? 0
    if (after > before) up = true
    else if (after < before) down = true
  }
  if (up) return 'up'
  return down ? 'down' : 'flat'
}

/** 输入价的涨跌幅（百分比）；输入价为 0 时看输出价，都为 0 返回 null。 */
export function entryPercent(e: SnapshotDiffEntry): number | null {
  const p = entryPrices(e)
  const [a, b] = p.oldIn ? [p.oldIn, p.newIn ?? 0] : [p.oldOut ?? 0, p.newOut ?? 0]
  if (!a) return null
  return ((b - a) / a) * 100
}

/** 价格记录里除输入、输出之外还有几项字段变了（缓存价、按图价等）。 */
export function otherChangedFieldCount(e: SnapshotDiffEntry): number {
  return (e.changed_fields ?? []).filter((f) => f !== 'input_cost_per_token' && f !== 'output_cost_per_token').length
}

export function effectiveDirection(c: EffectiveChange): Direction {
  return direction({ oldIn: c.old_input_per_mtok, oldOut: c.old_output_per_mtok, newIn: c.new_input_per_mtok, newOut: c.new_output_per_mtok })
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
        return entryDirection(e) === 'up'
      default:
        return e.change_type === filter
    }
  })
}
