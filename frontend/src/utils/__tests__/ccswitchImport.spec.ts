import { describe, expect, it } from 'vitest'
import {
  CC_SWITCH_USAGE_SCRIPT,
  OPENAI_CC_SWITCH_CODEX_MODEL,
  buildCcSwitchImportDeeplink,
  ccSwitchModelOptions,
  pickCcSwitchModels
} from '@/utils/ccswitchImport'
import type { GroupPlatform } from '@/types'

function paramsFromDeeplink(deeplink: string): URLSearchParams {
  const query = deeplink.split('?')[1] || ''
  return new URLSearchParams(query)
}

describe('ccswitchImport utils', () => {
  it('keeps gpt-5.6-sol as the preferred Codex model', () => {
    expect(OPENAI_CC_SWITCH_CODEX_MODEL).toBe('gpt-5.6-sol')
  })

  const baseInput = {
    baseUrl: 'https://api.example.com',
    providerName: 'Sub2API',
    apiKey: 'sk-test',
    usageScript: 'return true'
  }

  it.each([
    ['https://api.example.com', 'https://api.example.com'],
    ['https://api.example.com/', 'https://api.example.com'],
    ['https://api.example.com/v1', 'https://api.example.com/v1'],
    ['https://api.example.com/v1/', 'https://api.example.com/v1']
  ])('keeps Codex imports on the configured endpoint for base URL %s', (baseUrl, endpoint) => {
    const params = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({
        ...baseInput,
        baseUrl,
        platform: 'openai',
        clientType: 'claude'
      })
    )

    expect(params.get('resource')).toBe('provider')
    expect(params.get('app')).toBe('codex')
    expect(params.get('endpoint')).toBe(endpoint)
    expect(params.get('homepage')).toBe(baseUrl)
    expect(params.get('apiKey')).toBe(baseInput.apiKey)
    // 不再替 Codex 写死模型：没选就不带，由 CC Switch 用它自己的默认模型
    expect(params.has('model')).toBe(false)
    expect(atob(params.get('usageScript') || '')).toBe(baseInput.usageScript)
  })

  it('passes the chosen Codex model through', () => {
    const params = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({ ...baseInput, platform: 'openai', clientType: 'claude', model: ' gpt-5.5 ' })
    )
    expect(params.get('app')).toBe('codex')
    expect(params.get('model')).toBe('gpt-5.5')
    // 三档模型只属于 Claude
    expect(params.has('haikuModel')).toBe(false)
  })

  it.each([
    { platform: 'anthropic' as GroupPlatform, clientType: 'claude' as const, app: 'claude' },
    { platform: 'gemini' as GroupPlatform, clientType: 'gemini' as const, app: 'gemini' }
  ])('does not add a model parameter for $platform imports', ({ platform, clientType, app }) => {
    const params = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({
        ...baseInput,
        platform,
        clientType
      })
    )

    expect(params.get('app')).toBe(app)
    expect(params.get('endpoint')).toBe(baseInput.baseUrl)
    expect(params.has('model')).toBe(false)
  })

  it('keeps Antigravity imports on the selected client endpoint without a model parameter', () => {
    const params = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({
        ...baseInput,
        platform: 'antigravity',
        clientType: 'gemini'
      })
    )

    expect(params.get('app')).toBe('gemini')
    expect(params.get('endpoint')).toBe(`${baseInput.baseUrl}/antigravity`)
    expect(params.has('model')).toBe(false)
  })
})

describe('CC Switch 导入链接里的模型参数', () => {
  const baseInput = {
    baseUrl: 'https://api.example.com',
    providerName: 'Sub2API',
    apiKey: 'sk-test',
    usageScript: 'return true'
  }
  const params = (input: Partial<Parameters<typeof buildCcSwitchImportDeeplink>[0]>) =>
    paramsFromDeeplink(buildCcSwitchImportDeeplink({ ...baseInput, clientType: 'claude', ...input }))

  it('Claude：主模型和 Haiku / Sonnet / Opus 三档分别用 model / haikuModel / sonnetModel / opusModel（CC Switch 文档里的参数名）', () => {
    const p = params({
      platform: 'anthropic',
      model: 'claude-sonnet-5',
      haikuModel: 'claude-haiku-4-5',
      sonnetModel: 'claude-sonnet-5',
      opusModel: 'claude-opus-5'
    })
    expect(p.get('app')).toBe('claude')
    expect(p.get('model')).toBe('claude-sonnet-5')
    expect(p.get('haikuModel')).toBe('claude-haiku-4-5')
    expect(p.get('sonnetModel')).toBe('claude-sonnet-5')
    expect(p.get('opusModel')).toBe('claude-opus-5')
  })

  it('留空（空串、纯空白、没传）的不带', () => {
    const p = params({ platform: 'anthropic', model: 'claude-sonnet-5', haikuModel: '', sonnetModel: '   ' })
    expect(p.get('model')).toBe('claude-sonnet-5')
    for (const k of ['haikuModel', 'sonnetModel', 'opusModel']) expect(p.has(k)).toBe(false)
    const none = params({ platform: 'anthropic', model: '', haikuModel: '', sonnetModel: '', opusModel: '' })
    for (const k of ['model', 'haikuModel', 'sonnetModel', 'opusModel']) expect(none.has(k)).toBe(false)
  })

  it('三档模型紧跟在 app 后面，其余参数的相对顺序不变', () => {
    const keys = [
      ...new URLSearchParams(
        buildCcSwitchImportDeeplink({
          ...baseInput,
          clientType: 'claude',
          platform: 'anthropic',
          model: 'm',
          haikuModel: 'h',
          sonnetModel: 's',
          opusModel: 'o'
        }).split('?')[1]
      ).keys()
    ]
    expect(keys.slice(0, 7)).toEqual(['resource', 'app', 'model', 'haikuModel', 'sonnetModel', 'opusModel', 'name'])
    expect(keys.slice(7)).toEqual(['homepage', 'endpoint', 'apiKey', 'configFormat', 'usageEnabled', 'usageScript', 'usageAutoInterval'])
  })

  it('antigravity 选 Claude 时也带三档；选 Gemini 或 Codex 时三档不带，即使传了也忽略', () => {
    const claude = params({ platform: 'antigravity', clientType: 'claude', model: 'm', haikuModel: 'h' })
    expect(claude.get('app')).toBe('claude')
    expect(claude.get('haikuModel')).toBe('h')
    const gemini = params({ platform: 'antigravity', clientType: 'gemini', model: 'gemini-3-pro', haikuModel: 'h', sonnetModel: 's', opusModel: 'o' })
    expect(gemini.get('app')).toBe('gemini')
    expect(gemini.get('model')).toBe('gemini-3-pro')
    for (const k of ['haikuModel', 'sonnetModel', 'opusModel']) expect(gemini.has(k)).toBe(false)
    const codex = params({ platform: 'openai', haikuModel: 'h', sonnetModel: 's', opusModel: 'o' })
    for (const k of ['haikuModel', 'sonnetModel', 'opusModel']) expect(codex.has(k)).toBe(false)
  })
})

describe('pickCcSwitchModels：按分组里的模型预选', () => {
  it('Claude：三档各取名字里带 haiku / sonnet / opus 的、名字最短的；主模型取 Sonnet，没有就取 Opus', () => {
    expect(
      pickCcSwitchModels('claude', ['claude-opus-5', 'claude-sonnet-5-20261001', 'claude-sonnet-5', 'claude-haiku-4-5', 'claude-haiku-4-5-20251001'])
    ).toEqual({
      model: 'claude-sonnet-5',
      haikuModel: 'claude-haiku-4-5',
      sonnetModel: 'claude-sonnet-5',
      opusModel: 'claude-opus-5'
    })
    // 没有 Sonnet：主模型用 Opus
    expect(pickCcSwitchModels('claude', ['claude-opus-5', 'claude-haiku-4-5'])).toEqual({
      model: 'claude-opus-5',
      haikuModel: 'claude-haiku-4-5',
      sonnetModel: '',
      opusModel: 'claude-opus-5'
    })
    // Sonnet 和 Opus 都没有：主模型留空，不拿 Haiku 顶替
    expect(pickCcSwitchModels('claude', ['claude-haiku-4-5']).model).toBe('')
    // 大小写不敏感
    expect(pickCcSwitchModels('claude', ['Claude-Sonnet-5']).sonnetModel).toBe('Claude-Sonnet-5')
  })

  it('Claude：名字一样长时取字母序靠前的，结果不依赖分组里模型的先后', () => {
    const a = pickCcSwitchModels('claude', ['claude-sonnet-b', 'claude-sonnet-a'])
    const b = pickCcSwitchModels('claude', ['claude-sonnet-a', 'claude-sonnet-b'])
    expect(a.sonnetModel).toBe('claude-sonnet-a')
    expect(b).toEqual(a)
  })

  it('Codex：优先 gpt-5.6-sol，其次 gpt-5.5、gpt-5、codex 里名字最短的', () => {
    const codex = (models: string[]) => pickCcSwitchModels('codex', models)
    expect(codex(['gpt-5.5', 'gpt-5.6-luna', 'gpt-5.6-sol', 'gpt-5.6-terra'])).toEqual({
      model: 'gpt-5.6-sol',
      haikuModel: '',
      sonnetModel: '',
      opusModel: ''
    })
    expect(codex(['GPT-5.6-SOL']).model).toBe('GPT-5.6-SOL')
    expect(codex(['gpt-5.6-luna', 'gpt-5.5-mini', 'gpt-5.5']).model).toBe('gpt-5.5')
    expect(codex(['gpt-5.6-terra', 'gpt-5.6-luna', 'gpt-5-mini']).model).toBe('gpt-5-mini')
    expect(codex(['gpt-4.1', 'gpt-4.1-codex-max', 'gpt-4.1-codex']).model).toBe('gpt-4.1-codex')
    // 分组里没有 gpt-5.6-sol 时不会硬塞它
    expect(codex(['gpt-5.5']).model).toBe('gpt-5.5')
    // 都不沾边：留空，交给 CC Switch
    expect(codex(['gpt-4.1']).model).toBe('')
  })

  it('Gemini：名字里带 gemini 的、名字最短的一个', () => {
    expect(pickCcSwitchModels('gemini', ['gemini-3-pro-preview', 'gemini-3-pro', 'gemini-2.5-flash-lite']).model).toBe('gemini-3-pro')
    expect(pickCcSwitchModels('gemini', ['gemini-3-pro']).haikuModel).toBe('')
  })

  it('没有模型：全部留空', () => {
    for (const app of ['claude', 'codex', 'gemini'] as const) {
      expect(pickCcSwitchModels(app, [])).toEqual({ model: '', haikuModel: '', sonnetModel: '', opusModel: '' })
    }
  })

  it('筛选后一个都不剩时退回全部模型（预选仍然按各自的规则，找不到就留空）', () => {
    expect(pickCcSwitchModels('claude', ['gpt-5.6-sol']).model).toBe('')
    expect(pickCcSwitchModels('gemini', ['gpt-5.6-sol']).model).toBe('')
  })
})

describe('ccSwitchModelOptions：每个客户端下拉里的选项', () => {
  it('去重、去掉空白，按字母排序（数字按大小）', () => {
    expect(ccSwitchModelOptions('codex', [' gpt-5.6-sol', 'gpt-5.6-sol', 'gpt-5.10', 'gpt-5.5', '', 'gpt-5.9'])).toEqual([
      'gpt-5.5',
      'gpt-5.6-sol',
      'gpt-5.9',
      'gpt-5.10'
    ])
  })

  it('Claude：不筛掉别的模型，名字里带 claude 的排前面', () => {
    expect(ccSwitchModelOptions('claude', ['gemini-3-pro', 'claude-sonnet-5', 'claude-opus-5'])).toEqual([
      'claude-opus-5',
      'claude-sonnet-5',
      'gemini-3-pro'
    ])
  })

  it('Codex：只列 gpt-* 和带 codex 的对话模型，图片、语音、向量这类不列', () => {
    expect(
      ccSwitchModelOptions('codex', [
        'gpt-image-2',
        'gpt-5.6-sol',
        'gpt-4o-mini-tts',
        'gpt-4o-transcribe',
        'gpt-4o-realtime-preview',
        'text-embedding-3-large',
        'claude-sonnet-5',
        'gpt-5-codex'
      ])
    ).toEqual(['gpt-5-codex', 'gpt-5.6-sol'])
  })

  it('Gemini：只列带 gemini 的', () => {
    expect(ccSwitchModelOptions('gemini', ['claude-opus-5', 'gemini-3-pro', 'gemini-2.5-flash'])).toEqual(['gemini-2.5-flash', 'gemini-3-pro'])
  })

  it('筛选后一个都不剩时退回列出全部，不让下拉变空；本来就没有模型时是空数组', () => {
    expect(ccSwitchModelOptions('codex', ['gpt-image-2'])).toEqual(['gpt-image-2'])
    expect(ccSwitchModelOptions('gemini', ['gpt-5.6-sol', 'claude-opus-5'])).toEqual(['claude-opus-5', 'gpt-5.6-sol'])
    expect(ccSwitchModelOptions('claude', [])).toEqual([])
  })
})

describe('CC Switch usage script', () => {
  type UsageConfig = {
    request: { url: string; method: string; headers: { Authorization: string } }
    extractor: (response: Record<string, unknown>) => Record<string, unknown>
  }

  function usageConfigFor(endpoint: string, script = CC_SWITCH_USAGE_SCRIPT): UsageConfig {
    const substituted = script
      .replaceAll('{{baseUrl}}', endpoint)
      .replaceAll('{{apiKey}}', 'sk-test')
    // Match CC Switch's template substitution and script evaluation without sending HTTP.
    // eslint-disable-next-line no-new-func
    return new Function('return ' + substituted)() as UsageConfig
  }

  it.each([
    ['https://api.example.com', 'https://api.example.com', 'https://api.example.com/v1/usage'],
    ['https://api.example.com/', 'https://api.example.com', 'https://api.example.com/v1/usage'],
    ['https://api.example.com/v1', 'https://api.example.com/v1', 'https://api.example.com/v1/usage'],
    ['https://api.example.com/v1/', 'https://api.example.com/v1', 'https://api.example.com/v1/usage'],
    ['https://api.example.com/proxy', 'https://api.example.com/proxy', 'https://api.example.com/proxy/v1/usage'],
    ['https://api.example.com/proxy/', 'https://api.example.com/proxy', 'https://api.example.com/proxy/v1/usage'],
    ['https://api.example.com/proxy/v1', 'https://api.example.com/proxy/v1', 'https://api.example.com/proxy/v1/usage'],
    ['https://api.example.com/proxy/v1/', 'https://api.example.com/proxy/v1', 'https://api.example.com/proxy/v1/usage']
  ])('keeps the configured import endpoint and queries one /v1/usage for %s', (baseUrl, endpoint, usageUrl) => {
    const params = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({
        baseUrl,
        platform: 'openai',
        clientType: 'claude',
        providerName: 'Sub2API',
        apiKey: 'sk-test',
        usageScript: CC_SWITCH_USAGE_SCRIPT
      })
    )
    expect(params.get('endpoint')).toBe(endpoint)
    expect(params.get('homepage')).toBe(baseUrl)
    const importedScript = atob(params.get('usageScript') || '')
    expect(importedScript).toBe(CC_SWITCH_USAGE_SCRIPT)
    const config = usageConfigFor(endpoint, importedScript)
    expect(config.request.url).toBe(usageUrl)
    expect(config.request.method).toBe('GET')
    expect(config.request.headers.Authorization).toBe('Bearer sk-test')
  })

  it.each([
    { platform: 'anthropic' as GroupPlatform, clientType: 'claude' as const, endpoint: 'https://api.example.com', usageUrl: 'https://api.example.com/v1/usage' },
    { platform: 'gemini' as GroupPlatform, clientType: 'gemini' as const, endpoint: 'https://api.example.com', usageUrl: 'https://api.example.com/v1/usage' },
    { platform: 'antigravity' as GroupPlatform, clientType: 'claude' as const, endpoint: 'https://api.example.com/antigravity', usageUrl: 'https://api.example.com/antigravity/v1/usage' }
  ])('preserves the $platform usage route', ({ platform, clientType, endpoint, usageUrl }) => {
    const params = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({
        baseUrl: 'https://api.example.com',
        platform,
        clientType,
        providerName: 'Sub2API',
        apiKey: 'sk-test',
        usageScript: CC_SWITCH_USAGE_SCRIPT
      })
    )
    expect(params.get('endpoint')).toBe(endpoint)
    expect(usageConfigFor(endpoint, atob(params.get('usageScript') || '')).request.url).toBe(usageUrl)
  })

  it('preserves balance extraction from the imported script', () => {
    const config = usageConfigFor('https://api.example.com/v1')
    expect(config.extractor({
      quota: { remaining: 12, unit: 'USD' },
      is_active: false
    })).toEqual({ isValid: false, remaining: 12, unit: 'USD' })
  })
})
