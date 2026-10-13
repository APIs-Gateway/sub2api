/**
 * 价格写入接口的错误：按 reason 分支，message 是英文不展示。
 * 返回界面要用的提示键、可替换的参数，以及管理员下一步该做什么。
 */

export interface WriteError {
  status: number
  reason: string
  metadata: Record<string, string>
}

/** 需要先刷新数据再重新操作 / 重新预览即可 / 没有补救动作。 */
export type ErrorAction = 'refresh' | 'repreview' | 'none'

const KNOWN: Record<string, ErrorAction> = {
  ADMIN_TOKEN_MANAGEMENT_JWT_ONLY: 'none',
  PRICE_WRITE_ACTOR_REQUIRED: 'none',
  PRICE_WRITE_APPROVAL_REQUIRED: 'repreview',
  PRICE_WRITE_INTERACTIVE_REQUIRED: 'none',
  PRICE_WRITE_CONFIRM_REQUIRED: 'repreview',
  PRICE_WRITE_APPROVAL_NOT_FOUND: 'repreview',
  PRICE_WRITE_APPROVAL_EXPIRED: 'repreview',
  PRICE_WRITE_APPROVAL_CONSUMED: 'repreview',
  PRICE_WRITE_APPROVAL_MISMATCH: 'repreview',
  PRICE_WRITE_PLAN_CHANGED: 'repreview',
  PRICE_BASELINE_CHANGED: 'refresh',
  EXPOSURE_UNPRICED: 'none',
  OPEN_PRECHECK_BLOCKED: 'none',
  PRICE_WRITER_UNAVAILABLE: 'none',
  EXPOSURE_GUARD_UNAVAILABLE: 'none',
  CELL_OPS_EMPTY: 'none',
  CELL_OPS_TOO_MANY: 'none',
  CELL_OP_INVALID: 'none',
  CELL_OP_DUPLICATE: 'none',
  CELL_GROUP_BASELINE_MISSING: 'refresh',
  CELL_GROUP_NOT_V2: 'refresh',
  CELL_READONLY: 'none',
  MODEL_CATALOG_USAGE_CONFIRM_REQUIRED: 'none',
  MODEL_CATALOG_STATUS_CHANGED: 'refresh',
  MODEL_CATALOG_INVALID: 'none',
  MODEL_CATALOG_NOT_FOUND: 'refresh',
  MODEL_CATALOG_EXISTS: 'none',
  MODEL_CATALOG_ALIAS_CONFLICT: 'none'
}

export function toWriteError(err: unknown): WriteError {
  const e = (err ?? {}) as { status?: number; reason?: string; metadata?: Record<string, unknown> }
  const metadata: Record<string, string> = {}
  for (const [k, v] of Object.entries(e.metadata ?? {})) metadata[k] = String(v)
  return { status: typeof e.status === 'number' ? e.status : 0, reason: e.reason ?? '', metadata }
}

export interface ErrorInfo {
  /** i18n 键，在 admin.pricingConfig.write.error 下 */
  key: string
  params: Record<string, string | number>
  action: ErrorAction
}

export function errorInfo(err: WriteError): ErrorInfo {
  const { reason, metadata, status } = err
  const params: Record<string, string | number> = { ...metadata, status }
  if (reason in KNOWN) return { key: reason, params, action: KNOWN[reason] }
  if (status === 0) return { key: 'NETWORK', params, action: 'none' }
  if (status === 400) return { key: 'INVALID', params, action: 'none' }
  if (status === 401) return { key: 'UNAUTHORIZED', params, action: 'none' }
  if (status === 403) return { key: 'FORBIDDEN', params, action: 'none' }
  return { key: 'UNKNOWN', params, action: 'none' }
}

export interface ParsedIssue {
  groupId: number | null
  model: string
  reason: string
  target: string | null
}

/**
 * 解析 `分组:模型:原因[->映射目标]` 用分号连接的列表（EXPOSURE_UNPRICED、OPEN_PRECHECK_BLOCKED 的 metadata）。
 * 模型名里可能带冒号，所以第一个冒号前是分组，最后一个冒号后是原因。
 */
export function parseIssues(text: string | undefined): ParsedIssue[] {
  if (!text) return []
  const out: ParsedIssue[] = []
  for (const part of text.split(';')) {
    const item = part.trim()
    if (!item) continue
    const first = item.indexOf(':')
    const last = item.lastIndexOf(':')
    if (first < 0 || last <= first) continue
    const group = Number(item.slice(0, first))
    const model = item.slice(first + 1, last)
    const rest = item.slice(last + 1)
    const [reason, target] = rest.split('->')
    out.push({ groupId: Number.isInteger(group) && group > 0 ? group : null, model, reason, target: target ?? null })
  }
  return out
}
