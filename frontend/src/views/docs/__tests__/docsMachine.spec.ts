import { describe, expect, it } from 'vitest'

import zhCN from '@/i18n/locales/zh-CN'
import renderCases from './fixtures/machine-render-cases.json'
import { EXAMPLE_MODEL } from '../docsRender'
import { DOC_GROUPS, type DocGroup } from '../sections'
import {
  GROUP_LABELS,
  buildMachineFiles,
  endpointsSection,
  fillMachineText,
  fullMarkdown,
  llmsIndex,
  sectionMarkdown,
  sectionSummary,
} from '../docsMachine'

const allSections = DOC_GROUPS.flatMap((g) => g.sections)

/** 模拟以后新增一个工具章节：只加一项，不改 docsMachine。 */
const extraGroups: DocGroup[] = [
  ...DOC_GROUPS,
  {
    id: 'clients',
    sections: [
      {
        id: 'sample-tool',
        raw: '# Sample Tool\n\nSample Tool 是 AI 代码编辑器，支持自定义地址。后面是步骤。\n\n## 配置 {#sample-tool-setup}\n\n填 `{{v1}}`，站点 {{site}}。\n',
      },
    ],
  },
]

const ALLOWED_HOSTS = [
  'api.first.test', 'hiyo.test', 'github.com', 'nodejs.org', 'cherry-ai.com',
  'aider.chat', 'chatboxai.app', 'claude.ai', 'cline.bot', 'continue.dev', 'docs.openclaw.ai',
  'immersivetranslate.com', 'kiro.dev', 'lobehub.com', 'openclaw.ai', 'opencode.ai',
  'openwebui.com', 'roocode.com', 'windsurf.com',
]

const baseCtx = { site: 'Hiyo', apiBaseUrl: 'https://api.first.test', origin: 'https://hiyo.test' }

describe('machine files', () => {
  const files = buildMachineFiles()

  it('has llms.txt, llms-full.txt and one md per section, in sections.ts', () => {
    expect(Object.keys(files).sort()).toEqual(
      ['llms-full.txt', 'llms.txt', ...allSections.map((s) => `docs/${s.id}.md`)].sort()
    )
  })

  it('picks up a section added to the list with no other change', () => {
    const extra = buildMachineFiles(extraGroups)
    expect(Object.keys(extra)).toContain('docs/sample-tool.md')
    expect(extra['docs/sample-tool.md']).toContain('# Sample Tool')
    expect(extra['llms.txt']).toContain('- [Sample Tool]({{origin}}/docs/sample-tool.md{{q}})：Sample Tool 是 AI 代码编辑器，支持自定义地址。')
    expect(extra['llms-full.txt']).toContain('## 配置\n\n填 `{{v1}}`')
    expect(Object.keys(extra)).toHaveLength(Object.keys(files).length + 1)
  })

  it('only uses placeholders that the fillers know about', () => {
    const known = new Set(['base', 'v1', 'site', 'origin', 'llms', 'provider', 'providerName', 'endpoints', 'q'])
    for (const [path, text] of Object.entries(files)) {
      for (const match of text.matchAll(/\{\{(\w+)\}\}/g)) {
        expect(known.has(match[1]), `${path}: {{${match[1]}}}`).toBe(true)
      }
    }
  })

  it('fills everything for a real site, with no leftover placeholder, anchor or hard-coded domain', () => {
    for (const [path, template] of Object.entries(files)) {
      const text = fillMachineText(template, baseCtx)
      expect(text, path).not.toMatch(/\{\{|\}\}/)
      expect(text, path).not.toMatch(/\{#[\w-]+\}/)
      expect(text, path).not.toContain('<API 地址>')
      for (const match of text.matchAll(/https?:\/\/([^/\s)`"'：，]+)/g)) {
        expect(ALLOWED_HOSTS, `${path}: ${match[0]}`).toContain(match[1])
      }
    }
  })

  it('bakes the example model in at build time', () => {
    expect(files['docs/codex.md']).toContain(`model = "${EXAMPLE_MODEL}"`)
    expect(files['llms-full.txt']).not.toContain('{{model}}')
  })

  it('names the parts like the zh-CN page does', () => {
    expect(GROUP_LABELS).toEqual(zhCN.docs.groups)
  })
})

describe('docs/<id>.md', () => {
  it('starts with the title, says where the web version is, and tells the AI not to ask for keys', () => {
    const text = fillMachineText(sectionMarkdown('codex', DOC_GROUPS[1].sections[0].raw), baseCtx)
    expect(text.startsWith('# Codex CLI 与 Codex Desktop\n')).toBe(true)
    expect(text).toContain('Hiyo 文档，网页版：https://hiyo.test/docs#codex，文档索引：https://hiyo.test/llms.txt')
    expect(text).toContain('不要索要真实密钥')
    expect(text).toContain('base_url = "https://api.first.test/v1"')
  })

  it('drops the heading anchors but keeps the headings', () => {
    const text = sectionMarkdown('codex', '# T\n\n正文。\n\n## 安装 {#codex-install}\n\n内容\n')
    expect(text).toContain('## 安装\n')
    expect(text).not.toContain('{#')
  })
})

describe('llms.txt', () => {
  const text = fillMachineText(llmsIndex(), baseCtx)

  it('keeps the rules for AI assistants, now with the real addresses', () => {
    expect(text).toContain('## 给 AI 助手的规则')
    expect(text).toContain('不要向用户索要真实密钥')
    expect(text).toContain('Base URL 是 `https://api.first.test/v1`，带 /v1')
    expect(text).toContain('Base URL 是 `https://api.first.test`，不带 /v1')
    expect(text).toContain('ANTHROPIC_AUTH_TOKEN')
    expect(text).toContain('curl https://api.first.test/v1/models')
    expect(text).toContain('https://hiyo.test/available-channels')
  })

  it('links every section once with a one-line summary, grouped like the page', () => {
    for (const group of DOC_GROUPS) {
      expect(text).toContain(`## ${GROUP_LABELS[group.id]}\n`)
    }
    // 「让 AI 帮你接入」不进索引（见下面的用例），其余每节各链一次。
    for (const section of allSections.filter((s) => s.id !== 'ai-assist')) {
      const matches = text.match(new RegExp(`\\]\\(https://hiyo\\.test/docs/${section.id}\\.md\\)`, 'g'))
      expect(matches, section.id).toHaveLength(1)
    }
    expect(text).toContain('- [快速开始](https://hiyo.test/docs/quickstart.md)：接入 Hiyo 要三步，用什么工具都一样。')
    expect(text).toContain('(https://hiyo.test/llms-full.txt)')
  })

  it('only links to files that exist', () => {
    const files = buildMachineFiles()
    for (const match of text.matchAll(/\]\(https:\/\/hiyo\.test\/([^)?]+)\)/g)) {
      if (match[1] === 'docs') continue
      expect(Object.keys(files), match[1]).toContain(match[1])
    }
  })

  it('has no alternative-address section when the site has none', () => {
    expect(text).not.toContain('备用地址')
  })

  it('lists the custom endpoints with their names and descriptions, defaults unchanged', () => {
    const withEndpoints = fillMachineText(llmsIndex(), {
      ...baseCtx,
      customEndpoints: [
        { name: 'CDN 加速域名', endpoint: 'https://cdn.second.test/v1/', description: '全球\n支持' },
        { name: '重复', endpoint: 'https://api.first.test' },
        { name: '无效', endpoint: 'javascript:alert(1)' },
      ],
    })
    expect(withEndpoints).toContain('## 备用地址')
    expect(withEndpoints).toContain('- CDN 加速域名：`https://cdn.second.test`，全球 支持')
    expect(withEndpoints).not.toContain('重复')
    expect(withEndpoints).not.toContain('javascript')
    expect(withEndpoints).toContain('Base URL 是 `https://api.first.test/v1`')
    expect(withEndpoints).not.toContain('?endpoint=')
  })

  it('does not list the “让 AI 帮你接入” section, same as llms-full.txt', () => {
    expect(text).not.toContain('ai-assist.md')
    expect(text).not.toContain('让 AI 帮你接入')
    expect(buildMachineFiles()['docs/ai-assist.md']).toContain('# 让 AI 帮你接入')
  })

  it('lets a request pick one of the listed endpoints, and ignores anything else', () => {
    const customEndpoints = [{ name: 'CDN', endpoint: 'https://cdn.second.test', description: '' }]
    const picked = fillMachineText(llmsIndex(), { ...baseCtx, customEndpoints, requestedEndpoint: 'https://cdn.second.test' })
    expect(picked).toContain('Base URL 是 `https://cdn.second.test/v1`，带 /v1')
    expect(picked).toContain('(https://hiyo.test/docs/quickstart.md?endpoint=https://cdn.second.test)')
    expect(picked).not.toContain('Base URL 是 `https://api.first.test')

    for (const requestedEndpoint of ['https://evil.test', 'https://api.first.test', 'cdn.second.test']) {
      const ignored = fillMachineText(llmsIndex(), { ...baseCtx, customEndpoints, requestedEndpoint })
      expect(ignored, requestedEndpoint).toContain('Base URL 是 `https://api.first.test/v1`')
      expect(ignored, requestedEndpoint).not.toContain('?endpoint=')
    }
  })

  it('falls back to the origin and the default site name', () => {
    const bare = fillMachineText(llmsIndex(), { site: '', apiBaseUrl: '', origin: 'https://hiyo.test' })
    expect(bare).toContain('# Sub2API 接入文档')
    expect(bare).toContain('Base URL 是 `https://hiyo.test/v1`')
  })
})

describe('shared render cases (the backend test reads the same file)', () => {
  it('has cases', () => {
    expect(renderCases.cases.length).toBeGreaterThan(20)
  })

  for (const item of renderCases.cases) {
    it(item.name, () => {
      expect(
        fillMachineText(item.template, {
          site: item.settings.site_name,
          apiBaseUrl: item.settings.api_base_url,
          customEndpoints: item.settings.custom_endpoints,
          origin: item.origin,
          requestedEndpoint: item.requested,
        })
      ).toBe(item.expected)
    })
  }
})

describe('llms-full.txt', () => {
  const text = fillMachineText(fullMarkdown(), baseCtx)

  it('has the rules and every section, in page order', () => {
    expect(text).toContain('## 给 AI 助手的规则')
    let last = -1
    for (const section of allSections.filter((s) => s.id !== 'ai-assist')) {
      const title = section.raw.match(/^#\s+(.+?)\s*\n/)![1]
      const at = text.indexOf(`\n# ${title}\n`)
      expect(at, title).toBeGreaterThan(last)
      last = at
    }
  })

  it('leaves out the “让 AI 帮你接入” section, which only has its own docs/ai-assist.md', () => {
    expect(text).not.toContain('# 让 AI 帮你接入')
    expect(buildMachineFiles()['docs/ai-assist.md']).toContain('# 让 AI 帮你接入')
  })

  it('does not list alternative addresses itself', () => {
    expect(text).not.toContain('备用地址')
  })
})

describe('sectionSummary', () => {
  it('takes the first sentence of the first plain paragraph', () => {
    expect(sectionSummary('# T\n\n第一句。第二句。\n')).toBe('第一句。')
  })

  it('strips links, code marks and emphasis', () => {
    expect(sectionSummary('# T\n\n[Cherry Studio](https://x.test) 是 **多模型** 客户端，用 `base_url` 填。\n')).toBe(
      'Cherry Studio 是 多模型 客户端，用 base_url 填。'
    )
  })

  it('skips quotes, lists and code before the first paragraph', () => {
    expect(sectionSummary('# T\n\n> 提示。\n\n- 条目。\n\n```bash\nfoo。\n```\n\n正文第一句。\n')).toBe('正文第一句。')
  })

  it('joins a paragraph wrapped over several lines', () => {
    expect(sectionSummary('# T\n\n这一句\n拆成两行。\n')).toBe('这一句 拆成两行。')
  })

  it('uses the first sub-heading when the section has no paragraph', () => {
    expect(sectionSummary('# 常见问题\n\n## Base URL 要不要加 /v1？ {#faq-v1}\n\n看客户端。\n')).toBe('Base URL 要不要加 /v1？')
  })

  it('has a summary for every real section', () => {
    for (const section of allSections) {
      expect(sectionSummary(section.raw), section.id).not.toBe('')
    }
  })
})

describe('endpointsSection', () => {
  it('is empty when there is only the default address', () => {
    expect(endpointsSection([{ id: 'default', name: '', description: '', isDefault: true, base: 'https://a.test', v1: 'https://a.test/v1' }])).toBe('')
  })
})
