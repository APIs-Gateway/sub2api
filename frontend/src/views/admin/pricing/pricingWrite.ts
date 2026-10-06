/**
 * 价格配置页「写入」用的纯逻辑：把管理员的选择整理成接口要的操作、
 * 把预览回来的改动翻成界面要显示的格子和价格。不碰网络、不碰界面。
 */
import type {
  CellOp,
  CellsRequest,
  CustomPrice,
  GroupDeriveView,
  OfficialReference,
  PlannedCellState,
  PrecheckIssue,
  PriceMode,
  StoredCell
} from '@/api/admin/pricing'
import { mTokToPerToken, perTokenToMTok } from '@/components/admin/channel/types'
import { isGroupUnswitched, type CellView, type ModelRow, type PricingGroup } from './pricingModel'

/** 一个格子的目标态（界面侧）。 */
export interface CellSpec {
  open: boolean
  mode: PriceMode
  extra?: number | null
  custom?: CustomPrice | null
}

export function findDerive(derives: GroupDeriveView[], groupId: number): GroupDeriveView | undefined {
  return derives.find((d) => d.group_id === groupId)
}

export function storedCellOf(derive: GroupDeriveView | undefined, modelKey: string): StoredCell | null {
  const key = modelKey.toLowerCase()
  return derive?.stored_cells?.find((c) => !c.is_pattern && c.model_key === key) ?? null
}

/** 分组配置的 revision；没读到（分组没有库里配置）时为 null。 */
export function groupRevisionOf(derive: GroupDeriveView | undefined): number | null {
  const rev = derive?.stored_config?.revision
  return typeof rev === 'number' && rev > 0 ? rev : null
}

/** 分组能不能在这里写：已经切到新配置，并且读到了基线。 */
export function isGroupWritable(group: PricingGroup, derive: GroupDeriveView | undefined): boolean {
  return !isGroupUnswitched(group) && groupRevisionOf(derive) !== null
}

/** 矩阵里能被选中改的格子：分组可写，模型已上线。 */
export function isCellEditable(group: PricingGroup, derive: GroupDeriveView | undefined, model: ModelRow): boolean {
  return isGroupWritable(group, derive) && model.status === 'active'
}

/** 格子现在的目标态：库里有单元格就用它，没有就是分组的默认（开放分组开、白名单分组关）。 */
export function currentSpec(group: PricingGroup, stored: StoredCell | null): CellSpec {
  if (!stored) return { open: group.accessMode === 'open', mode: 'inherit' }
  return {
    open: stored.open,
    mode: stored.price_mode,
    extra: stored.extra_multiplier ?? null,
    custom: stored.custom_price ?? null
  }
}

function base(group: PricingGroup, model: string, stored: StoredCell | null) {
  return { group_id: group.id, model_key: model, baseline_revision: stored?.revision ?? 0 }
}

/** 开放：开放分组和白名单分组都写 open=true；保留原有的价格设置。 */
export function opOpen(group: PricingGroup, model: string, stored: StoredCell | null): CellOp | null {
  const cur = currentSpec(group, stored)
  if (cur.open) return null
  return upsert(group, model, stored, { ...cur, open: true })
}

/**
 * 关闭：白名单分组删掉单元格即可；开放分组要写一个显式的 open=false 例外。
 * 关闭的格子不带价格设置。
 */
export function opClose(group: PricingGroup, model: string, stored: StoredCell | null): CellOp | null {
  if (group.accessMode === 'allowlist') {
    return stored ? { ...base(group, model, stored), kind: 'delete' } : null
  }
  if (stored && !stored.open) return null
  return upsert(group, model, stored, { open: false, mode: 'inherit' })
}

/** 额外倍率：只改开放的格子；1 等于不加价，按「继承」写。 */
export function opExtra(group: PricingGroup, model: string, stored: StoredCell | null, extra: number): CellOp | null {
  if (!currentSpec(group, stored).open) return null
  const spec: CellSpec = extra === 1 ? { open: true, mode: 'inherit' } : { open: true, mode: 'extra', extra }
  return upsert(group, model, stored, spec)
}

/**
 * 自定义价界面上按「美元 / 每百万 Token」填写；接口按「美元 / 每 Token」存（与渠道定价同一个单位），
 * 所以这里换算之后再写。按次价格本来就是「美元 / 次」，不换算。
 */
export function customToApi(price: CustomPrice): CustomPrice {
  return {
    ...price,
    input_price: mTokToPerToken(price.input_price),
    output_price: mTokToPerToken(price.output_price),
    cache_write_price: mTokToPerToken(price.cache_write_price),
    cache_read_price: mTokToPerToken(price.cache_read_price),
    image_output_price: mTokToPerToken(price.image_output_price)
  }
}

/** 自定义价：只改开放的格子。price 的价格按每百万 Token。 */
export function opCustom(group: PricingGroup, model: string, stored: StoredCell | null, price: CustomPrice): CellOp | null {
  if (!currentSpec(group, stored).open) return null
  return upsert(group, model, stored, { open: true, mode: 'custom', custom: customToApi(price) })
}

/** 清除覆盖：回到按官方价 × 分组倍率。开放分组删掉单元格，白名单分组保留开放、价格设回继承。 */
export function opClear(group: PricingGroup, model: string, stored: StoredCell | null): CellOp | null {
  if (!stored) return null
  if (group.accessMode === 'open') return stored.open ? { ...base(group, model, stored), kind: 'delete' } : null
  if (!stored.open || stored.price_mode === 'inherit') return null
  return upsert(group, model, stored, { open: true, mode: 'inherit' })
}

/** 把目标态写成 upsert；价格模式之外的字段不带，免得和模式互斥。 */
export function upsert(group: PricingGroup, model: string, stored: StoredCell | null, spec: CellSpec): CellOp {
  const op: CellOp = {
    ...base(group, model, stored),
    kind: 'upsert',
    open: spec.open,
    price_mode: spec.open ? spec.mode : 'inherit'
  }
  if (spec.open && spec.mode === 'extra') op.extra_multiplier = spec.extra ?? null
  if (spec.open && spec.mode === 'custom') op.custom_price = spec.custom ?? null
  return op
}

/** 开放时单元格带的额外倍率输入：空串、1 都表示不加价。返回 null 表示输入不合法。 */
export function parseExtra(text: string | number | null | undefined): number | null | 'none' {
  const trimmed = String(text ?? '').trim()
  if (trimmed === '') return 'none'
  const n = Number(trimmed)
  if (!Number.isFinite(n) || n <= 0) return null
  return Number(n.toFixed(6))
}

/** 价格输入：空串表示不填，返回 undefined；不合法返回 null。 */
export function parsePrice(text: string | number | null | undefined): number | undefined | null {
  const trimmed = String(text ?? '').trim()
  if (trimmed === '') return undefined
  const n = Number(trimmed)
  if (!Number.isFinite(n) || n < 0) return null
  return n
}

/** 预览与提交共用的请求：group_revisions 的键正好是 ops 涉及的分组。 */
export function buildRequest(ops: CellOp[], derives: GroupDeriveView[]): CellsRequest {
  const revisions: Record<string, number> = {}
  for (const op of ops) {
    const key = String(op.group_id)
    if (key in revisions) continue
    revisions[key] = groupRevisionOf(findDerive(derives, op.group_id)) ?? 0
  }
  return { ops, group_revisions: revisions }
}

/**
 * 目标态翻成格子状态和用户实付价（美元 / 百万 Token）。
 * 价格 = 官方价 × 分组倍率 × 额外倍率；自定义价同样乘分组倍率，没填的项用官方价。
 * 单元格里的自定义价是每 Token，这里换成每百万 Token 显示。
 * 官方价和自定义价都没有时是「未定价」。state 为 null 表示单元格不存在。
 */
export function specToView(
  state: PlannedCellState | CellSpec | null,
  group: PricingGroup,
  official: OfficialReference | undefined
): CellView {
  const empty = { unswitched: isGroupUnswitched(group), usd: null, perRequestUsd: null, perRequestRange: null, extra: null, reason: null }
  const spec = normalizeSpec(state, group)
  if (!spec.open) {
    return { ...empty, kind: 'closed', reason: group.accessMode === 'allowlist' ? 'not_in_allowlist' : 'closed_in_group' }
  }
  const per = official?.priced ? official.per_mtok : undefined
  if (spec.mode === 'custom' && spec.custom) {
    const c = spec.custom
    if (c.billing_mode !== 'token') {
      return typeof c.per_request_price === 'number'
        ? { ...empty, kind: 'custom', perRequestUsd: c.per_request_price * group.rate }
        : { ...empty, kind: 'custom' }
    }
    // 接口里的自定义价是每 Token，换成每百万 Token 再和官方价一起算
    const input = perTokenToMTok(c.input_price) ?? per?.input
    const output = perTokenToMTok(c.output_price) ?? per?.output
    if (typeof input !== 'number' || typeof output !== 'number') return { ...empty, kind: 'unpriced' }
    return { ...empty, kind: 'custom', usd: { input: input * group.rate, output: output * group.rate } }
  }
  if (!per) return { ...empty, kind: 'unpriced' }
  const extra = spec.mode === 'extra' && typeof spec.extra === 'number' && spec.extra !== 1 ? spec.extra : null
  const factor = group.rate * (extra ?? 1)
  return {
    ...empty,
    kind: extra !== null ? 'extra' : 'open',
    extra,
    usd: { input: per.input * factor, output: per.output * factor }
  }
}

function normalizeSpec(state: PlannedCellState | CellSpec | null, group: PricingGroup): CellSpec {
  if (!state) return { open: group.accessMode === 'open', mode: 'inherit' }
  if ('price_mode' in state) {
    return { open: state.open, mode: state.price_mode, extra: state.extra_multiplier ?? null, custom: state.custom_price ?? null }
  }
  return state
}

/** 同一个格子改前改后的单价（输入、输出）是否有变化，用于标出「价格变了」。 */
export function usdChanged(before: CellView | null, after: CellView): boolean {
  const a = before?.usd
  const b = after.usd
  if (!a && !b) return false
  if (!a || !b) return true
  return a.input !== b.input || a.output !== b.output
}

export type ChangeKind = 'open' | 'close' | 'up' | 'down' | 'adjust' | 'same'

/** 一行改动的性质：开放、关闭、涨价、降价、调整（价格口径变了、实付没变）、无变化。 */
export function describeChange(before: CellView | null, after: CellView): ChangeKind {
  const wasOpen = !!before && before.kind !== 'closed' && before.kind !== 'error'
  const isOpen = after.kind !== 'closed'
  if (!wasOpen && isOpen) return 'open'
  if (wasOpen && !isOpen) return 'close'
  if (!isOpen) return 'same'
  const a = before?.usd
  const b = after.usd
  if (a && b) {
    const sum = (x: { input: number; output: number }) => x.input + x.output
    if (Math.abs(sum(b) - sum(a)) > 1e-9) return sum(b) > sum(a) ? 'up' : 'down'
  }
  if (before && (before.kind !== after.kind || before.extra !== after.extra)) return 'adjust'
  return 'same'
}

/** 预检里开放分组的问题（白名单分组的问题会让预览直接失败，到不了这里）。 */
export function planWarnings(ticket: { precheck?: { warnings?: PrecheckIssue[] }[] } | null): PrecheckIssue[] {
  return (ticket?.precheck ?? []).flatMap((r) => r.warnings ?? [])
}
