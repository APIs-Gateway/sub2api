/**
 * 接入弹窗的特征快照（characterization test）。
 *
 * 拆分页签组件（PR0）之前先写下来，拆分前后必须原样通过：它记录四个页签的 DOM、复制出去的内容、
 * 生成的链接、页签之间的状态保留。后续 PR 有意改动某个页签的输出时，更新对应的快照即可。
 *
 * 约定：
 * - 不依赖实现细节：只通过 data-test、角色、可见文字和剪贴板 / window.open 观察；
 * - DOM 用自带的序列化器输出：属性按名字排序，忽略 scoped 样式的 data-v-* 和模板里的注释，id 里的实例序号
 *   归一化（关联关系仍然保留），表单控件的当前值和选中状态也写进去；
 * - 一键安装的脚本很长，快照里只记长度、开头和 SHA-1；脚本本身由下面的 buildInstallScript 对照断言覆盖。
 */
import { createHash } from 'node:crypto'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { ref } from 'vue'

import KeyOnboardingModal from '../KeyOnboardingModal.vue'
import { CC_SWITCH_USAGE_SCRIPT } from '@/utils/ccswitchImport'
import { CLIENT_LABELS, CODEX_MIN_NODE_MAJOR, buildInstallScript, type OnboardingClient } from '@/utils/keyOnboarding'

const { getAvailable, showError, i18nState } = vi.hoisted(() => ({
  getAvailable: vi.fn(),
  showError: vi.fn(),
  i18nState: { lang: 'en' as 'en' | 'zh-CN' }
}))
vi.mock('@/api/channels', () => ({ userChannelsAPI: { getAvailable } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError }) }))

// 测试环境的 vue-i18n 是 runtime-only 构建，不能编译消息；这里直接用文案表做插值
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

const SECRET = 'sk-SECRET-1234567890abcdef'
const TABS = ['install', 'ai', 'ccswitch', 'manual'] as const
type Tab = (typeof TABS)[number]

const channels = [
  {
    name: 'ch',
    description: '',
    platforms: [
      { platform: 'openai', groups: [{ id: 7, name: 'g7' }], supported_models: [{ name: 'gpt-5.6-sol' }, { name: 'gpt-5.6-luna' }] },
      { platform: 'anthropic', groups: [{ id: 8, name: 'g8' }], supported_models: [{ name: 'claude-opus-5' }, { name: 'claude-sonnet-5' }] },
      { platform: 'gemini', groups: [{ id: 9, name: 'g9' }], supported_models: [{ name: 'gemini-3-pro' }] },
      {
        platform: 'antigravity',
        groups: [{ id: 10, name: 'g10' }],
        supported_models: [{ name: 'claude-opus-5' }, { name: 'gemini-3-pro' }]
      },
      // 同一个渠道里别的分组的模型不应出现
      { platform: 'openai', groups: [{ id: 99, name: 'other' }], supported_models: [{ name: 'not-mine' }] }
    ]
  }
]

const keyOf = (id: number | null, platform: string | null, extra: Record<string, unknown> = {}) => ({
  key: SECRET,
  name: 'my-key',
  group_id: id,
  group: id === null ? null : { id, platform, ...extra }
})

const CDN = 'https://cdn.example.com'
const CDN_ENDPOINTS = [{ name: 'CDN 加速', endpoint: `${CDN}/v1/`, description: '国内访问更快' }]

interface Scenario {
  name: string
  props: Record<string, unknown>
  lang?: 'en' | 'zh-CN'
  /** 预先写进 localStorage 的线路选择（和使用文档页共用） */
  endpointChoice?: string
  /** 每个页签都记录整个弹窗的 DOM（含页签栏、线路选择）；否则只记录当前内容区，避免快照里重复外壳 */
  fullDom?: boolean
}

const BASE = { baseUrl: 'https://codex.hiyo.top/', siteName: 'Hiyo', docUrl: 'https://docs.example.com' }

const SCENARIOS: Scenario[] = [
  { name: 'openai', fullDom: true, props: { ...BASE, apiKey: keyOf(7, 'openai') } },
  {
    // 配置的地址带 /v1、有备用线路并选中它；允许 /v1/messages 调度，所以 Codex 分组也有 Claude Code
    name: 'openai 备用线路 + 调度',
    fullDom: true,
    props: { baseUrl: 'https://api.example.com/v1', siteName: 'Hiyo', docUrl: '', customEndpoints: CDN_ENDPOINTS, apiKey: keyOf(7, 'openai', { allow_messages_dispatch: true }) },
    endpointChoice: CDN
  },
  { name: 'anthropic', fullDom: true, props: { ...BASE, apiKey: keyOf(8, 'anthropic') } },
  { name: 'anthropic 中文界面', lang: 'zh-CN', props: { ...BASE, apiKey: keyOf(8, 'anthropic') } },
  { name: 'gemini', props: { ...BASE, siteName: '', apiKey: keyOf(9, 'gemini') } },
  { name: 'antigravity', props: { ...BASE, apiKey: keyOf(10, 'antigravity') } },
  { name: '没有分组', props: { ...BASE, apiKey: keyOf(null, null) } },
  { name: '未知平台', props: { ...BASE, apiKey: keyOf(3, 'unknown') } }
]

const clipboard = { writeText: vi.fn().mockResolvedValue(undefined) }

async function mountModal(props: Record<string, unknown>, attachTo?: HTMLElement) {
  const w = mount(KeyOnboardingModal, {
    props: { show: true, ...props } as never,
    attachTo,
    global: { stubs: { BaseDialog: { props: ['show', 'title'], template: '<div v-if="show"><slot /></div>' } } }
  })
  await flushPromises()
  return w
}

// ---------------------------------------------------------------- 序列化

const sha1 = (s: string) => createHash('sha1').update(s).digest('hex').slice(0, 12)
/** 很长的文本（脚本、详细版提示词）：长度 + 开头 + 摘要 */
const brief = (s: string) => `<${s.length} 字符 sha1:${sha1(s)} 开头:${JSON.stringify(s.slice(0, 60))}>`

function serialize(node: Node, depth = 0, digestPre = false): string {
  const pad = ' '.repeat(depth)
  if (node.nodeType === Node.TEXT_NODE) {
    const text = (node.textContent || '').replace(/\s+/g, ' ').trim()
    return text ? `${pad}"${text}"\n` : ''
  }
  // 模板里写的注释不记（改注释不该让快照失败）；只留 Vue 的 v-if 占位，它说明某个分支此刻没有渲染
  if (node.nodeType === Node.COMMENT_NODE) return node.textContent === 'v-if' ? `${pad}<!--v-if-->\n` : ''
  if (node.nodeType !== Node.ELEMENT_NODE) return ''
  const el = node as HTMLElement
  const tag = el.tagName.toLowerCase()
  const attrs: string[] = []
  for (const a of Array.from(el.attributes)) {
    if (/^data-v-[0-9a-f]+$/.test(a.name)) continue
    attrs.push(`${a.name}="${a.value.replace(/onboarding-\d+/g, 'onboarding-N')}"`)
  }
  attrs.sort()
  // 属性里看不到的当前状态
  if (el instanceof HTMLInputElement) {
    if (el.type === 'radio' || el.type === 'checkbox') attrs.push(`:checked=${el.checked}`)
    else attrs.push(`:value=${JSON.stringify(el.value)}`)
  }
  if (el instanceof HTMLTextAreaElement) attrs.push(`:value=${JSON.stringify(el.value)}`)
  if (el instanceof HTMLSelectElement) attrs.push(`:value=${JSON.stringify(el.value)}`)
  const head = `${pad}<${tag}${attrs.length ? ' ' + attrs.join(' ') : ''}>\n`
  // 图标的内容来自 Icon 组件本身，只留摘要
  if (tag === 'svg') return `${head}${pad} <图标 sha1:${sha1(el.innerHTML)}>\n`
  if (tag === 'pre') {
    const code = el.textContent || ''
    return `${head}${pad} ${digestPre ? brief(code) : JSON.stringify(code)}\n`
  }
  const inScript = digestPre || (tag === 'details' && (el.getAttribute('data-test') || '').startsWith('script-'))
  let out = head
  for (const child of Array.from(el.childNodes)) out += serialize(child, depth + 1, inScript)
  return out
}

const dom = (w: VueWrapper) => serialize(w.element.parentElement ?? w.element)
/** 复制之后只看会变的部分：播报区和当前内容区 */
const domAfterCopy = (w: VueWrapper, tab: Tab) =>
  [w.find('[data-test="copy-status"]'), w.find(`[data-test="panel-${tab}"]`)].map((x) => serialize(x.element)).join('')
// 页签内的复制内容：脚本很长，只留摘要；其他原样
const payload = (s: string) => (s.length > 1200 ? brief(s) : JSON.stringify(s))

// ---------------------------------------------------------------- 逐个页签收集

const lastCopied = () => {
  const calls = clipboard.writeText.mock.calls
  return String(calls[calls.length - 1]?.[0])
}

async function click(w: VueWrapper, selector: string) {
  await w.get(selector).trigger('click')
  await flushPromises()
}

async function gotoTab(w: VueWrapper, tab: Tab) {
  if (w.find(`[data-test="tab-${tab}"]`).attributes('aria-selected') !== 'true') await click(w, `[data-test="tab-${tab}"]`)
}

async function collect(w: VueWrapper, tab: Tab, fullDom = false): Promise<string> {
  await gotoTab(w, tab)
  const sections: string[] = []
  const panel = () => w.find(`[data-test="panel-${tab}"]`)
  // 没有分组时当前页签没有 panel-*，内容区是 no-group 提示
  const content = () => (panel().exists() ? panel() : w.find('[data-test="no-group"]'))
  sections.push(`### DOM\n${fullDom ? dom(w) : serialize(content().element)}`)
  const out: string[] = []
  if (!panel().exists()) return sections.join('\n')

  if (tab === 'install') {
    const tiles = w.findAll('button.onb-tile')
    for (const [i, tile] of tiles.entries()) {
      const id = tile.attributes('data-test')
      clipboard.writeText.mockClear()
      await tile.trigger('click')
      await flushPromises()
      out.push(`${id} -> ${payload(lastCopied())}`)
      if (i === 0 && fullDom) sections.push(`### 复制第一块瓦片后的播报区与内容区\n${domAfterCopy(w, tab)}`)
    }
  }

  if (tab === 'ai') {
    const chips = w.findAll('[data-test^="ai-client-"]')
    const area = () => (w.get('[data-test="ai-prompt"]').element as HTMLTextAreaElement).value
    for (const chip of chips) {
      await chip.trigger('click')
      await flushPromises()
      out.push(`${chip.attributes('data-test')}: 简短版 = ${JSON.stringify(area())}`)
      clipboard.writeText.mockClear()
      await click(w, '[data-test="ai-copy"]')
      out.push(`  复制简短版 = ${JSON.stringify(lastCopied())}`)
      await click(w, '[data-test="ai-copy-detail"]')
      out.push(`  复制详细版 = ${brief(lastCopied())}`)
      const open = vi.spyOn(window, 'open').mockReturnValue(null)
      await click(w, '[data-test="ai-open-chatgpt"]')
      await click(w, '[data-test="ai-open-claude"]')
      // 链接里带的是简短版提示词的转义，只记前缀和摘要
      const links = open.mock.calls.map((c) => {
        const [prefix, q] = String(c[0]).split('?q=')
        return `${prefix}?q=${brief(decodeURIComponent(q))} ${JSON.stringify(c.slice(1))}`
      })
      out.push(`  打开 = ${JSON.stringify(links)}`)
      open.mockRestore()
    }
    // 详细版全文只在一个客户端上完整记录，方便看内容
    if (chips.length > 0) {
      await chips[0].trigger('click')
      await flushPromises()
      await click(w, '[data-test="ai-copy-detail"]')
      out.push(`第一个客户端的详细版全文:\n${lastCopied()}`)
    }
  }

  if (tab === 'ccswitch') {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const chips = w.findAll('[data-test^="ccs-client-"]')
    const targets = chips.length > 0 ? chips.map((c) => c.attributes('data-test')!) : [null]
    const readLink = (url: string) => {
      const u = new URL(url.replace(/^ccswitch:\/\//, 'http://'))
      const params: Record<string, string> = {}
      u.searchParams.forEach((v, k) => (params[k] = v.length > 400 ? brief(v) : v))
      return JSON.stringify({ host: u.host, path: u.pathname, params })
    }
    // 每个模型下拉：当前值、选项、是否禁用；以及模型下面的提示、两个导入按钮是否禁用
    const readModels = () => [
      ...w.findAll('select[data-test^="ccs-model"]').map((sel) => {
        const disabled = sel.attributes('disabled') !== undefined ? '（禁用）' : ''
        return `  ${sel.attributes('data-test')} = ${JSON.stringify((sel.element as HTMLSelectElement).value)}${disabled} 选项 = ${JSON.stringify(sel.findAll('option').map((o) => o.text()))}`
      }),
      `  模型提示 = ${JSON.stringify(w.find('[data-test="ccs-models-hint"]').exists() ? w.get('[data-test="ccs-models-hint"]').text() : null)}`,
      `  按钮禁用 = ${JSON.stringify(['ccs-open', 'ccs-copy-link'].map((t) => w.get(`[data-test="${t}"]`).attributes('disabled') !== undefined))}`
    ]
    for (const target of targets) {
      if (target) await click(w, `[data-test="${target}"]`)
      out.push(`${target ?? '(单一客户端)'}: placeholder = ${w.get('[data-test="ccs-name"]').attributes('placeholder')}`)
      out.push(...readModels())
      await click(w, '[data-test="ccs-copy-link"]')
      out.push(`  复制链接 = ${readLink(lastCopied())}`)
      open.mockClear()
      await click(w, '[data-test="ccs-open"]')
      out.push(`  打开 = ${JSON.stringify(open.mock.calls.map((c) => c[1]))} ${readLink(String(open.mock.calls[0]?.[0]))}`)
      // 改名，并把主模型换成下拉里的第一个具体模型
      await w.get('[data-test="ccs-name"]').setValue('Mine')
      const mainOptions = w.get('[data-test="ccs-model"]').findAll('option').map((o) => o.text())
      if (mainOptions.length > 1) await w.get('[data-test="ccs-model"]').setValue(mainOptions[1])
      await click(w, '[data-test="ccs-copy-link"]')
      out.push(`  改名并换主模型后 = ${readLink(lastCopied())}`)
    }
    open.mockRestore()
  }

  if (tab === 'manual') {
    // 先点一遍地址卡片、密钥行、配置片段里的复制按钮，再把每个代码页签依次点一遍、复制其中的代码块
    const copyButtons = (inCode: boolean) =>
      panel()
        .findAll('button:not([role="radio"])')
        .filter((b) => !!b.element.closest('[data-test="manual-code"]') === inCode)
    const labelOf = (b: ReturnType<typeof copyButtons>[number]) =>
      b.attributes('data-test') ??
      b.element.closest('details')?.querySelector('summary')?.textContent?.trim() ??
      b.element.closest('[data-test^="manual-code-"]')?.getAttribute('data-test') ??
      '?'
    let first = true
    const press = async (buttons: ReturnType<typeof copyButtons>) => {
      for (const b of buttons) {
        clipboard.writeText.mockClear()
        await b.trigger('click')
        await flushPromises()
        out.push(`按钮 ${labelOf(b)} -> ${payload(lastCopied())}`)
        if (first && fullDom) sections.push(`### 复制第一个按钮后的播报区与内容区\n${domAfterCopy(w, tab)}`)
        first = false
      }
    }
    await press(copyButtons(false))
    const tabs = w.findAll('[data-test^="manual-tab-"]').map((c) => c.attributes('data-test')!)
    out.push(`代码页签 = ${JSON.stringify(tabs.map((t) => t.replace('manual-tab-', '')))}`)
    for (const target of tabs) {
      await click(w, `[data-test="${target}"]`)
      out.push(`## ${target}`)
      await press(copyButtons(true))
    }
  }

  sections.push(`### 复制与链接\n${out.join('\n')}`)
  return sections.join('\n')
}

// ---------------------------------------------------------------- 测试

beforeEach(() => {
  localStorage.clear()
  getAvailable.mockReset()
  getAvailable.mockResolvedValue(channels)
  showError.mockReset()
  i18nState.lang = 'en'
  clipboard.writeText.mockReset()
  clipboard.writeText.mockResolvedValue(undefined)
  Object.defineProperty(navigator, 'clipboard', { value: clipboard, configurable: true })
})
afterEach(() => localStorage.clear())

describe('KeyOnboardingModal 页签快照', () => {
  describe.each(SCENARIOS)('$name', (scenario) => {
    it.each(TABS)('%s 页签', async (tab) => {
      i18nState.lang = scenario.lang ?? 'en'
      if (scenario.endpointChoice) localStorage.setItem('docs_api_endpoint', scenario.endpointChoice)
      const w = await mountModal(scenario.props)
      expect(await collect(w, tab, scenario.fullDom)).toMatchSnapshot()
      w.unmount()
    })
  })

  it('没有密钥时弹窗内容为空', async () => {
    const w = await mountModal({ ...BASE, apiKey: null })
    expect(dom(w)).toMatchSnapshot()
  })

  it('取不到模型列表：CC Switch 的模型下拉只剩留空一项并给出提示，交给 AI 的详细版没有模型', async () => {
    getAvailable.mockRejectedValue(new Error('x'))
    const w = await mountModal({ ...BASE, apiKey: keyOf(7, 'openai') })
    expect(await collect(w, 'ccswitch')).toMatchSnapshot()
    expect(await collect(w, 'ai')).toMatchSnapshot()
  })
})

describe('一键安装复制出去的脚本，与 buildInstallScript 对照', () => {
  // 页签里的瓦片 id → 脚本参数
  const tileArgs = (id: string): { client: OnboardingClient; os: 'unix' | 'windows'; mode?: 'full' | 'refresh' } => {
    const m = id.match(/^(claude|codex|gemini|opencode)(?:-(full|refresh))?-(unix|windows)$/)!
    return { client: m[1] as OnboardingClient, os: m[3] as 'unix' | 'windows', mode: m[2] as 'full' | 'refresh' | undefined }
  }

  it.each([
    ['openai', keyOf(7, 'openai'), 'en'],
    ['openai', keyOf(7, 'openai', { allow_messages_dispatch: true }), 'zh-CN'],
    ['anthropic', keyOf(8, 'anthropic'), 'en'],
    ['gemini', keyOf(9, 'gemini'), 'zh-CN'],
    ['antigravity', keyOf(10, 'antigravity'), 'en']
  ] as const)('%s（%#，%s）：每块瓦片复制的就是对应参数生成的脚本', async (platform, apiKey, lang) => {
    i18nState.lang = lang
    const w = await mountModal({ ...BASE, apiKey })
    // 终端提示的期望值：从同一份文案表取，不经过页签代码
    const { default: en } = await import('@/i18n/locales/en')
    const { default: zh } = await import('@/i18n/locales/zh-CN')
    const dict = (lang === 'zh-CN' ? zh : en) as unknown as { keyOnboarding: { install: { script: Record<string, string>; scriptDone: string } } }
    const s = dict.keyOnboarding.install.script
    const fill = (text: string, params: Record<string, string | number>) =>
      text.replace(/\{(\w+)\}/g, (_, k) => String(params[k] ?? ''))
    const tiles = w.findAll('button.onb-tile')
    expect(tiles.length).toBeGreaterThan(0)
    for (const tile of tiles) {
      const { client, os, mode } = tileArgs(tile.attributes('data-test')!.replace(/^copy-/, ''))
      clipboard.writeText.mockClear()
      await tile.trigger('click')
      await flushPromises()
      const expected = buildInstallScript(client, os, {
        baseUrl: 'https://codex.hiyo.top',
        apiKey: SECRET,
        platform,
        siteName: 'Hiyo',
        mode,
        doneMessage: fill(dict.keyOnboarding.install.scriptDone, { client: CLIENT_LABELS[client] }),
        messages: {
          pythonMissing: s.pythonMissing,
          xcodeMissing: s.xcodeMissing,
          backup: fill(s.backup, { path: '{path}' }),
          updated: fill(s.updated, { path: '{path}' }),
          failed: fill(s.failed, { error: '{error}' }),
          nodeMissing: fill(s.nodeMissing, { min: CODEX_MIN_NODE_MAJOR }),
          nodeTooOld: fill(s.nodeTooOld, { version: '{version}', min: CODEX_MIN_NODE_MAJOR }),
          npmMissing: s.npmMissing,
          npmInstalling: s.npmInstalling,
          npmPermission: s.npmPermission,
          npmFailed: s.npmFailed
        }
      })
      expect(lastCopied(), tile.attributes('data-test')).toBe(expected)
    }
    w.unmount()
  })

  it('折叠的脚本预览与瓦片复制的是同一份', async () => {
    const w = await mountModal({ ...BASE, apiKey: keyOf(7, 'openai') })
    const shown = w.findAll('[data-test="script-codex"] pre').map((p) => p.text())
    const copied: string[] = []
    for (const id of ['codex-full-unix', 'codex-full-windows', 'codex-refresh-unix', 'codex-refresh-windows']) {
      await click(w, `[data-test="copy-${id}"]`)
      copied.push(lastCopied())
    }
    expect(shown).toEqual(copied)
    w.unmount()
  })
})

describe('CC Switch 深链的用量脚本', () => {
  it('链接里带的 usageScript 就是 CC_SWITCH_USAGE_SCRIPT', async () => {
    const w = await mountModal({ ...BASE, apiKey: keyOf(8, 'anthropic') })
    await gotoTab(w, 'ccswitch')
    await click(w, '[data-test="ccs-copy-link"]')
    const u = new URL(lastCopied().replace(/^ccswitch:\/\//, 'http://'))
    expect(Buffer.from(u.searchParams.get('usageScript')!, 'base64').toString('utf8')).toBe(CC_SWITCH_USAGE_SCRIPT)
    w.unmount()
  })
})

describe('状态在页签之间的保留（拆分页签时最容易丢）', () => {
  const aiText = (w: VueWrapper) => (w.get('[data-test="ai-prompt"]').element as HTMLTextAreaElement).value
  const isChecked = (w: VueWrapper, sel: string) => w.get(sel).attributes('aria-checked')

  it('交给 AI 选的客户端：切到别的页签再回来、关闭再打开，都还在', async () => {
    const w = await mountModal({ ...BASE, apiKey: keyOf(7, 'openai'), initialTab: 'ai' })
    await click(w, '[data-test="ai-client-cursor"]')
    const cursor = aiText(w)
    expect(isChecked(w, '[data-test="ai-client-cursor"]')).toBe('true')
    await gotoTab(w, 'ccswitch')
    await gotoTab(w, 'ai')
    expect(aiText(w)).toBe(cursor)
    expect(isChecked(w, '[data-test="ai-client-cursor"]')).toBe('true')
    await w.setProps({ show: false })
    await w.setProps({ show: true })
    expect(w.find('[data-test="panel-ai"]').exists()).toBe(true)
    expect(aiText(w)).toBe(cursor)
    w.unmount()
  })

  it('CC Switch 的名称和模型：切页签保留；关闭再打开名称清空、模型重新预选；客户端选择保留', async () => {
    const w = await mountModal({ ...BASE, apiKey: keyOf(10, 'antigravity'), initialTab: 'ccswitch' })
    await click(w, '[data-test="ccs-client-gemini"]')
    await w.get('[data-test="ccs-name"]').setValue('Mine')
    await w.get('[data-test="ccs-model"]').setValue('')
    const value = (sel: string) => (w.get(sel).element as HTMLInputElement).value

    await gotoTab(w, 'manual')
    await gotoTab(w, 'ccswitch')
    expect(value('[data-test="ccs-name"]')).toBe('Mine')
    // 切页签不会重新预选：手动清掉的主模型还是空的
    expect(value('[data-test="ccs-model"]')).toBe('')
    expect(isChecked(w, '[data-test="ccs-client-gemini"]')).toBe('true')

    await w.setProps({ show: false })
    await w.setProps({ show: true })
    expect(w.find('[data-test="panel-ccswitch"]').exists()).toBe(true)
    expect(value('[data-test="ccs-name"]')).toBe('')
    expect(value('[data-test="ccs-model"]')).toBe('gemini-3-pro')
    expect(isChecked(w, '[data-test="ccs-client-gemini"]')).toBe('true')
    w.unmount()
  })

  it('换成别的分组：名称和模型清空；平台变了客户端回到该平台的第一个', async () => {
    const w = await mountModal({ ...BASE, apiKey: keyOf(10, 'antigravity'), initialTab: 'ccswitch' })
    await click(w, '[data-test="ccs-client-gemini"]')
    await w.get('[data-test="ccs-name"]').setValue('Mine')
    await w.setProps({ apiKey: keyOf(8, 'anthropic') })
    await flushPromises()
    expect((w.get('[data-test="ccs-name"]').element as HTMLInputElement).value).toBe('')
    // anthropic 只有 Claude，没有客户端选择
    expect(w.find('[data-test="ccs-client-gemini"]').exists()).toBe(false)
    expect(w.get('[data-test="ccs-name"]').attributes('placeholder')).toBe('Hiyo - Claude')
    w.unmount()
  })

  it('复制反馈在重新打开时清掉', async () => {
    const w = await mountModal({ ...BASE, apiKey: keyOf(7, 'openai') })
    await click(w, '[data-test="copy-codex-refresh-unix"]')
    expect(w.get('[data-test="copy-status"]').text()).toBe('Copied')
    await w.setProps({ show: false })
    await w.setProps({ show: true })
    expect(w.get('[data-test="copy-status"]').text()).toBe('')
    expect(w.get('[data-test="copy-codex-refresh-unix"]').attributes('data-copied')).toBe('false')
    w.unmount()
  })

  it('「已复制」显示 1.8 秒；期间再复制别的按钮，从那一刻重新计时', async () => {
    vi.useFakeTimers()
    try {
      const w = await mountModal({ ...BASE, apiKey: keyOf(7, 'openai') })
      const status = () => w.get('[data-test="copy-status"]').text()
      const copied = (id: string) => w.get(`[data-test="copy-${id}"]`).attributes('data-copied')
      await click(w, '[data-test="copy-codex-refresh-unix"]')
      await vi.advanceTimersByTimeAsync(1700)
      expect(status()).toBe('Copied')
      expect(copied('codex-refresh-unix')).toBe('true')
      await vi.advanceTimersByTimeAsync(200)
      expect(status()).toBe('')
      expect(copied('codex-refresh-unix')).toBe('false')

      await click(w, '[data-test="copy-codex-refresh-unix"]')
      await vi.advanceTimersByTimeAsync(1000)
      await click(w, '[data-test="copy-codex-full-unix"]')
      // 第一次复制的计时已被取消：到 1800ms 时第二个按钮还在
      await vi.advanceTimersByTimeAsync(1000)
      expect(copied('codex-refresh-unix')).toBe('false')
      expect(copied('codex-full-unix')).toBe('true')
      expect(status()).toBe('Copied')
      await vi.advanceTimersByTimeAsync(900)
      expect(copied('codex-full-unix')).toBe('false')
      expect(status()).toBe('')
      w.unmount()
    } finally {
      vi.useRealTimers()
    }
  })

  it('模型列表只请求一次：重新打开、切页签都不会再请求；失败后也不重试', async () => {
    const w = await mountModal({ ...BASE, apiKey: keyOf(7, 'openai') })
    await gotoTab(w, 'ai')
    await gotoTab(w, 'ccswitch')
    await w.setProps({ show: false })
    await w.setProps({ show: true })
    expect(getAvailable).toHaveBeenCalledTimes(1)
    w.unmount()

    getAvailable.mockReset()
    getAvailable.mockRejectedValue(new Error('x'))
    const w2 = await mountModal({ ...BASE, apiKey: keyOf(7, 'openai') })
    await w2.setProps({ show: false })
    await w2.setProps({ show: true })
    expect(getAvailable).toHaveBeenCalledTimes(1)
    w2.unmount()
  })

  it('弹窗没打开时不请求模型列表', async () => {
    const w = await mountModal({ ...BASE, apiKey: keyOf(7, 'openai'), show: false })
    expect(getAvailable).not.toHaveBeenCalled()
    await w.setProps({ show: true })
    expect(getAvailable).toHaveBeenCalledTimes(1)
    w.unmount()
  })
})

describe('页签键盘导航与 aria 的逐步快照', () => {
  let host: HTMLElement
  beforeEach(() => {
    host = document.createElement('div')
    document.body.appendChild(host)
  })
  afterEach(() => host.remove())

  it('按键序列：每一步的选中页签、tabindex、焦点、内容区 id 与 aria 关联', async () => {
    const w = await mountModal({ ...BASE, apiKey: keyOf(7, 'openai') }, host)
    const steps: string[] = []
    const state = (label: string) => {
      const tabs = w.findAll('[role="tab"]')
      const sel = tabs.find((t) => t.attributes('aria-selected') === 'true')!
      const panel = w.find('[role="tabpanel"]')
      const norm = (v: string | undefined) => (v ?? '').replace(/onboarding-\d+/g, 'onboarding-N')
      steps.push(
        [
          label,
          `选中=${sel.attributes('data-test')}`,
          `tabindex=${tabs.map((t) => t.attributes('tabindex')).join(',')}`,
          `焦点=${(document.activeElement as HTMLElement | null)?.getAttribute('data-test')}`,
          `面板=${panel.attributes('data-test')} id=${norm(panel.attributes('id'))} labelledby=${norm(panel.attributes('aria-labelledby'))}`,
          `controls=${norm(sel.attributes('aria-controls'))} tabId=${norm(sel.attributes('id'))}`
        ].join(' | ')
      )
    }
    state('初始')
    for (const [from, key] of [
      ['install', 'ArrowRight'],
      ['ai', 'ArrowDown'],
      ['ccswitch', 'End'],
      ['manual', 'ArrowRight'],
      ['install', 'ArrowLeft'],
      ['manual', 'Home'],
      ['install', 'ArrowUp']
    ] as const) {
      await w.get(`[data-test="tab-${from}"]`).trigger('keydown', { key })
      await flushPromises()
      state(`${from} 上按 ${key}`)
    }
    // 其他按键不拦截
    const ev = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true })
    w.get('[data-test="tab-manual"]').element.dispatchEvent(ev)
    steps.push(`Tab 键 defaultPrevented=${ev.defaultPrevented}`)
    expect(steps.join('\n')).toMatchSnapshot()
    w.unmount()
  })
})
