import type { GroupPlatform } from '@/types'

/** Codex 的首选模型：分组里有它就预选它；文档、手动配置的示例也用它。不会再写死进导入链接。 */
export const OPENAI_CC_SWITCH_CODEX_MODEL = 'gpt-5.6-sol'

export type CcSwitchClientType = 'claude' | 'gemini'

/** CC Switch 里能导入成哪几种客户端（导入链接的 app 参数） */
export type CcSwitchApp = 'claude' | 'codex' | 'gemini'

export interface CcSwitchImportConfig {
  app: string
  endpoint: string
}

export interface CcSwitchImportDeeplinkInput {
  baseUrl: string
  platform?: GroupPlatform | null
  clientType: CcSwitchClientType
  providerName: string
  apiKey: string
  usageScript: string
  /** 主模型；留空时不带这个参数，由 CC Switch 用它自己的默认模型 */
  model?: string
  /**
   * Claude 的三档模型，对应 CC Switch 导入链接的 haikuModel / sonnetModel / opusModel
   * （CC Switch 文档「供应商参数」里标注「仅 Claude」）。只在导入成 Claude 时带上，留空的不带。
   */
  haikuModel?: string
  sonnetModel?: string
  opusModel?: string
}

// CC Switch substitutes its stored provider endpoint for {{baseUrl}} before
// evaluating this script. A configured /v1 prefix must not become /v1/v1/usage.
// Keep other path prefixes (including /antigravity) for their matching usage route.
export const CC_SWITCH_USAGE_SCRIPT = `({
    request: {
      url: "{{baseUrl}}".replace(/\\/+$/, "").replace(/\\/v1$/, "") + "/v1/usage",
      method: "GET",
      headers: { "Authorization": "Bearer {{apiKey}}" }
    },
    extractor: function(response) {
      const remaining = response?.remaining ?? response?.quota?.remaining ?? response?.balance;
      const unit = response?.unit ?? response?.quota?.unit ?? "USD";
      return {
        isValid: response?.is_active ?? response?.isValid ?? true,
        remaining,
        unit
      };
    }
  })`

function withoutTrailingSlashes(baseUrl: string): string {
  return baseUrl.replace(/\/+$/, '')
}

export function resolveCcSwitchImportConfig(
  platform: GroupPlatform | undefined | null,
  clientType: CcSwitchClientType,
  baseUrl: string
): CcSwitchImportConfig {
  switch (platform || 'anthropic') {
    case 'antigravity':
      return {
        app: clientType === 'gemini' ? 'gemini' : 'claude',
        endpoint: `${baseUrl.replace(/\/+$/, '')}/antigravity`
      }
    case 'openai':
      return {
        app: 'codex',
        // CC Switch handles Codex request paths; preserve the provider's chosen prefix.
        // Adding /v1 to a root URL here can produce duplicate path segments.
        endpoint: withoutTrailingSlashes(baseUrl)
      }
    case 'gemini':
      return {
        app: 'gemini',
        endpoint: baseUrl
      }
    default:
      return {
        app: 'claude',
        endpoint: baseUrl
      }
  }
}

export function buildCcSwitchImportDeeplink(input: CcSwitchImportDeeplinkInput): string {
  const config = resolveCcSwitchImportConfig(input.platform, input.clientType, input.baseUrl)
  const entries: [string, string][] = [
    ['resource', 'provider'],
    ['app', config.app],
    ['name', input.providerName],
    ['homepage', input.baseUrl],
    ['endpoint', config.endpoint],
    ['apiKey', input.apiKey],
    ['configFormat', 'json'],
    ['usageEnabled', 'true'],
    ['usageScript', btoa(input.usageScript)],
    ['usageAutoInterval', '30']
  ]

  // 模型参数紧跟在 app 后面，只带非空的；三档模型只有 Claude 有
  const modelEntries: [string, string | undefined][] = [['model', input.model]]
  if (config.app === 'claude') {
    modelEntries.push(['haikuModel', input.haikuModel], ['sonnetModel', input.sonnetModel], ['opusModel', input.opusModel])
  }
  const picked = modelEntries.flatMap(([key, value]): [string, string][] => {
    const v = value?.trim()
    return v ? [[key, v]] : []
  })
  entries.splice(2, 0, ...picked)

  return `ccswitch://v1/import?${new URLSearchParams(entries).toString()}`
}

// ---------------------------------------------------------------------------
// 按这把密钥分组里的模型，给每个客户端的模型下拉做选项和预选。
// 规则来自对照 rivo 的差距表（1.3 预选、6.3 按工具筛选）：
// - Claude：主模型取 Sonnet 档，没有就取 Opus 档；Haiku / Sonnet / Opus 各取名字里带这个词的、名字最短的一个
//   （名字最短的通常是不带日期后缀的那个）；
// - Codex：优先 gpt-5.6-sol，其次 gpt-5.5、gpt-5、codex 里名字最短的；
// - Gemini：名字里带 gemini 的、名字最短的一个。
// 找不到的留空，导入后由 CC Switch 用它自己的默认模型。
// ---------------------------------------------------------------------------

/** 一个客户端要填的模型：主模型，以及 Claude 的三档 */
export interface CcSwitchModelPick {
  model: string
  haikuModel: string
  sonnetModel: string
  opusModel: string
}

export const NO_CC_SWITCH_MODELS: Readonly<CcSwitchModelPick> = Object.freeze({
  model: '',
  haikuModel: '',
  sonnetModel: '',
  opusModel: ''
})

/** 图片、向量、语音这类不是对话用的模型，不进 Codex 的选项 */
export const NON_CHAT_MODEL = /image|embed|tts|whisper|audio|moderation|dall-?e|realtime|transcribe|speech|rerank/i

const byName = (a: string, b: string) => a.localeCompare(b, 'en', { numeric: true })

/** 候选里名字符合 re 的、名字最短的一个（一样长时按字母序靠前的）；没有就是空串 */
function shortestMatch(list: string[], re: RegExp): string {
  return list.filter((m) => re.test(m)).sort((a, b) => a.length - b.length)[0] ?? ''
}

/**
 * 某个客户端的模型下拉里该列哪些：去重、按字母排序；
 * Claude 把名字里带 claude 的排前面；Codex 只列 gpt-* 和带 codex 的对话模型；Gemini 只列带 gemini 的。
 * 筛选后一个都不剩时退回列出全部，不让下拉变空。
 */
export function ccSwitchModelOptions(app: CcSwitchApp, models: readonly string[]): string[] {
  const all = [...new Set(models.map((m) => m.trim()).filter(Boolean))].sort(byName)
  switch (app) {
    case 'claude': {
      const claude = all.filter((m) => /claude/i.test(m))
      return [...claude, ...all.filter((m) => !/claude/i.test(m))]
    }
    case 'codex': {
      const chat = all.filter((m) => /^gpt-|codex/i.test(m) && !NON_CHAT_MODEL.test(m))
      return chat.length > 0 ? chat : all
    }
    case 'gemini': {
      const gemini = all.filter((m) => /gemini/i.test(m))
      return gemini.length > 0 ? gemini : all
    }
  }
}

/** 按分组里的模型给某个客户端预选；没有合适的就留空 */
export function pickCcSwitchModels(app: CcSwitchApp, models: readonly string[]): CcSwitchModelPick {
  const options = ccSwitchModelOptions(app, models)
  switch (app) {
    case 'claude': {
      const haikuModel = shortestMatch(options, /haiku/i)
      const sonnetModel = shortestMatch(options, /sonnet/i)
      const opusModel = shortestMatch(options, /opus/i)
      return { model: sonnetModel || opusModel, haikuModel, sonnetModel, opusModel }
    }
    case 'codex': {
      const model =
        options.find((m) => m.toLowerCase() === OPENAI_CC_SWITCH_CODEX_MODEL) ||
        shortestMatch(options, /gpt-5\.5/i) ||
        shortestMatch(options, /gpt-5/i) ||
        shortestMatch(options, /codex/i)
      return { ...NO_CC_SWITCH_MODELS, model }
    }
    case 'gemini':
      return { ...NO_CC_SWITCH_MODELS, model: shortestMatch(options, /gemini/i) }
  }
}
