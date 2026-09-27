import { describe, expect, it } from 'vitest'
import {
  CC_SWITCH_USAGE_SCRIPT,
  OPENAI_CC_SWITCH_CODEX_MODEL,
  buildCcSwitchImportDeeplink
} from '@/utils/ccswitchImport'
import type { GroupPlatform } from '@/types'

function paramsFromDeeplink(deeplink: string): URLSearchParams {
  const query = deeplink.split('?')[1] || ''
  return new URLSearchParams(query)
}

describe('ccswitchImport utils', () => {
  it('defaults OpenAI CC Switch imports to the current Codex model', () => {
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
    expect(params.get('model')).toBe(OPENAI_CC_SWITCH_CODEX_MODEL)
    expect(atob(params.get('usageScript') || '')).toBe(baseInput.usageScript)
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
