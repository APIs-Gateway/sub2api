/**
 * 价格写入类接口的错误翻译：按 reason 分支，后端的英文 message 不直接展示。
 * 文案在 admin.pricingOps.errors.<REASON> 下；没有登记的 reason 走通用提示，并把错误代码带出来方便反馈。
 */
import { asOpsError } from '@/api/admin/pricingOps'
import { gateFailureCodes } from './stageSwitchModel'

type Translate = (key: string, params?: Record<string, unknown>) => string
type HasKey = (key: string) => boolean

const NS = 'admin.pricingOps.errors'

/** 必须重新读取最新数据再操作的错误（预览过期、基线变化等）。 */
const STALE_REASONS = new Set([
  'PRICING_SNAPSHOT_PLAN_CHANGED',
  'PRICING_SNAPSHOT_BASELINE_CHANGED',
  'PRICING_SNAPSHOT_NOT_CANDIDATE',
  'PRICING_SNAPSHOT_NOT_FOUND',
  'PRICE_BASELINE_CHANGED',
  'PRICE_WRITE_APPROVAL_NOT_FOUND',
  'PRICE_WRITE_APPROVAL_CONSUMED',
  'PRICE_WRITE_APPROVAL_EXPIRED',
  'PRICE_WRITE_APPROVAL_MISMATCH',
  'PRICE_WRITE_PLAN_CHANGED',
  'PRICING_STAGE_CHANGED'
])

/** 这类错误说明界面上的数据已经过时，调用方应当刷新后让管理员重新确认。 */
export function isStaleError(err: unknown): boolean {
  const reason = asOpsError(err).reason
  return !!reason && STALE_REASONS.has(reason)
}

/** 需要交互式登录会话（令牌与全局密钥不能做）。 */
export function isSessionOnlyError(err: unknown): boolean {
  return asOpsError(err).reason === 'ADMIN_TOKEN_MANAGEMENT_JWT_ONLY'
}

const ISSUES_SHOWN = 3

/** 把 metadata.issues（「分组:模型:原因[->目标]」，分号分隔）整理成「涉及：模型（原因）…」，只列前几项。 */
function listExposureIssues(raw: string, count: string | undefined, t: Translate, te: HasKey): string {
  const issues = raw.split(';').map((x) => x.trim()).filter(Boolean)
  if (issues.length === 0) return ''
  const items = issues.slice(0, ISSUES_SHOWN).map((item) => {
    const parts = item.split('->')[0].split(':')
    // 第一段是分组，最后一段是原因，中间都算模型（模型名可能带冒号）
    if (parts.length < 3) return item
    const model = parts.slice(1, -1).join(':')
    const key = `admin.pricingOps.snapshots.exposureReason.${parts[parts.length - 1]}`
    const why = te(key) ? t(key) : parts[parts.length - 1]
    return `${model}（${why}）`
  })
  const total = Number(count)
  const more = Number.isFinite(total) && total > items.length ? t(`${NS}.exposureMore`, { count: total }) : ''
  return ' ' + t(`${NS}.exposureIssues`, { items: items.join('、') }) + more
}

export function opsErrorText(err: unknown, t: Translate, te: HasKey): string {
  const e = asOpsError(err)
  if (e.status === 0) return t(`${NS}.network`)
  if (e.status === 401) return t(`${NS}.sessionExpired`)
  const reason = e.reason
  // 阶段切换闸门：409，metadata.failures 带全部不满足的原因，逐条翻译
  if (reason?.startsWith('PRICING_GATE_')) {
    const codes = gateFailureCodes(reason, e.metadata)
    let text = codes.map((code) => (te(`${NS}.${code}`) ? t(`${NS}.${code}`) : code)).join('；')
    // 开放检查未通过：metadata.issues 是「分组:模型:原因」，列出前几项
    if (codes.includes('PRICING_GATE_EXPOSURE_BLOCKED') && e.metadata?.issues) {
      text += listExposureIssues(e.metadata.issues, e.metadata.count, t, te)
    }
    return text
  }
  if (reason && te(`${NS}.${reason}`)) {
    const meta = e.metadata ?? {}
    return t(`${NS}.${reason}`, { count: meta.count ?? '', group: meta.group_id ?? '' })
  }
  if (e.status === 403) return t(`${NS}.forbidden`)
  return t(`${NS}.unknown`, { code: reason || String(e.status ?? '') || '-' })
}

export interface ExposureViolation {
  group: string
  model: string
  reason: string
}

/**
 * 预览里的 exposure_error 是后端拼好的一段英文，违规项在 "of them:" 之后，形如「分组:模型:原因;…」。
 * 取出来按条展示；取不出来返回空数组，界面退回通用提示。
 */
export function parseExposureViolations(text: string): ExposureViolation[] {
  const idx = text.indexOf('of them:')
  if (idx < 0) return []
  const tail = text.slice(idx + 'of them:'.length).replace(/\)\s*$/, '')
  return tail
    .split(';')
    .map((s) => s.trim())
    .filter(Boolean)
    .map((item) => {
      const parts = item.split(':')
      // 模型名可能带冒号：第一段是分组，最后一段是原因，中间都算模型
      if (parts.length < 3) return { group: '', model: item, reason: '' }
      return { group: parts[0], model: parts.slice(1, -1).join(':'), reason: parts[parts.length - 1] }
    })
}
