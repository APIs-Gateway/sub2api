import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'
import { ref } from 'vue'
import { fakeQuoteBatch, installApiMocks } from './fixtures'
import { mountPricingView } from './mountHelpers'

const { api, toast } = vi.hoisted(() => ({
  api: {
    groups: { getAll: vi.fn() },
    pricing: {
      listModelCatalog: vi.fn(),
      getGroupDerive: vi.fn(),
      quoteBatch: vi.fn(),
      previewCells: vi.fn(),
      commitCells: vi.fn(),
      previewCatalogTransition: vi.fn(),
      transitionCatalog: vi.fn(),
      createCatalogEntry: vi.fn()
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
import ModelsView from '../ModelsView.vue'

const request = {
  ops: [
    { group_id: 3, model_key: 'claude-opus-5-5', kind: 'upsert', open: true, price_mode: 'extra', extra_multiplier: 1.5, baseline_revision: 4 }
  ],
  group_revisions: { '3': 7 }
}

function ticket(over: Record<string, unknown> = {}) {
  return {
    approval_id: 301,
    plan_hash: 'h',
    touches_price: true,
    price_delta: 'up',
    expires_at: '2026-10-06T08:30:00Z',
    planned: [
      {
        op: request.ops[0],
        action: 'update',
        before: { model_key: 'claude-opus-5-5', is_pattern: false, pattern_order: 0, open: true, price_mode: 'inherit', source: 'manual' },
        after: { model_key: 'claude-opus-5-5', is_pattern: false, pattern_order: 0, open: true, price_mode: 'extra', extra_multiplier: 1.5, source: 'manual' },
        touches_price: true
      }
    ],
    ...over
  }
}

async function pickAndOpenExtra() {
  const wrapper = await mountPricingView(MatrixView)
  await wrapper.find('[data-test="tab-anthropic"]').trigger('click')
  await wrapper.find('[data-test="edit"]').trigger('click')
  await wrapper.find('[data-test="pick-3|claude-opus-5-5"]').trigger('click')
  await wrapper.find('[data-test="op-extra"]').trigger('click')
  await wrapper.find('[data-test="extra-input"]').setValue('1.5')
  await wrapper.find('[data-test="extra-confirm"]').trigger('click')
  await flushPromises()
  return wrapper
}

describe('矩阵批量调整：预览 → 确认 → 提交', () => {
  beforeEach(() => {
    resetPricingDataForTest()
    installApiMocks(api)
    api.pricing.previewCells.mockReset().mockResolvedValue(ticket())
    api.pricing.commitCells.mockReset().mockResolvedValue({ planned: [], changed_group_ids: [3], touches_price: true })
    // 分组 3 的当前实付价：输入 30 / 输出 150（官方价 15 / 75 × 倍率 2）
    api.pricing.quoteBatch.mockImplementation(async (g: number[], m: string[]) => {
      const r = fakeQuoteBatch(g, m)
      r.cells = r.cells.map((c) => (c.group_id === 3 ? { ...c, per_request_price: undefined, final_per_mtok: { input: 30, output: 150 } } : c))
      return r
    })
    Object.values(toast).forEach((f) => f.mockReset())
  })

  it('没有切换的分组不能选；切换了的可以', async () => {
    const wrapper = await mountPricingView(MatrixView)
    await wrapper.find('[data-test="edit"]').trigger('click')
    // OpenAI 的分组都没切到新配置
    expect(wrapper.find('[data-test="edit"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.findAll('[data-test^="pick-"]')).toHaveLength(0)
    await wrapper.find('[data-test="tab-anthropic"]').trigger('click')
    expect(wrapper.findAll('[data-test^="pick-"]')).toHaveLength(1)
  })

  it('选格子设额外倍率：先预览，涨价会醒目提示，确认后才提交，且提交的请求与预览一致', async () => {
    const wrapper = await pickAndOpenExtra()
    expect(api.pricing.previewCells).toHaveBeenCalledWith(request)
    expect(api.pricing.commitCells).not.toHaveBeenCalled()

    expect(wrapper.find('[data-test="plan-delta-warn"]').text()).toContain('涨价')
    const row = wrapper.find('[data-test="plan-row"]').text()
    expect(row).toContain('kiro cc')
    // 官方价 15 / 75 × 分组倍率 2 = 30 / 150，加 ×1.5 后 45 / 225
    for (const usd of ['$30.00', '$45.00', '$150.00', '$225.00']) expect(row).toContain(usd)

    await wrapper.find('[data-test="plan-confirm"]').trigger('click')
    await flushPromises()
    expect(api.pricing.commitCells).toHaveBeenCalledWith(301, request)
    expect(toast.showSuccess).toHaveBeenCalled()
    // 提交后重新取数
    expect(api.groups.getAll).toHaveBeenCalledTimes(2)
  })

  it('预览里有开放后的问题时，要勾选确认才能提交', async () => {
    api.pricing.previewCells.mockResolvedValue(
      ticket({ precheck: [{ group_id: 3, stage: 'v2', access_mode: 'open', applicable: true, blocking: [], warnings: [{ group_id: 3, model_key: 'claude-opus-5-5', reason: 'unpriced' }] }] })
    )
    const wrapper = await pickAndOpenExtra()
    expect(wrapper.find('[data-test="plan-warnings"]').text()).toContain('没有价格')
    expect(wrapper.find('[data-test="plan-confirm"]').attributes('disabled')).toBeDefined()
    await wrapper.find('[data-test="plan-ack"] input').setValue(true)
    expect(wrapper.find('[data-test="plan-confirm"]').attributes('disabled')).toBeUndefined()
  })

  it('提交时基线过期：展示可读提示和刷新入口，不吞错误', async () => {
    api.pricing.commitCells.mockRejectedValue({ status: 409, reason: 'PRICE_BASELINE_CHANGED', metadata: { group_id: '3' }, message: 'the cell changed since the preview' })
    const wrapper = await pickAndOpenExtra()
    await wrapper.find('[data-test="plan-confirm"]').trigger('click')
    await flushPromises()
    const err = wrapper.find('[data-test="write-error"]')
    expect(err.text()).toContain('被别人改过')
    expect(err.text()).not.toContain('the cell changed')
    expect(err.find('[data-test="write-error-action"]').text()).toBe('刷新数据')
  })

  it('需要交互式会话和预览过期都有对应提示，过期可以重新预览', async () => {
    api.pricing.commitCells.mockRejectedValueOnce({ status: 409, reason: 'PRICE_WRITE_APPROVAL_EXPIRED' })
    const wrapper = await pickAndOpenExtra()
    await wrapper.find('[data-test="plan-confirm"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="write-error"]').text()).toContain('预览已过期')
    await wrapper.find('[data-test="write-error-action"]').trigger('click')
    await flushPromises()
    expect(api.pricing.previewCells).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-test="write-error"]').exists()).toBe(false)

    api.pricing.commitCells.mockRejectedValueOnce({ status: 403, reason: 'ADMIN_TOKEN_MANAGEMENT_JWT_ONLY' })
    await wrapper.find('[data-test="plan-confirm"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="write-error"]').text()).toContain('管理员账号登录后台')
  })

  it('预览被白名单预检拦下时，列出分组和模型', async () => {
    api.pricing.previewCells.mockRejectedValue({
      status: 400,
      reason: 'OPEN_PRECHECK_BLOCKED',
      metadata: { count: '1', issues: '3:claude-opus-5-5:unpriced' }
    })
    const wrapper = await pickAndOpenExtra()
    const err = wrapper.find('[data-test="write-error"]').text()
    expect(err).toContain('1 项不能开放')
    expect(err).toContain('kiro cc · claude-opus-5-5')
    expect(wrapper.find('[data-test="plan-confirm"]').attributes('disabled')).toBeDefined()
  })
})

describe('模型：上线、下线、新建', () => {
  beforeEach(() => {
    resetPricingDataForTest()
    installApiMocks(api)
    api.pricing.previewCells.mockReset()
    api.pricing.commitCells.mockReset().mockResolvedValue({ planned: [], changed_group_ids: [], touches_price: false })
    api.pricing.previewCatalogTransition.mockReset()
    api.pricing.transitionCatalog.mockReset().mockResolvedValue({})
    api.pricing.createCatalogEntry.mockReset().mockResolvedValue({})
    Object.values(toast).forEach((f) => f.mockReset())
  })

  it('下线：先看近 7 天用量，有用量要勾选确认，提交带 confirm_usage', async () => {
    api.pricing.previewCatalogTransition.mockResolvedValue({
      entry: {},
      from: 'active',
      to: 'retired',
      usage: { requests: 1284, last_used_at: '2026-10-06T06:41:12Z', window_days: 7 },
      confirm_required: true
    })
    const wrapper = await mountPricingView(ModelsView)
    await wrapper.find('[data-test="model-row-gpt-5.5"]').trigger('click')
    await wrapper.find('[data-test="drawer-action"]').trigger('click')
    await flushPromises()
    expect(api.pricing.previewCatalogTransition).toHaveBeenCalledWith(1, 'retired')
    expect(wrapper.find('[data-test="retire-usage"]').text()).toContain('1284')
    expect(wrapper.find('[data-test="transition-confirm"]').attributes('disabled')).toBeDefined()
    await wrapper.find('[data-test="retire-ack"] input').setValue(true)
    await wrapper.find('[data-test="transition-confirm"]').trigger('click')
    await flushPromises()
    expect(api.pricing.transitionCatalog).toHaveBeenCalledWith(1, 'retired', true)
  })

  it('上线草稿：默认一个分组都不勾；勾选后预览，先提交分组再改状态', async () => {
    api.pricing.previewCatalogTransition.mockResolvedValue({ entry: {}, from: 'draft', to: 'retired', usage: { requests: 0, last_used_at: null, window_days: 7 }, confirm_required: false })
    api.pricing.previewCells.mockImplementation(async (req: { ops: unknown[] }) => ({
      approval_id: 9,
      plan_hash: 'h',
      touches_price: true,
      price_delta: 'none',
      expires_at: '2026-10-06T08:30:00Z',
      planned: (req.ops as { group_id: number; model_key: string }[]).map((op) => ({ op, action: 'create', after: { model_key: op.model_key, is_pattern: false, pattern_order: 0, open: true, price_mode: 'inherit', source: 'manual' }, touches_price: true }))
    }))
    // 分组 1、2 在 fixtures 里没切换：只有 v2 的分组可勾选，这里把草稿模型换成 anthropic 平台的
    api.pricing.listModelCatalog.mockResolvedValue([
      { id: 8, model_key: 'claude-new', platform: 'anthropic', display_name: 'claude-new', aliases: [], reference_model: 'claude-opus-5-5', status: 'draft', note: '', created_at: '', updated_at: '' },
      { id: 5, model_key: 'claude-opus-5-5', platform: 'anthropic', display_name: 'claude-opus-5-5', aliases: [], reference_model: null, status: 'active', note: '', created_at: '', updated_at: '' }
    ])
    const wrapper = await mountPricingView(ModelsView)
    await wrapper.find('[data-test="model-row-claude-new"]').trigger('click')
    await wrapper.find('[data-test="drawer-action"]').trigger('click')
    await flushPromises()
    const check = wrapper.find('[data-test="launch-check-3"]')
    expect((check.element as HTMLInputElement).checked).toBe(false)
    expect(wrapper.find('[data-test="launch-preview-btn"]').attributes('disabled')).toBeDefined()

    // 照抄勾选：只勾上参考模型开放的分组
    await wrapper.find('[data-test="copy-ref"]').trigger('click')
    expect(api.pricing.previewCells).not.toHaveBeenCalled()
    await wrapper.find('[data-test="launch-preview-btn"]').trigger('click')
    await flushPromises()
    expect(api.pricing.previewCells).toHaveBeenCalledTimes(1)
    expect(api.pricing.previewCells.mock.calls[0][0]).toEqual({
      ops: [{ group_id: 3, model_key: 'claude-new', kind: 'upsert', open: true, price_mode: 'inherit', baseline_revision: 0 }],
      group_revisions: { '3': 7 }
    })
    expect(api.pricing.transitionCatalog).not.toHaveBeenCalled()

    await wrapper.find('[data-test="launch-confirm"]').trigger('click')
    await flushPromises()
    expect(api.pricing.commitCells).toHaveBeenCalledWith(9, api.pricing.previewCells.mock.calls[0][0])
    expect(api.pricing.transitionCatalog).toHaveBeenCalledWith(8, 'active', false)
  })

  it('新建模型：近 7 天有用量时要先确认，再带 confirm_usage 重新提交', async () => {
    api.pricing.createCatalogEntry
      .mockRejectedValueOnce({ status: 409, reason: 'MODEL_CATALOG_USAGE_CONFIRM_REQUIRED', metadata: { requests: '12', window_days: '7' } })
      .mockResolvedValueOnce({})
    const wrapper = await mountPricingView(ModelsView)
    await wrapper.find('[data-test="create-model"]').trigger('click')
    await wrapper.find('[data-test="create-model-key"]').setValue('new-model')
    await wrapper.find('[data-test="create-model-submit"]').trigger('click')
    await flushPromises()
    expect(api.pricing.createCatalogEntry.mock.calls[0][0]).toMatchObject({ model_key: 'new-model', status: 'draft', confirm_usage: false })
    expect(wrapper.find('[data-test="create-usage"]').text()).toContain('12')
    expect(wrapper.find('[data-test="create-model-submit"]').attributes('disabled')).toBeDefined()
    await wrapper.find('[data-test="create-usage-ack"]').setValue(true)
    await wrapper.find('[data-test="create-model-submit"]').trigger('click')
    await flushPromises()
    expect(api.pricing.createCatalogEntry.mock.calls[1][0]).toMatchObject({ confirm_usage: true })
  })
})
