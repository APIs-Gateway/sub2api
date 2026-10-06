/**
 * 阶段切换确认框的判断逻辑：什么时候能确认、为什么不能、观察期还差多久。
 * 只做纯计算，不碰界面和请求，方便单测。
 */
import type { StageGateObservation, StagePreview } from '@/api/admin/pricingOps'

/** 确认按钮不可用的原因；null 表示可以确认。 */
export type BlockReason = 'noop' | 'gate' | 'noApproval' | 'expired' | 'notExecutable' | null

/** 凭证快过期时（毫秒时间戳比较）提前拦下，避免管理员点了才被后端拒绝。 */
export function previewExpired(preview: StagePreview, now: number = Date.now()): boolean {
  if (!preview.expires_at) return false
  const at = Date.parse(preview.expires_at)
  return Number.isFinite(at) && at <= now
}

/** 预览结果能不能提交；不能时给出原因，界面据此说明。 */
export function blockReason(preview: StagePreview | null, now: number = Date.now()): BlockReason {
  if (!preview) return 'notExecutable'
  if (preview.kind === 'noop') return 'noop'
  if (!preview.executable) return preview.gate && !preview.gate.passed ? 'gate' : 'notExecutable'
  // 切到 v2（前进）必须有凭证；回拨和 legacy、shadow 之间不需要
  if (preview.to === 'v2' && preview.kind === 'advance') {
    if (!preview.approval_id) return 'noApproval'
    if (previewExpired(preview, now)) return 'expired'
  }
  return null
}

export function canConfirm(preview: StagePreview | null, now: number = Date.now()): boolean {
  return blockReason(preview, now) === null
}

/** 观察期还差多少小时（已满为 0），保留一位小数。 */
export function observationRemainingHours(obs: StageGateObservation): number {
  const left = Math.max(0, obs.required_hours - obs.observed_hours)
  return Math.round(left * 10) / 10
}

export function roundHours(h: number): number {
  return Math.round(h * 10) / 10
}

/** 闸门拦截的 409 里，metadata.failures 是分号分隔的全部原因码；缺失时退回 reason。 */
export function gateFailureCodes(reason: string | undefined, metadata: Record<string, string> | undefined): string[] {
  const raw = metadata?.failures
  const codes = raw ? raw.split(';').map((c) => c.trim()).filter(Boolean) : []
  if (codes.length === 0 && reason) codes.push(reason)
  return codes
}

/** 说明预览已经不能用了、需要重新预览的错误：凭证不存在、已用、过期、预览后配置变了。 */
const REPREVIEW_REASONS = new Set([
  'PRICE_WRITE_APPROVAL_NOT_FOUND',
  'PRICE_WRITE_APPROVAL_CONSUMED',
  'PRICE_WRITE_APPROVAL_EXPIRED',
  'PRICE_WRITE_APPROVAL_MISMATCH',
  'PRICE_WRITE_PLAN_CHANGED',
  'PRICING_STAGE_CHANGED',
  'PRICING_STAGE_APPROVAL_REQUIRED',
  'PRICE_WRITE_APPROVAL_REQUIRED'
])

export function needsRepreview(reason: string | undefined): boolean {
  return !!reason && (REPREVIEW_REASONS.has(reason) || reason.startsWith('PRICING_GATE_'))
}
