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
      prices: [{ platform: '', models: ['gpt-5.5'], price: { billing_mode: 'token', input_price: 1.25e-6, output_price: 1e-5, cache_write_price: null, cache_read_price: null } }]
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
})
