import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { ref } from 'vue'

import KeyOnboardingModal from '../KeyOnboardingModal.vue'

const { getAvailable, showError, i18nState } = vi.hoisted(() => ({
  getAvailable: vi.fn(),
  showError: vi.fn(),
  // 测试里可以切到中文，检查脚本里的提示是否跟随界面语言
  i18nState: { lang: 'en' as 'en' | 'zh-CN' }
}))
vi.mock('@/api/channels', () => ({ userChannelsAPI: { getAvailable } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError }) }))

const SECRET = 'sk-SECRET-1234567890abcdef'

// 测试环境的 vue-i18n 是 runtime-only 构建，不能编译消息；这里直接用英文文案做插值
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const { default: en } = await import('@/i18n/locales/en')
  const { default: zhCN } = await import('@/i18n/locales/zh-CN')
  const lookup = (key: string): string | undefined => {
    let cur: unknown = i18nState.lang === 'zh-CN' ? zhCN : en
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

async function mountModal(props: Record<string, unknown> = {}, attachTo?: HTMLElement) {
  const wrapper = mount(KeyOnboardingModal, {
    props: { show: true, apiKey: apiKey(), baseUrl: 'https://codex.hiyo.top/', siteName: 'Hiyo', docUrl: '', ...props } as never,
    attachTo,
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
    showError.mockReset()
    i18nState.lang = 'en'
    clipboard.writeText.mockReset()
    clipboard.writeText.mockResolvedValue(undefined)
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

  describe('页签的无障碍', () => {
    const IDS = ['install', 'ai', 'ccswitch', 'manual'] as const

    it('每个页签通过 aria-controls 指向内容区，内容区是 tabpanel 并回指页签', async () => {
      const w = await mountModal()
      for (const id of IDS) {
        const tab = w.get(`[data-test="tab-${id}"]`)
        expect(tab.attributes('id')).toBeTruthy()
        expect(tab.attributes('aria-controls')).toBeTruthy()
        expect(tab.attributes('tabindex')).toBe(id === 'install' ? '0' : '-1')
      }
      const install = w.get('[data-test="tab-install"]')
      let panel = w.get('[data-test="panel-install"]')
      expect(panel.attributes('role')).toBe('tabpanel')
      expect(panel.attributes('id')).toBe(install.attributes('aria-controls'))
      expect(panel.attributes('aria-labelledby')).toBe(install.attributes('id'))

      await w.get('[data-test="tab-manual"]').trigger('click')
      panel = w.get('[data-test="panel-manual"]')
      expect(panel.attributes('role')).toBe('tabpanel')
      expect(panel.attributes('id')).toBe(w.get('[data-test="tab-manual"]').attributes('aria-controls'))
      expect(panel.attributes('aria-labelledby')).toBe(w.get('[data-test="tab-manual"]').attributes('id'))
      expect(w.get('[data-test="tab-manual"]').attributes('tabindex')).toBe('0')
      expect(install.attributes('tabindex')).toBe('-1')
    })

    it('没有分组时的提示也是当前页签的内容区', async () => {
      const w = await mountModal({ apiKey: { key: SECRET, name: 'k', group_id: null, group: null } })
      const hint = w.get('[data-test="no-group"]')
      expect(hint.attributes('role')).toBe('tabpanel')
      expect(hint.attributes('aria-labelledby')).toBe(w.get('[data-test="tab-install"]').attributes('id'))
    })

    describe('键盘', () => {
      let host: HTMLElement
      beforeEach(() => {
        host = document.createElement('div')
        document.body.appendChild(host)
      })
      afterEach(() => host.remove())

      const press = async (w: VueWrapper, from: string, key: string) => {
        await w.get(`[data-test="tab-${from}"]`).trigger('keydown', { key })
        await flushPromises()
      }
      const activeTab = (w: VueWrapper) => w.findAll('[role="tab"]').find((t) => t.attributes('aria-selected') === 'true')!.attributes('data-test')

      it('左右、上下方向键切换页签并移动焦点，首尾循环', async () => {
        const w = await mountModal({}, host)
        await press(w, 'install', 'ArrowRight')
        expect(activeTab(w)).toBe('tab-ai')
        expect(document.activeElement).toBe(w.get('[data-test="tab-ai"]').element)
        await press(w, 'ai', 'ArrowDown')
        expect(activeTab(w)).toBe('tab-ccswitch')
        await press(w, 'ccswitch', 'ArrowLeft')
        expect(activeTab(w)).toBe('tab-ai')
        await press(w, 'ai', 'ArrowUp')
        expect(activeTab(w)).toBe('tab-install')
        // 第一个再往前回到最后一个，最后一个再往后回到第一个
        await press(w, 'install', 'ArrowLeft')
        expect(activeTab(w)).toBe('tab-manual')
        expect(document.activeElement).toBe(w.get('[data-test="tab-manual"]').element)
        await press(w, 'manual', 'ArrowRight')
        expect(activeTab(w)).toBe('tab-install')
        w.unmount()
      })

      it('Home 回到第一个，End 到最后一个，内容区跟着切换', async () => {
        const w = await mountModal({ initialTab: 'ai' }, host)
        await press(w, 'ai', 'End')
        expect(activeTab(w)).toBe('tab-manual')
        expect(w.find('[data-test="panel-manual"]').exists()).toBe(true)
        expect(document.activeElement).toBe(w.get('[data-test="tab-manual"]').element)
        await press(w, 'manual', 'Home')
        expect(activeTab(w)).toBe('tab-install')
        expect(w.find('[data-test="panel-install"]').exists()).toBe(true)
        w.unmount()
      })

      it('其他按键不处理', async () => {
        const w = await mountModal({}, host)
        const ev = new KeyboardEvent('keydown', { key: 'a', bubbles: true, cancelable: true })
        w.get('[data-test="tab-install"]').element.dispatchEvent(ev)
        await flushPromises()
        expect(ev.defaultPrevented).toBe(false)
        expect(activeTab(w)).toBe('tab-install')
        w.unmount()
      })
    })
  })

  it('CC Switch 的客户端单选组有名称，标签与它关联', async () => {
    const w = await mountModal({ initialTab: 'ccswitch', apiKey: apiKey('antigravity') })
    const group = w.get('[role="radiogroup"]')
    expect(group.attributes('aria-label')).toBe('Client')
    const labelId = group.attributes('aria-labelledby')!
    expect(labelId).toBeTruthy()
    const label = w.get(`[id="${labelId}"]`)
    expect(label.text()).toBe('Client')
    expect(label.element.tagName).toBe('SPAN')
    // 名称、模型这类有输入框的标签仍然用 for 关联到控件
    expect(w.get('label[for="ccs-name"]').exists()).toBe(true)
  })

  describe('复制反馈', () => {
    it('复制成功后用 aria-live 区域播报，到时间后清空', async () => {
      vi.useFakeTimers()
      try {
        const w = await mountModal()
        const status = w.get('[data-test="copy-status"]')
        expect(status.attributes('role')).toBe('status')
        expect(status.attributes('aria-live')).toBe('polite')
        expect(status.text()).toBe('')
        await w.get('[data-test="copy-codex-unix"]').trigger('click')
        await flushPromises()
        expect(w.get('[data-test="copy-status"]').text()).toBe('Copied')
        await vi.advanceTimersByTimeAsync(2000)
        await flushPromises()
        expect(w.get('[data-test="copy-status"]').text()).toBe('')
      } finally {
        vi.useRealTimers()
      }
    })

    it('复制失败时弹出提示，按钮不显示已复制', async () => {
      clipboard.writeText.mockRejectedValue(new Error('denied'))
      const w = await mountModal()
      await w.get('[data-test="copy-codex-unix"]').trigger('click')
      await flushPromises()
      expect(showError).toHaveBeenCalledTimes(1)
      expect(showError).toHaveBeenCalledWith('Failed to copy')
      expect(w.get('[data-test="copy-codex-unix"]').text()).not.toBe('Copied')
      expect(w.get('[data-test="copy-status"]').text()).toBe('')
    })

    it('复制成功时不弹错误提示', async () => {
      const w = await mountModal()
      await w.get('[data-test="copy-codex-unix"]').trigger('click')
      await flushPromises()
      expect(showError).not.toHaveBeenCalled()
    })
  })

  describe('手动配置里的代码块标签', () => {
    it('只有路径用等宽字体，「环境变量」这类普通文字用正常字体', async () => {
      const w = await mountModal({ initialTab: 'manual', apiKey: apiKey('anthropic') })
      const labelOf = (text: string) => w.findAll('details span').find((el) => el.text() === text)!
      const path = labelOf('~/.claude/settings.json')
      const plain = labelOf('Environment variables')
      expect(path.classes()).toContain('font-mono')
      expect(plain.classes()).not.toContain('font-mono')
    })

    it('codex 的配置文件路径用等宽字体', async () => {
      const w = await mountModal({ initialTab: 'manual' })
      const path = w.findAll('details span').find((el) => el.text() === '~/.codex/config.toml')!
      expect(path.classes()).toContain('font-mono')
    })
  })

  describe('脚本里的终端提示跟随界面语言', () => {
    const copyScript = async (w: VueWrapper, os: 'unix' | 'windows') => {
      await w.get(`[data-test="copy-codex-${os}"]`).trigger('click')
      await flushPromises()
      const calls = clipboard.writeText.mock.calls
      return calls[calls.length - 1][0] as string
    }

    it('英文界面：英文提示', async () => {
      const w = await mountModal()
      const unix = await copyScript(w, 'unix')
      expect(unix).toContain("'Backup: '")
      expect(unix).toContain("'Updated: '")
      expect(unix).toContain('command line developer tools')
      expect(unix).toContain('Python 3 is required')
      const win = await copyScript(w, 'windows')
      expect(win).toContain("('Failed: ' + $_.Exception.Message + '')")
    })

    it('中文界面：提示、备份、更新、失败都是中文', async () => {
      i18nState.lang = 'zh-CN'
      const w = await mountModal()
      const unix = await copyScript(w, 'unix')
      expect(unix).toContain("'已备份：'")
      expect(unix).toContain("'已更新：'")
      expect(unix).toContain('这台 Mac 还没有命令行开发工具，请改用「CC Switch」或「手动配置」页签。')
      expect(unix).toContain('更新配置文件需要 Python 3。')
      expect(unix).not.toContain("'Backup: '")
      const win = await copyScript(w, 'windows')
      expect(win).toContain("('失败：' + $_.Exception.Message + '')")
      expect(win).toContain("('已备份：' + $b + '')")
      expect(win).not.toContain('Failed:')
    })
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
