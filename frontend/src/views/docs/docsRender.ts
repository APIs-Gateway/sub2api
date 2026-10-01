/**
 * 使用文档的渲染工具：占位符替换、Markdown 渲染、章节解析。
 * 文档里的地址一律用占位符，渲染时按站点的 api_base_url 填入，不写死域名。
 */
import { Marked, Renderer } from 'marked'
import DOMPurify from 'dompurify'
import { OPENAI_CC_SWITCH_CODEX_MODEL } from '@/utils/ccswitchImport'

/**
 * 示例里用的模型名。配置文件必须写一个具体值，真正可用的模型以价格页为准。
 * 直接引用 CC Switch 导入用的 Codex 默认模型，两处不会各改各的。
 */
export const EXAMPLE_MODEL = OPENAI_CC_SWITCH_CODEX_MODEL

export interface DocVars {
  /** API 根地址，不带 /v1 */
  base: string
  /** OpenAI 兼容地址，带 /v1 */
  v1: string
  /** 站点名 */
  site: string
  /** 示例模型名 */
  model: string
  /** 给 AI 读的说明文件地址 */
  llms: string
}

export interface DocLabels {
  copy: string
  copied: string
}

export interface DocHeading {
  id: string
  text: string
}

export interface RenderedSection {
  id: string
  title: string
  html: string
  headings: DocHeading[]
}

/** 从公开设置的 api_base_url 推出两个常用地址。留空时退回当前站点的来源。 */
export function resolveApiBases(apiBaseUrl: string | undefined | null, fallbackOrigin: string): Pick<DocVars, 'base' | 'v1'> {
  const raw = (apiBaseUrl || '').trim() || fallbackOrigin
  const base = raw.replace(/\/+$/, '').replace(/\/v1$/, '')
  return { base, v1: `${base}/v1` }
}

/**
 * Codex 的 provider id：由站点名派生，只含小写字母、数字、下划线，且不与内置 provider 重名。
 * 规则与「接入密钥」弹窗的一键安装一致。
 */
export function codexProviderId(siteName?: string): string {
  const raw = (siteName || '').toLowerCase().replace(/[^a-z0-9_]/g, '')
  const id = raw || 'sub2api'
  return ['openai', 'ollama', 'lmstudio'].includes(id) ? `${id}_site` : id
}

/** 写进 TOML 双引号字符串的站点名：换行和控制字符换成空格，反斜杠和双引号转义。 */
export function codexProviderName(siteName?: string): string {
  // eslint-disable-next-line no-control-regex
  const name = (siteName || '').replace(/[\u0000-\u001f\u007f]+/g, ' ').trim() || 'sub2api'
  return name.replace(/\\/g, '\\\\').replace(/"/g, '\\"')
}

/**
 * 替换文档里的占位符。除了 DocVars 里的取值，还有两个由站点名派生的占位符：
 * {{provider}} 是 Codex 配置里的 provider id，{{providerName}} 是写进 TOML 的显示名。
 */
export function fillVars(text: string, vars: DocVars): string {
  return text.replace(/\{\{(base|v1|site|model|llms|provider|providerName)\}\}/g, (_, key: string) => {
    if (key === 'provider') return codexProviderId(vars.site)
    if (key === 'providerName') return codexProviderName(vars.site)
    return vars[key as keyof DocVars]
  })
}

function escapeHtml(value: string): string {
  return value
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
}

/** 解析代码块的信息串，例如 `bash title="~/.codex/config.toml"` */
function parseFenceInfo(info: string | undefined): { lang: string; title: string } {
  const text = (info || '').trim()
  const titleMatch = text.match(/title="([^"]*)"/)
  const lang = text.replace(/title="[^"]*"/, '').trim().split(/\s+/)[0] || ''
  return { lang, title: titleMatch ? titleMatch[1] : '' }
}

function isExternal(href: string): boolean {
  return /^https?:\/\//i.test(href)
}

/** 拆出章节标题（第一行 `# 标题`）和正文。 */
export function splitSection(raw: string): { title: string; body: string } {
  const match = raw.match(/^#\s+(.+?)\s*\n/)
  if (!match) return { title: '', body: raw }
  return { title: match[1], body: raw.slice(match[0].length) }
}

export function renderSection(id: string, raw: string, vars: DocVars, labels: DocLabels): RenderedSection {
  const { title, body } = splitSection(raw)
  const headings: DocHeading[] = []

  const marked = new Marked({
    gfm: true,
    renderer: {
      heading({ tokens, depth }) {
        const text = this.parser.parseInline(tokens)
        const anchor = text.match(/\s*\{#([\w-]+)\}\s*$/)
        const headingId = anchor ? anchor[1] : `${id}-${headings.length + 1}`
        const clean = text.replace(/\s*\{#[\w-]+\}\s*$/, '')
        const level = Math.min(depth + 1, 6) // 页面里章节标题是 h2，正文里的 ## 降成 h3
        headings.push({ id: headingId, text: clean.replace(/<[^>]+>/g, '') })
        return `<h${level} id="${headingId}">${clean}</h${level}>\n`
      },
      code({ text, lang }) {
        const { lang: language, title: caption } = parseFenceInfo(lang)
        const label = caption ? `<span class="docs-code-title">${escapeHtml(caption)}</span>` : '<span></span>'
        return (
          `<figure class="docs-code${caption ? '' : ' docs-code-plain'}">` +
          `<figcaption>${label}` +
          `<button type="button" class="docs-copy" data-docs-copy>` +
          `<span class="docs-copy-idle">${escapeHtml(labels.copy)}</span>` +
          `<span class="docs-copy-done" aria-live="polite">${escapeHtml(labels.copied)}</span>` +
          '</button></figcaption>' +
          `<pre><code${language ? ` class="language-${escapeHtml(language)}"` : ''}>${escapeHtml(text)}</code></pre>` +
          '</figure>\n'
        )
      },
      // 表格外面包一层，比阅读栏宽的表格靠它在窄屏上横向滚动
      table(token) {
        return `<div class="docs-table">${Renderer.prototype.table.call(this, token)}</div>\n`
      },
      link({ href, title, tokens }) {
        const inner = this.parser.parseInline(tokens)
        const titleAttr = title ? ` title="${escapeHtml(title)}"` : ''
        const external = isExternal(href) ? ' target="_blank" rel="noopener noreferrer"' : ''
        return `<a href="${escapeHtml(href)}"${titleAttr}${external}>${inner}</a>`
      },
    },
  })

  const html = DOMPurify.sanitize(marked.parse(fillVars(body, vars)) as string, {
    ADD_ATTR: ['target', 'data-docs-copy'],
  })
  return { id, title, html, headings }
}

/** 解析 ai-prompts.md：每个 `## id` 下面是一段提示词。 */
export function parseAiPrompts(raw: string, vars: DocVars): Record<string, string> {
  const result: Record<string, string> = {}
  const parts = raw.split(/^##\s+/m).slice(1)
  for (const part of parts) {
    const newline = part.indexOf('\n')
    const id = part.slice(0, newline).trim()
    result[id] = fillVars(part.slice(newline + 1).trim(), vars)
  }
  return result
}

/** 复制文本。Clipboard API 不可用时退回 textarea。 */
export async function copyText(text: string): Promise<boolean> {
  if (!text) return false
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // 退回下面的方式
  }
  const textarea = document.createElement('textarea')
  textarea.value = text
  textarea.style.cssText = 'position:fixed;left:-9999px;top:-9999px'
  document.body.appendChild(textarea)
  textarea.select()
  try {
    return document.execCommand('copy')
  } catch {
    return false
  } finally {
    document.body.removeChild(textarea)
  }
}
