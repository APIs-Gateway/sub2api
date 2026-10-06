import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'
import { ref } from 'vue'
import { useAppStore } from '@/stores/app'
import { mountPricingView } from './mountHelpers'

const { api, admin } = vi.hoisted(() => ({
  api: {
    getGroupOpsView: vi.fn(),
    previewGroupStage: vi.fn(),
    switchGroupStage: vi.fn(),
    getStageAudit: vi.fn(),
    getShadowStats: vi.fn(),
    getShadowSamples: vi.fn()
  },
  admin: { groups: { getAll: vi.fn() } }
}))

vi.mock('@/api/admin', () => ({ adminAPI: admin }))
vi.mock('@/api/admin/pricingOps', async () => ({ ...(await vi.importActual<object>('@/api/admin/pricingOps')), ...api }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const { translate, hasKey } = await import('./opsI18n')
  return { ...actual, useI18n: () => ({ locale: ref('zh-CN'), t: translate, te: hasKey }) }
})

import StagesView from '../StagesView.vue'

const view = (groupId: number, stage: string | null, revision: number | null = 3) => ({
  groupId, platform: 'openai', stage, stageChangedAt: null, revision, costRules: []
})

const groups = [
  { id: 1, name: 'codex', platform: 'openai' },
  { id: 2, name: 'shadowed', platform: 'openai' },
  { id: 3, name: 'fresh', platform: 'openai' },
  { id: 4, name: 'live', platform: 'openai' }
]

const preview = (over: Record<string, unknown> = {}) => ({
  action: 'pricing.stage_switch', category: 'pricing_stage', touches_price: true, group_id: 1,
  from: 'legacy', to: 'shadow', kind: 'advance', price_delta: 'none', approval_id: 0,
  executable: true, accepted_differences: [], ...over
})

const gate = (over: Record<string, unknown> = {}) => ({
  required: true, passed: true, failures: [],
  observation: { since: '2026-10-01T00:00:00Z', observed_hours: 100, required_hours: 1, eligible_at: '2026-10-04T00:00:00Z', satisfied: true },
  shadow: { window_from: '2026-10-01T00:00:00Z', translation_diffs: 0, expected_diffs: 2, expected_models: ['gpt-5.4'], compared_in_process: 3400 },
  replay: { present: true, id: 11, rows_replayed: 800, translation_diffs: 0, expected_diffs: 0, rows_errored: 0, passed: true, binding_current: true, channel_config_hash_match: true },
  ...over
})

/** shadow 到 v2、闸门通过：价格方向证明不了，带一条预期差异 */
const v2Ready = (over: Record<string, unknown> = {}) => preview({
  group_id: 2, from: 'shadow', to: 'v2', price_delta: 'unknown', approval_id: 512, plan_hash: 'a'.repeat(64),
  expires_at: '2999-01-01T00:00:00Z', gate: gate(),
  accepted_differences: [{ source: 'replay', kind: 'cost', reason: 'unpriced_zero_equivalent', count: 3, price_delta: 'none' }],
  ...over
})

describe('StagesView', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    admin.groups.getAll.mockResolvedValue(groups)
    api.getGroupOpsView.mockImplementation(async (id: number) => view(id, id === 1 ? 'legacy' : id === 2 ? 'shadow' : id === 4 ? 'v2' : null, id === 3 ? null : 3))
    api.getShadowStats.mockResolvedValue({
      compared: [{ group_id: 2, count: 120 }],
      diffs: [
        { group_id: 2, kind: 'cost', class: 'translation', count: 2 },
        { group_id: 2, kind: 'access', class: 'expected', count: 5 }
      ]
    })
    api.previewGroupStage.mockResolvedValue(preview())
    api.switchGroupStage.mockResolvedValue({ kind: 'advance', group_id: 1, from: 'legacy', to: 'shadow', changed: true, revision: 4, changed_at: '', snapshot_ready: true })
    api.getStageAudit.mockResolvedValue([])
  })

  it('v2 选项不再置灰，也没有「暂未开放」；当前阶段不能再点', async () => {
    const wrapper = await mountPricingView(StagesView)
    expect(wrapper.find('[data-test="stage-2-v2"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.find('[data-test="stage-4-shadow"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.find('[data-test="stage-2-shadow"]').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).not.toContain('暂未开放')
  })

  it('legacy 切到 shadow：先确认，再按契约调用，成功后重读分组', async () => {
    const wrapper = await mountPricingView(StagesView)
    await wrapper.find('[data-test="stage-1-shadow"]').trigger('click')
    await flushPromises()
    expect(api.previewGroupStage).toHaveBeenCalledWith(1, 'shadow')
    expect(api.switchGroupStage).not.toHaveBeenCalled()
    expect(wrapper.find('[data-test="stage-dialog"]').text()).toContain('把「codex」从「渠道配置」切换到「对照运行」')
    const calls = api.getGroupOpsView.mock.calls.length
    await wrapper.find('[data-test="stage-confirm"]').trigger('click')
    await flushPromises()
    expect(api.switchGroupStage).toHaveBeenCalledWith(1, 'shadow', undefined)
    expect(api.getGroupOpsView.mock.calls.length).toBeGreaterThan(calls)
  })

  it('shadow 切回 legacy，确认框里带对照结果摘要', async () => {
    const wrapper = await mountPricingView(StagesView)
    api.previewGroupStage.mockResolvedValue(preview({ group_id: 2, from: 'shadow', to: 'legacy' }))
    await wrapper.find('[data-test="stage-2-legacy"]').trigger('click')
    await flushPromises()
    const summary = wrapper.find('[data-test="stage-dialog-summary"]').text()
    expect(summary).toContain('对照 120 次')
    expect(summary).toContain('异常差异 2 个')
    expect(summary).toContain('预期差异 5 个')
    await wrapper.find('[data-test="stage-confirm"]').trigger('click')
    await flushPromises()
    expect(api.switchGroupStage).toHaveBeenCalledWith(2, 'legacy', undefined)
  })

  it('每行显示对照结果；没有对照记录的分组不编造数字', async () => {
    const wrapper = await mountPricingView(StagesView)
    expect(wrapper.find('[data-test="shadow-2"]').text()).toContain('对照 120 次')
    expect(wrapper.find('[data-test="shadow-1"]').text()).toContain('还没有对照记录')
  })

  it('读不到对照统计时不显示摘要，也不挡切换', async () => {
    api.getShadowStats.mockRejectedValue({ status: 500, message: 'x' })
    const wrapper = await mountPricingView(StagesView)
    expect(wrapper.find('[data-test="shadow-2"]').text()).toBe('—')
    api.previewGroupStage.mockResolvedValue(preview({ group_id: 2, from: 'shadow', to: 'legacy' }))
    await wrapper.find('[data-test="stage-2-legacy"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="stage-dialog-summary"]').exists()).toBe(false)
  })

  it('没有价格配置的分组不能切换', async () => {
    const wrapper = await mountPricingView(StagesView)
    expect(wrapper.find('[data-test="no-config-3"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="stage-3-shadow"]').exists()).toBe(false)
  })

  it('切换失败时在确认框里用能看懂的话说明，并保留确认框', async () => {
    api.switchGroupStage.mockRejectedValue({ status: 409, reason: 'PRICING_STAGE_GROUP_NOT_DERIVED', message: 'x' })
    const wrapper = await mountPricingView(StagesView)
    await wrapper.find('[data-test="stage-1-shadow"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-test="stage-confirm"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="stage-error"]').text()).toContain('还没有生成价格配置')
  })

  // ---------- 预览 → 确认 ----------

  const open = async (wrapper: Awaited<ReturnType<typeof mountPricingView>>, group: number, to: string) => {
    await wrapper.find(`[data-test="stage-${group}-${to}"]`).trigger('click')
    await flushPromises()
  }
  const confirmBtn = (wrapper: Awaited<ReturnType<typeof mountPricingView>>) => wrapper.find('[data-test="stage-confirm"]')

  it('切到 v2、闸门通过：展示价格可能变化、闸门结果与差异汇总，确认时带上 approval_id', async () => {
    api.previewGroupStage.mockResolvedValue(v2Ready())
    api.switchGroupStage.mockResolvedValue({ kind: 'advance', group_id: 2, from: 'shadow', to: 'v2', changed: true, revision: 9, changed_at: '', snapshot_ready: true })
    const wrapper = await mountPricingView(StagesView)
    await open(wrapper, 2, 'v2')
    expect(api.previewGroupStage).toHaveBeenCalledWith(2, 'v2')
    expect(wrapper.find('[data-test="preview-line"]').text()).toContain('把「shadowed」从「对照运行」切换到「新配置」')
    expect(wrapper.find('[data-test="delta-unknown"]').text()).toContain('价格可能变化')
    expect(wrapper.find('[data-test="gate-passed"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="gate-observation"]').text()).toContain('已满 100 小时（需要 1 小时）')
    expect(wrapper.find('[data-test="gate-replay"]').text()).toContain('30 天回放已通过')
    expect(wrapper.find('[data-test="gate-binding"]').text()).toContain('最新的渠道配置')
    expect(wrapper.find('[data-test="accepted-0"]').text()).toContain('没有价格的模型，费用和现在一致')
    expect(wrapper.find('[data-test="accepted-0"]').text()).toContain('3 次')
    expect(confirmBtn(wrapper).attributes('disabled')).toBeUndefined()
    await confirmBtn(wrapper).trigger('click')
    await flushPromises()
    expect(api.switchGroupStage).toHaveBeenCalledWith(2, 'v2', 512)
    expect(wrapper.find('[data-test="stage-dialog"]').exists()).toBe(false)
  })

  it('闸门不通过：逐条列出原因，写明观察期还差多少，确认按钮禁用', async () => {
    api.previewGroupStage.mockResolvedValue(v2Ready({
      executable: false, approval_id: 0, plan_hash: '', expires_at: undefined,
      gate: gate({
        passed: false,
        failures: [
          { code: 'PRICING_GATE_OBSERVATION_SHORT', message: 'x' },
          { code: 'PRICING_GATE_REPLAY_STALE', message: 'y' }
        ],
        observation: { since: '', observed_hours: 0.4, required_hours: 1, eligible_at: '2026-10-07T00:00:00Z', satisfied: false },
        replay: { present: true, rows_replayed: 10, translation_diffs: 0, expected_diffs: 0, rows_errored: 0, passed: true, binding_current: false, channel_config_hash_match: false }
      })
    }))
    const wrapper = await mountPricingView(StagesView)
    await open(wrapper, 2, 'v2')
    expect(wrapper.find('[data-test="gate-failed"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="gate-observation"]').text()).toContain('还差 0.6 小时')
    expect(wrapper.find('[data-test="gate-binding"]').text()).toContain('需要重新回放')
    expect(wrapper.find('[data-test="failure-PRICING_GATE_OBSERVATION_SHORT"]').text()).toContain('不够 1 小时')
    expect(wrapper.find('[data-test="failure-PRICING_GATE_OBSERVATION_SHORT"]').text()).not.toContain('72')
    expect(wrapper.find('[data-test="failure-PRICING_GATE_REPLAY_STALE"]').text()).toContain('需重新执行回放')
    expect(wrapper.find('[data-test="failure-PRICING_GATE_REPLAY_STALE"]').text()).not.toContain('运行回放')
    expect(wrapper.find('[data-test="stage-blocked"]').text()).toContain('切换条件没有满足')
    expect(confirmBtn(wrapper).attributes('disabled')).toBeDefined()
    await confirmBtn(wrapper).trigger('click')
    expect(api.switchGroupStage).not.toHaveBeenCalled()
  })

  it('回拨：展示归档与漂移，不带 approval_id', async () => {
    api.previewGroupStage.mockResolvedValue(preview({
      group_id: 4, from: 'v2', to: 'shadow', kind: 'rollback', price_delta: 'none',
      rollback: { archived_cells: 2, archived_rules: 1, drift: { changed: true, config_changed: false, cells_inserted: 1, cells_updated: 3, cells_deleted: 0, rules_replaced: 1 } }
    }))
    api.switchGroupStage.mockResolvedValue({ kind: 'rollback', group_id: 4, from: 'v2', to: 'shadow', changed: true, revision: 9, changed_at: '', snapshot_ready: true, archived: { cells: 2, rules: 1 } })
    const wrapper = await mountPricingView(StagesView)
    await open(wrapper, 4, 'shadow')
    expect(wrapper.find('[data-test="preview-line"]').text()).toContain('回拨到「对照运行」')
    expect(wrapper.find('[data-test="gate"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="rollback-archived"]').text()).toContain('归档 2 个价格、1 条成本规则')
    expect(wrapper.find('[data-test="rollback-drift"]').text()).toContain('新增 1 个、更新 3 个、删除 0 个')
    await confirmBtn(wrapper).trigger('click')
    await flushPromises()
    expect(api.switchGroupStage).toHaveBeenCalledWith(4, 'shadow', undefined)
  })

  it('noop：说明已经在这个阶段，确认按钮禁用', async () => {
    api.previewGroupStage.mockResolvedValue(preview({ kind: 'noop', from: 'legacy', to: 'legacy' }))
    const wrapper = await mountPricingView(StagesView)
    await open(wrapper, 1, 'shadow')
    expect(wrapper.find('[data-test="preview-line"]').text()).toContain('已经在')
    expect(confirmBtn(wrapper).attributes('disabled')).toBeDefined()
  })

  it('预览凭证已过期或没拿到凭证时禁用确认', async () => {
    api.previewGroupStage.mockResolvedValue(v2Ready({ expires_at: '2000-01-01T00:00:00Z' }))
    let wrapper = await mountPricingView(StagesView)
    await open(wrapper, 2, 'v2')
    expect(confirmBtn(wrapper).attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-test="stage-blocked"]').text()).toContain('预览已过期')

    api.previewGroupStage.mockResolvedValue(v2Ready({ approval_id: 0 }))
    wrapper = await mountPricingView(StagesView)
    await open(wrapper, 2, 'v2')
    expect(confirmBtn(wrapper).attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-test="stage-blocked"]').text()).toContain('没有拿到预览凭证')
  })

  it('预览请求失败：显示原因，不能确认，可以重新预览', async () => {
    api.previewGroupStage.mockRejectedValueOnce({ status: 500, reason: 'SOMETHING_NEW', message: 'boom' })
    const wrapper = await mountPricingView(StagesView)
    await open(wrapper, 1, 'shadow')
    expect(wrapper.find('[data-test="stage-error"]').text()).toContain('SOMETHING_NEW')
    expect(confirmBtn(wrapper).attributes('disabled')).toBeDefined()
    await wrapper.find('[data-test="stage-repreview"]').trigger('click')
    await flushPromises()
    expect(api.previewGroupStage).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-test="stage-error"]').exists()).toBe(false)
    expect(confirmBtn(wrapper).attributes('disabled')).toBeUndefined()
  })

  it.each([
    ['PRICE_WRITE_APPROVAL_EXPIRED', 409, '预览已过期'],
    ['PRICE_WRITE_APPROVAL_CONSUMED', 409, '已经提交过'],
    ['PRICE_WRITE_APPROVAL_NOT_FOUND', 404, '预览已失效'],
    ['PRICE_WRITE_APPROVAL_MISMATCH', 409, '和预览时不一样']
  ])('提交时遇到 %s：提示重新预览，作废当前预览并刷新分组', async (reason, status, text) => {
    api.previewGroupStage.mockResolvedValue(v2Ready())
    api.switchGroupStage.mockRejectedValue({ status, reason, message: 'x' })
    const wrapper = await mountPricingView(StagesView)
    await open(wrapper, 2, 'v2')
    const reads = api.getGroupOpsView.mock.calls.length
    await confirmBtn(wrapper).trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="stage-error"]').text()).toContain(text)
    expect(wrapper.find('[data-test="stage-repreview"]').exists()).toBe(true)
    expect(confirmBtn(wrapper).attributes('disabled')).toBeDefined()
    expect(api.getGroupOpsView.mock.calls.length).toBeGreaterThan(reads)
    // 重新预览后恢复可确认
    await wrapper.find('[data-test="stage-repreview"]').trigger('click')
    await flushPromises()
    expect(api.previewGroupStage).toHaveBeenCalledTimes(2)
    expect(confirmBtn(wrapper).attributes('disabled')).toBeUndefined()
  })

  it('提交时闸门拦截（409）：把 metadata.failures 里的全部原因译成人话', async () => {
    api.previewGroupStage.mockResolvedValue(v2Ready())
    api.switchGroupStage.mockRejectedValue({
      status: 409, reason: 'PRICING_GATE_REPLAY_MISSING', message: 'x',
      metadata: { group_id: '2', failures: 'PRICING_GATE_REPLAY_MISSING;PRICING_GATE_OBSERVATION_SHORT' }
    })
    const wrapper = await mountPricingView(StagesView)
    await open(wrapper, 2, 'v2')
    await confirmBtn(wrapper).trigger('click')
    await flushPromises()
    const text = wrapper.find('[data-test="stage-error"]').text()
    expect(text).toContain('30 天回放缺失')
    expect(text).toContain('观察期未满')
    expect(text).not.toContain('72')
  })

  it('预览里开放检查未通过：作为失败项展示并禁用确认；提交 409 时列出涉及的模型', async () => {
    api.previewGroupStage.mockResolvedValue(v2Ready({
      executable: false, approval_id: 0, plan_hash: '', expires_at: undefined,
      gate: gate({ passed: false, failures: [{ code: 'PRICING_GATE_EXPOSURE_BLOCKED', message: 'x' }] })
    }))
    let wrapper = await mountPricingView(StagesView)
    await open(wrapper, 2, 'v2')
    expect(wrapper.find('[data-test="failure-PRICING_GATE_EXPOSURE_BLOCKED"]').text()).toContain('已知免费名单')
    expect(confirmBtn(wrapper).attributes('disabled')).toBeDefined()

    api.previewGroupStage.mockResolvedValue(v2Ready())
    api.switchGroupStage.mockRejectedValue({
      status: 409, reason: 'PRICING_GATE_EXPOSURE_BLOCKED', message: 'x',
      metadata: { group_id: '2', failures: 'PRICING_GATE_EXPOSURE_BLOCKED', count: '2', issues: '2:gpt-5.5:unpriced;2:m-1:zero_price' }
    })
    wrapper = await mountPricingView(StagesView)
    await open(wrapper, 2, 'v2')
    await confirmBtn(wrapper).trigger('click')
    await flushPromises()
    const text = wrapper.find('[data-test="stage-error"]').text()
    expect(text).toContain('gpt-5.5（没有价格）')
    expect(text).toContain('m-1（价格为 0）')
    expect(wrapper.find('[data-test="stage-repreview"]').exists()).toBe(true)
  })

  it('已提交但本实例没能加载快照：提示部分实例稍后生效', async () => {
    api.switchGroupStage.mockResolvedValue({ kind: 'advance', group_id: 1, from: 'legacy', to: 'shadow', changed: true, revision: 4, changed_at: '', snapshot_ready: false })
    const wrapper = await mountPricingView(StagesView)
    const app = useAppStore()
    const warn = vi.spyOn(app, 'showWarning')
    const ok = vi.spyOn(app, 'showSuccess')
    await open(wrapper, 1, 'shadow')
    await confirmBtn(wrapper).trigger('click')
    await flushPromises()
    expect(ok).toHaveBeenCalled()
    expect(warn).toHaveBeenCalledWith(expect.stringContaining('部分服务实例稍后生效'))
  })

  it('shadow 回 legacy（后端也叫 rollback，但没有 rollback 块）：不显示回拨说明', async () => {
    api.previewGroupStage.mockResolvedValue(preview({ group_id: 2, from: 'shadow', to: 'legacy', kind: 'rollback' }))
    const wrapper = await mountPricingView(StagesView)
    await open(wrapper, 2, 'legacy')
    const text = wrapper.find('[data-test="stage-dialog"]').text()
    expect(text).toContain('切换到「渠道配置」')
    expect(text).toContain('切回渠道配置，对照会停止')
    expect(text).not.toContain('回拨')
    expect(wrapper.find('[data-test="rollback"]').exists()).toBe(false)
  })

  it('提交返回 changed=false：不弹已切换，也不弹实例稍后生效，改提示无需切换并刷新', async () => {
    api.switchGroupStage.mockResolvedValue({ kind: 'noop', group_id: 1, from: 'shadow', to: 'shadow', changed: false, revision: 4, changed_at: '', snapshot_ready: false })
    const wrapper = await mountPricingView(StagesView)
    const app = useAppStore()
    const ok = vi.spyOn(app, 'showSuccess')
    const warn = vi.spyOn(app, 'showWarning')
    const info = vi.spyOn(app, 'showInfo')
    await open(wrapper, 1, 'shadow')
    const reads = api.getGroupOpsView.mock.calls.length
    await confirmBtn(wrapper).trigger('click')
    await flushPromises()
    expect(ok).not.toHaveBeenCalled()
    expect(warn).not.toHaveBeenCalled()
    expect(info).toHaveBeenCalledWith(expect.stringContaining('无需切换'))
    expect(api.getGroupOpsView.mock.calls.length).toBeGreaterThan(reads)
  })

  it('从 v2 回拨成功才说已回拨；重复点确认只提交一次', async () => {
    api.previewGroupStage.mockResolvedValue(preview({ group_id: 4, from: 'v2', to: 'shadow', kind: 'rollback', rollback: { archived_cells: 1, archived_rules: 0, drift: { changed: false, config_changed: false, cells_inserted: 0, cells_updated: 0, cells_deleted: 0, rules_replaced: 0 } } }))
    let release: (v: unknown) => void = () => {}
    api.switchGroupStage.mockReturnValue(new Promise((r) => (release = r)))
    const wrapper = await mountPricingView(StagesView)
    const ok = vi.spyOn(useAppStore(), 'showSuccess')
    await open(wrapper, 4, 'shadow')
    await confirmBtn(wrapper).trigger('click')
    await confirmBtn(wrapper).trigger('click')
    expect(api.switchGroupStage).toHaveBeenCalledTimes(1)
    release({ kind: 'rollback', group_id: 4, from: 'v2', to: 'shadow', changed: true, revision: 9, changed_at: '', snapshot_ready: true, archived: { cells: 1, rules: 0 } })
    await flushPromises()
    expect(ok).toHaveBeenCalledWith(expect.stringContaining('已回拨'))
  })

  it('旧预览请求晚到，不会关掉新分组的加载态', async () => {
    let releaseFirst: (v: unknown) => void = () => {}
    api.previewGroupStage
      .mockReturnValueOnce(new Promise((r) => (releaseFirst = r)))
      .mockReturnValueOnce(new Promise(() => {}))
    const wrapper = await mountPricingView(StagesView)
    await wrapper.find('[data-test="stage-1-shadow"]').trigger('click')
    await wrapper.find('[data-test="stage-2-legacy"]').trigger('click')
    releaseFirst(preview())
    await flushPromises()
    expect(wrapper.find('[data-test="stage-previewing"]').exists()).toBe(true)
  })

  it('切换记录：读取审计，新的在前，写明操作人与版本', async () => {
    api.getStageAudit.mockResolvedValue([
      { id: 34, created_at: '2026-10-06T00:00:00Z', group_id: 2, from: 'v2', to: 'shadow', kind: 'rollback', operator_id: 1, interactive: true, price_delta: '', config_revision_before: 14, config_revision_after: 15 },
      { id: 33, created_at: '2026-10-05T00:00:00Z', group_id: 2, from: 'shadow', to: 'v2', kind: 'advance', operator_id: 1, interactive: true, approval_id: 512, price_delta: 'unknown', config_revision_before: 13, config_revision_after: 14 }
    ])
    const wrapper = await mountPricingView(StagesView)
    await wrapper.find('[data-test="audit-2"]').trigger('click')
    await flushPromises()
    expect(api.getStageAudit).toHaveBeenCalledWith(2)
    const items = wrapper.findAll('[data-test^="audit-item-"]')
    expect(items.map((i) => i.attributes('data-test'))).toEqual(['audit-item-34', 'audit-item-33'])
    expect(items[0].text()).toContain('回拨')
    expect(items[0].find('[data-test="audit-delta"]').text()).toBe('—')
    expect(items[0].text()).not.toContain('priceDelta')
    expect(items[1].text()).toContain('预览凭证 #512')
    expect(items[1].text()).toContain('价格可能变化')
    expect(items[1].text()).toContain('配置版本 13 到 14')
  })

  it('没有切换记录、读取失败各有提示', async () => {
    const wrapper = await mountPricingView(StagesView)
    await wrapper.find('[data-test="audit-2"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="audit-empty"]').exists()).toBe(true)
    api.getStageAudit.mockRejectedValue({ status: 500, reason: 'SOMETHING_NEW' })
    await wrapper.find('[data-test="audit-1"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="audit-error"]').text()).toContain('SOMETHING_NEW')
  })

  it('打开差异样本抽屉', async () => {
    api.getShadowSamples.mockResolvedValue([
      { created_at: '2026-10-06T00:00:00Z', group_id: 2, model: 'gpt-5.5', kind: 'cost', class: 'translation', legacy_view: { a: 1 }, v2_view: { a: 2 } }
    ])
    const wrapper = await mountPricingView(StagesView)
    await wrapper.find('[data-test="samples-2"]').trigger('click')
    await flushPromises()
    expect(api.getShadowSamples).toHaveBeenCalledWith(2)
    const list = wrapper.find('[data-test="samples-list"]').text()
    expect(list).toContain('gpt-5.5')
    expect(list).toContain('用户费用')
    expect(list).toContain('异常')
  })
})
