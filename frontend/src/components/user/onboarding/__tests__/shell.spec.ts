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

  it('CC Switch：拿到模型是否在加载，表单是一个对象；模型加载完按规则预选', async () => {
    let resolve!: (v: unknown) => void
    getAvailable.mockReturnValue(new Promise((r) => (resolve = r)))
    const w = mountModal({ initialTab: 'ccswitch' })
    await flushPromises()

    const ccs = w.getComponent(CcSwitchTab)
    const empty = { haikuModel: '', sonnetModel: '', opusModel: '' }
    expect(ccs.props('modelsLoading')).toBe(true)
    expect(ccs.props('form')).toEqual({ client: 'codex', name: '', model: '', ...empty })

    resolve(channels)
    await flushPromises()
    expect(ccs.props('modelsLoading')).toBe(false)
    // 分组里有 gpt-5.6-sol：Codex 预选它
    expect(ccs.props('form')).toEqual({ client: 'codex', name: '', model: 'gpt-5.6-sol', ...empty })

    // 页签里改名字，外壳存下来的是新对象；切走再回来还在
    await w.get('[data-test="ccs-name"]').setValue('Mine')
    expect(w.getComponent(CcSwitchTab).props('form')).toEqual({ client: 'codex', name: 'Mine', model: 'gpt-5.6-sol', ...empty })
    await w.get('[data-test="tab-manual"]').trigger('click')
    await w.get('[data-test="tab-ccswitch"]').trigger('click')
    expect((w.get('[data-test="ccs-name"]').element as HTMLInputElement).value).toBe('Mine')
  })

  it('CC Switch：Claude 分组的四个下拉预选好；切换客户端一次换完整个表单', async () => {
    getAvailable.mockResolvedValue([
      {
        name: 'ch',
        description: '',
        platforms: [
          {
            platform: 'antigravity',
            groups: [{ id: 7, name: 'g7' }],
            supported_models: [{ name: 'claude-opus-5' }, { name: 'claude-sonnet-5' }, { name: 'claude-haiku-4-5' }, { name: 'gemini-3-pro' }]
          }
        ]
      }
    ])
    const w = mountModal({ initialTab: 'ccswitch', apiKey: keyOf('antigravity') })
    await flushPromises()
    const value = (test: string) => (w.get(`[data-test="${test}"]`).element as HTMLSelectElement).value
    expect(['ccs-model', 'ccs-model-haiku', 'ccs-model-sonnet', 'ccs-model-opus'].map(value)).toEqual([
      'claude-sonnet-5',
      'claude-haiku-4-5',
      'claude-sonnet-5',
      'claude-opus-5'
    ])

    // 把名称和 Opus 改掉，切到 Gemini：名称回到默认、只剩一个主模型，已经预选好
    await w.get('[data-test="ccs-name"]').setValue('Mine')
    await w.get('[data-test="ccs-model-opus"]').setValue('claude-sonnet-5')
    await w.get('[data-test="ccs-client-gemini"]').trigger('click')
    expect(w.getComponent(CcSwitchTab).props('form')).toEqual({
      client: 'gemini',
      name: '',
      model: 'gemini-3-pro',
      haikuModel: '',
      sonnetModel: '',
      opusModel: ''
    })
    expect(w.findAll('[data-test="ccs-models"] select')).toHaveLength(1)

    // 再切回 Claude：三档重新预选，上次手改的 Opus 不再保留
    await w.get('[data-test="ccs-client-claude"]').trigger('click')
    expect(['ccs-model', 'ccs-model-haiku', 'ccs-model-sonnet', 'ccs-model-opus'].map(value)).toEqual([
      'claude-sonnet-5',
      'claude-haiku-4-5',
      'claude-sonnet-5',
      'claude-opus-5'
    ])
  })

  it('CC Switch：模型加载中导入和复制都禁用，加载完恢复；加载失败时给出「没有可选的模型」，仍可导入', async () => {
    let resolve!: (v: unknown) => void
    getAvailable.mockReturnValue(new Promise((r) => (resolve = r)))
    const w = mountModal({ initialTab: 'ccswitch' })
    await flushPromises()
    expect(w.get('[data-test="ccs-open"]').attributes('disabled')).toBeDefined()
    expect(w.get('[data-test="ccs-models-hint"]').text()).toBe('Loading available models…')
    resolve(channels)
    await flushPromises()
    expect(w.get('[data-test="ccs-open"]').attributes('disabled')).toBeUndefined()
    expect(w.find('[data-test="ccs-models-hint"]').exists()).toBe(false)

    getAvailable.mockReset()
    getAvailable.mockRejectedValue(new Error('x'))
    const failed = mountModal({ initialTab: 'ccswitch' })
    await flushPromises()
    expect(failed.get('[data-test="ccs-models-hint"]').text()).toContain('No models are available')
    expect(failed.get('[data-test="ccs-open"]').attributes('disabled')).toBeUndefined()
  })
})
