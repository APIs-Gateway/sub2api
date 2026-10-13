import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'
import { ref } from 'vue'
import { installApiMocks } from './fixtures'
import { mountPricingView } from './mountHelpers'

const { api, toast } = vi.hoisted(() => ({
  api: {
    groups: { getAll: vi.fn() },
    pricing: {
      listModelCatalog: vi.fn(),
      getGroupDerive: vi.fn(),
      quoteBatch: vi.fn(),
      previewGroupConfig: vi.fn(),
      commitGroupConfig: vi.fn(),
      getPublishCheck: vi.fn()
    }
  },
  toast: { showSuccess: vi.fn(), showWarning: vi.fn(), showError: vi.fn() }
}))

vi.mock('@/api/admin', () => ({ adminAPI: api }))
vi.mock('@/stores/app', async () => {
  const actual = await vi.importActual<typeof import('@/stores/app')>('@/stores/app')
  return { ...actual, useAppStore: () => ({ ...toast, cachedPublicSettings: { balance_recharge_multiplier: 13, official_price_cny_rate: 7 } }) }
})
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const { default: zhCN } = await import('@/i18n/locales/zh-CN')
  const translate = (key: string, params: Record<string, unknown> = {}) => {
    const hit = key.split('.').reduce<unknown>((o, k) => (o as Record<string, unknown> | undefined)?.[k], zhCN)
    if (typeof hit !== 'string') return key
    return hit.replace(/\{(\w+)\}/g, (_, name) => String(params[name] ?? `{${name}}`))
  }
  return { ...actual, useI18n: () => ({ locale: ref('zh-CN'), t: translate }) }
})

import { resetPricingDataForTest } from '../usePricingData'
import MatrixView from '../MatrixView.vue'
import { buildConfigRequest, currentConfig, mappingInvalid, toDraft } from '../groupConfig'
import { derives } from './fixtures'

function ticket(over: Record<string, unknown> = {}) {
  const before = { access_mode: 'open', billing_model_source: null, model_mapping: [], features: {}, cost_mode: 'account_rate' }
  return {
    approval_id: 302,
    plan_hash: 'h',
    changed: true,
    exposure_relevant: true,
    touches_price: false,
    price_delta: 'none',
    expires_at: '2026-10-06T08:30:00Z',
    before,
    after: { ...before, access_mode: 'allowlist' },
    ...over
  }
}

async function openDrawer(groupId: number, tab = 'anthropic') {
  const wrapper = await mountPricingView(MatrixView)
  await wrapper.find(`[data-test="tab-${tab}"]`).trigger('click')
  await wrapper.find(`[data-test="gear-${groupId}"]`).trigger('click')
  await flushPromises()
  return wrapper
}

describe('分组设置：纯逻辑', () => {
  it('只把改过的项放进请求；映射不合法时识别出来', () => {
    const current = currentConfig(derives[3])
    const draft = toDraft(current)
    expect(buildConfigRequest(current, draft, 7)).toEqual({ baseline_revision: 7 })
    draft.access = 'allowlist'
    draft.cost = 'follow_billing'
    draft.mapping = [{ src: ' a ', dst: 'b' }, { src: '', dst: '' }]
    expect(buildConfigRequest(current, draft, 7)).toEqual({
      baseline_revision: 7,
      access_mode: 'allowlist',
      cost_mode: 'follow_billing',
      model_mapping: [{ src: 'a', dst: 'b' }]
    })
    expect(mappingInvalid([{ src: 'a', dst: '' }])).toBe(true)
    expect(mappingInvalid([{ src: '', dst: '' }])).toBe(false)
  })

  it('计费来源改回空要走 clear，不能带 billing_model_source', () => {
    const current = { ...currentConfig(derives[3]), billing_model_source: 'requested' as const }
    const draft = toDraft(current)
    draft.billingSource = null
    expect(buildConfigRequest(current, draft, 7)).toEqual({ baseline_revision: 7, clear_billing_model_source: true })
  })
})

describe('分组设置抽屉：预览 → 确认 → 提交', () => {
  beforeEach(() => {
    resetPricingDataForTest()
    installApiMocks(api)
    api.pricing.previewGroupConfig.mockReset().mockResolvedValue(ticket())
    api.pricing.commitGroupConfig.mockReset().mockResolvedValue({ changed: true, exposure_relevant: true })
    api.pricing.getPublishCheck.mockReset()
    Object.values(toast).forEach((f) => f.mockReset())
  })

  it('还没切换的分组只能查看', async () => {
    const wrapper = await openDrawer(1, 'openai')
    expect(wrapper.find('[data-test="group-readonly"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="group-save"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-test="access-allowlist"]').attributes('disabled')).toBeDefined()
  })

  it('改准入方式：保存先预览，确认后提交，请求带分组配置基线', async () => {
    const wrapper = await openDrawer(3)
    expect(wrapper.find('[data-test="group-save"]').attributes('disabled')).toBeDefined()
    await wrapper.find('[data-test="access-allowlist"]').trigger('click')
    await wrapper.find('[data-test="group-save"]').trigger('click')
    await flushPromises()
    const req = { baseline_revision: 7, access_mode: 'allowlist' }
    expect(api.pricing.previewGroupConfig).toHaveBeenCalledWith(3, req)
    expect(api.pricing.commitGroupConfig).not.toHaveBeenCalled()
    expect(wrapper.find('[data-test="group-plan-access"]').text()).toContain('白名单')

    await wrapper.find('[data-test="group-plan-confirm"]').trigger('click')
    await flushPromises()
    expect(api.pricing.commitGroupConfig).toHaveBeenCalledWith(3, 302, req)
    expect(toast.showSuccess).toHaveBeenCalled()
  })

  it('涉价或无法确认方向时醒目提示；开放后的问题要勾选确认', async () => {
    api.pricing.previewGroupConfig.mockResolvedValue(
      ticket({
        touches_price: true,
        price_delta: 'unknown',
        precheck: { group_id: 3, stage: 'v2', access_mode: 'allowlist', applicable: true, blocking: [], warnings: [{ group_id: 3, model_key: 'alias-b', reason: 'mapping_target_unpriced', target: 'no-such' }] }
      })
    )
    const wrapper = await openDrawer(3)
    await wrapper.find('[data-test="access-allowlist"]').trigger('click')
    await wrapper.find('[data-test="group-save"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="group-delta-warn"]').text()).toContain('没能确认')
    expect(wrapper.find('[data-test="group-plan-warnings"]').text()).toContain('no-such')
    expect(wrapper.find('[data-test="group-plan-confirm"]').attributes('disabled')).toBeDefined()
    await wrapper.find('[data-test="group-plan-ack"] input').setValue(true)
    expect(wrapper.find('[data-test="group-plan-confirm"]').attributes('disabled')).toBeUndefined()
  })

  it('白名单预检拦截时列出问题，不能提交', async () => {
    api.pricing.previewGroupConfig.mockRejectedValue({ status: 400, reason: 'OPEN_PRECHECK_BLOCKED', metadata: { count: '1', issues: '3:alias-b:mapping_target_unpriced->no-such' } })
    const wrapper = await openDrawer(3)
    await wrapper.find('[data-test="access-allowlist"]').trigger('click')
    await wrapper.find('[data-test="group-save"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="write-error"]').text()).toContain('alias-b')
    expect(wrapper.find('[data-test="group-plan-confirm"]').attributes('disabled')).toBeDefined()
  })

  it('提交时基线过期给出提示和刷新入口', async () => {
    api.pricing.commitGroupConfig.mockRejectedValue({ status: 409, reason: 'PRICE_BASELINE_CHANGED', metadata: { group_id: '3' } })
    const wrapper = await openDrawer(3)
    await wrapper.find('[data-test="access-allowlist"]').trigger('click')
    await wrapper.find('[data-test="group-save"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-test="group-plan-confirm"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="write-error"]').text()).toContain('被别人改过')
    expect(wrapper.find('[data-test="write-error-action"]').text()).toBe('刷新数据')
  })

  it('发布预检：展示开放后的问题；被拦下时列出问题', async () => {
    api.pricing.getPublishCheck.mockResolvedValueOnce({
      group_id: 3,
      stage: 'v2',
      access_mode: 'open',
      applicable: true,
      blocking: [],
      warnings: [{ group_id: 3, model_key: 'minimax-m3', reason: 'unpriced' }]
    })
    const wrapper = await openDrawer(3)
    await wrapper.find('[data-test="publish-check-btn"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="publish-warnings"]').text()).toContain('minimax-m3')

    api.pricing.getPublishCheck.mockRejectedValueOnce({ status: 400, reason: 'GROUP_PUBLISH_BLOCKED', metadata: { count: '1', issues: '3:minimax-m3:unpriced' } })
    await wrapper.find('[data-test="publish-check-btn"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="publish-check"] [data-test="write-error"]').text()).toContain('minimax-m3')
  })
})
