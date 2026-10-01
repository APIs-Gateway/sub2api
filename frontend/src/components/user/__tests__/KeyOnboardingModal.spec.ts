import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { ref } from 'vue'

import KeyOnboardingModal from '../KeyOnboardingModal.vue'

const { getAvailable } = vi.hoisted(() => ({ getAvailable: vi.fn() }))
vi.mock('@/api/channels', () => ({ userChannelsAPI: { getAvailable } }))

const SECRET = 'sk-SECRET-1234567890abcdef'

// 测试环境的 vue-i18n 是 runtime-only 构建，不能编译消息；这里直接用英文文案做插值
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

const apiKey = (platform = 'openai') => ({
  key: SECRET,
  name: 'my-key',
  group_id: 7,
  group: { id: 7, platform }
})

const channels = [
  {
    name: 'ch',
    description: '',
    platforms: [
      {
        platform: 'openai',
        groups: [{ id: 7, name: 'g' }],
        supported_models: [{ name: 'gpt-5.6-sol' }, { name: 'gpt-5.6-luna' }]
      },
      { platform: 'openai', groups: [{ id: 99, name: 'other' }], supported_models: [{ name: 'not-mine' }] }
    ]
  }
]

async function mountModal(props: Record<string, unknown> = {}) {
  const wrapper = mount(KeyOnboardingModal, {
    props: { show: true, apiKey: apiKey(), baseUrl: 'https://codex.hiyo.top/', siteName: 'Hiyo', docUrl: '', ...props } as never,
    global: {
      stubs: { BaseDialog: { props: ['show', 'title'], template: '<div v-if="show"><slot /></div>' } }
    }
  })
  await flushPromises()
  return wrapper
}

const clipboard = { writeText: vi.fn().mockResolvedValue(undefined) }

describe('KeyOnboardingModal', () => {
  beforeEach(() => {
    getAvailable.mockReset()
    getAvailable.mockResolvedValue(channels)
    clipboard.writeText.mockClear()
    Object.defineProperty(navigator, 'clipboard', { value: clipboard, configurable: true })
  })

  it('有四个页签，默认打开一键安装', async () => {
    const w = await mountModal()
    expect(['install', 'ai', 'ccswitch', 'manual'].map((id) => w.find(`[data-test="tab-${id}"]`).exists())).toEqual([true, true, true, true])
    expect(w.find('[data-test="panel-install"]').exists()).toBe(true)
    expect(w.get('[data-test="tab-install"]').attributes('aria-selected')).toBe('true')
  })

  it('initialTab 决定打开时落在哪个页签，点击页签可切换', async () => {
    const w = await mountModal({ initialTab: 'ccswitch' })
    expect(w.find('[data-test="panel-ccswitch"]').exists()).toBe(true)
    await w.get('[data-test="tab-ai"]').trigger('click')
    expect(w.find('[data-test="panel-ai"]').exists()).toBe(true)
    await w.get('[data-test="tab-manual"]').trigger('click')
    expect(w.find('[data-test="panel-manual"]').exists()).toBe(true)
    // 重新打开回到初始页签
    await w.setProps({ show: false })
    await w.setProps({ show: true })
    expect(w.find('[data-test="panel-ccswitch"]').exists()).toBe(true)
  })

  it('弹窗已打开时 initialTab 变化会切换页签', async () => {
    const w = await mountModal({ initialTab: 'install' })
    await w.setProps({ initialTab: 'ccswitch' })
    expect(w.find('[data-test="panel-ccswitch"]').exists()).toBe(true)
  })

  it('一键安装按平台分块，每块有 macOS/Linux 和 Windows 两个复制按钮', async () => {
    const w = await mountModal()
    expect(w.find('[data-test="client-codex"]').exists()).toBe(true)
    expect(w.find('[data-test="client-opencode"]').exists()).toBe(true)
    expect(w.find('[data-test="client-claude"]').exists()).toBe(false)
    expect(w.get('[data-test="copy-codex-unix"]').text()).toContain('macOS / Linux')
    expect(w.get('[data-test="copy-codex-windows"]').text()).toContain('Windows')
  })

  it('点复制：写入剪贴板的是完整脚本，按钮变成已复制', async () => {
    const w = await mountModal()
    await w.get('[data-test="copy-codex-unix"]').trigger('click')
    await flushPromises()
    expect(clipboard.writeText).toHaveBeenCalledTimes(1)
    const copied = clipboard.writeText.mock.calls[0][0] as string
    expect(copied).toContain(SECRET)
    expect(copied).toContain('https://codex.hiyo.top/v1')
    expect(copied).toContain('bak-')
    expect(w.get('[data-test="copy-codex-unix"]').text()).toBe('Copied')
  })

  it('没有分组时给出提示，不展示客户端', async () => {
    const w = await mountModal({ apiKey: { key: SECRET, name: 'k', group_id: null, group: null } })
    expect(w.find('[data-test="no-group"]').exists()).toBe(true)
    expect(w.find('[data-test="panel-install"]').exists()).toBe(false)
  })

  describe('交给 AI', () => {
    it('文本里没有密钥，复制简短版/详细版也没有', async () => {
      const w = await mountModal({ initialTab: 'ai', docUrl: 'https://docs.example.com' })
      const shown = (w.get('[data-test="ai-prompt"]').element as HTMLTextAreaElement).value
      expect(shown).not.toContain(SECRET)
      expect(shown).toContain('https://codex.hiyo.top')

      await w.get('[data-test="ai-copy"]').trigger('click')
      await w.get('[data-test="ai-copy-detail"]').trigger('click')
      await flushPromises()
      const [short, detail] = clipboard.writeText.mock.calls.map((c) => c[0] as string)
      for (const text of [short, detail]) {
        expect(text).not.toContain(SECRET)
        expect(text).not.toContain('sk-')
      }
      expect(detail).toContain('gpt-5.6-sol, gpt-5.6-luna')
      expect(detail).not.toContain('not-mine')
      expect(detail).toContain('https://docs.example.com')
    })

    it('切换客户端会改文本', async () => {
      const w = await mountModal({ initialTab: 'ai' })
      const text = () => (w.get('[data-test="ai-prompt"]').element as HTMLTextAreaElement).value
      await w.get('[data-test="ai-client-claude"]').trigger('click')
      const claude = text()
      await w.get('[data-test="ai-client-cursor"]').trigger('click')
      expect(text()).not.toBe(claude)
      expect(text()).toContain('Cursor')
    })

    it('在 ChatGPT / Claude 中打开：链接只带不含密钥的文本', async () => {
      const open = vi.spyOn(window, 'open').mockReturnValue(null)
      const w = await mountModal({ initialTab: 'ai' })
      await w.get('[data-test="ai-open-chatgpt"]').trigger('click')
      await w.get('[data-test="ai-open-claude"]').trigger('click')
      const [gpt, claude] = open.mock.calls.map((c) => String(c[0]))
      expect(gpt.startsWith('https://chatgpt.com/?q=')).toBe(true)
      expect(claude.startsWith('https://claude.ai/new?q=')).toBe(true)
      expect(decodeURIComponent(gpt)).not.toContain(SECRET)
      expect(decodeURIComponent(claude)).not.toContain(SECRET)
      open.mockRestore()
    })
  })

  describe('CC Switch', () => {
    it('默认名称是「站点名 - 客户端」，深链带名称和模型', async () => {
      const open = vi.spyOn(window, 'open').mockReturnValue(null)
      const w = await mountModal({ initialTab: 'ccswitch' })
      const nameInput = w.get('[data-test="ccs-name"]')
      expect(nameInput.attributes('placeholder')).toBe('Hiyo - Codex')

      await w.get('[data-test="ccs-model"]').setValue('gpt-5.6-luna')
      await nameInput.setValue('Mine')
      await w.get('[data-test="ccs-open"]').trigger('click')
      const url = new URL(String(open.mock.calls[0][0]).replace('ccswitch://', 'http://'))
      expect(url.searchParams.get('name')).toBe('Mine')
      expect(url.searchParams.get('model')).toBe('gpt-5.6-luna')
      expect(url.searchParams.get('app')).toBe('codex')
      expect(url.searchParams.get('apiKey')).toBe(SECRET)
      open.mockRestore()
    })

    it('模型选项只来自该密钥的分组', async () => {
      const w = await mountModal({ initialTab: 'ccswitch' })
      const options = w.findAll('[data-test="ccs-model"] option').map((o) => o.text())
      expect(options).toEqual(['Default', 'gpt-5.6-sol', 'gpt-5.6-luna'])
    })

    it('取不到模型列表时不显示模型字段', async () => {
      getAvailable.mockRejectedValue(new Error('x'))
      const w = await mountModal({ initialTab: 'ccswitch' })
      expect(w.find('[data-test="ccs-model"]').exists()).toBe(false)
    })

    it('antigravity 分组可以选 Claude 或 Gemini', async () => {
      const w = await mountModal({ initialTab: 'ccswitch', apiKey: apiKey('antigravity') })
      expect(w.find('[data-test="ccs-client-claude"]').exists()).toBe(true)
      await w.get('[data-test="ccs-client-gemini"]').trigger('click')
      expect(w.get('[data-test="ccs-name"]').attributes('placeholder')).toBe('Hiyo - Gemini')
    })
  })

  it('手动配置：密钥只显示掩码，复制的是完整密钥', async () => {
    const w = await mountModal({ initialTab: 'manual' })
    const panel = w.get('[data-test="panel-manual"]')
    expect(panel.text()).toContain('https://codex.hiyo.top')
    expect(panel.find('table').text()).not.toContain(SECRET)
    await panel.findAll('button').find((b) => b.text() === 'Copy' && b.element.closest('tr')?.textContent?.includes('API'))!.trigger('click')
    await flushPromises()
    expect(clipboard.writeText).toHaveBeenCalledWith(SECRET)
  })
})
