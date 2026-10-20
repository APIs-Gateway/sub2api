import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AdminPromptAuditEventsView from '../AdminPromptAuditEventsView.vue'
import { pointsViewMountOptions } from '@/views/__tests__/pointsTestStubs'

// Prompt Audit (issue #884) —— 后台事件列表视图测试:加载 / 过滤 / 分页 / 空状态 / 错误状态 +
// 单元格渲染 + 打开详情弹窗时正确传递 event id。契约对齐 backend prompt_event_handler.go /
// prompt_event_repository.go 的 EventFilter + EventPage,列表渲染只使用其中已脱敏的字段。

const { listPromptAuditEvents } = vi.hoisted(() => ({ listPromptAuditEvents: vi.fn() }))
vi.mock('@/api/admin/promptAudit', () => ({ listPromptAuditEvents }))

const showError = vi.fn()
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess: vi.fn() }) }))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const DetailModalStub = {
  props: ['show', 'eventId'],
  template: '<div data-test="detail-modal" :data-show="show" :data-event-id="eventId" />',
}

const eventRows = [
  {
    id: 1,
    job_id: 1,
    decision: 'critical',
    risk_level: 'high',
    action: 'Block',
    categories: ['jailbreak'],
    matched_scanners: ['qwen3guard'],
    scanner_scores: {},
    scanner_evidence: {},
    scanner_backend: 'qwen3guard',
    scanner_version: '1.0',
    guard_endpoint_id: 'ep-1',
    policy_id: 'default',
    policy_version: 1,
    config_version: 1,
    chunk_total: 1,
    latency_ms: 120,
    issue_summaries: [],
    created_at: '2026-06-01T00:00:00Z',
    snapshot: {
      request_id: 'req-1',
      user_id: 7,
      username: 'alice',
      user_email: 'alice@example.com',
      api_key_id: 3,
      api_key_name: 'key-a',
      group_id: 1,
      group_name: 'group-a',
      provider: 'openai',
      endpoint: '/v1/chat/completions',
      protocol: 'openai',
      model: 'gpt-5',
      prompt_hash: 'hash-1',
      redacted_preview: '[redacted] ignore previous instructions',
      prompt_length: 42,
      message_count: 2,
      stage: 'input',
    },
  },
  {
    id: 2,
    job_id: 2,
    decision: 'pass',
    risk_level: 'low',
    action: 'Allow',
    categories: [],
    matched_scanners: [],
    scanner_scores: {},
    scanner_evidence: {},
    scanner_backend: '',
    scanner_version: '',
    guard_endpoint_id: '',
    policy_id: '',
    policy_version: 0,
    config_version: 1,
    chunk_total: 1,
    latency_ms: 30,
    issue_summaries: [],
    created_at: '2026-06-02T00:00:00Z',
    snapshot: {
      request_id: 'req-2',
      user_id: 8,
      username: '',
      user_email: '',
      api_key_id: 4,
      api_key_name: '',
      group_id: null,
      group_name: '',
      provider: 'anthropic',
      endpoint: '/v1/messages',
      protocol: 'anthropic',
      model: 'claude',
      prompt_hash: '',
      redacted_preview: '',
      prompt_length: 0,
      message_count: 0,
      stage: 'input',
    },
  },
]

function mountView() {
  return mount(
    AdminPromptAuditEventsView,
    pointsViewMountOptions({ PromptAuditEventDetailModal: DetailModalStub }),
  )
}

describe('admin AdminPromptAuditEventsView', () => {
  beforeEach(() => {
    listPromptAuditEvents.mockReset()
    showError.mockReset()
    listPromptAuditEvents.mockResolvedValue({ items: eventRows, total: 2, page: 1, page_size: 20, pages: 1 })
  })

  it('loads events on mount and renders redacted cells only (no full_prompt)', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(listPromptAuditEvents).toHaveBeenCalledWith({ decision: '', risk_level: '', keyword: '', page: 1, page_size: 20 })
    const text = wrapper.text()
    expect(text).toContain('#7')
    expect(text).toContain('alice@example.com')
    expect(text).toContain('key-a')
    expect(text).toContain('[redacted] ignore previous instructions')
    expect(text).toContain('hash-1')
    expect(text).toContain('critical')
    expect(text).toContain('high')
    expect(text).not.toContain('full_prompt')
  })

  it('reloads with decision filter (resets to page 1)', async () => {
    const wrapper = mountView()
    await flushPromises()
    const selects = wrapper.findAll('select')
    await selects[0].setValue('critical')
    await flushPromises()
    expect(listPromptAuditEvents).toHaveBeenLastCalledWith({ decision: 'critical', risk_level: '', keyword: '', page: 1, page_size: 20 })
  })

  it('reloads with risk level filter (resets to page 1)', async () => {
    const wrapper = mountView()
    await flushPromises()
    const selects = wrapper.findAll('select')
    await selects[1].setValue('high')
    await flushPromises()
    expect(listPromptAuditEvents).toHaveBeenLastCalledWith({ decision: '', risk_level: 'high', keyword: '', page: 1, page_size: 20 })
  })

  it('search on enter triggers reload', async () => {
    const wrapper = mountView()
    await flushPromises()
    const input = wrapper.find('input[type="text"]')
    await input.setValue('req-1')
    await input.trigger('keyup.enter')
    await flushPromises()
    expect(listPromptAuditEvents).toHaveBeenLastCalledWith({ decision: '', risk_level: '', keyword: 'req-1', page: 1, page_size: 20 })
  })

  it('refresh button re-loads', async () => {
    const wrapper = mountView()
    await flushPromises()
    listPromptAuditEvents.mockClear()
    await wrapper.find('button').trigger('click')
    await flushPromises()
    expect(listPromptAuditEvents).toHaveBeenCalledTimes(1)
  })

  it('pagination change page and page size', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.find('[data-test="next-page"]').trigger('click')
    await flushPromises()
    expect(listPromptAuditEvents).toHaveBeenLastCalledWith({ decision: '', risk_level: '', keyword: '', page: 2, page_size: 20 })
    await wrapper.find('[data-test="change-size"]').trigger('click')
    await flushPromises()
    expect(listPromptAuditEvents).toHaveBeenLastCalledWith({ decision: '', risk_level: '', keyword: '', page: 1, page_size: 50 })
  })

  it('shows error toast when load fails', async () => {
    listPromptAuditEvents.mockRejectedValueOnce(new Error('boom'))
    mountView()
    await flushPromises()
    expect(showError).toHaveBeenCalled()
  })

  it('renders empty slot when no rows', async () => {
    listPromptAuditEvents.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-test="empty"]').exists()).toBe(true)
  })

  it('opens the detail modal with the clicked row id', async () => {
    const wrapper = mountView()
    await flushPromises()

    const modalBefore = wrapper.find('[data-test="detail-modal"]')
    expect(modalBefore.attributes('data-show')).toBe('false')

    const rows = wrapper.findAll('[data-test="row"]')
    await rows[0].find('button').trigger('click')
    await flushPromises()

    const modal = wrapper.find('[data-test="detail-modal"]')
    expect(modal.attributes('data-show')).toBe('true')
    expect(modal.attributes('data-event-id')).toBe('1')
  })
})
