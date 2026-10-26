import { describe, expect, it } from 'vitest'
import {
  CC_SWITCH_ENABLE_ON_IMPORT,
  CC_SWITCH_USAGE_INTERVAL_MINUTES,
  CC_SWITCH_USAGE_SCRIPT,
  OPENAI_CC_SWITCH_CODEX_MODEL,
  buildCcSwitchImportDeeplink,
  ccSwitchClientsForPlatform,
  ccSwitchModelOptions,
  pickCcSwitchModels,
  type CcSwitchApp
} from '@/utils/ccswitchImport'
import type { GroupPlatform } from '@/types'
import en from '@/i18n/locales/en'
import zhCN from '@/i18n/locales/zh-CN'
import zhHK from '@/i18n/locales/zh-HK'

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
        clientType: 'codex'
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
      buildCcSwitchImportDeeplink({ ...baseInput, platform: 'openai', clientType: 'codex', model: ' gpt-5.5 ' })
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
    expect(keys.slice(7)).toEqual(['homepage', 'endpoint', 'apiKey', 'configFormat', 'enabled', 'usageEnabled', 'usageScript', 'usageAutoInterval'])
  })

  it('antigravity 选 Claude 时也带三档；选 Gemini 或 Codex 时三档不带，即使传了也忽略', () => {
    const claude = params({ platform: 'antigravity', clientType: 'claude', model: 'm', haikuModel: 'h' })
    expect(claude.get('app')).toBe('claude')
    expect(claude.get('haikuModel')).toBe('h')
    const gemini = params({ platform: 'antigravity', clientType: 'gemini', model: 'gemini-3-pro', haikuModel: 'h', sonnetModel: 's', opusModel: 'o' })
    expect(gemini.get('app')).toBe('gemini')
    expect(gemini.get('model')).toBe('gemini-3-pro')
    for (const k of ['haikuModel', 'sonnetModel', 'opusModel']) expect(gemini.has(k)).toBe(false)
    const codex = params({ platform: 'openai', clientType: 'codex', haikuModel: 'h', sonnetModel: 's', opusModel: 'o' })
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
        clientType: 'codex',
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

// ---------------------------------------------------------------------------
// 导入语义（enabled / 余额刷新间隔 / openai 分组开了调度时的 Claude）
// ---------------------------------------------------------------------------

describe('CC Switch 导入后是否立即切换（enabled）', () => {
  it('Claude、Gemini 立即切换，Codex 先不切换；表里就这三项', () => {
    expect(CC_SWITCH_ENABLE_ON_IMPORT).toEqual({ claude: true, codex: false, gemini: true })
  })

  it('余额自动刷新间隔是 5 分钟', () => {
    expect(CC_SWITCH_USAGE_INTERVAL_MINUTES).toBe(5)
  })
})

describe('CC Switch 各分组可导入的客户端', () => {
  it.each<[string | null, boolean | undefined, CcSwitchApp[]]>([
    // openai：没开调度（包括字段缺失）只有 Codex；开了调度再补上 Claude，Codex 仍排第一（默认选中）
    ['openai', undefined, ['codex']],
    ['openai', false, ['codex']],
    ['openai', true, ['codex', 'claude']],
    // 调度开关只对 openai 分组有意义，其他平台不受它影响
    ['anthropic', undefined, ['claude']],
    ['anthropic', true, ['claude']],
    ['grok', true, ['claude']],
    ['gemini', undefined, ['gemini']],
    ['gemini', true, ['gemini']],
    ['antigravity', undefined, ['claude', 'gemini']],
    ['antigravity', true, ['claude', 'gemini']],
    [null, undefined, ['claude']],
    ['unknown', undefined, ['claude']]
  ])('%s，开了调度=%s -> %j', (platform, dispatch, expected) => {
    expect(ccSwitchClientsForPlatform(platform, { allowMessagesDispatch: dispatch })).toEqual(expected)
    // 不传选项等于没开调度
    if (!dispatch) expect(ccSwitchClientsForPlatform(platform)).toEqual(expected)
  })

  // 生产里分组 30（codex pro+plus）、27（稳定 luna 专用）是 openai 分组、没开 Messages 调度：
  // cxw 2026-10-04 决定它们的 Claude Code 保持隐藏。这里按它们的真实形状钉住：不管字段是 false、缺失还是 null，都只有 Codex。
  it.each([
    ['分组 30，调度 false', { id: 30, platform: 'openai', allow_messages_dispatch: false }],
    ['分组 27，调度字段缺失', { id: 27, platform: 'openai' }],
    ['分组 27，调度 null', { id: 27, platform: 'openai', allow_messages_dispatch: null }]
  ])('%s：只有 Codex，没有 Claude', (_label, group) => {
    const clients = ccSwitchClientsForPlatform(group.platform, { allowMessagesDispatch: group.allow_messages_dispatch as boolean | undefined })
    expect(clients).toEqual(['codex'])
    expect(clients).not.toContain('claude')
  })
})

describe('CC Switch 导入链接：分组平台 × 调度开关 × 客户端，逐项断言', () => {
  const ROOT = 'https://api.example.com'
  // 管理员把地址配成带 /v1 的：Codex 沿用它，其他客户端用 API 根地址（页签就是这样选 baseUrl 的）
  const CONFIGURED = 'https://api.example.com/v1'
  const KEY = 'sk-test-KEY'
  const NAME = 'Hiyo - X'
  const SCRIPT = CC_SWITCH_USAGE_SCRIPT

  // 页签预选后的模型，用来确认「哪个客户端带哪些模型参数」
  const PICKED = { model: 'm-main', haikuModel: 'm-haiku', sonnetModel: 'm-sonnet', opusModel: 'm-opus' }

  const rows: [string | null, boolean | undefined, CcSwitchApp][] = []
  for (const [platform, dispatch] of [
    ['openai', undefined],
    ['openai', false],
    ['openai', true],
    ['anthropic', undefined],
    ['anthropic', true],
    ['grok', undefined],
    ['gemini', undefined],
    ['antigravity', undefined],
    [null, undefined]
  ] as const) {
    for (const client of ccSwitchClientsForPlatform(platform, { allowMessagesDispatch: dispatch })) rows.push([platform, dispatch, client])
  }

  it('矩阵覆盖了每个平台的每个客户端，包括 openai 开了调度时的 Claude', () => {
    expect(rows.map(([p, d, c]) => `${p}/${d ?? '-'}/${c}`)).toEqual([
      'openai/-/codex',
      'openai/false/codex',
      'openai/true/codex',
      'openai/true/claude',
      'anthropic/-/claude',
      'anthropic/true/claude',
      'grok/-/claude',
      'gemini/-/gemini',
      'antigravity/-/claude',
      'antigravity/-/gemini',
      'null/-/claude'
    ])
  })

  it.each(rows)('%s，开了调度=%s，导入成 %s：所有参数', (platform, _dispatch, client) => {
    const baseUrl = client === 'codex' ? CONFIGURED : ROOT
    const link = buildCcSwitchImportDeeplink({
      baseUrl,
      platform: platform as GroupPlatform | null,
      clientType: client,
      providerName: NAME,
      apiKey: KEY,
      usageScript: SCRIPT,
      ...PICKED
    })
    expect(link.startsWith('ccswitch://v1/import?')).toBe(true)
    const p = paramsFromDeeplink(link)

    const endpoint = platform === 'antigravity' ? `${ROOT}/antigravity` : baseUrl
    const expected: Record<string, string> = {
      resource: 'provider',
      app: client,
      name: NAME,
      homepage: baseUrl,
      endpoint,
      apiKey: KEY,
      configFormat: 'json',
      // Claude / Gemini 立即切换，Codex 不切换
      enabled: client === 'codex' ? 'false' : 'true',
      usageEnabled: 'true',
      usageAutoInterval: '5',
      model: PICKED.model
    }
    // 三档模型只有导入成 Claude 才带
    if (client === 'claude') Object.assign(expected, { haikuModel: PICKED.haikuModel, sonnetModel: PICKED.sonnetModel, opusModel: PICKED.opusModel })

    for (const [k, v] of Object.entries(expected)) expect(p.get(k), `${k}`).toBe(v)
    // 没有多带别的参数
    expect([...p.keys()].sort()).toEqual([...Object.keys(expected), 'usageScript'].sort())
    // 用量脚本原样随链接带上，请求的是 /v1/usage
    expect(atob(p.get('usageScript') || '')).toBe(SCRIPT)
  })

  it('openai 分组导入成 Claude：用 API 根地址，不用带 /v1 的 Codex 地址', () => {
    const p = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({ baseUrl: ROOT, platform: 'openai', clientType: 'claude', providerName: NAME, apiKey: KEY, usageScript: SCRIPT })
    )
    expect(p.get('app')).toBe('claude')
    expect(p.get('endpoint')).toBe(ROOT)
    expect(p.get('enabled')).toBe('true')
    // 没选模型就一个都不带：Claude Code 用它自己的模型名，调度那边按分组配置换成实际模型
    for (const k of ['model', 'haikuModel', 'sonnetModel', 'opusModel']) expect(p.has(k)).toBe(false)
  })

  it('同一个 openai 分组：Codex 和 Claude 两条链接只在 app / endpoint / enabled（和三档模型）上不同', () => {
    const codex = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({ baseUrl: CONFIGURED, platform: 'openai', clientType: 'codex', providerName: NAME, apiKey: KEY, usageScript: SCRIPT })
    )
    const claude = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({ baseUrl: ROOT, platform: 'openai', clientType: 'claude', providerName: NAME, apiKey: KEY, usageScript: SCRIPT })
    )
    const differing = [...codex.keys()].filter((k) => codex.get(k) !== claude.get(k)).sort()
    expect(differing).toEqual(['app', 'enabled', 'endpoint', 'homepage'])
    expect(codex.get('enabled')).toBe('false')
    expect(claude.get('enabled')).toBe('true')
  })
})

describe('CC Switch 余额查询（随链接导入的用量脚本）', () => {
  type UsageConfig = {
    request: { url: string; method: string; headers: { Authorization: string } }
    extractor: (response: Record<string, unknown>) => Record<string, unknown>
  }
  const load = (endpoint = 'https://api.example.com'): UsageConfig =>
    // eslint-disable-next-line no-new-func
    new Function('return ' + CC_SWITCH_USAGE_SCRIPT.replaceAll('{{baseUrl}}', endpoint).replaceAll('{{apiKey}}', 'sk-test'))() as UsageConfig

  it('链接里是 usageEnabled=true、每 5 分钟刷新，脚本请求 /v1/usage', () => {
    const p = paramsFromDeeplink(
      buildCcSwitchImportDeeplink({ baseUrl: 'https://api.example.com', platform: 'anthropic', clientType: 'claude', providerName: 'n', apiKey: 'k', usageScript: CC_SWITCH_USAGE_SCRIPT })
    )
    expect(p.get('usageEnabled')).toBe('true')
    expect(p.get('usageAutoInterval')).toBe('5')
    expect(p.has('usageBaseUrl')).toBe(false)
    const config = load()
    expect(config.request.url).toBe('https://api.example.com/v1/usage')
  })

  it('单位跟着接口返回的 unit 走，脚本里不写死币种', () => {
    expect(CC_SWITCH_USAGE_SCRIPT).not.toMatch(/USD|CNY|RMB|¥|美元|人民币/)
    const { extractor } = load()
    // 接口说是什么单位就是什么单位：今天是 USD，记账单位换成人民币后脚本不用改
    expect(extractor({ remaining: 12.5, unit: 'USD' })).toEqual({ isValid: true, remaining: 12.5, unit: 'USD' })
    expect(extractor({ remaining: 12.5, unit: 'CNY' })).toEqual({ isValid: true, remaining: 12.5, unit: 'CNY' })
    expect(extractor({ quota: { remaining: 7, unit: 'CNY' } })).toEqual({ isValid: true, remaining: 7, unit: 'CNY' })
    // 接口没给单位：不编一个，交给 CC Switch 只显示数字
    expect(extractor({ remaining: 3 })).toEqual({ isValid: true, remaining: 3, unit: null })
  })

  it('余额读 remaining，其次 quota.remaining、balance；is_active / isValid 决定是否有效', () => {
    const { extractor } = load()
    expect(extractor({ remaining: 1, balance: 9, quota: { remaining: 5 } }).remaining).toBe(1)
    expect(extractor({ balance: 9, quota: { remaining: 5 } }).remaining).toBe(5)
    expect(extractor({ balance: 9 }).remaining).toBe(9)
    expect(extractor({ remaining: 1, is_active: false }).isValid).toBe(false)
    expect(extractor({ remaining: 1, isValid: false }).isValid).toBe(false)
  })
})

describe('CC Switch 页签文案（三种语言）', () => {
  const locales = { 'zh-CN': zhCN, 'zh-HK': zhHK, en } as const
  const NEW_KEYS = ['title', 'intro', 'codexWarning', 'switchNote', 'balanceNote'] as const

  it.each(Object.entries(locales))('%s：新增的几句都有', (_name, locale) => {
    const ccs = locale.keyOnboarding.ccs as Record<string, string>
    for (const k of NEW_KEYS) expect(typeof ccs[k] === 'string' && ccs[k].length > 0, k).toBe(true)
  })

  it.each(Object.entries(locales))('%s：不写死币种、符号、换算倍数，不提上游和内部机制', (_name, locale) => {
    const ccs = locale.keyOnboarding.ccs as Record<string, string>
    for (const k of NEW_KEYS) {
      expect(ccs[k], k).not.toMatch(/[$¥￥]|USD|CNY|RMB|\b13\b|倍率|匯率|汇率|換算|换算|multiplier|exchange rate/i)
      expect(ccs[k], k).not.toMatch(/上游|账号池|帳號池|号池|upstream|pool|channel|渠道/i)
    }
  })

  it.each(Object.entries(locales))('%s：刷新间隔用 {minutes} 占位，和导入链接里的数字同源', (_name, locale) => {
    const text = (locale.keyOnboarding.ccs as Record<string, string>).balanceNote
    expect(text).toContain('{minutes}')
    expect(text.replace('{minutes}', String(CC_SWITCH_USAGE_INTERVAL_MINUTES))).toMatch(/5/)
    expect(text).not.toMatch(/\b(5|30)\b/)
  })

  it('zh-HK 用「金鑰」「設定」「匯入」，不夹简体；zh-CN 不夹繁体', () => {
    const hk = zhHK.keyOnboarding.ccs as Record<string, string>
    const cn = zhCN.keyOnboarding.ccs as Record<string, string>
    for (const k of NEW_KEYS) {
      expect(hk[k], k).not.toMatch(/密钥|配置|导入|设置|启用|刷新页面/)
      expect(cn[k], k).not.toMatch(/金鑰|設定|匯入|啟用/)
    }
    expect(hk.codexWarning).toContain('啟用')
    expect(hk.switchNote).toContain('匯入')
  })

  // 上面的词表只拦得住几个词；整句里夹了别的简体字（如「余额」「设定」）要靠字表拦
  const SIMPLIFIED_ONLY = /[余额随设汇导钥给选项码装认换时问击载开关这个启复链点续]/
  const TRADITIONAL_ONLY = /[餘額隨設匯導鑰給選項碼裝認換時問擊載開關這個啟復連點續]/

  it('zh-HK 新增文案里没有简体字；zh-CN 新增文案里没有繁体字', () => {
    const hk = zhHK.keyOnboarding.ccs as Record<string, string>
    const cn = zhCN.keyOnboarding.ccs as Record<string, string>
    for (const k of NEW_KEYS) {
      expect(hk[k], `zh-HK ${k}`).not.toMatch(SIMPLIFIED_ONLY)
      expect(cn[k], `zh-CN ${k}`).not.toMatch(TRADITIONAL_ONLY)
    }
  })
})
