import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

import UsageView from '../UsageView.vue'

const { query, getStatsByDateRange, list } = vi.hoisted(() => ({
  query: vi.fn(),
  getStatsByDateRange: vi.fn(),
  list: vi.fn()
}))

vi.mock('@/api', () => ({ usageAPI: { query, getStatsByDateRange }, keysAPI: { list } }))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(), showWarning: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn(),
    cachedPublicSettings: { balance_recharge_multiplier: 13 }
  })
}))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const { default: zhCN } = await import('@/i18n/locales/zh-CN')
  const translate = (key: string) => {
    const hit = key.split('.').reduce<unknown>((o, k) => (o as Record<string, unknown> | undefined)?.[k], zhCN)
    return typeof hit === 'string' ? hit : key
  }
  return { ...actual, useI18n: () => ({ t: translate }) }
})

const row = (request_id: string, extra: Record<string, unknown> = {}) => ({
  request_id, model: 'gpt-5.5', actual_cost: 0.1, total_cost: 0.1, rate_multiplier: 1,
  input_cost: 0, output_cost: 0, cache_creation_cost: 0, cache_read_cost: 0,
  input_tokens: 1, output_tokens: 1, cache_creation_tokens: 0, cache_read_tokens: 0,
  cache_creation_5m_tokens: 0, cache_creation_1h_tokens: 0, image_count: 0, image_size: null,
  first_token_ms: null, duration_ms: 1, created_at: '2026-10-01T00:00:00Z',
  group: { id: 16, name: 'Codex Plus' }, ...extra
})

const DataTableStub = {
  props: ['data'],
  template: '<div><div v-for="r in data" :key="r.request_id" :data-row="r.request_id"><slot name="cell-group" :row="r" /></div></div>'
}

async function mountView(rows: unknown[]) {
  query.mockResolvedValue({ items: rows, total: rows.length, pages: 1 })
  getStatsByDateRange.mockResolvedValue({ total_requests: 1, total_tokens: 1, total_cost: 0.1, avg_duration_ms: 1 })
  list.mockResolvedValue({ items: [] })
  ;(globalThis as any).ResizeObserver = class { observe() {} disconnect() {} }
  const w = mount(UsageView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="actions" /><slot name="filters" /><slot name="table" /><slot /></div>' },
        Pagination: true, EmptyState: true, Select: true, DateRangePicker: true,
        DataTable: DataTableStub, Icon: true, Teleport: true
      }
    }
  })
  await flushPromises()
  return w
}

describe('user UsageView group column', () => {
  beforeEach(() => { setActivePinia(createPinia()); query.mockReset(); getStatsByDateRange.mockReset(); list.mockReset() })

  it('没有兜底时显示 Key 所属的分组，不带兜底标签', async () => {
    const w = await mountView([row('a')])
    const cell = w.get('[data-row="a"]')
    expect(cell.text()).toContain('Codex Plus')
    expect(cell.find('[data-test="served-tag"]').exists()).toBe(false)
  })

  it('走用户自己的兜底链时显示实际服务的分组和「兜底」标签', async () => {
    const w = await mountView([row('b', { served_group_id: 21, served_group: { id: 21, name: 'Codex 稳定' } })])
    const cell = w.get('[data-row="b"]')
    expect(cell.get('[data-test="served-group"]').text()).toBe('Codex 稳定')
    expect(cell.get('[data-test="served-tag"]').text()).toBe('兜底')
    expect(cell.get('[data-test="served-tag"]').attributes('title')).toContain('按兜底分组的价格计费')
    expect(cell.text()).not.toContain('Codex Plus')
  })

  it('管理员隐藏链的行没有 served 字段，按主分组展示', async () => {
    // 后端对 served_route_source=2 的行不返回 served 字段
    const w = await mountView([row('c')])
    expect(w.get('[data-row="c"]').text()).toBe('Codex Plus')
  })
})
