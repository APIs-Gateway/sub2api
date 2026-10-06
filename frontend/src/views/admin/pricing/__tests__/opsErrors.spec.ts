import { describe, expect, it } from 'vitest'
import zhCN from '@/i18n/locales/zh-CN'
import { isSessionOnlyError, isStaleError, opsErrorText, parseExposureViolations } from '../opsErrors'

const lookup = (key: string) => key.split('.').reduce<unknown>((o, k) => (o as Record<string, unknown> | undefined)?.[k], zhCN)
const t = (key: string, params: Record<string, unknown> = {}) => {
  const hit = lookup(key)
  if (typeof hit !== 'string') return key
  return hit.replace(/\{(\w+)\}/g, (_, n) => String(params[n] ?? `{${n}}`))
}
const te = (key: string) => typeof lookup(key) === 'string'

describe('opsErrorText', () => {
  it('按 reason 翻译，不展示后端的英文 message', () => {
    const text = opsErrorText({ status: 403, reason: 'ADMIN_TOKEN_MANAGEMENT_JWT_ONLY', message: 'forbidden: jwt only' }, t, te)
    expect(text).toContain('管理员账号登录后台')
    expect(text).not.toContain('jwt only')
  })

  it('会话问题、网络问题、没有登记的 reason 各有提示', () => {
    expect(opsErrorText({ status: 401, message: 'x' }, t, te)).toContain('登录已过期')
    expect(opsErrorText({ status: 0, message: 'Network error' }, t, te)).toContain('网络不通')
    expect(opsErrorText({ status: 500, reason: 'SOMETHING_NEW', message: 'boom' }, t, te)).toContain('SOMETHING_NEW')
    expect(opsErrorText({ status: 403, message: 'x' }, t, te)).toContain('没有权限')
  })

  it('带出 metadata 里的数量', () => {
    const text = opsErrorText({ status: 400, reason: 'EXPOSURE_UNPRICED', metadata: { count: '3' } }, t, te)
    expect(text).toContain('3 个')
  })

  it('预览过期、基线变化一类算过期错误，需要刷新后重新确认', () => {
    for (const reason of ['PRICING_SNAPSHOT_PLAN_CHANGED', 'PRICE_BASELINE_CHANGED', 'PRICE_WRITE_APPROVAL_EXPIRED']) {
      expect(isStaleError({ reason })).toBe(true)
    }
    expect(isStaleError({ reason: 'COST_RULE_INVALID' })).toBe(false)
    expect(isSessionOnlyError({ reason: 'ADMIN_TOKEN_MANAGEMENT_JWT_ONLY' })).toBe(true)
  })

  it('闸门 409：按 metadata.failures 逐条翻译，缺失时用 reason', () => {
    const many = opsErrorText({ status: 409, reason: 'PRICING_GATE_REPLAY_MISSING', metadata: { failures: 'PRICING_GATE_REPLAY_MISSING;PRICING_GATE_REPLAY_STALE' } }, t, te)
    expect(many).toContain('30 天回放记录')
    expect(many).toContain('重新运行回放')
    expect(opsErrorText({ status: 409, reason: 'PRICING_GATE_NOT_IN_SHADOW' }, t, te)).toContain('先切到对照运行')
  })

  it('凭证问题（404/409）和预览后配置变化都算过期错误', () => {
    for (const reason of ['PRICE_WRITE_APPROVAL_NOT_FOUND', 'PRICE_WRITE_APPROVAL_CONSUMED', 'PRICE_WRITE_APPROVAL_MISMATCH', 'PRICING_STAGE_CHANGED']) {
      expect(isStaleError({ reason }), reason).toBe(true)
    }
  })

  it('契约里列出的 reason 都有文案', () => {
    const reasons = [
      'ADMIN_TOKEN_MANAGEMENT_JWT_ONLY', 'PRICE_WRITE_ACTOR_REQUIRED', 'PRICE_WRITE_APPROVAL_REQUIRED', 'PRICE_WRITE_INTERACTIVE_REQUIRED',
      'PRICE_WRITE_CONFIRM_REQUIRED', 'PRICE_WRITE_APPROVAL_NOT_FOUND', 'PRICE_WRITE_APPROVAL_EXPIRED', 'PRICE_WRITE_APPROVAL_CONSUMED',
      'PRICE_WRITE_APPROVAL_MISMATCH', 'PRICE_WRITE_PLAN_CHANGED', 'PRICE_BASELINE_CHANGED', 'EXPOSURE_UNPRICED', 'PRICE_WRITER_UNAVAILABLE',
      'EXPOSURE_GUARD_UNAVAILABLE', 'PRICING_STAGE_NOT_ALLOWED', 'PRICING_STAGE_CONFIRM_REQUIRED', 'PRICING_STAGE_ACTOR_REQUIRED',
      'PRICING_STAGE_GROUP_NOT_DERIVED', 'PRICING_SNAPSHOT_NOT_FOUND', 'PRICING_NOT_PINNED', 'PRICING_ALREADY_PINNED',
      'PRICING_BOOTSTRAP_MISMATCH', 'PRICING_SNAPSHOT_NOT_CANDIDATE', 'PRICING_SNAPSHOT_PLAN_CHANGED', 'PRICING_SNAPSHOT_BASELINE_CHANGED',
      'PRICING_SNAPSHOT_NOTHING_TO_APPROVE', 'PRICING_SNAPSHOT_UNKNOWN_HOLD', 'PRICING_SNAPSHOT_EXPOSURE_UNAVAILABLE', 'CONFIRMATION_REQUIRED',
      'PRICING_STAGE_CHANGED', 'PRICING_STAGE_APPROVAL_REQUIRED', 'PRICING_GATE_NOT_IN_SHADOW', 'PRICING_GATE_OBSERVATION_SHORT',
      'PRICING_GATE_SHADOW_DIFFS', 'PRICING_GATE_SHADOW_DIFFS_IN_PROCESS', 'PRICING_GATE_REPLAY_EMPTY', 'PRICING_GATE_REPLAY_MISSING',
      'PRICING_GATE_REPLAY_FAILED', 'PRICING_GATE_REPLAY_WINDOW_SHORT', 'PRICING_GATE_REPLAY_TOO_OLD', 'PRICING_GATE_REPLAY_STALE',
      'PRICING_GATE_DERIVE_FAILED'
    ]
    for (const r of reasons) expect(te(`admin.pricingOps.errors.${r}`), r).toBe(true)
  })
})

describe('parseExposureViolations', () => {
  it('取出后端拼在文本里的违规项', () => {
    const text = 'exposure check failed (2 of them: 7:gpt-5.5:unpriced; 9:vendor/m:1:zero_price)'
    expect(parseExposureViolations(text)).toEqual([
      { group: '7', model: 'gpt-5.5', reason: 'unpriced' },
      { group: '9', model: 'vendor/m:1', reason: 'zero_price' }
    ])
  })

  it('没有违规项清单时返回空', () => {
    expect(parseExposureViolations('something else')).toEqual([])
  })
})
