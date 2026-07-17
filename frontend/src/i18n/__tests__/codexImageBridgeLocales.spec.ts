import { describe, expect, it } from 'vitest'
import en from '../locales/en'
import zhCN from '../locales/zh-CN'

describe('Codex image bridge locale copy', () => {
  it('keeps the English copy explicit about hosted and local image boundaries', () => {
    const messages = (en as any).admin
    expect(messages.channels.form.codexImageGenerationBridgeHint).toContain('non-Responses Lite')
    expect(messages.channels.form.codexImageGenerationBridgeHint).toContain('local image_gen')
    expect(messages.accounts.openai.codexImageGenerationBridgeDesc).toContain('image-only model routing')
    expect(messages.accounts.openai.codexImageGenerationBridgeEnabledDesc).toContain('client-declared image tools')
  })

  it('keeps the Simplified Chinese copy explicit about hosted and local image boundaries', () => {
    const messages = (zhCN as any).admin
    expect(messages.channels.form.codexImageGenerationBridgeHint).toContain('Responses Lite')
    expect(messages.channels.form.codexImageGenerationBridgeHint).toContain('本地 image_gen')
    expect(messages.accounts.openai.codexImageGenerationBridgeDesc).toContain('image-only 模型路由')
    expect(messages.accounts.openai.codexImageGenerationBridgeEnabledDesc).toContain('客户端显式携带')
  })
})
