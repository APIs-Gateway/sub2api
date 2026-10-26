import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'
import { ref } from 'vue'
import type { ShadowSample } from '@/api/admin/pricingOps'
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
import SideDrawer from '../components/SideDrawer.vue'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

const sample = (groupId: number, model: string): ShadowSample => ({
  created_at: '2026-10-06T00:00:00Z', group_id: groupId, model, kind: 'cost', class: 'translation'
})

type SamplesState = {
  samplesGroup: unknown
  samples: ShadowSample[]
  samplesLoading: boolean
  samplesError: string
}

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

  it.each([
    { stale: 'success', current: 'success' },
    { stale: 'error', current: 'success' },
    { stale: 'success', current: 'error' },
    { stale: 'error', current: 'error' }
  ] as const)('关闭 A 打开 B，B $current 后 A $stale 不覆盖 B', async ({ stale, current }) => {
    api.getGroupOpsView.mockImplementation(async (id: number) => view(id, 'shadow'))
    const old = deferred<ShadowSample[]>()
    const latest = deferred<ShadowSample[]>()
    api.getShadowSamples.mockReturnValueOnce(old.promise).mockReturnValueOnce(latest.promise)
    const wrapper = await mountPricingView(StagesView)
    await wrapper.find('[data-test="samples-2"]').trigger('click')
    await wrapper.findComponent(SideDrawer).find('button').trigger('click')
    await wrapper.find('[data-test="samples-1"]').trigger('click')

    if (current === 'success') latest.resolve([sample(1, 'current-group-sample')])
    else latest.reject({ status: 500, reason: 'CURRENT_SAMPLES' })
    await flushPromises()
    const drawer = wrapper.findComponent(SideDrawer)
    expect(drawer.find('h2').text()).toContain('codex')
    const currentContent = drawer.text()

    if (stale === 'success') old.resolve([sample(2, 'stale-group-sample')])
    else old.reject({ status: 500, reason: 'STALE_SAMPLES' })
    await flushPromises()

    expect(api.getShadowSamples.mock.calls).toEqual([[2], [1]])
    expect(drawer.text()).toBe(currentContent)
    expect(drawer.text()).not.toContain('stale-group-sample')
    expect(drawer.text()).not.toContain('STALE_SAMPLES')
    if (current === 'success') {
      expect(drawer.find('[data-test="samples-list"]').text()).toContain('current-group-sample')
      expect(drawer.find('[data-test="samples-error"]').exists()).toBe(false)
    } else {
      expect(drawer.find('[data-test="samples-error"]').text()).toContain('CURRENT_SAMPLES')
      expect(drawer.find('[data-test="samples-list"]').exists()).toBe(false)
    }
    wrapper.unmount()
  })

  it.each(['success', 'error'] as const)('A 的晚到 %s 不会提前结束 B 的加载', async (outcome) => {
    api.getGroupOpsView.mockImplementation(async (id: number) => view(id, 'shadow'))
    const old = deferred<ShadowSample[]>()
    const latest = deferred<ShadowSample[]>()
    api.getShadowSamples.mockReturnValueOnce(old.promise).mockReturnValueOnce(latest.promise)
    const wrapper = await mountPricingView(StagesView)
    await wrapper.find('[data-test="samples-2"]').trigger('click')
    await wrapper.findComponent(SideDrawer).find('button').trigger('click')
    await wrapper.find('[data-test="samples-1"]').trigger('click')

    if (outcome === 'success') old.resolve([sample(2, 'stale-group-sample')])
    else old.reject({ status: 500, reason: 'STALE_SAMPLES' })
    await flushPromises()
    const drawer = wrapper.findComponent(SideDrawer)
    expect(drawer.find('.state-block').exists()).toBe(true)
    expect(drawer.find('[data-test="samples-list"]').exists()).toBe(false)
    expect(drawer.find('[data-test="samples-error"]').exists()).toBe(false)

    latest.resolve([sample(1, 'current-group-sample')])
    await flushPromises()
    expect(drawer.find('.state-block').exists()).toBe(false)
    expect(drawer.find('[data-test="samples-list"]').text()).toContain('current-group-sample')
    wrapper.unmount()
  })

  it('重新打开同一个分组时也只接受最新一次样本请求', async () => {
    const old = deferred<ShadowSample[]>()
    const latest = deferred<ShadowSample[]>()
    api.getShadowSamples.mockReturnValueOnce(old.promise).mockReturnValueOnce(latest.promise)
    const wrapper = await mountPricingView(StagesView)
    await wrapper.find('[data-test="samples-2"]').trigger('click')
    await wrapper.findComponent(SideDrawer).find('button').trigger('click')
    await wrapper.find('[data-test="samples-2"]').trigger('click')
    latest.resolve([sample(2, 'latest-same-group-sample')])
    await flushPromises()
    old.resolve([sample(2, 'stale-same-group-sample')])
    await flushPromises()
    const list = wrapper.find('[data-test="samples-list"]').text()
    expect(list).toContain('latest-same-group-sample')
    expect(list).not.toContain('stale-same-group-sample')
    wrapper.unmount()
  })

  it.each([
    { lifecycle: 'close', outcome: 'success' },
    { lifecycle: 'close', outcome: 'error' },
    { lifecycle: 'unmount', outcome: 'success' },
    { lifecycle: 'unmount', outcome: 'error' }
  ] as const)('$lifecycle 后 pending $outcome 不再更新样本状态', async ({ lifecycle, outcome }) => {
    const request = deferred<ShadowSample[]>()
    api.getShadowSamples.mockReturnValueOnce(request.promise)
    const wrapper = await mountPricingView(StagesView)
    await wrapper.find('[data-test="samples-2"]').trigger('click')
    // Keep the actual component's reactive state observable after its DOM is removed.
    const state = (wrapper.vm.$ as unknown as { setupState: SamplesState }).setupState
    if (lifecycle === 'close') await wrapper.findComponent(SideDrawer).find('button').trigger('click')
    else wrapper.unmount()
    const before = {
      samplesGroup: state.samplesGroup,
      samples: state.samples,
      samplesLoading: state.samplesLoading,
      samplesError: state.samplesError
    }
    expect(before.samplesLoading).toBe(true)

    if (outcome === 'success') request.resolve([sample(2, 'late-sample')])
    else request.reject({ status: 500, reason: 'LATE_SAMPLES' })
    await flushPromises()

    expect(state.samplesGroup).toBe(before.samplesGroup)
    expect(state.samples).toBe(before.samples)
    expect(state.samplesLoading).toBe(before.samplesLoading)
    expect(state.samplesError).toBe(before.samplesError)
    if (lifecycle === 'close') {
      expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
      wrapper.unmount()
    }
  })
})
