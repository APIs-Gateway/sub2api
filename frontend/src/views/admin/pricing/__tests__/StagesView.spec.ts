import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'
import { ref } from 'vue'
import { mountPricingView } from './mountHelpers'

const { api, admin } = vi.hoisted(() => ({
  api: { getGroupOpsView: vi.fn(), switchGroupStage: vi.fn(), getShadowStats: vi.fn(), getShadowSamples: vi.fn() },
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
  { id: 3, name: 'fresh', platform: 'openai' }
]

describe('StagesView', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    admin.groups.getAll.mockResolvedValue(groups)
    api.getGroupOpsView.mockImplementation(async (id: number) => view(id, id === 1 ? 'legacy' : id === 2 ? 'shadow' : null, id === 3 ? null : 3))
    api.getShadowStats.mockResolvedValue({
      compared: [{ group_id: 2, count: 120 }],
      diffs: [
        { group_id: 2, kind: 'cost', class: 'translation', count: 2 },
        { group_id: 2, kind: 'access', class: 'expected', count: 5 }
      ]
    })
    api.switchGroupStage.mockResolvedValue({ group_id: 1, from: 'legacy', to: 'shadow', changed: true, revision: 4, changed_at: '' })
  })

  it('v2 选项置灰并写暂未开放，点了也不发请求', async () => {
    const wrapper = await mountPricingView(StagesView)
    const v2 = wrapper.find('[data-test="stage-1-v2"]')
    expect(v2.attributes('disabled')).toBeDefined()
    expect(v2.text()).toContain('暂未开放')
    await v2.trigger('click')
    expect(wrapper.find('[data-test="stage-dialog"]').exists()).toBe(false)
    expect(api.switchGroupStage).not.toHaveBeenCalled()
  })

  it('legacy 切到 shadow：先确认，再按契约调用，成功后重读分组', async () => {
    const wrapper = await mountPricingView(StagesView)
    await wrapper.find('[data-test="stage-1-shadow"]').trigger('click')
    expect(wrapper.find('[data-test="stage-dialog"]').text()).toContain('把「codex」从「渠道配置」切换到「对照运行」')
    const calls = api.getGroupOpsView.mock.calls.length
    await wrapper.find('[data-test="stage-confirm"]').trigger('click')
    await flushPromises()
    expect(api.switchGroupStage).toHaveBeenCalledWith(1, 'shadow')
    expect(api.getGroupOpsView.mock.calls.length).toBeGreaterThan(calls)
  })

  it('shadow 切回 legacy，确认框里带对照结果摘要', async () => {
    const wrapper = await mountPricingView(StagesView)
    await wrapper.find('[data-test="stage-2-legacy"]').trigger('click')
    const summary = wrapper.find('[data-test="stage-dialog-summary"]').text()
    expect(summary).toContain('对照 120 次')
    expect(summary).toContain('异常差异 2 个')
    expect(summary).toContain('预期差异 5 个')
    await wrapper.find('[data-test="stage-confirm"]').trigger('click')
    await flushPromises()
    expect(api.switchGroupStage).toHaveBeenCalledWith(2, 'legacy')
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
    await wrapper.find('[data-test="stage-2-legacy"]').trigger('click')
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
    await wrapper.find('[data-test="stage-confirm"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="stage-error"]').text()).toContain('还没有生成价格配置')
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
