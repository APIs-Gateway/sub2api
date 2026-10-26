import { describe, expect, it } from 'vitest'

import * as oldGen from './fixtures/keyOnboarding.baseline'
import * as newGen from '../keyOnboarding'
import { buildCcSwitchImportDeeplink, CC_SWITCH_USAGE_SCRIPT } from '../ccswitchImport'
import { resolveEndpointOptions } from '../apiEndpoints'

// 默认线路：和改动前（1a4a797f6）的生成结果逐字节一致
const BASES = [
  'https://codex.hiyo.top',
  'https://codex.hiyo.top/',
  '  https://api.example.com//  ',
  'https://api.example.com/v1',
  'https://api.example.com/v1/'
]
const PLATFORMS = ['anthropic', 'openai', 'gemini', 'antigravity', null]
const CLIENTS = ['claude', 'codex', 'gemini', 'opencode'] as const

describe('keyOnboarding 默认线路回归', () => {
  it('endpointFor 不变', () => {
    for (const b of BASES) for (const p of PLATFORMS) {
      for (const c of CLIENTS) expect(newGen.endpointFor(c, p, b)).toBe(oldGen.endpointFor(c, p, b))
    }
  })

  it('所有客户端 x 系统 x 模式的安装脚本逐字节一致', () => {
    let n = 0
    for (const b of BASES) for (const p of PLATFORMS) for (const c of CLIENTS) for (const os of ['unix', 'windows'] as const) {
      for (const mode of ['full', 'refresh'] as const) {
        const input = { baseUrl: b, apiKey: "sk-a'b\"c $x", platform: p, siteName: 'Hiyo Site', mode }
        expect(newGen.buildInstallScript(c, os, input)).toBe(oldGen.buildInstallScript(c, os, input))
        n++
      }
    }
    expect(n).toBeGreaterThan(100)
  })

  // 弹窗（改动前 1a4a797f6）里的 base 只去结尾的 /；CC Switch 导入链接直接用它。
  const oldModalBase = (raw: string) => raw.trim().replace(/\/+$/, '')
  const KEY = 'sk-test-123'
  // 改动前 openai 分组只会导入成 Codex，clientType 只分 claude / gemini；现在页签传的是真实客户端（openai 分组开了调度时 Claude 也能选），
  // 所以这里把 openai 的 claude 换成 codex，保持「openai 分组 = Codex」这条基线
  const link = (baseUrl: string, platform: string | null, clientType: 'claude' | 'gemini') =>
    buildCcSwitchImportDeeplink({
      baseUrl,
      platform: platform as never,
      clientType: platform === 'openai' && clientType === 'claude' ? 'codex' : clientType,
      providerName: 'Hiyo - X',
      apiKey: KEY,
      usageScript: CC_SWITCH_USAGE_SCRIPT
    })
  /** 现在弹窗实际传给 buildCcSwitchImportDeeplink 的地址：通过真实的 resolveEndpointOptions 取得 */
  const newLink = (raw: string, platform: string | null, clientType: 'claude' | 'gemini') => {
    const opt = resolveEndpointOptions(raw, [], 'https://fallback.example')[0]
    return link(platform === 'openai' ? opt.configured : opt.base, platform, clientType)
  }

  it('CC Switch 导入链接：api_base_url 不带 /v1 时与改动前逐字节一致（所有平台与客户端）', () => {
    for (const b of ['https://codex.hiyo.top', 'https://codex.hiyo.top/', '  https://api.example.com//  ']) for (const p of PLATFORMS) {
      for (const ct of ['claude', 'gemini'] as const) expect(newLink(b, p, ct)).toBe(link(oldModalBase(b), p, ct))
    }
  })

  it('CC Switch 导入链接：api_base_url 以 /v1 结尾时，只有「旧值本来就是错的」的几处有意改变', () => {
    const decode = (u: string) => new URLSearchParams(u.split('?')[1])
    for (const b of ['https://api.example.com/v1', 'https://api.example.com/v1/']) {
      // Codex：和旧值一致（CC Switch 自己处理 Codex 的请求路径，沿用管理员写的前缀）
      for (const ct of ['claude', 'gemini'] as const) expect(newLink(b, 'openai', ct)).toBe(link(oldModalBase(b), 'openai', ct))
      // 有意改变：Claude Code / Gemini 自己会拼 /v1/messages 等路径，旧值 .../v1 会请求 /v1/v1/...，改为根地址
      for (const p of ['anthropic', 'gemini', null]) {
        const o = decode(link(oldModalBase(b), p, 'claude'))
        const n = decode(newLink(b, p, 'claude'))
        expect(o.get('endpoint')).toBe('https://api.example.com/v1')
        expect(n.get('endpoint')).toBe('https://api.example.com')
        expect(n.get('homepage')).toBe('https://api.example.com')
        for (const k of ['app', 'apiKey', 'usageScript', 'name']) expect(n.get(k)).toBe(o.get(k))
      }
      // 有意改变：antigravity 旧值 .../v1/antigravity（路由是 /antigravity/v1/...，旧值不存在），改为 .../antigravity
      expect(decode(link(oldModalBase(b), 'antigravity', 'claude')).get('endpoint')).toBe('https://api.example.com/v1/antigravity')
      expect(decode(newLink(b, 'antigravity', 'claude')).get('endpoint')).toBe('https://api.example.com/antigravity')
    }
  })

  it('/v1 结尾的 api_base_url：安装脚本、提示词、各客户端地址仍与改动前一致（旧实现本来就归一化）', () => {
    for (const b of ['https://api.example.com/v1', 'https://api.example.com/v1/']) {
      const root = 'https://api.example.com'
      for (const p of PLATFORMS) for (const c of CLIENTS) {
        expect(newGen.endpointFor(c, p, b)).toBe(oldGen.endpointFor(c, p, b))
        expect(newGen.endpointFor(c, p, b)).toBe(newGen.endpointFor(c, p, root))
      }
    }
  })
})
