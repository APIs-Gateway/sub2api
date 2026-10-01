import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { createPinia, setActivePinia } from 'pinia'
import { ref } from 'vue'

import zhCN from '@/i18n/locales/zh-CN'
import { OPENAI_CC_SWITCH_CODEX_MODEL } from '@/utils/ccswitchImport'
import DocsView from '../DocsView.vue'
import DocsAiPrompts from '../DocsAiPrompts.vue'
import { DOC_GROUPS } from '../sections'
import aiPromptsRaw from '../ai-prompts.md?raw'
import {
  EXAMPLE_MODEL,
  codexProviderId,
  codexProviderName,
  fillVars,
  parseAiPrompts,
  renderSection,
  resolveApiBases,
  type DocVars,
} from '../docsRender'

const llmsRaw = readFileSync(resolve(__dirname, '../../../../public/llms.txt'), 'utf8')

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
        llmsRaw,
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

  describe('example content', () => {
    it('uses the same example model as the CC Switch import', async () => {
      expect(EXAMPLE_MODEL).toBe(OPENAI_CC_SWITCH_CODEX_MODEL)

      const wrapper = await mountDocs('https://api.first.test')
      expect(wrapper.get('#codex').text()).toContain(`model = "${OPENAI_CC_SWITCH_CODEX_MODEL}"`)
      wrapper.unmount()
    })

    it('writes Codex as a single config.toml with a provider derived from the site name', async () => {
      const wrapper = await mountDocs('https://api.first.test')
      const codex = wrapper.get('#codex').text()

      expect(codex).toContain('model_provider = "hiyo"')
      expect(codex).toContain('[model_providers.hiyo]')
      expect(codex).toContain('name = "Hiyo"')
      expect(codex).toContain('base_url = "https://api.first.test/v1"')
      expect(codex).toContain('wire_api = "responses"')
      expect(codex).toContain('requires_openai_auth = false')
      expect(codex).toContain('experimental_bearer_token = "sk-你的密钥"')
      expect(codex).not.toContain('auth.json')
      expect(codex).not.toContain('{{')
      wrapper.unmount()
    })

    it('never tells the reader to delete the whole Codex config file', async () => {
      const wrapper = await mountDocs('https://api.first.test')
      const codex = wrapper.get('#codex').text()

      expect(codex).not.toMatch(/rm -f|Remove-Item/)
      expect(codex).toContain('[model_providers.hiyo]')
      wrapper.unmount()
    })

    it('does not quote internal error text or name infrastructure', async () => {
      const wrapper = await mountDocs('https://api.first.test')
      const article = wrapper.get('[data-testid="docs-article"]')
      const rendered = [article.text(), article.html()]
      const sources = [...DOC_GROUPS.flatMap((g) => g.sections.map((s) => s.raw)), aiPromptsRaw, llmsRaw]

      for (const banned of ['No available accounts', 'Cloudflare']) {
        for (const text of [...rendered, ...sources]) {
          expect(text).not.toContain(banned)
        }
      }
      expect(article.text()).toContain('Service temporarily unavailable')
      wrapper.unmount()
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

  describe('hero and edge rail', () => {
    it('lists the groups in the hero, each linking to the first section of its group', async () => {
      const wrapper = await mountDocs('https://api.first.test')

      const links = wrapper.findAll('.docs-hero-toc a')
      expect(links).toHaveLength(DOC_GROUPS.length)
      links.forEach((link, index) => {
        expect(link.attributes('href')).toBe(`#${DOC_GROUPS[index].sections[0].id}`)
        expect(link.text()).toContain(zhCN.docs.groups[DOC_GROUPS[index].id])
      })
      expect(wrapper.get('h1').text()).toBe(`Hiyo ${zhCN.docs.title}`)

      await links[1].trigger('click')
      expect(Element.prototype.scrollIntoView).toHaveBeenCalled()
      expect(window.location.hash).toBe(`#${DOC_GROUPS[1].sections[0].id}`)
      wrapper.unmount()
    })

    it('labels the edge rail and gives every tick a readable section name', async () => {
      const wrapper = await mountDocs('https://api.first.test')
      const rail = wrapper.get('[data-testid="docs-toc"]')

      expect(rail.attributes('aria-label')).toBe(zhCN.docs.tocTitle)
      for (const link of rail.findAll('[data-toc-id]')) {
        expect(link.text().length).toBeGreaterThan(0)
      }
      wrapper.unmount()
    })

    it('marks the section that has scrolled to the top as current', async () => {
      const wrapper = await mountDocs('https://api.first.test')
      const reached = 4
      vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
        const index = allSectionIds.indexOf(this.id)
        const top = index === -1 ? 0 : index <= reached ? -300 + index * 50 : 600 + index * 40
        return { top, bottom: top, left: 0, right: 0, width: 0, height: 0, x: 0, y: top, toJSON: () => ({}) } as DOMRect
      })

      window.dispatchEvent(new Event('scroll'))
      await vi.waitFor(() => {
        const current = wrapper.findAll('[data-testid="docs-toc"] [aria-current="true"]')
        expect(current).toHaveLength(1)
        expect(current[0].attributes('data-toc-id')).toBe(allSectionIds[reached])
      })
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

  it('states the connection address in every prompt', () => {
    const prompts = parseAiPrompts(aiPromptsRaw, vars)
    for (const prompt of Object.values(prompts)) {
      expect(prompt).toContain('接入地址是 https://api.first.test（OpenAI 兼容客户端用 https://api.first.test/v1）')
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
  it('wraps tables in a scroll container and keeps title-less code blocks plain', () => {
    const vars: DocVars = { base: 'https://a.test', v1: 'https://a.test/v1', site: 'S', model: 'm', llms: 'x' }
    const rendered = renderSection(
      'demo',
      '# 标题\n\n| 方法 | 路径 |\n|---|---|\n| GET | `/v1/models` |\n\n```bash\nls\n```\n\n```bash title="终端"\nls\n```\n',
      vars,
      { copy: '复制', copied: '已复制' }
    )
    const container = document.createElement('div')
    container.innerHTML = rendered.html

    expect(container.querySelectorAll('.docs-table > table')).toHaveLength(1)
    const blocks = container.querySelectorAll('figure.docs-code')
    expect(blocks).toHaveLength(2)
    expect(blocks[0].classList.contains('docs-code-plain')).toBe(true)
    expect(blocks[1].classList.contains('docs-code-plain')).toBe(false)
  })

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

  it('keeps a quote or backslash in the site name from breaking the TOML example', () => {
    const vars: DocVars = { base: 'https://a.test', v1: 'https://a.test/v1', site: 'My "Site" \\ 1', model: 'm', llms: 'x' }
    const rendered = renderSection('demo', '# T\n\n```toml\nname = "{{providerName}}"\n```\n', vars, { copy: 'c', copied: 'd' })
    const container = document.createElement('div')
    container.innerHTML = rendered.html
    expect(container.textContent).toContain('name = "My \\"Site\\" \\\\ 1"')
  })

})

describe('Codex provider placeholders', () => {
  const vars: DocVars = { base: 'https://a.test', v1: 'https://a.test/v1', site: 'Hiyo', model: 'm', llms: 'x' }

  it('derives the provider id from the site name using lowercase letters, digits and underscores only', () => {
    expect(codexProviderId('Hiyo')).toBe('hiyo')
    expect(codexProviderId('My Site-2_x!')).toBe('mysite2_x')
  })

  it('falls back to sub2api when nothing usable is left', () => {
    expect(codexProviderId('')).toBe('sub2api')
    expect(codexProviderId(undefined)).toBe('sub2api')
    expect(codexProviderId('我的站点')).toBe('sub2api')
  })

  it('never uses a built-in provider id', () => {
    for (const reserved of ['openai', 'OpenAI', 'ollama', 'lmstudio']) {
      expect(codexProviderId(reserved)).toBe(`${reserved.toLowerCase()}_site`)
    }
  })

  it('escapes the display name for a TOML string and flattens line breaks', () => {
    expect(codexProviderName('A "B" \\ C\nD')).toBe('A \\"B\\" \\\\ C D')
    expect(codexProviderName(' \n ')).toBe('sub2api')
    expect(codexProviderName(undefined)).toBe('sub2api')
  })

  it('fills {{provider}} and {{providerName}} from the site name', () => {
    expect(fillVars('[model_providers.{{provider}}] name = "{{providerName}}"', vars)).toBe(
      '[model_providers.hiyo] name = "Hiyo"'
    )
    expect(fillVars('{{site}} {{v1}}', { ...vars, site: 'OpenAI' })).toBe('OpenAI https://a.test/v1')
    expect(fillVars('{{provider}}', { ...vars, site: 'OpenAI' })).toBe('openai_site')
  })
})

describe('llms.txt', () => {
  it('writes Codex as a single config.toml with placeholders only', () => {
    expect(llmsRaw).toContain('experimental_bearer_token = "sk-你的密钥"')
    expect(llmsRaw).toContain('requires_openai_auth = false')
    expect(llmsRaw).toContain('base_url = "<API 地址>/v1"')
    expect(llmsRaw).not.toContain('~/.codex/auth.json')
    expect(llmsRaw).not.toContain('OPENAI_API_KEY": ')
  })

  it('does not send the reader to the /docs page, which is not readable without scripts', () => {
    expect(llmsRaw).not.toContain('<站点地址>')
    expect(llmsRaw).not.toContain('完整的图文说明')
    expect(llmsRaw).not.toContain('/docs#')
  })
})
