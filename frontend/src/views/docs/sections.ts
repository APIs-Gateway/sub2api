/**
 * 使用文档的章节清单。
 * 每一节是 sections/ 下的一个 Markdown 文件：第一行 `# 标题` 是章节标题，
 * `## 小标题 {#锚点}` 是本节的子标题。正文里的 {{base}} 等占位符在渲染时替换。
 * 新增一节：加一个 .md 文件，并在下面的 DOC_GROUPS 里登记。
 */
import quickstart from './sections/quickstart.md?raw'
import apiInfo from './sections/api-info.md?raw'
import aiAssist from './sections/ai-assist.md?raw'
import codex from './sections/codex.md?raw'
import claudeCode from './sections/claude-code.md?raw'
import ccSwitch from './sections/cc-switch.md?raw'
import cursor from './sections/cursor.md?raw'
import cherryStudio from './sections/cherry-studio.md?raw'
import otherClients from './sections/other-clients.md?raw'
import openaiSdk from './sections/openai-sdk.md?raw'
import endpoints from './sections/endpoints.md?raw'
import envVars from './sections/env-vars.md?raw'
import models from './sections/models.md?raw'
import errors from './sections/errors.md?raw'
import faq from './sections/faq.md?raw'

export interface DocSectionSource {
  /** 锚点 id，同时用于目录链接 */
  id: string
  raw: string
}

export interface DocGroup {
  /** i18n key，位于 docs.groups.* */
  id: 'start' | 'clients' | 'developers' | 'reference'
  sections: DocSectionSource[]
}

export const DOC_GROUPS: DocGroup[] = [
  {
    id: 'start',
    sections: [
      { id: 'quickstart', raw: quickstart },
      { id: 'api-info', raw: apiInfo },
      { id: 'ai-assist', raw: aiAssist },
    ],
  },
  {
    id: 'clients',
    sections: [
      { id: 'codex', raw: codex },
      { id: 'claude-code', raw: claudeCode },
      { id: 'cc-switch', raw: ccSwitch },
      { id: 'cursor', raw: cursor },
      { id: 'cherry-studio', raw: cherryStudio },
      { id: 'other-clients', raw: otherClients },
    ],
  },
  {
    id: 'developers',
    sections: [
      { id: 'openai-sdk', raw: openaiSdk },
      { id: 'endpoints', raw: endpoints },
      { id: 'env-vars', raw: envVars },
    ],
  },
  {
    id: 'reference',
    sections: [
      { id: 'models', raw: models },
      { id: 'errors', raw: errors },
      { id: 'faq', raw: faq },
    ],
  },
]
