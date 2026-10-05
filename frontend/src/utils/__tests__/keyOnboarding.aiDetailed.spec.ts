/**
 * 「交给 AI」的「复制详细版」：每个分组平台 x 每个工具 x 默认线路 / 备用线路，输出都留一份快照；
 * 另外对整张矩阵（三种语言）逐项断言：没有密钥、地址行和分组平台一致、读的文档真的存在、
 * 模型按工具筛选并且最多 20 个、备用线路的链接都带 ?endpoint=、不出现价格 / 充值 / 倍率一类的字眼。
 *
 * 快照只记 zh-CN 的完整矩阵；en、zh-HK 各记几个有代表性的组合，其余靠矩阵断言（三种语言的行数、链接、模型必须一致）。
 * 有意改动提示词的写法时，更新快照并看一遍 diff。
 */
import { describe, expect, it } from 'vitest'

import en from '@/i18n/locales/en'
import zhCN from '@/i18n/locales/zh-CN'
import zhHK from '@/i18n/locales/zh-HK'
import { OPENAI_CC_SWITCH_CODEX_MODEL } from '@/utils/ccswitchImport'
import { buildMachineFiles } from '@/views/docs/docsMachine'
import { DOC_GROUPS } from '@/views/docs/sections'
import {
  AI_CLIENTS,
  AI_PROMPT_ERRORS_SECTION,
  AI_PROMPT_MODEL_LIMIT,
  aiClientsForPlatform,
  aiPromptDocSections,
  buildAiPrompt,
  pickAiPromptModels,
  type AiClient
} from '../keyOnboarding'

const LOCALES = { 'zh-CN': zhCN, 'zh-HK': zhHK, en } as const
type Lang = keyof typeof LOCALES

function translator(lang: Lang) {
  return (key: string, params: Record<string, unknown> = {}): string => {
    let cur: unknown = LOCALES[lang]
    for (const seg of key.split('.')) {
      if (typeof cur !== 'object' || cur === null) return key
      cur = (cur as Record<string, unknown>)[seg]
    }
    return typeof cur === 'string' ? cur.replace(/\{(\w+)\}/g, (_, k) => String(params[k] ?? `{${k}}`)) : key
  }
}

const ORIGIN = 'https://codex.hiyo.top'
const CDN = 'https://cdn.example.com'
const LINES = [
  { id: 'default', baseUrl: `${ORIGIN}/`, docEndpoint: undefined },
  { id: 'cdn', baseUrl: `${CDN}/v1/`, docEndpoint: CDN }
] as const

interface Group {
  platform: string
  allowMessagesDispatch: boolean
  models: string[]
}
// 每个分组平台的模型：带图片、向量这类不是对话用的，也带带日期后缀的重名
const GROUPS: Record<string, Group> = {
  'openai（没开调度）': {
    platform: 'openai',
    allowMessagesDispatch: false,
    models: ['gpt-5.6-luna', 'gpt-image-2', 'gpt-5.5', 'text-embedding-3-large', 'gpt-5.6-sol', 'gpt-5.3-codex', 'gpt-5.6-terra']
  },
  'openai（开了调度）': {
    platform: 'openai',
    allowMessagesDispatch: true,
    models: ['gpt-5.6-luna', 'gpt-image-2', 'gpt-5.5', 'text-embedding-3-large', 'gpt-5.6-sol', 'gpt-5.3-codex', 'gpt-5.6-terra']
  },
  anthropic: {
    platform: 'anthropic',
    allowMessagesDispatch: false,
    models: ['claude-opus-5', 'claude-sonnet-5-20261001', 'claude-haiku-5', 'claude-sonnet-5', 'claude-opus-4-1']
  },
  gemini: {
    platform: 'gemini',
    allowMessagesDispatch: false,
    models: ['gemini-3.1-pro-preview', 'gemini-3-flash', 'gemini-3-pro-image']
  },
  antigravity: {
    platform: 'antigravity',
    allowMessagesDispatch: false,
    models: ['gemini-3.1-pro-high', 'claude-opus-5', 'claude-sonnet-5']
  }
}

const prompt = (lang: Lang, g: Group, client: AiClient, line: (typeof LINES)[number], models = g.models) =>
  buildAiPrompt({
    t: translator(lang),
    client,
    baseUrl: line.baseUrl,
    platform: g.platform,
    allowMessagesDispatch: g.allowMessagesDispatch,
    siteName: 'Hiyo codex',
    origin: ORIGIN,
    docEndpoint: line.docEndpoint,
    models
  })

/** 矩阵：每个分组平台 x 它能选的工具 x 线路。title 就是快照和用例的名字 */
const MATRIX = Object.entries(GROUPS).flatMap(([name, g]) =>
  aiClientsForPlatform(g.platform, { allowMessagesDispatch: g.allowMessagesDispatch }).flatMap((client) =>
    LINES.map((line) => ({ title: `${name} / ${client} / ${line.id}`, g, client, line }))
  )
)

describe('复制详细版：快照（zh-CN 完整矩阵）', () => {
  it('矩阵覆盖 5 种分组 x 各自的工具 x 2 条线路', () => {
    // openai 5 + 6、anthropic 5、gemini 3、antigravity 4，每个两条线路
    expect(MATRIX.length).toBe((5 + 6 + 5 + 3 + 4) * 2)
  })

  it.each(MATRIX)('$title', ({ g, client, line }) => {
    expect(prompt('zh-CN', g, client, line)).toMatchSnapshot()
  })
})

describe('复制详细版：en 和 zh-HK 的代表性快照', () => {
  const PICKS: [string, AiClient][] = [
    ['openai（没开调度）', 'codex'],
    ['openai（开了调度）', 'claude'],
    ['anthropic', 'cursor'],
    ['gemini', 'code'],
    ['antigravity', 'chat']
  ]
  for (const lang of ['en', 'zh-HK'] as const) {
    it.each(PICKS)(`${lang}：%s / %s`, (name, client) => {
      expect(prompt(lang, GROUPS[name], client, LINES[0])).toMatchSnapshot()
    })
  }
})

describe('复制详细版：整张矩阵逐项断言（三种语言）', () => {
  const served = new Set(Object.keys(buildMachineFiles()))
  const urlsIn = (text: string) => text.match(/https?:\/\/[^\s，。、）)]+/g) ?? []
  const FREE_SITE_FORBIDDEN =
    /[¥￥$]|USD|CNY|RMB|\bprice|pricing|billing|balance|recharge|payment|top[- ]?up|multiplier|\bratio\b|exchange|currency|价格|计费|余额|充值|支付|付费|倍率|汇率|换算|單價|價格|計費|餘額|充值|支付|付費|倍率|匯率|換算/i
  const PLACEHOLDER: Record<Lang, string> = { 'zh-CN': 'sk-你的密钥', 'zh-HK': 'sk-你的金鑰', en: 'sk-YOUR_API_KEY' }

  for (const lang of ['zh-CN', 'zh-HK', 'en'] as const) {
    describe(lang, () => {
      it.each(MATRIX)('$title：没有密钥，文案都取到了，没有价格一类的字眼', ({ g, client, line }) => {
        const text = prompt(lang, g, client, line)
        // 密钥：只有占位，出现一次
        expect(text.match(/sk-\S*/g)).toEqual([PLACEHOLDER[lang]])
        // 没有漏取的文案、没有没替换的 {占位符}
        expect(text).not.toMatch(/keyOnboarding\.|\{\w+\}/)
        expect(text).not.toMatch(FREE_SITE_FORBIDDEN)
        // 不暴露上游、账号池一类的内部说法
        expect(text).not.toMatch(/上游|upstream|账号池|賬號池|account pool/i)
        // 链接前后都是空白或行首行尾：中文标点、英文逗号句号紧贴在链接后面时，有的工具会把它们当成链接的一部分
        for (const url of text.match(/https?:\/\/\S+/g) ?? []) expect(url, url).toMatch(/^https?:\/\/[\w.\/:?=%&-]+[\w\/]$/)
      })

      it.each(MATRIX)('$title：文档链接都存在，备用线路的链接都带 ?endpoint=，默认线路的都不带', ({ g, client, line }) => {
        const docs = urlsIn(prompt(lang, g, client, line)).filter((u) => u.startsWith(`${ORIGIN}/docs/`) || u.startsWith(`${ORIGIN}/llms.txt`))
        expect(docs.length).toBeGreaterThanOrEqual(2) // 至少：先读的文档、错误排查
        for (const url of docs) {
          const parsed = new URL(url)
          expect(served.has(parsed.pathname.slice(1)), url).toBe(true)
          expect(parsed.searchParams.get('endpoint'), url).toBe(line.docEndpoint ?? null)
        }
        // 错误排查一定在
        expect(docs.some((u) => new URL(u).pathname === `/docs/${AI_PROMPT_ERRORS_SECTION}.md`)).toBe(true)
      })

      it.each(MATRIX)('$title：地址行和分组平台一致，用的是选中线路的地址', ({ g, client, line }) => {
        const text = prompt(lang, g, client, line)
        const root = line.id === 'cdn' ? CDN : ORIGIN
        // 默认线路不出现备用线路的地址；备用线路的 API 地址里不出现默认线路的（文档链接用站点自己的来源，除外）
        const apiUrls = urlsIn(text).filter((u) => !u.startsWith(`${ORIGIN}/docs/`) && !u.startsWith(`${ORIGIN}/llms.txt`))
        for (const u of apiUrls) expect(u.startsWith(root), u).toBe(true)
        const has = (url: string) => apiUrls.includes(url)
        switch (g.platform) {
          case 'openai':
            expect(has(`${root}/v1`)).toBe(true)
            expect(has(root)).toBe(g.allowMessagesDispatch)
            break
          case 'anthropic':
            expect(has(root) && has(`${root}/v1`)).toBe(true)
            break
          case 'gemini':
            expect(has(root) && has(`${root}/v1`)).toBe(true)
            break
          case 'antigravity':
            expect(has(`${root}/antigravity`)).toBe(true)
            expect(has(`${root}/v1`)).toBe(false)
            break
        }
        // 完整模型列表去哪查：用这把密钥请求 /v1/models
        expect(apiUrls).toContain(`${root}/v1/models`)
      })
    })
  }

  it('三种语言：行数、链接、模型名完全一致（只有措辞不同）', () => {
    const modelNames = (text: string) => text.match(/\b(?:gpt|claude|gemini|o4)-[\w.-]+/g)
    for (const { title, g, client, line } of MATRIX) {
      const [cn, hk, us] = (['zh-CN', 'zh-HK', 'en'] as const).map((lang) => prompt(lang, g, client, line))
      for (const other of [hk, us]) {
        expect(other.split('\n').length, title).toBe(cn.split('\n').length)
        expect(urlsIn(other), title).toEqual(urlsIn(cn))
        expect(modelNames(other), title).toEqual(modelNames(cn))
      }
    }
  })

  it('zh-HK 用「金鑰」，不混入简体的「密钥」；zh-CN 不出现「金鑰」', () => {
    for (const { g, client, line } of MATRIX) {
      const hk = prompt('zh-HK', g, client, line)
      expect(hk).toContain('金鑰')
      expect(hk).not.toMatch(/密钥|[钥设这软档请问误证们给应览认项]/)
      expect(prompt('zh-CN', g, client, line)).not.toContain('金鑰')
    }
  })
})

describe('复制详细版：骨架', () => {
  const lines = (lang: Lang, group: string, client: AiClient, line = LINES[0]) => prompt(lang, GROUPS[group], client, line).split('\n')

  it('示例：openai 分组、Codex、默认线路', () => {
    expect(lines('zh-CN', 'openai（没开调度）', 'codex')).toEqual([
      '请帮我把 Codex 接入 Hiyo codex（兼容 OpenAI 和 Anthropic 格式的 API 服务）。',
      `请先阅读：${ORIGIN}/docs/codex.md`,
      '按文档里的步骤一步步带我配置。',
      '',
      '接入信息：',
      `- OpenAI 兼容 Base URL：${ORIGIN}/v1`,
      '- 密钥：用 sk-你的密钥 占位，我会自己填，不要让我把真实密钥发给你。',
      '- 这个密钥可以调用的模型（部分）：gpt-5.6-sol、gpt-5.3-codex、gpt-5.5、gpt-5.6-luna、gpt-5.6-terra',
      `- 完整、最新的模型列表：用这个密钥请求 GET ${ORIGIN}/v1/models 查看，不要凭记忆填写模型名。`,
      '- config.toml 的 wire_api 用 responses，model_provider 和 model 要放在文件最前面；改完要完全退出 Codex 再打开，新建会话。',
      '',
      '要求：',
      '1. 要改的文件路径或设置项写清楚。',
      '2. 需要我的系统、工具等信息时直接问我。',
      `3. 最后告诉我怎么验证是否成功；出错时对照 ${ORIGIN}/docs/errors.md 排查。`,
      '4. 如果你本身就是运行在我电脑上的编程助手，可以直接帮我修改配置文件，但动手前先告诉我要改哪些内容。'
    ])
  })

  it('三段：开头、接入信息、要求，中间各空一行；没有多余的空行和行首尾空白', () => {
    for (const { g, client, line } of MATRIX) {
      const text = prompt('zh-CN', g, client, line)
      expect(text.split('\n\n')).toHaveLength(3)
      expect(text).not.toMatch(/\n\n\n|^\s|\s$|[ \t]\n/)
    }
  })

  it('第 4 条（你要是本机的编程助手可以直接改配置）只有 Claude Code、Codex、Cursor 有', () => {
    for (const { g, client, line } of MATRIX) {
      const text = prompt('zh-CN', g, client, line)
      expect(text.includes('4. 如果你本身就是运行在我电脑上的编程助手'), client).toBe(client === 'claude' || client === 'codex' || client === 'cursor')
    }
  })

  it('写代码调用的要求 1 是密钥放环境变量、最小示例、流式；其他工具是写清文件路径', () => {
    for (const { g, client, line } of MATRIX) {
      const text = prompt('zh-CN', g, client, line)
      expect(text.includes('1. 密钥从环境变量里读取'), client).toBe(client === 'code')
      expect(text.includes('1. 要改的文件路径或设置项写清楚。'), client).toBe(client !== 'code')
    }
  })

  it('没有价格、充值这类字眼；也不给站点没有的页面：列表地址是密钥自己能请求的 /v1/models，不是 /models.md', () => {
    for (const { g, client, line } of MATRIX) {
      const text = prompt('zh-CN', g, client, line)
      expect(text).not.toContain('models.md')
    }
  })

  it('openai 分组：Claude Code 的地址行排最前，开了调度才有 Anthropic 兼容地址', () => {
    const claude = lines('zh-CN', 'openai（开了调度）', 'claude')
    expect(claude[5]).toBe(`- Anthropic 兼容 Base URL（不带 /v1，Claude Code 用）：${ORIGIN}`)
    expect(claude[6]).toBe(`- OpenAI 兼容 Base URL：${ORIGIN}/v1`)
    const codex = lines('zh-CN', 'openai（开了调度）', 'codex')
    expect(codex[5]).toBe(`- OpenAI 兼容 Base URL：${ORIGIN}/v1`)
    expect(prompt('zh-CN', GROUPS['openai（没开调度）'], 'cursor', LINES[0])).not.toContain('Anthropic 兼容')
  })

  it('anthropic 分组：Cursor 把 OpenAI 兼容地址排最前，Claude Code 把 Anthropic 兼容地址排最前', () => {
    expect(lines('zh-CN', 'anthropic', 'cursor')[5]).toContain('OpenAI 兼容 Base URL')
    expect(lines('zh-CN', 'anthropic', 'claude')[5]).toContain('Anthropic 兼容 Base URL')
  })

  describe('分组专属的内容', () => {
    it('antigravity：只有 /antigravity 一个地址；文档里的接入地址要换成它；非 Claude Code 的工具只用 Anthropic / Gemini 格式', () => {
      for (const client of aiClientsForPlatform('antigravity')) {
        const text = prompt('zh-CN', GROUPS.antigravity, client, LINES[0])
        expect(text).toContain(`- 地址（不带 /v1；Claude Code 和 Gemini CLI 都填这个）：${ORIGIN}/antigravity`)
        expect(text).toContain(`文档里所有写着 ${ORIGIN} 的接入地址，配置时都换成 ${ORIGIN}/antigravity 使用。`)
        expect(text).not.toContain('OpenAI 兼容')
        expect(text.includes('只接受 Anthropic 和 Gemini 格式的请求'), client).toBe(client !== 'claude')
      }
    })

    it('gemini：写代码调用不读 OpenAI SDK 一份文档，改读 OpenAI SDK + 接口列表（有 Gemini 原生格式），并说明两种 SDK 各用哪个地址', () => {
      const text = prompt('zh-CN', GROUPS.gemini, 'code', LINES[0])
      expect(text).toContain(`请先阅读：${ORIGIN}/docs/openai-sdk.md ${ORIGIN}/docs/endpoints.md\n`)
      expect(text).toContain('Gemini 官方 SDK 用 Gemini 原生地址（不带 /v1）')
      expect(text).toContain(`- Gemini 原生地址（Gemini CLI 用，不带 /v1）：${ORIGIN}`)
    })

    it('antigravity：写代码调用读 API 调用示例（Anthropic 格式的那一节），不读 OpenAI SDK', () => {
      const text = prompt('zh-CN', GROUPS.antigravity, 'code', LINES[0])
      expect(text).toContain(`请先阅读：${ORIGIN}/docs/examples.md\n`)
      expect(text).not.toContain('openai-sdk.md')
    })

    it('anthropic：写代码调用说明 OpenAI SDK 和 Anthropic SDK 各用哪个地址', () => {
      expect(prompt('zh-CN', GROUPS.anthropic, 'code', LINES[0])).toContain('Anthropic SDK 用 Anthropic 兼容 Base URL（不带 /v1）')
    })

    it('不是 openai 分组时，提醒文档示例里的模型名只是示例（Claude Code 的文档没有示例模型名，不用提醒）', () => {
      for (const { g, client, line } of MATRIX) {
        const text = prompt('zh-CN', g, client, line)
        expect(text.includes(`文档示例里的模型名（如 ${OPENAI_CC_SWITCH_CODEX_MODEL}）只是示例`), `${g.platform} ${client}`).toBe(g.platform !== 'openai' && client !== 'claude')
      }
    })
  })

  it('聊天客户端和其他工具读文档目录，先问用的是哪一个', () => {
    for (const client of ['chat', 'other'] as const) {
      const text = prompt('zh-CN', GROUPS['openai（没开调度）'], client, LINES[0])
      expect(text).toContain(`请先阅读：${ORIGIN}/llms.txt\n先问我用的是`)
    }
    // 备用线路时目录链接也带线路
    expect(prompt('zh-CN', GROUPS.anthropic, 'chat', LINES[1])).toContain(`请先阅读：${ORIGIN}/llms.txt?endpoint=${CDN}\n`)
  })
})

describe('复制详细版：先读的文档章节', () => {
  it('每个工具 x 每个分组平台读的章节都存在，再加错误排查', () => {
    const sectionIds = new Set(DOC_GROUPS.flatMap((g) => g.sections.map((s) => s.id)))
    for (const platform of ['openai', 'anthropic', 'grok', 'gemini', 'antigravity', null]) {
      for (const client of AI_CLIENTS) {
        for (const id of aiPromptDocSections(client, platform)) expect(sectionIds.has(id), `${platform} ${client} ${id}`).toBe(true)
      }
    }
    expect(sectionIds.has(AI_PROMPT_ERRORS_SECTION)).toBe(true)
  })

  it('没有分组平台（空）按 anthropic 处理，不会漏掉地址', () => {
    const g: Group = { platform: '', allowMessagesDispatch: false, models: ['claude-sonnet-5'] }
    const text = prompt('zh-CN', g, 'chat', LINES[0])
    expect(text).toContain(`- Anthropic 兼容 Base URL（不带 /v1）：${ORIGIN}`)
  })
})

describe('pickAiPromptModels：按工具筛选、排序，最多 20 个', () => {
  const MIXED = [
    'gpt-image-2',
    'text-embedding-3-large',
    'gpt-5.6-luna',
    'claude-sonnet-5-20261001',
    'gpt-5.6-sol',
    'gemini-3-flash',
    'claude-opus-5',
    'gpt-5.5',
    'gpt-5.3-codex',
    'claude-sonnet-5',
    'tts-1',
    'claude-haiku-5',
    'o4-mini'
  ]

  it('Codex：只列 gpt-* 和带 codex 的对话模型，gpt-5.6-sol 第一个，其余按名字排序', () => {
    expect(pickAiPromptModels('codex', MIXED)).toEqual({
      list: ['gpt-5.6-sol', 'gpt-5.3-codex', 'gpt-5.5', 'gpt-5.6-luna'],
      partial: true
    })
    // 没有 gpt-5.6-sol 时不硬塞
    expect(pickAiPromptModels('codex', ['gpt-5.5', 'gpt-5.4']).list).toEqual(['gpt-5.4', 'gpt-5.5'])
  })

  it('Claude Code：claude-* 在前，名字最短的 sonnet 第一个；其余模型排在 claude-* 后面；不列图片、向量、语音', () => {
    expect(pickAiPromptModels('claude', MIXED).list).toEqual([
      'claude-sonnet-5',
      'claude-haiku-5',
      'claude-opus-5',
      'claude-sonnet-5-20261001',
      'gemini-3-flash',
      'gpt-5.3-codex',
      'gpt-5.5',
      'gpt-5.6-luna',
      'gpt-5.6-sol',
      'o4-mini'
    ])
  })

  it('Cursor、聊天客户端、其他：只列对话模型；写代码调用：全部', () => {
    const chat = ['claude-haiku-5', 'claude-opus-5', 'claude-sonnet-5', 'claude-sonnet-5-20261001', 'gemini-3-flash', 'gpt-5.3-codex', 'gpt-5.5', 'gpt-5.6-luna', 'gpt-5.6-sol', 'o4-mini']
    for (const client of ['cursor', 'chat', 'other'] as const) expect(pickAiPromptModels(client, MIXED).list).toEqual(chat)
    const code = pickAiPromptModels('code', MIXED)
    expect(code.list).toHaveLength(MIXED.length)
    expect(code.list).toContain('gpt-image-2')
    expect(code.partial).toBe(false)
  })

  it('最多 20 个，超出的标记为部分；数字按大小排（gpt-5.10 在 gpt-5.9 后面）', () => {
    const many = Array.from({ length: 30 }, (_, i) => `gpt-5.${i}`)
    const picked = pickAiPromptModels('code', many)
    expect(picked.list).toHaveLength(AI_PROMPT_MODEL_LIMIT)
    expect(picked.list.slice(0, 3)).toEqual(['gpt-5.0', 'gpt-5.1', 'gpt-5.2'])
    expect(picked.list.indexOf('gpt-5.10')).toBeGreaterThan(picked.list.indexOf('gpt-5.9'))
    expect(picked.partial).toBe(true)
    // 刚好 20 个不算部分
    expect(pickAiPromptModels('code', many.slice(0, 20))).toMatchObject({ partial: false })
    expect(pickAiPromptModels('code', many.slice(0, 20)).list).toHaveLength(20)
  })

  it('筛完一个都不剩时退回全部；去重、去空白、没有模型就是空', () => {
    expect(pickAiPromptModels('codex', ['claude-opus-5', ' claude-opus-5 ', '']).list).toEqual(['claude-opus-5'])
    expect(pickAiPromptModels('chat', ['gpt-image-2']).list).toEqual(['gpt-image-2'])
    expect(pickAiPromptModels('codex', [])).toEqual({ list: [], partial: false })
  })

  it('筛掉了一部分，或超过上限，才写「（部分）」；列全了就不写', () => {
    const full = prompt('zh-CN', GROUPS.gemini, 'code', LINES[0])
    expect(full).toContain('- 这个密钥可以调用的模型：gemini-3-flash、gemini-3-pro-image、gemini-3.1-pro-preview')
    const partial = prompt('zh-CN', GROUPS.gemini, 'chat', LINES[0])
    expect(partial).toContain('- 这个密钥可以调用的模型（部分）：gemini-3-flash、gemini-3.1-pro-preview')
  })

  it('没有取到模型时，没有「可以调用的模型」这一行，「完整模型列表」那行还在', () => {
    const text = prompt('zh-CN', GROUPS['openai（没开调度）'], 'codex', LINES[0], [])
    expect(text).not.toContain('可以调用的模型')
    expect(text).toContain('完整、最新的模型列表')
    expect(text.split('\n\n')).toHaveLength(3)
  })
})
