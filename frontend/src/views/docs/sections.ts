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
import geminiCli from './sections/gemini-cli.md?raw'
import opencode from './sections/opencode.md?raw'
import aider from './sections/aider.md?raw'
import openclaw from './sections/openclaw.md?raw'
import ccSwitch from './sections/cc-switch.md?raw'
import cursor from './sections/cursor.md?raw'
import cline from './sections/cline.md?raw'
import rooCode from './sections/roo-code.md?raw'
import continueDev from './sections/continue.md?raw'
import kiro from './sections/kiro.md?raw'
import windsurf from './sections/windsurf.md?raw'
import cherryStudio from './sections/cherry-studio.md?raw'
import chatbox from './sections/chatbox.md?raw'
import openWebui from './sections/open-webui.md?raw'
import lobechat from './sections/lobechat.md?raw'
import nextchat from './sections/nextchat.md?raw'
import immersive from './sections/immersive.md?raw'
import otherClients from './sections/other-clients.md?raw'
import openaiSdk from './sections/openai-sdk.md?raw'
import examples from './sections/examples.md?raw'
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
  id: 'start' | 'clients' | 'editors' | 'chat' | 'developers' | 'reference'
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
      { id: 'gemini-cli', raw: geminiCli },
      { id: 'opencode', raw: opencode },
      { id: 'aider', raw: aider },
      { id: 'openclaw', raw: openclaw },
      { id: 'cc-switch', raw: ccSwitch },
    ],
  },
  {
    id: 'editors',
    sections: [
      { id: 'cursor', raw: cursor },
      { id: 'cline', raw: cline },
      { id: 'roo-code', raw: rooCode },
      { id: 'continue', raw: continueDev },
      { id: 'kiro', raw: kiro },
      { id: 'windsurf', raw: windsurf },
    ],
  },
  {
    id: 'chat',
    sections: [
      { id: 'cherry-studio', raw: cherryStudio },
      { id: 'chatbox', raw: chatbox },
      { id: 'open-webui', raw: openWebui },
      { id: 'lobechat', raw: lobechat },
      { id: 'nextchat', raw: nextchat },
      { id: 'immersive', raw: immersive },
      { id: 'other-clients', raw: otherClients },
    ],
  },
  {
    id: 'developers',
    sections: [
      { id: 'openai-sdk', raw: openaiSdk },
      { id: 'examples', raw: examples },
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
