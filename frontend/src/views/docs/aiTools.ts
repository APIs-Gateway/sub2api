/**
 * 「让 AI 帮你接入」里的工具按钮。
 *
 * 新增一个工具章节后，想让它出现在按钮里：
 *   1. 在 AI_TOOLS 里加一项，section 填章节 id（见 sections.ts）；
 *   2. 工具名是品牌名（Cursor、Windsurf 之类）时填 name，不用动翻译；
 *   3. 在 ai-prompts.md 里加一段同名 id 的完整提示词（没有就退回「任意工具」那段）。
 * 工具名需要翻译的（任意工具、聊天客户端、写代码调用），不填 name，
 * 改在三个语言文件的 docs.ai.tools.<id> 里写 label 和 sentence。
 */
import { DOC_GROUPS } from './sections'
import { endpointQuery } from './docsMachine'

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
  { id: 'chat', section: 'other-clients' },
  { id: 'code', section: 'openai-sdk' },
]

/** 配置里写的章节必须真的存在，否则链接会指向 404。 */
export function missingToolSections(tools: AiTool[] = AI_TOOLS): string[] {
  const ids = new Set(DOC_GROUPS.flatMap((g) => g.sections.map((s) => s.id)))
  return tools.flatMap((t) => (t.section && !ids.has(t.section) ? [t.section] : []))
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
