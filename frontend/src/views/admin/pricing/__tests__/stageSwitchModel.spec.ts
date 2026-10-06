import { describe, expect, it } from 'vitest'
import type { StagePreview } from '@/api/admin/pricingOps'
import { blockReason, canConfirm, gateFailureCodes, needsRepreview, observationRemainingHours } from '../stageSwitchModel'

const base = (over: Partial<StagePreview> = {}): StagePreview => ({
  action: 'pricing.stage_switch', category: 'pricing_stage', touches_price: true, group_id: 1,
  from: 'shadow', to: 'v2', kind: 'advance', price_delta: 'unknown', approval_id: 7, executable: true,
  expires_at: '2999-01-01T00:00:00Z', accepted_differences: [], ...over
})

describe('blockReason / canConfirm', () => {
  it('切到 v2：有凭证、可执行、未过期才能确认', () => {
    expect(blockReason(base())).toBeNull()
    expect(canConfirm(base())).toBe(true)
  })
  it('没有预览、noop、不可执行各自禁用', () => {
    expect(canConfirm(null)).toBe(false)
    expect(blockReason(base({ kind: 'noop' }))).toBe('noop')
    expect(blockReason(base({ executable: false }))).toBe('notExecutable')
  })
  it('闸门不通过时原因是 gate', () => {
    const gate = { required: true, passed: false, failures: [], observation: {}, shadow: {}, replay: {} } as never
    expect(blockReason(base({ executable: false, approval_id: 0, gate }))).toBe('gate')
  })
  it('切 v2 缺凭证或凭证过期禁用；回拨与 legacy、shadow 之间不需要凭证', () => {
    expect(blockReason(base({ approval_id: 0 }))).toBe('noApproval')
    expect(blockReason(base({ expires_at: '2000-01-01T00:00:00Z' }))).toBe('expired')
    expect(canConfirm(base({ from: 'v2', to: 'shadow', kind: 'rollback', approval_id: 0, expires_at: undefined }))).toBe(true)
    expect(canConfirm(base({ from: 'legacy', to: 'shadow', approval_id: 0, expires_at: undefined }))).toBe(true)
  })
})

describe('observationRemainingHours', () => {
  const obs = (observed: number, satisfied = false) => ({ since: '', observed_hours: observed, required_hours: 1, eligible_at: '', satisfied })
  it('还差几小时，已满为 0', () => {
    expect(observationRemainingHours(obs(0.4))).toBe(0.6)
    expect(observationRemainingHours(obs(100, true))).toBe(0)
  })
})

describe('gateFailureCodes / needsRepreview', () => {
  it('分号分隔的原因逐条取出；没有 metadata 时退回 reason', () => {
    expect(gateFailureCodes('A', { failures: 'A; B;C' })).toEqual(['A', 'B', 'C'])
    expect(gateFailureCodes('A', undefined)).toEqual(['A'])
    expect(gateFailureCodes(undefined, undefined)).toEqual([])
  })
  it('凭证失效、预览后配置变化、闸门不满足都要重新预览', () => {
    for (const r of ['PRICE_WRITE_APPROVAL_NOT_FOUND', 'PRICE_WRITE_APPROVAL_CONSUMED', 'PRICE_WRITE_APPROVAL_EXPIRED', 'PRICE_WRITE_APPROVAL_MISMATCH', 'PRICING_GATE_REPLAY_STALE']) {
      expect(needsRepreview(r), r).toBe(true)
    }
    expect(needsRepreview('PRICING_STAGE_GROUP_NOT_DERIVED')).toBe(false)
    expect(needsRepreview(undefined)).toBe(false)
  })
})
