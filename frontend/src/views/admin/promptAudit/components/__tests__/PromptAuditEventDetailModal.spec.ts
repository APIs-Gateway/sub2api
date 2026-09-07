import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import PromptAuditEventDetailModal from '../PromptAuditEventDetailModal.vue'

// Prompt Audit (issue #884) —— 事件详情弹窗测试:按需加载(打开时才请求)/加载态/未找到态/
// 错误提示 + 脱敏字段渲染(snapshot / issue_summaries / scanner_scores)+ 绝不出现 full_prompt +
// 关闭事件。

const { getPromptAuditEvent } = vi.hoisted(() => ({ getPromptAuditEvent: vi.fn() }))
vi.mock('@/api/admin/promptAudit', () => ({ getPromptAuditEvent }))

const showError = vi.fn()
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess: vi.fn() }) }))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const BaseDialogStub = {
  props: ['show', 'title', 'width'],
  emits: ['close'],
  template: '<div v-if="show" data-test="dialog"><slot /><slot name="footer" /></div>',
}

function baseEvent(overrides: Record<string, unknown> = {}) {
  return {
    id: 1,
    job_id: 1,
    decision: 'critical',
    risk_level: 'high',
    action: 'Block',
    categories: ['jailbreak'],
    matched_scanners: ['qwen3guard'],
    scanner_scores: { qwen3guard: 0.92 },
    scanner_evidence: { qwen3guard: 'matched pattern' },
    scanner_backend: 'qwen3guard',
    scanner_version: '1.0',
    guard_endpoint_id: 'ep-1',
    policy_id: 'default',
    policy_version: 3,
    config_version: 1,
    chunk_total: 1,
    latency_ms: 120,
    issue_summaries: [
      {
        category: 'jailbreak',
        scanner_id: 'qwen3guard',
        title: 'Jailbreak attempt',
        description: 'Detected jailbreak pattern',
        severity: 'high',
        severity_label: 'High',
        action: 'block',
        action_label: 'Block',
        code: 'JAILBREAK_1',
        score: 0.92,
        evidence: 'redacted evidence snippet',
        evidence_hash: 'hash-evidence',
      },
    ],
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
    ...overrides,
  }
}

function mountModal(props: { show: boolean; eventId: number | null }) {
  return mount(PromptAuditEventDetailModal, {
    props,
    global: { stubs: { BaseDialog: BaseDialogStub } },
  })
}

describe('admin PromptAuditEventDetailModal', () => {
  beforeEach(() => {
    getPromptAuditEvent.mockReset()
    showError.mockReset()
  })

  it('does not fetch while closed', async () => {
    mountModal({ show: false, eventId: null })
    await flushPromises()
    expect(getPromptAuditEvent).not.toHaveBeenCalled()
  })

  it('shows a loading state while the fetch is in flight', async () => {
    let resolveFetch: (value: unknown) => void = () => {}
    getPromptAuditEvent.mockImplementation(() => new Promise((resolve) => { resolveFetch = resolve }))

    const wrapper = mountModal({ show: false, eventId: null })
    await wrapper.setProps({ show: true, eventId: 1 })
    await flushPromises()

    expect(wrapper.text()).toContain('admin.promptAudit.detail.loading')

    resolveFetch(baseEvent())
    await flushPromises()

    expect(wrapper.text()).toContain('req-1')
  })

  it('fetches on open and renders only redacted event fields (never full_prompt)', async () => {
    getPromptAuditEvent.mockResolvedValue(baseEvent())
    const wrapper = mountModal({ show: false, eventId: null })
    await wrapper.setProps({ show: true, eventId: 1 })
    await flushPromises()

    expect(getPromptAuditEvent).toHaveBeenCalledWith(1)
    const text = wrapper.text()
    expect(text).toContain('req-1')
    expect(text).toContain('alice@example.com')
    expect(text).toContain('key-a')
    expect(text).toContain('group-a')
    expect(text).toContain('[redacted] ignore previous instructions')
    expect(text).toContain('hash-1')
    expect(text).toContain('jailbreak')
    expect(text).toContain('qwen3guard')
    expect(text).toContain('0.92')
    expect(text).toContain('Jailbreak attempt')
    expect(text).toContain('redacted evidence snippet')
    expect(text).not.toContain('full_prompt')
  })

  it('shows the not-found state without fetching when there is no event id', async () => {
    const wrapper = mountModal({ show: false, eventId: null })
    await wrapper.setProps({ show: true, eventId: null })
    await flushPromises()

    expect(getPromptAuditEvent).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('admin.promptAudit.detail.notFound')
  })

  it('shows an error toast and falls back to not-found state when the fetch fails', async () => {
    getPromptAuditEvent.mockRejectedValueOnce(new Error('boom'))
    const wrapper = mountModal({ show: false, eventId: null })
    await wrapper.setProps({ show: true, eventId: 99 })
    await flushPromises()

    expect(showError).toHaveBeenCalled()
    expect(wrapper.text()).toContain('admin.promptAudit.detail.notFound')
  })

  it('renders the issues-empty copy when there are no issue summaries', async () => {
    getPromptAuditEvent.mockResolvedValue(baseEvent({ issue_summaries: [] }))
    const wrapper = mountModal({ show: false, eventId: null })
    await wrapper.setProps({ show: true, eventId: 1 })
    await flushPromises()

    expect(wrapper.text()).toContain('admin.promptAudit.detail.issuesEmpty')
  })

  it('re-fetches when reopened with a different event id', async () => {
    getPromptAuditEvent.mockResolvedValueOnce(baseEvent({ id: 1 }))
    const wrapper = mountModal({ show: false, eventId: null })
    await wrapper.setProps({ show: true, eventId: 1 })
    await flushPromises()
    expect(getPromptAuditEvent).toHaveBeenNthCalledWith(1, 1)

    await wrapper.setProps({ show: false, eventId: 1 })
    getPromptAuditEvent.mockResolvedValueOnce(baseEvent({ id: 2, snapshot: { ...baseEvent().snapshot, request_id: 'req-2' } }))
    await wrapper.setProps({ show: true, eventId: 2 })
    await flushPromises()

    expect(getPromptAuditEvent).toHaveBeenNthCalledWith(2, 2)
    expect(wrapper.text()).toContain('req-2')
  })

  it('emits close when the footer close button is clicked', async () => {
    getPromptAuditEvent.mockResolvedValue(baseEvent())
    const wrapper = mountModal({ show: false, eventId: null })
    await wrapper.setProps({ show: true, eventId: 1 })
    await flushPromises()

    await wrapper.find('button').trigger('click')
    expect(wrapper.emitted('close')).toBeTruthy()
  })
})
