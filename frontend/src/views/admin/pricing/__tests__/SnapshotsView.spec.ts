import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'
import { ref } from 'vue'
import { mountPricingView } from './mountHelpers'

const { api, admin } = vi.hoisted(() => ({
  api: {
    getSnapshotOverview: vi.fn(),
    pinSnapshot: vi.fn(),
    fetchSnapshotCandidate: vi.fn(),
    previewSnapshot: vi.fn(),
    approveSnapshot: vi.fn(),
    rejectSnapshot: vi.fn()
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

import { useAppStore } from '@/stores/app'
import SnapshotsView from '../SnapshotsView.vue'

const meta = (over: Record<string, unknown> = {}) => ({
  id: 1, label: 'v1', source: 'bootstrap', contentSha256: 'abcdef0123456789', modelCount: 900, status: 'active',
  fetchedAt: '2026-10-01T00:00:00Z', approvedBy: 1, approvedAt: '2026-10-01T00:00:00Z', note: '', ...over
})

const overview = (over: Record<string, unknown> = {}) => ({
  mode: 'pinned',
  active: meta(),
  pending: [meta({ id: 5, label: 'v2', status: 'candidate', approvedAt: null, approvedBy: null, contentSha256: '1234567890abcdef' })],
  history: [meta()],
  ...over
})

const plan = (over: Record<string, unknown> = {}) => ({
  base: meta(),
  candidate: meta({ id: 5 }),
  stats: { added: 1, removed: 1, changed: 1, unchanged: 897, approved: 3, held: 0 },
  entries: [
    { model_key: 'new-model', change_type: 'added', changed_fields: [], new: { input_cost_per_token: 1e-6, output_cost_per_token: 2e-6 }, decision: 'approve' },
    { model_key: 'gpt-up', change_type: 'changed', changed_fields: ['input_cost_per_token', 'cache_read_input_token_cost'], old: { input_cost_per_token: 1e-6, output_cost_per_token: 4e-6 }, new: { input_cost_per_token: 2e-6, output_cost_per_token: 4e-6 }, decision: 'approve' },
    { model_key: 'old-model', change_type: 'removed', changed_fields: [], old: { input_cost_per_token: 1e-6 }, decision: 'approve' }
  ],
  heldModels: [],
  planHash: 'hash-1',
  effectiveChanges: [{ model: 'gpt-up', old_missing: false, new_missing: false, old_input_per_mtok: 1, new_input_per_mtok: 2, old_output_per_mtok: 4, new_output_per_mtok: 4 }],
  effectiveChangesError: '',
  exposureError: '',
  ...over
})

async function mountView() {
  const wrapper = await mountPricingView(SnapshotsView)
  const app = useAppStore()
  return { wrapper, app, showError: vi.spyOn(app, 'showError'), showSuccess: vi.spyOn(app, 'showSuccess') }
}

describe('SnapshotsView', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    admin.groups.getAll.mockResolvedValue([{ id: 7, name: 'codex' }])
    api.getSnapshotOverview.mockResolvedValue(overview())
    api.previewSnapshot.mockResolvedValue(plan())
    api.approveSnapshot.mockResolvedValue(undefined)
  })

  it('展示生效版本、待批准版本的差异统计、差异表和实际变价的模型', async () => {
    const { wrapper } = await mountView()
    expect(wrapper.find('[data-test="snapshot-active"]').text()).toContain('v1')
    expect(wrapper.find('[data-test="has-new"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="snapshot-stats"]').text()).toContain('897')
    expect(api.previewSnapshot).toHaveBeenCalledWith(5, [])
    const rows = wrapper.findAll('[data-test^="snap-row-"]').map((r) => r.attributes('data-test'))
    expect(rows).toEqual(['snap-row-new-model', 'snap-row-gpt-up', 'snap-row-old-model'])
    expect(wrapper.find('[data-test="snap-row-gpt-up"]').text()).toContain('涨价')
    expect(wrapper.find('[data-test="snap-row-gpt-up"]').text()).toContain('另有 1 项价格字段变化')
    expect(wrapper.find('[data-test="effective-gpt-up"]').text()).toContain('涨价')
    expect(wrapper.find('[data-test="submit-approval"]').attributes('disabled')).toBeUndefined()
  })

  it('只看涨价筛选', async () => {
    const { wrapper } = await mountView()
    await wrapper.find('[data-test="filter-up"]').trigger('click')
    expect(wrapper.findAll('[data-test^="snap-row-"]')).toHaveLength(1)
  })

  it('搁置一个模型后，带着搁置名单重新预览', async () => {
    const { wrapper } = await mountView()
    vi.useFakeTimers()
    try {
      api.previewSnapshot.mockResolvedValue(plan({ heldModels: ['gpt-up'], planHash: 'hash-2' }))
      await wrapper.find('[data-test="hold-gpt-up"]').trigger('click')
      // 重新计算期间不允许提交
      expect(wrapper.find('[data-test="submit-approval"]').attributes('disabled')).toBeDefined()
      await vi.advanceTimersByTimeAsync(500)
    } finally {
      vi.useRealTimers()
    }
    await flushPromises()
    expect(api.previewSnapshot).toHaveBeenLastCalledWith(5, ['gpt-up'])
    expect(wrapper.find('[data-test="action-bar"]').text()).toContain('批准 2 项，搁置 1 项')
    expect(wrapper.find('[data-test="submit-approval"]').attributes('disabled')).toBeUndefined()
  })

  it('预览在途时又点了搁置：旧响应不覆盖本地搁置名单，且在新预览回来前禁止提交', async () => {
    const { wrapper } = await mountView()
    vi.useFakeTimers()
    try {
      let releaseFirst: (p: unknown) => void = () => {}
      api.previewSnapshot.mockImplementationOnce(() => new Promise((resolve) => { releaseFirst = resolve }))
      await wrapper.find('[data-test="hold-gpt-up"]').trigger('click')
      await vi.advanceTimersByTimeAsync(400)
      expect(api.previewSnapshot).toHaveBeenLastCalledWith(5, ['gpt-up'])
      // 第一个预览还没回来，再搁置第二个
      await wrapper.find('[data-test="hold-new-model"]').trigger('click')
      // 第一个预览带着只含 gpt-up 的名单回来
      releaseFirst(plan({ heldModels: ['gpt-up'], planHash: 'hash-old' }))
      await vi.advanceTimersByTimeAsync(0)
      expect(wrapper.find('[data-test="hold-new-model"]').attributes('aria-pressed')).toBe('true')
      expect(wrapper.find('[data-test="submit-approval"]').attributes('disabled')).toBeDefined()
      expect(wrapper.find('[data-test="action-bar"]').text()).toContain('重新计算')
      api.previewSnapshot.mockResolvedValueOnce(plan({ heldModels: ['gpt-up', 'new-model'], planHash: 'hash-new' }))
      await vi.advanceTimersByTimeAsync(400)
    } finally {
      vi.useRealTimers()
    }
    await flushPromises()
    expect(api.previewSnapshot).toHaveBeenLastCalledWith(5, ['gpt-up', 'new-model'])
    expect(wrapper.find('[data-test="hold-new-model"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.find('[data-test="submit-approval"]').attributes('disabled')).toBeUndefined()
    await wrapper.find('[data-test="submit-approval"]').trigger('click')
    await wrapper.find('[data-test="approval-confirm"]').trigger('click')
    await flushPromises()
    expect(api.approveSnapshot).toHaveBeenCalledWith(5, ['gpt-up', 'new-model'], 'hash-new')
  })

  it('乱序响应：先发的预览后回来时被丢弃，只采用最新一次的 plan_hash', async () => {
    const { wrapper } = await mountView()
    vi.useFakeTimers()
    try {
      const resolvers: ((p: unknown) => void)[] = []
      api.previewSnapshot.mockImplementation(() => new Promise((resolve) => resolvers.push(resolve)))
      await wrapper.find('[data-test="hold-gpt-up"]').trigger('click')
      await vi.advanceTimersByTimeAsync(400)
      await wrapper.find('[data-test="hold-new-model"]').trigger('click')
      await vi.advanceTimersByTimeAsync(400)
      expect(resolvers).toHaveLength(2)
      // 后发的先回来，先发的后回来
      resolvers[1](plan({ planHash: 'hash-latest' }))
      await vi.advanceTimersByTimeAsync(0)
      resolvers[0](plan({ planHash: 'hash-stale' }))
      await vi.advanceTimersByTimeAsync(0)
    } finally {
      vi.useRealTimers()
    }
    await flushPromises()
    expect(wrapper.find('[data-test="submit-approval"]').attributes('disabled')).toBeUndefined()
    await wrapper.find('[data-test="submit-approval"]').trigger('click')
    await wrapper.find('[data-test="approval-confirm"]').trigger('click')
    await flushPromises()
    expect(api.approveSnapshot).toHaveBeenCalledWith(5, ['gpt-up', 'new-model'], 'hash-latest')
  })

  it('只涨了缓存价的模型显示涨价，参与「只看涨价」和「搁置所有涨价项」', async () => {
    const cacheUp = { model_key: 'cache-up', change_type: 'changed', changed_fields: ['cache_read_input_token_cost'], old: { input_cost_per_token: 1e-6, output_cost_per_token: 4e-6, cache_read_input_token_cost: 1e-7 }, new: { input_cost_per_token: 1e-6, output_cost_per_token: 4e-6, cache_read_input_token_cost: 2e-7 }, decision: 'approve' }
    const p = plan()
    p.entries.push(cacheUp as never)
    api.previewSnapshot.mockResolvedValue(p)
    const { wrapper } = await mountView()
    expect(wrapper.find('[data-test="snap-row-cache-up"]').text()).toContain('涨价')
    await wrapper.find('[data-test="filter-up"]').trigger('click')
    expect(wrapper.findAll('[data-test^="snap-row-"]').map((r) => r.attributes('data-test'))).toEqual(['snap-row-gpt-up', 'snap-row-cache-up'])
    expect(wrapper.find('[data-test="hold-up"]').text()).toContain('2')
    vi.useFakeTimers()
    try {
      await wrapper.find('[data-test="hold-up"]').trigger('click')
    } finally {
      vi.useRealTimers()
    }
    expect(wrapper.find('[data-test="hold-cache-up"]').attributes('aria-pressed')).toBe('true')
  })

  it('「搁置所有涨价项」把有涨有跌的项也搁置，按钮计数归零', async () => {
    const mix = { model_key: 'mix-model', change_type: 'changed', changed_fields: ['input_cost_per_token', 'output_cost_per_token'], old: { input_cost_per_token: 2e-6, output_cost_per_token: 1e-6 }, new: { input_cost_per_token: 1e-6, output_cost_per_token: 2e-6 }, decision: 'approve' }
    const p = plan()
    p.entries.push(mix as never)
    api.previewSnapshot.mockResolvedValue(p)
    const { wrapper } = await mountView()
    expect(wrapper.find('[data-test="hold-up"]').text()).toContain('2')
    vi.useFakeTimers()
    try {
      await wrapper.find('[data-test="hold-up"]').trigger('click')
    } finally {
      vi.useRealTimers()
    }
    expect(wrapper.find('[data-test="hold-mix-model"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.find('[data-test="hold-gpt-up"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.find('[data-test="hold-up"]').text()).toContain('0')
  })

  it('先看确认框里的摘要，确认后带着 plan_hash 批准', async () => {
    const { wrapper, showSuccess } = await mountView()
    await wrapper.find('[data-test="submit-approval"]').trigger('click')
    const dialog = wrapper.find('[data-test="approval-dialog"]')
    expect(dialog.text()).toContain('批准 3 项，搁置 0 项')
    expect(wrapper.find('[data-test="confirm-effective"]').text()).toContain('1 个近期用过的模型会变价，1 个涨价')
    await wrapper.find('[data-test="approval-confirm"]').trigger('click')
    await flushPromises()
    expect(api.approveSnapshot).toHaveBeenCalledWith(5, [], 'hash-1')
    expect(showSuccess).toHaveBeenCalled()
  })

  it('需要登录会话的错误显示在确认框里，不吞掉，也不关框', async () => {
    api.approveSnapshot.mockRejectedValue({ status: 403, reason: 'ADMIN_TOKEN_MANAGEMENT_JWT_ONLY', message: 'x' })
    const { wrapper } = await mountView()
    await wrapper.find('[data-test="submit-approval"]').trigger('click')
    await wrapper.find('[data-test="approval-confirm"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-test="approval-error"]').text()).toContain('管理员账号登录后台')
    expect(wrapper.find('[data-test="approval-dialog"]').exists()).toBe(true)
  })

  it('预览已变（plan_hash 对不上）时关框、提示，并按最新数据重新预览', async () => {
    api.approveSnapshot.mockRejectedValue({ status: 409, reason: 'PRICING_SNAPSHOT_PLAN_CHANGED', message: 'x' })
    const { wrapper, showError } = await mountView()
    const before = api.previewSnapshot.mock.calls.length
    await wrapper.find('[data-test="submit-approval"]').trigger('click')
    await wrapper.find('[data-test="approval-confirm"]').trigger('click')
    await flushPromises()
    expect(showError).toHaveBeenCalledWith(expect.stringContaining('重新计算'))
    expect(api.previewSnapshot.mock.calls.length).toBeGreaterThan(before)
    expect(wrapper.find('[data-test="approval-dialog"]').exists()).toBe(false)
  })

  it('批准后会让白名单分组没有价格时，列出违规项并禁止提交', async () => {
    api.previewSnapshot.mockResolvedValue(plan({ exposureError: 'exposure check failed (1 of them: 7:gpt-up:unpriced)' }))
    const { wrapper } = await mountView()
    const box = wrapper.find('[data-test="exposure-error"]')
    expect(box.text()).toContain('codex：gpt-up（没有价格）')
    expect(wrapper.find('[data-test="submit-approval"]').attributes('disabled')).toBeDefined()
  })

  it('没有待批准版本时给出空状态', async () => {
    api.getSnapshotOverview.mockResolvedValue(overview({ pending: [] }))
    const { wrapper } = await mountView()
    expect(wrapper.find('[data-test="snapshot-empty"]').exists()).toBe(true)
    expect(api.previewSnapshot).not.toHaveBeenCalled()
  })

  it('还没固定价格时只给「固定当前价格」，不给拉取', async () => {
    api.getSnapshotOverview.mockResolvedValue(overview({ mode: 'auto', active: null, pending: [], history: [] }))
    api.pinSnapshot.mockResolvedValue(undefined)
    const { wrapper } = await mountView()
    expect(wrapper.find('[data-test="pull"]').exists()).toBe(false)
    await wrapper.find('[data-test="pin"]').trigger('click')
    const buttons = wrapper.findAll('button').filter((b) => b.text() === '固定当前价格')
    await buttons[buttons.length - 1].trigger('click')
    await flushPromises()
    expect(api.pinSnapshot).toHaveBeenCalledTimes(1)
  })

  it('拒绝候选版本', async () => {
    api.rejectSnapshot.mockResolvedValue(undefined)
    const { wrapper } = await mountView()
    await wrapper.find('[data-test="reject"]').trigger('click')
    const buttons = wrapper.findAll('button').filter((b) => b.text() === '拒绝')
    await buttons[buttons.length - 1].trigger('click')
    await flushPromises()
    expect(api.rejectSnapshot).toHaveBeenCalledWith(5)
  })

  it('拉取到没有变化时只提示，不报错', async () => {
    api.fetchSnapshotCandidate.mockResolvedValue({ unchanged: true, created: false, candidate: null })
    const { wrapper, showError } = await mountView()
    const info = vi.spyOn(useAppStore(), 'showInfo')
    await wrapper.find('[data-test="pull"]').trigger('click')
    await flushPromises()
    expect(info).toHaveBeenCalledWith(expect.stringContaining('没有变化'))
    expect(showError).not.toHaveBeenCalled()
  })
})
