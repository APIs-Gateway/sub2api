/**
 * 「让 AI 帮你接入」的工具表：文档页和密钥接入弹窗共用。
 * 这里锁住：弹窗的每个工具读哪份文档、文档真的存在（不会是死链）、三种语言的句子、
 * 以及页面上新增的两段文字里没有不该出现的词。
 */
import { describe, expect, it } from 'vitest'

import en from '@/i18n/locales/en'
import zhCN from '@/i18n/locales/zh-CN'
import zhHK from '@/i18n/locales/zh-HK'
import { AI_CLIENTS, type AiClient } from '@/utils/keyOnboarding'
import { AI_TOOLS, ONBOARDING_AI_TOOLS, aiCatalogUrl, aiToolForClient, aiToolSentence, aiToolUrl, missingToolSections } from '../aiTools'
import { buildMachineFiles, endpointQuery, ENDPOINT_QUERY_PARAM } from '../docsMachine'
import { DOC_GROUPS } from '../sections'

const LOCALES = { en, 'zh-CN': zhCN, 'zh-HK': zhHK } as const
type Lang = keyof typeof LOCALES

/** 和页面里一样按 key 取文案、替换 {占位符}；取不到时返回 key，断言里一眼能看出缺了哪条 */
function translator(lang: Lang) {
  return (key: string, params: Record<string, unknown> = {}): string => {
    let cur: unknown = LOCALES[lang]
    for (const seg of key.split('.')) {
      if (typeof cur !== 'object' || cur === null) return key
      cur = (cur as Record<string, unknown>)[seg]
    }
    return typeof cur === 'string' ? cur.replace(/\{(\w+)\}/g, (_, k) => String(params[k] ?? '')) : key
  }
}

const ORIGIN = 'https://site.example.com'
const sectionIds = DOC_GROUPS.flatMap((g) => g.sections.map((s) => s.id))

describe('AI_TOOLS 和文档章节', () => {
  it('每个工具指向的章节都存在；找不到的会被 missingToolSections 列出来', () => {
    expect(missingToolSections(sectionIds)).toEqual([])
    expect(missingToolSections(sectionIds, [{ id: 'x', section: 'no-such-section' }, { id: 'y' }])).toEqual(['no-such-section'])
  })

  it('每个工具的链接（带或不带 ?endpoint=）都能在站内机器文件里找到，上线后 /docs/<id>.md 和 /llms.txt 都能访问', () => {
    const served = new Set(Object.keys(buildMachineFiles()))
    const urls = [...AI_TOOLS.map((tool) => aiToolUrl(tool, ORIGIN)), aiCatalogUrl(ORIGIN)]
    for (const url of urls) expect(served.has(new URL(url).pathname.slice(1)), url).toBe(true)
    // 备用线路只多一个查询串，路径不变
    for (const tool of AI_TOOLS) {
      expect(aiToolUrl(tool, ORIGIN, 'https://cdn.example.com')).toBe(`${aiToolUrl(tool, ORIGIN)}${endpointQuery('https://cdn.example.com')}`)
    }
  })

  it('?endpoint= 的参数名和拼法没变（从 docsMachine 再导出，后端 machineEndpointQuery 同规则）', () => {
    expect(ENDPOINT_QUERY_PARAM).toBe('endpoint')
    expect(endpointQuery('https://cdn.example.com/x')).toBe('?endpoint=https://cdn.example.com/x')
    expect(endpointQuery('https://a.test:8443')).toBe('?endpoint=https://a.test:8443')
  })
})

describe('密钥接入弹窗的工具和 AI_TOOLS 的对应', () => {
  const DOC: Record<AiClient, string> = {
    claude: '/docs/claude-code.md',
    codex: '/docs/codex.md',
    cursor: '/docs/cursor.md',
    chat: '/llms.txt',
    code: '/docs/openai-sdk.md',
    other: '/llms.txt'
  }

  it('弹窗里的每个工具都对应 AI_TOOLS 里存在的一项，读的文档如下', () => {
    expect(Object.keys(ONBOARDING_AI_TOOLS).sort()).toEqual([...AI_CLIENTS].sort())
    for (const client of AI_CLIENTS) {
      const tool = aiToolForClient(client)
      expect(AI_TOOLS, client).toContain(tool)
      expect(new URL(aiToolUrl(tool, ORIGIN)).pathname, client).toBe(DOC[client])
    }
  })

  it('对应表里写了不存在的工具时退回「任意工具」（指向 /llms.txt），不会生成死链', () => {
    const broken = { ...ONBOARDING_AI_TOOLS }
    ONBOARDING_AI_TOOLS.codex = 'no-such-tool'
    try {
      expect(new URL(aiToolUrl(aiToolForClient('codex'), ORIGIN)).pathname).toBe('/llms.txt')
    } finally {
      Object.assign(ONBOARDING_AI_TOOLS, broken)
    }
  })
})

describe('一句话（三种语言）', () => {
  const url = (path: string) => `${ORIGIN}${path}`
  // 品牌名的工具共用一句模板；聊天客户端、写代码调用、任意工具各有自己的句子
  const EXPECTED: Record<Lang, Record<string, string>> = {
    'zh-CN': {
      'claude-code': `请按这份文档，帮我把 Claude Code 接入 Hiyo：${url('/docs/claude-code.md')}`,
      codex: `请按这份文档，帮我把 Codex 接入 Hiyo：${url('/docs/codex.md')}`,
      cursor: `请按这份文档，帮我把 Cursor 接入 Hiyo：${url('/docs/cursor.md')}`,
      chat: `请按这份文档，帮我把聊天客户端接入 Hiyo：${url('/llms.txt')}`,
      code: `请按这份文档，帮我在代码里调用 Hiyo：${url('/docs/openai-sdk.md')}`,
      any: `请按这份文档，帮我把正在用的工具接入 Hiyo：${url('/llms.txt')}`
    },
    'zh-HK': {
      'claude-code': `請按這份文件，幫我把 Claude Code 接入 Hiyo：${url('/docs/claude-code.md')}`,
      codex: `請按這份文件，幫我把 Codex 接入 Hiyo：${url('/docs/codex.md')}`,
      cursor: `請按這份文件，幫我把 Cursor 接入 Hiyo：${url('/docs/cursor.md')}`,
      chat: `請按這份文件，幫我把聊天客戶端接入 Hiyo：${url('/llms.txt')}`,
      code: `請按這份文件，幫我在程式碼裡調用 Hiyo：${url('/docs/openai-sdk.md')}`,
      any: `請按這份文件，幫我把正在用的工具接入 Hiyo：${url('/llms.txt')}`
    },
    en: {
      'claude-code': `Follow this guide to connect Claude Code to Hiyo: ${url('/docs/claude-code.md')}`,
      codex: `Follow this guide to connect Codex to Hiyo: ${url('/docs/codex.md')}`,
      cursor: `Follow this guide to connect Cursor to Hiyo: ${url('/docs/cursor.md')}`,
      chat: `Follow this guide to connect my chat client to Hiyo: ${url('/llms.txt')}`,
      code: `Follow this guide to call Hiyo from my code: ${url('/docs/openai-sdk.md')}`,
      any: `Follow this guide to connect the tool I use to Hiyo: ${url('/llms.txt')}`
    }
  }

  it.each(Object.keys(EXPECTED) as Lang[])('%s：弹窗里六个工具的句子', (lang) => {
    const t = translator(lang)
    for (const client of AI_CLIENTS) {
      const tool = aiToolForClient(client)
      const sentence = aiToolSentence(tool, t, { site: 'Hiyo', url: aiToolUrl(tool, ORIGIN) })
      expect(sentence, `${lang} ${client}`).toBe(EXPECTED[lang][tool.id])
    }
  })

  it('文档页里的其余工具（Gemini CLI、OpenCode）也是同一句模板', () => {
    const t = translator('zh-CN')
    const gemini = AI_TOOLS.find((x) => x.id === 'gemini-cli')!
    expect(aiToolSentence(gemini, t, { site: 'Hiyo', url: aiToolUrl(gemini, ORIGIN) })).toBe(
      `请按这份文档，帮我把 Gemini CLI 接入 Hiyo：${url('/docs/gemini-cli.md')}`
    )
  })
})

describe('「交给 AI」页尾的新文案（三种语言）', () => {
  const KEYS = ['keyOnboarding.ai.footnote', 'keyOnboarding.ai.docCatalog'] as const
  // 页面只写用户要知道的话：不提价格、充值、换算（free 站没有这些），也不提上游、账号池、内部路由
  const FORBIDDEN = /[¥￥$]|USD|CNY|RMB|price|pricing|billing|balance|recharge|payment|top[- ]?up|multiplier|upstream|account pool|价格|计费|余额|充值|支付|付费|倍率|汇率|换算|上游|账号池|帳號池|路由/i

  it.each(Object.keys(LOCALES) as Lang[])('%s：两条文案都有，没有价格、充值、上游一类的词', (lang) => {
    const t = translator(lang)
    for (const key of KEYS) {
      const text = t(key)
      expect(text, `${lang} ${key}`).not.toBe(key)
      expect(text, `${lang} ${key}`).not.toMatch(FORBIDDEN)
    }
  })

  it('沿用现有词汇：提示用户点的「复制详细版」和按钮上的字一致；繁体写「金鑰」，不混简体「密钥」', () => {
    for (const lang of Object.keys(LOCALES) as Lang[]) {
      const t = translator(lang)
      expect(t('keyOnboarding.ai.footnote'), lang).toContain(t('keyOnboarding.ai.copyDetail'))
    }
    const hk = translator('zh-HK')
    expect(hk('keyOnboarding.ai.footnote')).toContain('金鑰')
    for (const key of KEYS) expect(hk(key)).not.toContain('密钥')
    expect(translator('zh-CN')('keyOnboarding.ai.footnote')).toContain('密钥')
    // 文档目录在文档页里叫「文档 / 文件」，这里用同一个词
    expect(translator('zh-CN')('keyOnboarding.ai.docCatalog')).toBe('给 AI 读的文档目录')
    expect(hk('keyOnboarding.ai.docCatalog')).toBe('給 AI 讀的文件目錄')
    expect(translator('en')('keyOnboarding.ai.docCatalog')).toBe('Docs index for AI')
  })
})
