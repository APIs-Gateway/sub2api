/**
 * 外壳交给页签的接口：后面按页签拆开做的改动（交给 AI 按工具过滤、详细版、CC Switch 模型和导入语义）
 * 都从这些 props 取数据，所以先把它们锁住，免得谁改外壳时悄悄丢掉。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { ref } from 'vue'

import KeyOnboardingModal from '../../KeyOnboardingModal.vue'
import AiTab from '../AiTab.vue'
import CcSwitchTab from '../CcSwitchTab.vue'

const { getAvailable } = vi.hoisted(() => ({ getAvailable: vi.fn() }))
vi.mock('@/api/channels', () => ({ userChannelsAPI: { getAvailable } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn() }) }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const { default: en } = await import('@/i18n/locales/en')
  const lookup = (key: string): string | undefined => {
    let cur: unknown = en
    for (const seg of key.split('.')) {
      if (typeof cur !== 'object' || cur === null) return undefined
      cur = (cur as Record<string, unknown>)[seg]
    }
    return typeof cur === 'string' ? cur : undefined
  }
  return {
    ...actual,
    useI18n: () => ({
      locale: ref('en'),
      t: (key: string, params: Record<string, unknown> = {}) =>
        (lookup(key) ?? key).replace(/\{(\w+)\}/g, (_, k) => String(params[k] ?? ''))
    })
  }
})

const channels = [
  {
    name: 'ch',
    description: '',
    platforms: [{ platform: 'openai', groups: [{ id: 7, name: 'g7' }], supported_models: [{ name: 'gpt-5.6-sol' }] }]
  }
]

const keyOf = (platform: string, extra: Record<string, unknown> = {}) => ({
  key: 'sk-SECRET-1234567890abcdef',
  name: 'my-key',
  group_id: 7,
  group: { id: 7, platform, ...extra }
})

function mountModal(props: Record<string, unknown>) {
  return mount(KeyOnboardingModal, {
    props: { show: true, baseUrl: 'https://codex.hiyo.top', siteName: 'Hiyo', docUrl: '', apiKey: keyOf('openai'), ...props } as never,
    global: { stubs: { BaseDialog: { props: ['show', 'title'], template: '<div v-if="show"><slot /></div>' } } }
  })
}

describe('外壳交给页签的接口', () => {
  beforeEach(() => {
    getAvailable.mockReset()
    localStorage.clear()
  })

  it('交给 AI：拿到分组是否开了调度、能用的客户端（和一键安装是同一份）、模型是否在加载', async () => {
    let resolve!: (v: unknown) => void
    getAvailable.mockReturnValue(new Promise((r) => (resolve = r)))
    const w = mountModal({ initialTab: 'ai', apiKey: keyOf('openai', { allow_messages_dispatch: true }) })
    await flushPromises()

    const ai = w.getComponent(AiTab)
    expect(ai.props('allowMessagesDispatch')).toBe(true)
    expect(ai.props('clients')).toEqual(['codex', 'claude', 'opencode'])
    expect(ai.props('modelsLoading')).toBe(true)
    expect(ai.props('models')).toEqual([])

    resolve(channels)
    await flushPromises()
    expect(ai.props('modelsLoading')).toBe(false)
    expect(ai.props('models')).toEqual(['gpt-5.6-sol'])
  })

  it('交给 AI：分组没开调度时 allowMessagesDispatch 不为 true，clients 里没有 Claude Code', async () => {
    getAvailable.mockResolvedValue(channels)
    const w = mountModal({ initialTab: 'ai' })
    await flushPromises()
    const ai = w.getComponent(AiTab)
    expect(ai.props('allowMessagesDispatch')).toBeFalsy()
    expect(ai.props('clients')).toEqual(['codex', 'opencode'])
  })

  it('CC Switch：拿到模型是否在加载，表单是一个对象', async () => {
    let resolve!: (v: unknown) => void
    getAvailable.mockReturnValue(new Promise((r) => (resolve = r)))
    const w = mountModal({ initialTab: 'ccswitch' })
    await flushPromises()

    const ccs = w.getComponent(CcSwitchTab)
    expect(ccs.props('modelsLoading')).toBe(true)
    expect(ccs.props('form')).toEqual({ client: 'codex', name: '', model: '' })

    resolve(channels)
    await flushPromises()
    expect(ccs.props('modelsLoading')).toBe(false)

    // 页签里改名字，外壳存下来的是新对象；切走再回来还在
    await w.get('[data-test="ccs-name"]').setValue('Mine')
    expect(w.getComponent(CcSwitchTab).props('form')).toEqual({ client: 'codex', name: 'Mine', model: '' })
    await w.get('[data-test="tab-manual"]').trigger('click')
    await w.get('[data-test="tab-ccswitch"]').trigger('click')
    expect((w.get('[data-test="ccs-name"]').element as HTMLInputElement).value).toBe('Mine')
  })
})
