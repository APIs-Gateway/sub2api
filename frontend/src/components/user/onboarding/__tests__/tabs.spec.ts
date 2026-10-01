/**
 * 四个页签组件单独挂载时的 props / emits 约定。
 * 弹窗整体的行为由 ../../__tests__/KeyOnboardingModal*.spec.ts 覆盖；这里只验证页签自己：
 * 拿到 props 就能渲染，复制只通过 copy 事件交出去，选择类状态走 v-model，面板属性落在根元素上。
 */
import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { ref } from 'vue'

import AiTab from '../AiTab.vue'
import CcSwitchTab from '../CcSwitchTab.vue'
import InstallTab from '../InstallTab.vue'
import { CodeBlock } from '../CodeBlock'
import ManualTab from '../ManualTab.vue'
import type { EndpointOption } from '@/utils/apiEndpoints'

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

const KEY = 'sk-SECRET-1234567890abcdef'
// 配置的地址带 /v1：base（API 根地址）和 configured（原样）不同，用来区分页签取了哪个
const endpoint: EndpointOption = {
  id: 'default',
  base: 'https://api.example.com',
  v1: 'https://api.example.com/v1',
  configured: 'https://api.example.com/v1',
  name: '',
  description: '',
  isDefault: true
}
const panelAttrs = { role: 'tabpanel', id: 'panel-x', 'aria-labelledby': 'tab-x' }

describe('InstallTab', () => {
  const mountTab = (props: Record<string, unknown> = {}) =>
    mount(InstallTab, {
      props: { endpoint, fullKey: KEY, platform: 'anthropic', siteName: 'Hiyo', clients: ['claude', 'opencode'], copiedId: '', ...props } as never,
      attrs: panelAttrs
    })

  it('面板属性落在根元素上；每个客户端一张卡片', () => {
    const w = mountTab()
    expect(w.attributes('role')).toBe('tabpanel')
    expect(w.attributes('id')).toBe('panel-x')
    expect(w.attributes('aria-labelledby')).toBe('tab-x')
    expect(w.attributes('data-test')).toBe('panel-install')
    expect(w.findAll('section.onb-card').map((s) => s.attributes('data-test'))).toEqual(['client-claude', 'client-opencode'])
  })

  it('点瓦片：通过 copy 交出脚本和瓦片 id，不自己写剪贴板', async () => {
    const write = vi.fn()
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: write }, configurable: true })
    const w = mountTab()
    await w.get('[data-test="copy-claude-unix"]').trigger('click')
    const [text, id] = w.emitted('copy')![0] as [string, string]
    expect(id).toBe('claude-unix')
    expect(text).toContain(KEY)
    expect(text).toContain("SUB_ENDPOINT='https://api.example.com'")
    expect(write).not.toHaveBeenCalled()
  })

  it('copiedId 对应的瓦片显示勾，其他不受影响', () => {
    const w = mountTab({ copiedId: 'claude-windows' })
    expect(w.get('[data-test="copy-claude-windows"]').attributes('data-copied')).toBe('true')
    expect(w.find('[data-test="copy-claude-windows"] [data-test="icon-check"]').exists()).toBe(true)
    expect(w.get('[data-test="copy-claude-unix"]').attributes('data-copied')).toBe('false')
  })
})

describe('AiTab', () => {
  const mountTab = (props: Record<string, unknown> = {}) =>
    mount(AiTab, {
      props: {
        endpoint,
        platform: 'openai',
        siteName: 'Hiyo',
        models: ['gpt-5.6-sol', 'gpt-5.6-luna'],
        docUrl: 'https://docs.example.com',
        copiedId: '',
        client: 'claude',
        ...props
      } as never,
      attrs: panelAttrs
    })

  it('选客户端走 v-model:client；提示词随 client 变化，不含密钥', async () => {
    const w = mountTab()
    const text = () => (w.get('[data-test="ai-prompt"]').element as HTMLTextAreaElement).value
    const claude = text()
    expect(claude).toContain('https://api.example.com/v1')
    await w.get('[data-test="ai-client-cursor"]').trigger('click')
    expect(w.emitted('update:client')![0]).toEqual(['cursor'])
    // 外壳把新值传回来之后，提示词跟着变
    await w.setProps({ client: 'cursor' })
    expect(text()).toContain('Cursor')
    expect(text()).not.toBe(claude)
    expect(text()).not.toContain('sk-')
  })

  it('复制简短版和详细版通过 copy 交出，按钮 id 固定', async () => {
    const w = mountTab()
    await w.get('[data-test="ai-copy"]').trigger('click')
    await w.get('[data-test="ai-copy-detail"]').trigger('click')
    const [[short, shortId], [detail, detailId]] = w.emitted('copy') as [string, string][]
    expect([shortId, detailId]).toEqual(['ai-short', 'ai-detail'])
    expect(detail).toContain('gpt-5.6-sol, gpt-5.6-luna')
    expect(detail).toContain('https://docs.example.com')
    expect(detail.length).toBeGreaterThan(short.length)
  })

  it('copiedId 对应的按钮显示「已复制」', () => {
    const w = mountTab({ copiedId: 'ai-short' })
    expect(w.get('[data-test="ai-copy"]').text()).toBe('Copied')
    expect(w.get('[data-test="ai-copy-detail"]').text()).not.toBe('Copied')
  })
})

describe('CcSwitchTab', () => {
  const mountTab = (props: Record<string, unknown> = {}) =>
    mount(CcSwitchTab, {
      props: {
        endpoint,
        fullKey: KEY,
        platform: 'openai',
        siteName: 'Hiyo',
        models: ['gpt-5.6-sol', 'gpt-5.6-luna'],
        clients: ['codex'],
        idPrefix: 'onboarding-9',
        copiedId: '',
        client: 'codex',
        name: '',
        model: '',
        ...props
      } as never,
      attrs: panelAttrs
    })
  const linkOf = (text: string) => new URL(text.replace(/^ccswitch:\/\//, 'http://'))

  it('只有一个客户端时不显示客户端选择；有多个时单选组用 idPrefix 关联标签', () => {
    expect(mountTab().find('[role="radiogroup"]').exists()).toBe(false)
    const w = mountTab({ platform: 'antigravity', clients: ['claude', 'gemini'], client: 'claude' })
    expect(w.get('[role="radiogroup"]').attributes('aria-labelledby')).toBe('onboarding-9-ccs-client')
    expect(w.get('#onboarding-9-ccs-client').text()).toBe('Client')
  })

  it('客户端、名称、模型都走 v-model', async () => {
    const w = mountTab({ platform: 'antigravity', clients: ['claude', 'gemini'], client: 'claude' })
    await w.get('[data-test="ccs-client-gemini"]').trigger('click')
    expect(w.emitted('update:client')![0]).toEqual(['gemini'])
    await w.get('[data-test="ccs-name"]').setValue('Mine')
    expect(w.emitted('update:name')![0]).toEqual(['Mine'])
    await w.get('[data-test="ccs-model"]').setValue('gpt-5.6-luna')
    expect(w.emitted('update:model')![0]).toEqual(['gpt-5.6-luna'])
  })

  it('没有模型时不显示模型字段；默认名称是「站点名 - 客户端」', () => {
    const w = mountTab({ models: [] })
    expect(w.find('[data-test="ccs-model"]').exists()).toBe(false)
    expect(w.get('[data-test="ccs-name"]').attributes('placeholder')).toBe('Hiyo - Codex')
  })

  it('复制链接通过 copy 交出；Codex 用配置的地址（带 /v1），其他客户端用 API 根地址', async () => {
    const codex = mountTab({ name: 'Mine', model: 'gpt-5.6-luna' })
    await codex.get('[data-test="ccs-copy-link"]').trigger('click')
    const [text, id] = codex.emitted('copy')![0] as [string, string]
    expect(id).toBe('deeplink')
    const url = linkOf(text)
    expect(url.searchParams.get('app')).toBe('codex')
    expect(url.searchParams.get('name')).toBe('Mine')
    expect(url.searchParams.get('model')).toBe('gpt-5.6-luna')
    expect(url.searchParams.get('apiKey')).toBe(KEY)
    expect(url.searchParams.get('endpoint')).toBe('https://api.example.com/v1')

    const claude = mountTab({ platform: 'anthropic', clients: ['claude'], client: 'claude' })
    await claude.get('[data-test="ccs-copy-link"]').trigger('click')
    expect(linkOf((claude.emitted('copy')![0] as [string])[0]).searchParams.get('endpoint')).toBe('https://api.example.com')
  })

  it('打开 CC Switch：用 window.open(链接, _self)', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const w = mountTab()
    await w.get('[data-test="ccs-open"]').trigger('click')
    expect(open).toHaveBeenCalledTimes(1)
    expect(String(open.mock.calls[0][0]).startsWith('ccswitch://')).toBe(true)
    expect(open.mock.calls[0][1]).toBe('_self')
    open.mockRestore()
  })
})

describe('ManualTab', () => {
  const mountTab = (props: Record<string, unknown> = {}) =>
    mount(ManualTab, {
      props: {
        endpoint,
        fullKey: KEY,
        maskedKey: 'sk-SEC…cdef',
        platform: 'anthropic',
        siteName: 'Hiyo',
        clients: ['claude', 'opencode'],
        docUrl: '',
        copiedId: '',
        ...props
      } as never,
      attrs: panelAttrs
    })

  it('表格显示掩码，复制的是完整密钥；地址行复制 API 根地址和 /v1 地址', async () => {
    const w = mountTab()
    const table = w.get('table')
    expect(table.text()).toContain('sk-SEC…cdef')
    expect(table.text()).not.toContain(KEY)
    const buttons = table.findAll('button')
    for (const b of buttons) await b.trigger('click')
    expect(w.emitted('copy')).toEqual([
      ['https://api.example.com', 'm-base'],
      ['https://api.example.com/v1', 'm-v1'],
      [KEY, 'm-key']
    ])
  })

  it('每个客户端一段配置片段；代码块的复制按钮也走 copy', async () => {
    const w = mountTab()
    const titles = w.findAll('details > summary').map((s) => s.text())
    // OpenCode 没有手动配置片段，只有 Claude Code 一段
    expect(titles).toEqual(['Claude Code'])
    await w.findAllComponents(CodeBlock)[0].get('button').trigger('click')
    const [text, id] = w.emitted('copy')![0] as [string, string]
    expect(id.startsWith('claude-')).toBe(true)
    expect(text).toContain("export ANTHROPIC_BASE_URL='https://api.example.com'")
    expect(text).toContain(KEY)
  })

  it('有文档地址才显示「查看文档」链接', () => {
    expect(mountTab().find('a[target="_blank"]').exists()).toBe(false)
    const w = mountTab({ docUrl: 'https://docs.example.com' })
    expect(w.get('a[target="_blank"]').attributes('href')).toBe('https://docs.example.com')
  })
})
