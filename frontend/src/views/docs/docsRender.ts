/**
 * 使用文档的渲染工具：占位符替换、Markdown 渲染、章节解析。
 * 文档里的地址一律用占位符，渲染时按站点的 api_base_url 填入，不写死域名。
 */
import { Marked } from 'marked'
import DOMPurify from 'dompurify'

/** 示例里用的模型名。配置文件必须写一个具体值，真正可用的模型以价格页为准。 */
export const EXAMPLE_MODEL = 'gpt-5.5'

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

export function fillVars(text: string, vars: DocVars): string {
  return text.replace(/\{\{(base|v1|site|model|llms)\}\}/g, (_, key: keyof DocVars) => vars[key])
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
          '<figure class="docs-code">' +
          `<figcaption>${label}` +
          `<button type="button" class="docs-copy" data-docs-copy>` +
          `<span class="docs-copy-idle">${escapeHtml(labels.copy)}</span>` +
          `<span class="docs-copy-done" aria-live="polite">${escapeHtml(labels.copied)}</span>` +
          '</button></figcaption>' +
          `<pre><code${language ? ` class="language-${escapeHtml(language)}"` : ''}>${escapeHtml(text)}</code></pre>` +
          '</figure>\n'
        )
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
