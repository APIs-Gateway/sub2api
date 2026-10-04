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

  describe('一键安装：卡片', () => {
    const tilesOf = (w: VueWrapper, client: string) => w.findAll(`[data-test="client-${client}"] button.onb-tile`)

    it('每个客户端一张卡片；Claude Code、OpenCode 等只有 macOS / Linux 和 Windows 两块瓦片', async () => {
      const w = await mountModal({ apiKey: apiKey('anthropic') })
      expect(w.find('[data-test="client-claude"]').exists()).toBe(true)
      expect(w.find('[data-test="client-opencode"]').exists()).toBe(true)
      expect(w.find('[data-test="client-codex"]').exists()).toBe(false)
      for (const c of ['claude', 'opencode']) {
        const tiles = tilesOf(w, c)
        expect(tiles.map((t) => t.text())).toEqual(['macOS / Linux', 'Windows'])
      }
      expect(w.get('[data-test="copy-claude-unix"]').exists()).toBe(true)
      expect(w.get('[data-test="copy-claude-windows"]').exists()).toBe(true)
    })

    it('没有分组可用的客户端时不渲染卡片', async () => {
      const w = await mountModal({ apiKey: { key: SECRET, name: 'k', group_id: 3, group: { id: 3, platform: 'unknown' } } })
      expect(w.findAll('[data-test^="client-"]')).toHaveLength(0)
    })

    it('Codex 卡片有两行瓦片：完整安装、只刷新配置，各有 macOS / Linux 和 Windows', async () => {
      const w = await mountModal()
      expect(w.find('[data-test="client-claude"]').exists()).toBe(false)
      expect(tilesOf(w, 'codex').map((t) => t.text())).toEqual([
        'Full install · macOS / Linux',
        'Full install · Windows',
        'Refresh config only · macOS / Linux',
        'Refresh config only · Windows'
      ])
      expect(tilesOf(w, 'opencode').map((t) => t.text())).toEqual(['macOS / Linux', 'Windows'])
      for (const id of ['full', 'refresh']) {
        for (const os of ['unix', 'windows']) expect(w.find(`[data-test="copy-codex-${id}-${os}"]`).exists()).toBe(true)
      }
      // 说明两种模式的区别，并带出 Node.js 版本要求
      expect(w.get('[data-test="codex-modes"]').text()).toContain('Node.js 16')
    })

    it('瓦片上只显示平台名和一个复制图标，不显示脚本', async () => {
      const w = await mountModal({ apiKey: apiKey('anthropic') })
      const tile = w.get('[data-test="copy-claude-unix"]')
      expect(tile.text()).toBe('macOS / Linux')
      expect(tile.find('svg').exists()).toBe(true)
      expect(tile.find('[data-test="icon-copy"]').exists()).toBe(true)
      expect(tile.attributes('aria-label')).toBe('Copy command: Claude Code, macOS / Linux')
    })

    it('弹窗头：副标题带密钥名，底部说明带站点名', async () => {
      const w = await mountModal()
      expect(w.get('[data-test="subtitle"]').text()).toContain('Connect "my-key" to your client')
      expect(w.get('[data-test="install-footnote"]').text()).toContain('The script writes the Hiyo endpoint and key')
      expect(w.get('[data-test="install-footnote"]').text()).toContain('backs up your existing config first')
    })

    it('页签带图标，选中的页签有选中样式', async () => {
      const w = await mountModal()
      for (const id of ['install', 'ai', 'ccswitch', 'manual']) {
        expect(w.get(`[data-test="tab-${id}"] svg`).exists()).toBe(true)
      }
      expect(w.get('[data-test="tab-install"]').classes()).toContain('onb-tab-active')
      expect(w.get('[data-test="tab-ai"]').classes()).not.toContain('onb-tab-active')
      await w.get('[data-test="tab-ai"]').trigger('click')
      expect(w.get('[data-test="tab-ai"]').classes()).toContain('onb-tab-active')
    })
  })

  describe('一键安装：教程链接', () => {
    it('指向站内文档的对应小节，新标签页打开', async () => {
      const w = await mountModal({ apiKey: apiKey('anthropic') })
      const claude = w.get('[data-test="tutorial-claude"]')
      expect(claude.attributes('href')).toBe('/docs#claude-code')
      expect(claude.attributes('target')).toBe('_blank')
      expect(claude.attributes('rel')).toContain('noopener')
      expect(claude.text()).toContain('View guide')
      expect(w.get('[data-test="tutorial-opencode"]').attributes('href')).toBe('/docs#other-clients')
    })

    it('Codex 卡片指向 codex 小节', async () => {
      const w = await mountModal()
      expect(w.get('[data-test="tutorial-codex"]').attributes('href')).toBe('/docs#codex')
    })

    it('没有对应小节的客户端不显示链接', async () => {
      const w = await mountModal({ apiKey: apiKey('gemini') })
      expect(w.find('[data-test="client-gemini"]').exists()).toBe(true)
      expect(w.find('[data-test="tutorial-gemini"]').exists()).toBe(false)
      expect(w.find('[data-test="tutorial-opencode"]').exists()).toBe(true)
    })
  })

  describe('一键安装：查看脚本', () => {
    it('每张卡片保留折叠的脚本预览，里面是与复制相同的脚本', async () => {
      const w = await mountModal({ apiKey: apiKey('anthropic') })
      const details = w.get('[data-test="script-claude"]')
      expect(details.element.tagName).toBe('DETAILS')
      expect(details.attributes('open')).toBeUndefined()
      expect(details.text()).toContain('View script')
      await w.get('[data-test="copy-claude-unix"]').trigger('click')
      await flushPromises()
      const copied = clipboard.writeText.mock.calls[0][0] as string
      const shown = details.findAll('pre').map((p) => p.text())
      expect(shown).toHaveLength(2)
      expect(shown).toContain(copied)
    })

    it('Codex 的预览包含完整安装和只刷新配置的四份脚本', async () => {
      const w = await mountModal()
      const blocks = w.get('[data-test="script-codex"]').findAll('pre').map((p) => p.text())
      expect(blocks).toHaveLength(4)
      // 只有完整安装的两份（macOS / Linux 和 Windows）带 npm 安装
      expect(blocks.filter((b) => b.includes('npm install -g') || b.includes('npm.cmd install -g'))).toHaveLength(2)
    })
  })

  describe('一键安装：复制', () => {
    const copiedTexts = () => clipboard.writeText.mock.calls.map((c) => c[0] as string)

    it('点瓦片：写入剪贴板的是完整脚本，图标变成勾', async () => {
      const w = await mountModal()
      const tile = w.get('[data-test="copy-codex-refresh-unix"]')
      expect(tile.find('[data-test="icon-copy"]').exists()).toBe(true)
      await tile.trigger('click')
      await flushPromises()
      expect(clipboard.writeText).toHaveBeenCalledTimes(1)
      const copied = copiedTexts()[0]
      expect(copied).toContain(SECRET)
      expect(copied).toContain('https://codex.hiyo.top/v1')
      expect(copied).toContain('bak-')
      expect(w.get('[data-test="copy-codex-refresh-unix"]').find('[data-test="icon-check"]').exists()).toBe(true)
      expect(w.get('[data-test="copy-codex-refresh-unix"]').find('[data-test="icon-copy"]').exists()).toBe(false)
      expect(w.get('[data-test="copy-codex-refresh-unix"]').attributes('data-copied')).toBe('true')
      // 其他瓦片不受影响
      expect(w.get('[data-test="copy-codex-full-unix"]').attributes('data-copied')).toBe('false')
    })

    it('每块瓦片复制各自平台、各自模式的脚本', async () => {
      const w = await mountModal()
      for (const id of ['codex-full-unix', 'codex-full-windows', 'codex-refresh-unix', 'codex-refresh-windows']) {
        await w.get(`[data-test="copy-${id}"]`).trigger('click')
      }
      await flushPromises()
      const [fullUnix, fullWin, refreshUnix, refreshWin] = copiedTexts()
      expect(fullUnix.startsWith("bash <<'SUB2API_INSTALL_EOF'")).toBe(true)
      expect(fullUnix).toContain('npm install -g @openai/codex@latest')
      expect(fullWin.startsWith('& {')).toBe(true)
      expect(fullWin).toContain("npm.cmd install -g '@openai/codex@latest'")
      expect(refreshUnix.startsWith("bash <<'SUB2API_INSTALL_EOF'")).toBe(true)
      expect(refreshUnix).not.toContain('npm')
      expect(refreshWin.startsWith('& {')).toBe(true)
      expect(refreshWin).not.toContain('npm')
      // 四份都写同一个配置文件，密钥都在
      for (const text of [fullUnix, fullWin, refreshUnix, refreshWin]) {
        expect(text).toContain(SECRET)
        expect(text).toContain('wire_api = "responses"')
      }
    })

    it('其他客户端的瓦片复制各自的脚本，不含 npm 安装', async () => {
      const w = await mountModal({ apiKey: apiKey('anthropic') })
      await w.get('[data-test="copy-claude-windows"]').trigger('click')
      await w.get('[data-test="copy-opencode-unix"]').trigger('click')
      await flushPromises()
      const [claudeWin, opencodeUnix] = copiedTexts()
      expect(claudeWin).toContain('ANTHROPIC_AUTH_TOKEN')
      expect(claudeWin).not.toContain('npm')
      expect(opencodeUnix).toContain('opencode.json')
      expect(opencodeUnix).not.toContain('npm')
    })
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
        await w.get('[data-test="copy-codex-refresh-unix"]').trigger('click')
        await flushPromises()
        expect(w.get('[data-test="copy-status"]').text()).toBe('Copied')
        expect(w.find('[data-test="copy-codex-refresh-unix"] [data-test="icon-check"]').exists()).toBe(true)
        await vi.advanceTimersByTimeAsync(2000)
        await flushPromises()
        expect(w.get('[data-test="copy-status"]').text()).toBe('')
        // 勾变回复制图标
        expect(w.find('[data-test="copy-codex-refresh-unix"] [data-test="icon-check"]').exists()).toBe(false)
        expect(w.find('[data-test="copy-codex-refresh-unix"] [data-test="icon-copy"]').exists()).toBe(true)
      } finally {
        vi.useRealTimers()
      }
    })

    it('复制失败时弹出提示，图标不变成勾', async () => {
      clipboard.writeText.mockRejectedValue(new Error('denied'))
      const w = await mountModal()
      await w.get('[data-test="copy-codex-refresh-unix"]').trigger('click')
      await flushPromises()
      expect(showError).toHaveBeenCalledTimes(1)
      expect(showError).toHaveBeenCalledWith('Failed to copy')
      expect(w.find('[data-test="copy-codex-refresh-unix"] [data-test="icon-check"]').exists()).toBe(false)
      expect(w.get('[data-test="copy-codex-refresh-unix"]').attributes('data-copied')).toBe('false')
      expect(w.get('[data-test="copy-status"]').text()).toBe('')
    })

    it('复制成功时不弹错误提示', async () => {
      const w = await mountModal()
      await w.get('[data-test="copy-codex-refresh-unix"]').trigger('click')
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
      await w.get(`[data-test="copy-codex-refresh-${os}"]`).trigger('click')
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

  describe('完整安装脚本里的终端提示跟随界面语言', () => {
    const copyFull = async (w: VueWrapper, os: 'unix' | 'windows') => {
      await w.get(`[data-test="copy-codex-full-${os}"]`).trigger('click')
      await flushPromises()
      const calls = clipboard.writeText.mock.calls
      return calls[calls.length - 1][0] as string
    }

    it('英文界面：英文提示，版本要求来自同一个常量', async () => {
      const w = await mountModal()
      const unix = await copyFull(w, 'unix')
      expect(unix).toContain('Node.js 16 or newer is required')
      expect(unix).toContain("'Node.js ' \"$NODE_VER\" ' is too old. Install Node.js 16 or newer")
      expect(unix).toContain('npm was not found')
      expect(unix).toContain('Installing Codex CLI with npm...')
      expect(unix).toContain('npm does not have permission to install global packages')
      expect(unix).toContain('Codex CLI could not be installed')
      const win = await copyFull(w, 'windows')
      expect(win).toContain("('Node.js ' + $nodeVer + ' is too old. Install Node.js 16 or newer")
      expect(win).toContain('npm does not have permission to install global packages')
    })

    it('中文界面：Node、npm 的提示都是中文，教大家怎么处理', async () => {
      i18nState.lang = 'zh-CN'
      const w = await mountModal()
      const unix = await copyFull(w, 'unix')
      expect(unix).toContain('需要先安装 Node.js 16 或更高版本')
      expect(unix).toContain("'Node.js ' \"$NODE_VER\" ' 版本过低，需要 16 或更高版本")
      expect(unix).toContain('没有找到 npm')
      expect(unix).toContain('正在用 npm 安装 Codex CLI…')
      expect(unix).toContain('nvm')
      expect(unix).toContain('npm config set prefix')
      expect(unix).not.toContain('sudo')
      expect(unix).not.toContain('Node.js 16 or newer')
      const win = await copyFull(w, 'windows')
      expect(win).toContain('npm 没有权限全局安装')
      expect(win).toContain("('Node.js ' + $nodeVer + ' 版本过低，需要 16 或更高版本")
      expect(win).not.toContain('sudo')
    })
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
      const codex = text()
      await w.get('[data-test="ai-client-cursor"]').trigger('click')
      expect(text()).not.toBe(codex)
      expect(text()).toContain('Cursor')
    })

    describe('工具按分组过滤，默认选中第一个可用的', () => {
      const chips = (w: VueWrapper) => w.findAll('[data-test^="ai-client-"]').map((c) => c.attributes('data-test')!.replace('ai-client-', ''))
      const checked = (w: VueWrapper) =>
        w.findAll('[data-test^="ai-client-"][aria-checked="true"]').map((c) => c.attributes('data-test')!.replace('ai-client-', ''))
      const text = (w: VueWrapper) => (w.get('[data-test="ai-prompt"]').element as HTMLTextAreaElement).value
      const group = (platform: string, extra: Record<string, unknown> = {}) => ({
        key: SECRET,
        name: 'my-key',
        group_id: 7,
        group: { id: 7, platform, ...extra }
      })

      it('openai 分组：没有 Claude Code，默认 Codex，指令里是 /v1 地址和 OpenAI 格式（以前默认 Claude Code，三者互相矛盾）', async () => {
        const w = await mountModal({ initialTab: 'ai' })
        expect(chips(w)).toEqual(['codex', 'cursor', 'chat', 'code', 'other'])
        expect(checked(w)).toEqual(['codex'])
        expect(text(w)).toContain('"Codex"')
        expect(text(w)).toContain('https://codex.hiyo.top/v1')
        expect(text(w)).toContain('OpenAI')
        expect(text(w)).not.toContain('ANTHROPIC_BASE_URL')
      })

      it('openai 分组开了调度：Claude Code 可选但排第二，默认仍是 Codex；切到 Claude Code 才是根地址 + Anthropic + ANTHROPIC_BASE_URL', async () => {
        const w = await mountModal({ initialTab: 'ai', apiKey: group('openai', { allow_messages_dispatch: true }) })
        expect(chips(w)).toEqual(['codex', 'claude', 'cursor', 'chat', 'code', 'other'])
        expect(checked(w)).toEqual(['codex'])
        expect(text(w)).toContain('"Codex"')
        expect(text(w)).toContain('The endpoint is https://codex.hiyo.top/v1 and')
        expect(text(w)).toContain('API format is OpenAI')
        expect(text(w)).not.toContain('ANTHROPIC_BASE_URL')
        // 同一个分组切到 Claude Code：地址和格式跟着变
        await w.get('[data-test="ai-client-claude"]').trigger('click')
        expect(text(w)).toContain('"Claude Code"')
        expect(text(w)).toContain('The endpoint is https://codex.hiyo.top and')
        expect(text(w)).toContain('API format is Anthropic')
        expect(text(w)).toContain('ANTHROPIC_BASE_URL')
      })

      it.each([
        ['anthropic', ['claude', 'cursor', 'chat', 'code', 'other']],
        ['antigravity', ['claude', 'chat', 'code', 'other']],
        ['gemini', ['chat', 'code', 'other']]
      ])('%s 分组：%j，默认选中第一个', async (platform, expected) => {
        const w = await mountModal({ initialTab: 'ai', apiKey: group(platform) })
        expect(chips(w)).toEqual(expected)
        expect(checked(w)).toEqual([expected[0]])
      })

      it('换到另一个密钥时，换了分组就回到新分组的默认工具；同一类分组之间换密钥，选的保留', async () => {
        const w = await mountModal({ initialTab: 'ai', apiKey: group('anthropic') })
        await w.get('[data-test="ai-client-cursor"]').trigger('click')
        await w.setProps({ show: false })
        await w.setProps({ apiKey: group('gemini'), show: true })
        await flushPromises()
        expect(checked(w)).toEqual(['chat'])
        await w.setProps({ show: false })
        await w.setProps({ apiKey: group('openai'), show: true })
        await flushPromises()
        // 以前 chat 在 openai 分组里也能用就保留，默认就取决于上一个打开的密钥；现在换了分组就回到 Codex
        expect(checked(w)).toEqual(['codex'])

        // 同一个分组（或同一类）换密钥：用户选的聊天客户端保留
        await w.get('[data-test="ai-client-chat"]').trigger('click')
        await w.setProps({ show: false })
        await w.setProps({ apiKey: { ...group('openai'), name: 'other-key' }, show: true })
        await flushPromises()
        expect(checked(w)).toEqual(['chat'])
      })

      // 默认只取决于当前分组，不取决于上一个打开的密钥
      it.each([
        ['anthropic → openai（开了调度）', [group('anthropic'), group('openai', { allow_messages_dispatch: true })]],
        ['gemini → openai', [group('gemini'), group('openai')]],
        ['openai → anthropic → openai（开了调度）', [group('openai'), group('anthropic'), group('openai', { allow_messages_dispatch: true })]],
        ['openai（没开调度）→ openai（开了调度）', [group('openai'), group('openai', { allow_messages_dispatch: true })]]
      ])('打开顺序 %s：最后一个密钥默认选中 Codex', async (_name, keys) => {
        const w = await mountModal({ initialTab: 'ai', apiKey: keys[0] })
        for (const k of keys.slice(1)) {
          await w.setProps({ show: false })
          await w.setProps({ apiKey: k, show: true })
          await flushPromises()
        }
        expect(checked(w)).toEqual(['codex'])
        expect(text(w)).toContain('"Codex"')
        expect(text(w)).toContain('https://codex.hiyo.top/v1')
      })

      it('切到别的页签再回来，修正后的选择还在', async () => {
        const w = await mountModal({ initialTab: 'ai' })
        expect(checked(w)).toEqual(['codex'])
        await w.get('[data-test="tab-manual"]').trigger('click')
        await w.get('[data-test="tab-ai"]').trigger('click')
        expect(checked(w)).toEqual(['codex'])
      })
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
