import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { createPinia, setActivePinia } from 'pinia'
import { ref } from 'vue'

import zhCN from '@/i18n/locales/zh-CN'
import DocsView from '../DocsView.vue'
import DocsAiPrompts from '../DocsAiPrompts.vue'
import { DOC_GROUPS } from '../sections'
import aiPromptsRaw from '../ai-prompts.md?raw'
import { parseAiPrompts, renderSection, resolveApiBases, type DocVars } from '../docsRender'

const { getPublicSettings, currentLocale } = vi.hoisted(() => ({
  getPublicSettings: vi.fn(),
  currentLocale: { value: 'zh-CN' },
}))

vi.mock('@/api/auth', () => ({ getPublicSettings }))

vi.mock('@/components/common/LocaleSwitcher.vue', () => ({ default: { template: '<span />' } }))

vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({
    locale: ref(currentLocale.value),
    t: (key: string, params?: Record<string, string>) => {
      const value = key.split('.').reduce<unknown>((node, part) => (node as Record<string, unknown> | undefined)?.[part], zhCN)
      if (typeof value !== 'string') return key
      return value.replace(/\{(\w+)\}/g, (_, name: string) => params?.[name] ?? '')
    },
  }),
}))

const RouterLinkStub = {
  props: ['to'],
  template: '<a :href="typeof to === \'string\' ? to : to.path"><slot /></a>',
}

const pushMock = vi.fn()
vi.mock('vue-router', () => ({
  RouterLink: { props: ['to'], template: '<a :href="typeof to === \'string\' ? to : to.path"><slot /></a>' },
  useRoute: () => ({ hash: '' }),
  useRouter: () => ({ push: pushMock }),
}))

function settingsWith(apiBaseUrl: string) {
  return {
    site_name: 'Hiyo',
    site_logo: '',
    api_base_url: apiBaseUrl,
    login_agreement_documents: [],
  }
}

async function mountDocs(apiBaseUrl: string): Promise<VueWrapper> {
  getPublicSettings.mockResolvedValue(settingsWith(apiBaseUrl))
  const wrapper = mount(DocsView, {
    attachTo: document.body,
    global: {
      stubs: { RouterLink: RouterLinkStub, LocaleSwitcher: true, Icon: true },
    },
  })
  await flushPromises()
  return wrapper
}

const allSectionIds = DOC_GROUPS.flatMap((g) => g.sections.map((s) => s.id))

describe('DocsView', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    currentLocale.value = 'zh-CN'
    pushMock.mockReset()
    Element.prototype.scrollIntoView = vi.fn()
    delete (window as { __APP_CONFIG__?: unknown }).__APP_CONFIG__
  })

  afterEach(() => {
    document.body.innerHTML = ''
    vi.restoreAllMocks()
  })

  describe('examples follow api_base_url', () => {
    it('fills the configured address into the connect strip and every section', async () => {
      const wrapper = await mountDocs('https://api.first.test')

      expect(wrapper.get('[data-testid="docs-connect-openai"]').text()).toBe('https://api.first.test/v1')
      expect(wrapper.get('[data-testid="docs-connect-anthropic"]').text()).toBe('https://api.first.test')

      const article = wrapper.get('[data-testid="docs-article"]').html()
      expect(article).toContain('https://api.first.test/v1')
      expect(article).not.toContain('{{')
      expect(article).not.toContain('hiyo.top')
      wrapper.unmount()
    })

    it('switches to the new address when the setting changes, and strips a trailing /v1', async () => {
      const wrapper = await mountDocs('https://api.second.test/v1/')

      expect(wrapper.get('[data-testid="docs-connect-openai"]').text()).toBe('https://api.second.test/v1')
      expect(wrapper.get('[data-testid="docs-connect-anthropic"]').text()).toBe('https://api.second.test')
      expect(wrapper.get('[data-testid="docs-article"]').html()).not.toContain('api.first.test')
      wrapper.unmount()
    })

    it('uses the site name from public settings', async () => {
      const wrapper = await mountDocs('https://api.first.test')
      expect(wrapper.text()).toContain('Hiyo')
      wrapper.unmount()
    })

    it('falls back to the current origin when api_base_url is empty', () => {
      expect(resolveApiBases('', 'https://site.test')).toEqual({ base: 'https://site.test', v1: 'https://site.test/v1' })
    })

    it('has no hard-coded site domain in any doc source', () => {
      const sources = [
        ...DOC_GROUPS.flatMap((g) => g.sections.map((s) => s.raw)),
        aiPromptsRaw,
        readFileSync(resolve(__dirname, '../../../../public/llms.txt'), 'utf8'),
      ]
      const allowedHosts = ['github.com', 'nodejs.org', 'cherry-ai.com']
      for (const source of sources) {
        expect(source).not.toMatch(/hiyo\.top/)
        for (const match of source.matchAll(/https?:\/\/([^/\s)`"']+)/g)) {
          expect(allowedHosts).toContain(match[1])
        }
      }
    })
  })

  describe('copy buttons', () => {
    it('copies the code text and shows the copied state', async () => {
      const writeText = vi.fn().mockResolvedValue(undefined)
      Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
      Object.defineProperty(window, 'isSecureContext', { value: true, configurable: true })

      const wrapper = await mountDocs('https://api.first.test')
      const figure = wrapper.get('#api-info .docs-code')
      const button = figure.get('[data-docs-copy]')
      expect(button.attributes('data-copied')).toBeUndefined()

      await button.trigger('click')
      await flushPromises()

      expect(writeText).toHaveBeenCalledTimes(1)
      expect(writeText.mock.calls[0][0]).toBe(figure.get('code').text())
      expect(writeText.mock.calls[0][0]).toContain('https://api.first.test/v1/models')
      expect(button.attributes('data-copied')).toBe('true')
      expect(button.text()).toContain('已复制')
      wrapper.unmount()
    })

    it('copies the address from the connect strip', async () => {
      const writeText = vi.fn().mockResolvedValue(undefined)
      Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
      Object.defineProperty(window, 'isSecureContext', { value: true, configurable: true })

      const wrapper = await mountDocs('https://api.first.test')
      const copyButton = wrapper.get('[data-testid="docs-connect"] button')
      await copyButton.trigger('click')
      await flushPromises()

      expect(writeText).toHaveBeenCalledWith('https://api.first.test/v1')
      expect(copyButton.text()).toBe('已复制')
      wrapper.unmount()
    })
  })

  describe('table of contents', () => {
    it('has one link and one anchor per section, in the same order', async () => {
      const wrapper = await mountDocs('https://api.first.test')

      const links = wrapper.findAll('[data-testid="docs-toc"] [data-toc-id]')
      expect(links.map((l) => l.attributes('data-toc-id'))).toEqual(allSectionIds)
      for (const link of links) {
        const id = link.attributes('data-toc-id')!
        expect(link.attributes('href')).toBe(`#${id}`)
        expect(document.querySelectorAll(`section#${id}`)).toHaveLength(1)
      }
      expect(wrapper.findAll('section.docs-section')).toHaveLength(allSectionIds.length)
      wrapper.unmount()
    })

    it('gives every sub-heading a unique anchor that does not clash with a section id', async () => {
      const wrapper = await mountDocs('https://api.first.test')
      const headingIds = Array.from(document.querySelectorAll('.docs-prose h3')).map((h) => h.id)

      expect(headingIds.length).toBeGreaterThan(20)
      expect(new Set(headingIds).size).toBe(headingIds.length)
      for (const id of headingIds) {
        expect(id).not.toBe('')
        expect(allSectionIds).not.toContain(id)
      }
      wrapper.unmount()
    })

    it('scrolls to the section when a TOC link is clicked', async () => {
      const wrapper = await mountDocs('https://api.first.test')
      await wrapper.get('[data-testid="docs-toc"] [data-toc-id="errors"]').trigger('click')
      expect(Element.prototype.scrollIntoView).toHaveBeenCalled()
      expect(window.location.hash).toBe('#errors')
      wrapper.unmount()
    })
  })

  describe('language', () => {
    it('shows a Chinese-only notice outside zh-CN', async () => {
      currentLocale.value = 'en'
      const wrapper = await mountDocs('https://api.first.test')
      expect(wrapper.find('[data-testid="docs-lang-note"]').exists()).toBe(true)
      wrapper.unmount()
    })

    it('hides the notice in zh-CN', async () => {
      const wrapper = await mountDocs('https://api.first.test')
      expect(wrapper.find('[data-testid="docs-lang-note"]').exists()).toBe(false)
      wrapper.unmount()
    })
  })
})

describe('AI prompts', () => {
  const vars: DocVars = {
    base: 'https://api.first.test',
    v1: 'https://api.first.test/v1',
    site: 'Hiyo',
    model: 'example-model',
    llms: 'https://site.test/llms.txt',
  }

  it('never contains anything that looks like a real key', () => {
    const prompts = parseAiPrompts(aiPromptsRaw, vars)
    expect(Object.keys(prompts).sort()).toEqual(['chat', 'claude-code', 'codex', 'general'])
    for (const prompt of Object.values(prompts)) {
      expect(prompt).not.toMatch(/sk-[A-Za-z0-9_-]{8,}/)
      expect(prompt).toContain('sk-你的密钥')
      expect(prompt).toContain('https://site.test/llms.txt')
    }
  })

  it('builds the ChatGPT and Claude links from the prompt text', () => {
    setActivePinia(createPinia())
    const prompts = parseAiPrompts(aiPromptsRaw, vars)
    const wrapper = mount(DocsAiPrompts, { props: { prompts } })

    const chatgpt = wrapper.get('[data-testid="docs-ai-chatgpt"]').attributes('href')!
    const claude = wrapper.get('[data-testid="docs-ai-claude"]').attributes('href')!
    expect(chatgpt.startsWith('https://chatgpt.com/?q=')).toBe(true)
    expect(claude.startsWith('https://claude.ai/new?q=')).toBe(true)
    expect(decodeURIComponent(chatgpt.split('?q=')[1])).toBe(prompts.general)
    expect(decodeURIComponent(claude.split('?q=')[1])).toBe(prompts.general)
  })

  it('copies the selected prompt', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    Object.defineProperty(window, 'isSecureContext', { value: true, configurable: true })
    const prompts = parseAiPrompts(aiPromptsRaw, vars)
    const wrapper = mount(DocsAiPrompts, { props: { prompts } })

    await wrapper.findAll('[role="tab"]')[1].trigger('click')
    await wrapper.get('[data-testid="docs-ai-copy"]').trigger('click')
    await flushPromises()

    expect(writeText).toHaveBeenCalledWith(prompts['claude-code'])
    expect(wrapper.get('[data-testid="docs-ai-copy"]').text()).toBe('已复制')
  })
})

describe('renderSection', () => {
  it('turns fenced code into a block with a title and a copy button', () => {
    const vars: DocVars = { base: 'https://a.test', v1: 'https://a.test/v1', site: 'S', model: 'm', llms: 'x' }
    const rendered = renderSection(
      'demo',
      '# 标题\n\n## 小节 {#demo-part}\n\n```bash title="终端"\ncurl {{v1}}\n```\n',
      vars,
      { copy: '复制', copied: '已复制' }
    )
    expect(rendered.title).toBe('标题')
    expect(rendered.headings).toEqual([{ id: 'demo-part', text: '小节' }])
    expect(rendered.html).toContain('data-docs-copy')
    expect(rendered.html).toContain('终端')
    expect(rendered.html).toContain('curl https://a.test/v1')
  })

  it('does not let a malicious site name inject markup', () => {
    const vars: DocVars = { base: 'https://a.test', v1: 'https://a.test/v1', site: '<img src=x onerror=alert(1)>', model: 'm', llms: 'x' }
    const rendered = renderSection('demo', '# T\n\n欢迎使用 {{site}}\n', vars, { copy: 'c', copied: 'd' })
    expect(rendered.html).not.toContain('onerror')
  })
})
