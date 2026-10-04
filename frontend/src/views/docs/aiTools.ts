/**
 * 「让 AI 帮你接入」里的工具按钮。文档页（DocsAiPrompts.vue）和密钥接入弹窗的「交给 AI」页签共用这一份。
 *
 * 新增一个工具章节后，想让它出现在按钮里：
 *   1. 在 AI_TOOLS 里加一项，section 填章节 id（见 sections.ts）；
 *   2. 工具名是品牌名（Cursor、Windsurf 之类）时填 name，不用动翻译；
 *   3. 在 ai-prompts.md 里加一段同名 id 的完整提示词（没有就退回「任意工具」那段）。
 * 工具名需要翻译的（任意工具、聊天客户端、写代码调用），不填 name，
 * 改在三个语言文件的 docs.ai.tools.<id> 里写 label 和 sentence。
 *
 * 这个文件会被密钥页引用，所以不能引入 sections.ts（整份文档的 Markdown）或 docsMachine.ts（再加上渲染器）。
 * 「章节必须存在」的检查放在测试里（见 missingToolSections）。
 */
import { endpointQuery } from './endpointQuery'
import type { AiClient } from '@/utils/keyOnboarding'

export interface AiTool {
  /** 按钮 id，也是 ai-prompts.md 里完整提示词的小标题 */
  id: string
  /** 对应的章节 id。省略就指向 /llms.txt，由 AI 自己挑章节。 */
  section?: string
  /** 品牌名，不翻译。省略时按钮文字和句子取自 docs.ai.tools.<id> */
  name?: string
}

export const AI_TOOLS: AiTool[] = [
  { id: 'any' },
  { id: 'claude-code', section: 'claude-code', name: 'Claude Code' },
  { id: 'codex', section: 'codex', name: 'Codex' },
  { id: 'gemini-cli', section: 'gemini-cli', name: 'Gemini CLI' },
  { id: 'opencode', section: 'opencode', name: 'OpenCode' },
  { id: 'cursor', section: 'cursor', name: 'Cursor' },
  // 聊天客户端有好几个，各有各的章节：指向索引，让 AI 问清楚用的是哪个再挑对应章节。
  { id: 'chat' },
  { id: 'code', section: 'openai-sdk' },
]

/**
 * 配置里写的章节必须真的存在，否则链接会指向 404。
 * sectionIds 传文档页的全部章节 id（sections.ts 的 DOC_GROUPS）；这里不自己去读，免得把整份文档带进来。
 */
export function missingToolSections(sectionIds: Iterable<string>, tools: AiTool[] = AI_TOOLS): string[] {
  const ids = new Set(sectionIds)
  return tools.flatMap((t) => (t.section && !ids.has(t.section) ? [t.section] : []))
}

/**
 * 密钥接入弹窗「交给 AI」里的工具，对应上面哪一项。弹窗的选项是按分组筛过的（见 aiClientsForPlatform），
 * 这里只负责告诉它每个选项读哪份文档。弹窗新增工具时，这张表缺一项会在类型检查时报错。
 */
export const ONBOARDING_AI_TOOLS: Record<AiClient, AiTool['id']> = {
  claude: 'claude-code',
  codex: 'codex',
  cursor: 'cursor',
  chat: 'chat',
  code: 'code',
  other: 'any',
}

/** 弹窗里的某个工具对应的 AI_TOOLS 项；查不到就退回「任意工具」（指向 /llms.txt，不会是死链）。 */
export function aiToolForClient(client: AiClient): AiTool {
  return AI_TOOLS.find((x) => x.id === ONBOARDING_AI_TOOLS[client]) ?? AI_TOOLS[0]
}

/**
 * 工具对应的文档链接。选了备用地址时带上 ?endpoint=，AI 读到的文档里就是那个地址。
 * 域名用站点自己的来源：备用地址是给 API 用的，不保证能打开文档。
 */
export function aiToolUrl(tool: AiTool, origin: string, endpoint?: string): string {
  const path = tool.section ? `/docs/${tool.section}.md` : '/llms.txt'
  const query = endpoint ? endpointQuery(endpoint) : ''
  return `${origin}${path}${query}`
}

/** 文档目录（/llms.txt）的链接：AI 用的工具不在选项里，或者打不开某一节时，可以从这里找。 */
export function aiCatalogUrl(origin: string, endpoint?: string): string {
  return aiToolUrl({ id: 'any' }, origin, endpoint)
}

export type AiSentenceTranslate = (key: string, params?: Record<string, unknown>) => string

/**
 * 发给 AI 的那一句话：「请按这份文档，帮我把 {工具} 接入 {站点}：{文档链接}」。
 * 品牌名的工具用同一句模板；需要翻译的（聊天客户端、写代码调用、任意工具）各有自己的句子。
 */
export function aiToolSentence(tool: AiTool, t: AiSentenceTranslate, params: { site: string; url: string }): string {
  return tool.name
    ? t('docs.ai.sentenceNamed', { ...params, tool: tool.name })
    : t(`docs.ai.tools.${tool.id}.sentence`, params)
}
