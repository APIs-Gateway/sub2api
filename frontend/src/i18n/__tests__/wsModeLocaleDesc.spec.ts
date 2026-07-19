import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zhCN from '../locales/zh-CN'
import zhHK from '../locales/zh-HK'

describe('OpenAI WS mode locale descriptions', () => {
  it('documents the global v2 router requirement for account WS modes', () => {
    for (const description of [
      en.admin.accounts.openai.wsModeDesc,
      zhCN.admin.accounts.openai.wsModeDesc,
      zhHK.admin.accounts.openai.wsModeDesc,
    ]) {
      expect(description).toContain('mode_router_v2_enabled')
      expect(description).toContain('http_bridge')
    }
  })
})
