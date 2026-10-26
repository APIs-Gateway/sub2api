import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'
import { ref } from 'vue'
import { mountPricingView } from './mountHelpers'

const { api, admin } = vi.hoisted(() => ({
  api: { getGroupOpsView: vi.fn(), createCostRule: vi.fn(), updateCostRule: vi.fn(), deleteCostRule: vi.fn() },
  admin: { groups: { getAll: vi.fn() } }
}))

vi.mock('@/api/admin', () => ({ adminAPI: admin }))
vi.mock('@/api/admin/pricingOps', async () => ({ ...(await vi.importActual<object>('@/api/admin/pricingOps')), ...api }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const { translate, hasKey } = await import('./opsI18n')
  return { ...actual, useI18n: () => ({ locale: ref('zh-CN'), t: translate, te: hasKey }) }
})

import { useAppStore } from '@/stores/app'
import CostRulesView from '../CostRulesView.vue'
import CostRuleDrawer from '../components/CostRuleDrawer.vue'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

const baselineConflict = { status: 409, reason: 'PRICE_BASELINE_CHANGED', metadata: { group_id: '7', revision: '13' } }

const rule = (over: Record<string, unknown> = {}) => ({
  id: 41, scope_group_id: 7, source: 'manual', name: '官方成本', group_ids: [7], account_ids: [21, 22], sort_order: 0, enabled: true,
  prices: [{ platform: 'openai', models: ['gpt-5.5', 'gpt-5.5-mini'], price: { billing_mode: 'token', input_price: 1.25e-6, output_price: 1e-5 } }],
  ...over
})

const view = (stage: string, rules: unknown[], revision = 12) => ({ groupId: 7, platform: 'openai', stage, stageChangedAt: null, revision, costRules: rules })

async function mountView() {
  const wrapper = await mountPricingView(CostRulesView)
  const app = useAppStore()
  return { wrapper, showError: vi.spyOn(app, 'showError'), showSuccess: vi.spyOn(app, 'showSuccess') }
}

describe('CostRulesView', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    admin.groups.getAll.mockResolvedValue([{ id: 7, name: 'codex', platform: 'openai' }])
    api.getGroupOpsView.mockResolvedValue(view('v2', [rule(), rule({ id: 42, name: '渠道推出', source: 'legacy_derived', sort_order: 1, enabled: false })]))
    api.createCostRule.mockResolvedValue({ group_id: 7, rule_id: 43, revision: 13 })
    api.updateCostRule.mockResolvedValue({ group_id: 7, rule_id: 41, revision: 13 })
    api.deleteCostRule.mockResolvedValue({ group_id: 7, rule_id: 41, revision: 13 })
  })

  it('列出规则：来源、命中条件、每百万 Token 的成本价；说明只影响成本统计', async () => {
    const { wrapper } = await mountView()
    expect(wrapper.find('[data-test="cost-note"]').text()).toContain('不改变用户实付的价格')
    const row = wrapper.find('[data-test="rule-41"]').text()
    expect(row).toContain('官方成本')
    expect(row).toContain('1 个分组')
    expect(row).toContain('2 个账号')
    expect(row).toContain('gpt-5.5 等 2 个模型')
    expect(row).toContain('$1.25')
    expect(wrapper.find('[data-test="disabled-42"]').exists()).toBe(true)
  })

  it('渠道推出的规则只读', async () => {
    const { wrapper } = await mountView()
    expect(wrapper.find('[data-test="derived-42"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="edit-42"]').exists()).toBe(false)
  })

  it('分组不在 v2 时只能看，新建、编辑、删除都置灰并说明原因', async () => {
    api.getGroupOpsView.mockResolvedValue(view('legacy', [rule()]))
    const { wrapper } = await mountView()
    expect(wrapper.find('[data-test="locked-note"]').text()).toContain('还没有切换到新配置')
    expect(wrapper.find('[data-test="create-rule"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-test="edit-41"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-test="delete-41"]').attributes('disabled')).toBeDefined()
  })

  it('新建：价格按每 Token 提交，基线用当前 revision，成功后重读', async () => {
    const { wrapper, showSuccess } = await mountView()
    await wrapper.find('[data-test="create-rule"]').trigger('click')
    await wrapper.find('[data-test="cr-name"]').setValue('新规则')
    await wrapper.find('[data-test="cr-models-0"]').setValue('gpt-5.5')
    await wrapper.find('[data-test="cr-input-0"]').setValue('1.25')
    await wrapper.find('[data-test="cr-output-0"]').setValue('10')
    await wrapper.find('[data-test="cr-save"]').trigger('click')
    await flushPromises()
    expect(api.createCostRule).toHaveBeenCalledWith(7, 12, {
      name: '新规则',
      group_ids: [7],
      account_ids: [],
      sort_order: 0,
      enabled: true,
      prices: [{ platform: '', models: ['gpt-5.5'], price: { billing_mode: 'token', input_price: 1.25e-6, output_price: 1e-5, cache_write_price: null, cache_read_price: null, image_output_price: null } }]
    })
    expect(showSuccess).toHaveBeenCalled()
  })

  it('表单不合法时不发请求，直接说哪里不对', async () => {
    const { wrapper } = await mountView()
    await wrapper.find('[data-test="create-rule"]').trigger('click')
    await wrapper.find('[data-test="cr-save"]').trigger('click')
    expect(api.createCostRule).not.toHaveBeenCalled()
    expect(wrapper.find('[data-test="cr-error"]').text()).toContain('规则名称不能为空')
  })

  it('编辑：回填成每百万 Token，整条替换', async () => {
    const { wrapper } = await mountView()
    await wrapper.find('[data-test="edit-41"]').trigger('click')
    expect((wrapper.find('[data-test="cr-input-0"]').element as HTMLInputElement).value).toBe('1.25')
    expect((wrapper.find('[data-test="cr-output-0"]').element as HTMLInputElement).value).toBe('10')
    await wrapper.find('[data-test="cr-output-0"]').setValue('12')
    await wrapper.find('[data-test="cr-save"]').trigger('click')
    await flushPromises()
    expect(api.updateCostRule).toHaveBeenCalledTimes(1)
    const [gid, rid, rev, body] = api.updateCostRule.mock.calls[0]
    expect([gid, rid, rev]).toEqual([7, 41, 12])
    expect(body.prices[0].price.output_price).toBeCloseTo(1.2e-5, 12)
    expect(body.account_ids).toEqual([21, 22])
  })

  it('编辑按 Token 计费的规则：抽屉显示图片输出价，保存后保持原值', async () => {
    api.getGroupOpsView.mockResolvedValue(view('v2', [rule({
      prices: [{ platform: 'openai', models: ['gpt-image-2'], price: { billing_mode: 'token', input_price: 5e-6, output_price: 4e-5, image_output_price: 3e-5 } }]
    })]))
    const { wrapper } = await mountView()
    await wrapper.find('[data-test="edit-41"]').trigger('click')
    expect((wrapper.find('[data-test="cr-imageOutput-0"]').element as HTMLInputElement).value).toBe('30')
    await wrapper.find('[data-test="cr-name"]').setValue('改个名字')
    await wrapper.find('[data-test="cr-save"]').trigger('click')
    await flushPromises()
    const body = api.updateCostRule.mock.calls[0][3]
    expect(body.prices[0].price.image_output_price).toBeCloseTo(3e-5, 12)
  })

  it('按次/按图片计费不显示图片输出价', async () => {
    const { wrapper } = await mountView()
    await wrapper.find('[data-test="create-rule"]').trigger('click')
    expect(wrapper.find('[data-test="cr-imageOutput-0"]').exists()).toBe(true)
    await wrapper.find('[data-test="cr-mode-0"]').setValue('image')
    expect(wrapper.find('[data-test="cr-imageOutput-0"]').exists()).toBe(false)
    await wrapper.find('[data-test="cr-mode-0"]').setValue('per_request')
    expect(wrapper.find('[data-test="cr-imageOutput-0"]').exists()).toBe(false)
  })

  it('基线过期：提示并重读这个分组，抽屉保留', async () => {
    api.updateCostRule.mockRejectedValue({ status: 409, reason: 'PRICE_BASELINE_CHANGED', metadata: { group_id: '7', revision: '14' }, message: 'x' })
    const { wrapper } = await mountView()
    const before = api.getGroupOpsView.mock.calls.length
    await wrapper.find('[data-test="edit-41"]').trigger('click')
    await wrapper.find('[data-test="cr-save"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="cr-error"]').text()).toContain('刚被别人改过')
    expect(api.getGroupOpsView.mock.calls.length).toBeGreaterThan(before)
  })

  it('需要登录会话的错误不吞掉', async () => {
    api.createCostRule.mockRejectedValue({ status: 403, reason: 'ADMIN_TOKEN_MANAGEMENT_JWT_ONLY', message: 'x' })
    const { wrapper } = await mountView()
    await wrapper.find('[data-test="create-rule"]').trigger('click')
    await wrapper.find('[data-test="cr-name"]').setValue('r')
    await wrapper.find('[data-test="cr-models-0"]').setValue('m')
    await wrapper.find('[data-test="cr-save"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="cr-error"]').text()).toContain('管理员账号登录后台')
  })

  it('价格超限（后端 COST_RULE_INVALID / PRICE_TOO_HIGH）时说明上限', async () => {
    api.createCostRule.mockRejectedValue({ status: 400, reason: 'COST_RULE_INVALID', metadata: { field: 'input_price', reason: 'PRICE_TOO_HIGH' }, message: 'x' })
    const { wrapper } = await mountView()
    await wrapper.find('[data-test="create-rule"]').trigger('click')
    await wrapper.find('[data-test="cr-name"]').setValue('r')
    await wrapper.find('[data-test="cr-models-0"]').setValue('m')
    await wrapper.find('[data-test="cr-save"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="cr-error"]').text()).toContain('价格超出上限')
  })

  it('删除：确认后带基线调用', async () => {
    const { wrapper } = await mountView()
    await wrapper.find('[data-test="delete-41"]').trigger('click')
    const buttons = wrapper.findAll('button').filter((b) => b.text() === '删除')
    await buttons[buttons.length - 1].trigger('click')
    await flushPromises()
    expect(api.deleteCostRule).toHaveBeenCalledWith(7, 41, 12)
  })

  it('冲突后按钮和表单都不能重试旧草稿，关闭重开后完整提交服务端新规则', async () => {
    api.updateCostRule.mockRejectedValueOnce(baselineConflict)
    const { wrapper } = await mountView()
    const intervals = [{ max_tokens: 50, input_price: 1e-6 }]
    api.getGroupOpsView.mockResolvedValue(view('v2', [rule({
      name: '服务端新版', group_ids: [7, 8], account_ids: [30], sort_order: 4, enabled: false,
      prices: [{ platform: 'anthropic', models: ['model-new'], price: {
        billing_mode: 'token', input_price: 2e-6, output_price: 1.2e-5,
        cache_write_price: 3e-6, cache_read_price: 4e-6, image_output_price: 5e-6, intervals
      } }]
    })], 13))
    await wrapper.find('[data-test="edit-41"]').trigger('click')
    await wrapper.find('[data-test="cr-name"]').setValue('本地旧草稿')
    await wrapper.find('[data-test="cr-save"]').trigger('click')
    await flushPromises()
    expect(api.updateCostRule.mock.calls[0].slice(0, 3)).toEqual([7, 41, 12])
    expect(api.updateCostRule.mock.calls[0][3].prices[0].price.output_price).toBeCloseTo(1e-5, 12)
    expect((wrapper.find('[data-test="cr-name"]').element as HTMLInputElement).value).toBe('本地旧草稿')
    expect(wrapper.find('[data-test="cr-error"]').text()).toContain('请关闭抽屉')
    expect(wrapper.find('[data-test="cr-save"]').attributes('disabled')).toBeDefined()
    await wrapper.find('[data-test="cr-save"]').trigger('click')
    await wrapper.find('[data-test="cost-form"]').trigger('submit')
    expect(api.updateCostRule).toHaveBeenCalledTimes(1)

    await wrapper.findComponent(CostRuleDrawer).find('button').trigger('click')
    await wrapper.find('[data-test="edit-41"]').trigger('click')
    expect((wrapper.find('[data-test="cr-name"]').element as HTMLInputElement).value).toBe('服务端新版')
    expect((wrapper.find('[data-test="cr-output-0"]').element as HTMLInputElement).value).toBe('12')
    await wrapper.find('[data-test="cr-name"]').setValue('重新确认的名字')
    await wrapper.find('[data-test="cr-save"]').trigger('click')
    await flushPromises()
    expect(api.updateCostRule).toHaveBeenCalledTimes(2)
    expect(api.updateCostRule.mock.calls[1]).toEqual([7, 41, 13, {
      name: '重新确认的名字', group_ids: [7, 8], account_ids: [30], sort_order: 4, enabled: false,
      prices: [{ platform: 'anthropic', models: ['model-new'], price: {
        billing_mode: 'token', input_price: expect.closeTo(2e-6, 12), output_price: expect.closeTo(1.2e-5, 12),
        cache_write_price: expect.closeTo(3e-6, 12), cache_read_price: expect.closeTo(4e-6, 12),
        image_output_price: expect.closeTo(5e-6, 12), intervals
      } }]
    }])
    wrapper.unmount()
  })

  it.each(['deleted', 'readonly', 'derived', 'reload-failed'] as const)('冲突重读后 %s 也不能再次提交旧规则', async (outcome) => {
    api.updateCostRule.mockRejectedValueOnce(baselineConflict)
    const { wrapper } = await mountView()
    if (outcome === 'reload-failed') api.getGroupOpsView.mockRejectedValue({ status: 500 })
    else api.getGroupOpsView.mockResolvedValue(view(
      outcome === 'readonly' ? 'legacy' : 'v2',
      outcome === 'deleted' ? [] : [rule(outcome === 'derived' ? { source: 'legacy_derived' } : {})], 13
    ))
    await wrapper.find('[data-test="edit-41"]').trigger('click')
    await wrapper.find('[data-test="cr-name"]').setValue('旧草稿')
    await wrapper.find('[data-test="cr-save"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="cr-error"]').text()).toContain('当前草稿不能再保存')
    expect(wrapper.find('[data-test="cr-save"]').attributes('disabled')).toBeDefined()
    await wrapper.find('[data-test="cr-save"]').trigger('click')
    await wrapper.find('[data-test="cost-form"]').trigger('submit')
    expect(api.updateCostRule).toHaveBeenCalledTimes(1)
    expect(api.createCostRule).not.toHaveBeenCalled()
    await wrapper.findComponent(CostRuleDrawer).find('button').trigger('click')
    if (outcome === 'deleted' || outcome === 'derived') expect(wrapper.find('[data-test="edit-41"]').exists()).toBe(false)
    else expect(wrapper.find('[data-test="create-rule"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it.each(['revision', 'stage'] as const)('手动刷新改变 %s 时不把新基线附到打开的旧表单', async (change) => {
    const { wrapper } = await mountView()
    await wrapper.find('[data-test="edit-41"]').trigger('click')
    await wrapper.find('[data-test="cr-name"]').setValue('旧草稿')
    api.getGroupOpsView.mockResolvedValue(view(change === 'stage' ? 'legacy' : 'v2', [rule()], change === 'revision' ? 13 : 12))
    await wrapper.find('[data-test="refresh"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="cr-error"]').text()).toContain('请关闭抽屉')
    expect(wrapper.find('[data-test="cr-save"]').attributes('disabled')).toBeDefined()
    await wrapper.find('[data-test="cr-save"]').trigger('click')
    await wrapper.find('[data-test="cost-form"]').trigger('submit')
    expect(api.updateCostRule).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('请求期间切换分组后仍重读原请求分组，也不能将旧表单提交到新分组', async () => {
    admin.groups.getAll.mockResolvedValue([{ id: 7, name: 'codex', platform: 'openai' }, { id: 8, name: 'other', platform: 'openai' }])
    api.getGroupOpsView.mockImplementation(async (id: number) => ({ ...view('v2', [rule()]), groupId: id }))
    const request = deferred<unknown>()
    api.updateCostRule.mockReturnValueOnce(request.promise)
    const { wrapper } = await mountView()
    await wrapper.find('[data-test="edit-41"]').trigger('click')
    await wrapper.find('[data-test="cr-save"]').trigger('click')
    await wrapper.find('[data-test="cost-group"]').find('button').trigger('click')
    const other = wrapper.findAll('[role="option"]').find((option) => option.text().includes('other'))!
    await other.trigger('click')
    const readsBeforeError = api.getGroupOpsView.mock.calls.length
    request.reject(baselineConflict)
    await flushPromises()
    expect(api.getGroupOpsView.mock.calls.slice(readsBeforeError)).toEqual([[7]])
    expect(wrapper.find('[data-test="cr-save"]').attributes('disabled')).toBeDefined()
    await wrapper.find('[data-test="cost-form"]').trigger('submit')
    expect(api.updateCostRule).toHaveBeenCalledTimes(1)
    expect(api.updateCostRule.mock.calls[0].slice(0, 3)).toEqual([7, 41, 12])
    wrapper.unmount()
  })

  it('新建冲突后必须关闭重开，新表单才使用刷新后的 revision', async () => {
    api.createCostRule.mockRejectedValueOnce(baselineConflict)
    const { wrapper } = await mountView()
    api.getGroupOpsView.mockResolvedValue(view('v2', [rule()], 13))
    await wrapper.find('[data-test="create-rule"]').trigger('click')
    await wrapper.find('[data-test="cr-name"]').setValue('旧新建草稿')
    await wrapper.find('[data-test="cr-models-0"]').setValue('old-model')
    await wrapper.find('[data-test="cr-save"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-test="cost-form"]').trigger('submit')
    expect(api.createCostRule).toHaveBeenCalledTimes(1)
    expect(api.createCostRule.mock.calls[0].slice(0, 2)).toEqual([7, 12])
    await wrapper.findComponent(CostRuleDrawer).find('button').trigger('click')
    await wrapper.find('[data-test="create-rule"]').trigger('click')
    expect((wrapper.find('[data-test="cr-name"]').element as HTMLInputElement).value).toBe('')
    await wrapper.find('[data-test="cr-name"]').setValue('重新确认的新规则')
    await wrapper.find('[data-test="cr-models-0"]').setValue('new-model')
    await wrapper.find('[data-test="cr-save"]').trigger('click')
    await flushPromises()
    expect(api.createCostRule).toHaveBeenCalledTimes(2)
    expect(api.createCostRule.mock.calls[1].slice(0, 2)).toEqual([7, 13])
    expect(api.createCostRule.mock.calls[1][2].name).toBe('重新确认的新规则')
    expect(api.createCostRule.mock.calls[1][2].prices[0].models).toEqual(['new-model'])
    wrapper.unmount()
  })
})
