import { describe, expect, it } from 'vitest'

import * as oldGen from './fixtures/keyOnboarding.baseline'
import * as newGen from '../keyOnboarding'

// 默认线路：和改动前（1a4a797f6）的生成结果逐字节一致
const BASES = ['https://codex.hiyo.top', 'https://codex.hiyo.top/', '  https://api.example.com//  ']
const PLATFORMS = ['anthropic', 'openai', 'gemini', 'antigravity', null]
const CLIENTS = ['claude', 'codex', 'gemini', 'opencode'] as const
const t = (key: string, params: Record<string, unknown> = {}) => `${key}${JSON.stringify(params)}`

describe('keyOnboarding 默认线路回归', () => {
  it('endpointFor / nativeEndpoint 不变', () => {
    for (const b of BASES) for (const p of PLATFORMS) {
      for (const c of CLIENTS) expect(newGen.endpointFor(c, p, b)).toBe(oldGen.endpointFor(c, p, b))
      expect(newGen.nativeEndpoint(p, b)).toBe(oldGen.nativeEndpoint(p, b))
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

  it('交给 AI 的提示词（简短与详细）逐字节一致', () => {
    for (const b of BASES) for (const p of PLATFORMS) for (const client of newGen.AI_CLIENTS) for (const detailed of [false, true]) {
      const input = { t, client, clientLabel: client, baseUrl: b, platform: p, siteName: 'Hiyo', models: ['m1', 'm2'], docUrl: 'https://doc.example', detailed }
      expect(newGen.buildAiPrompt(input)).toBe(oldGen.buildAiPrompt(input))
    }
  })
})
