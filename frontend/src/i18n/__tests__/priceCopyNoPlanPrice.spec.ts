import { describe, expect, it } from 'vitest'
import zhCN from '../locales/zh-CN'
import zhHK from '../locales/zh-HK'
import en from '../locales/en'

/** 价格页与计费规则卡的用户端文案：套餐优势只用「倍率」表达，不再出现「套餐价」。 */
const FORBIDDEN = /套餐[价價]|plan price/i

function strings(node: unknown, path = ''): Array<[string, string]> {
  if (typeof node === 'string') return [[path, node]]
  if (node && typeof node === 'object') {
    return Object.entries(node).flatMap(([k, v]) => strings(v, path ? `${path}.${k}` : k))
  }
  return []
}

describe('价格页 / 计费规则卡文案', () => {
  const locales = { 'zh-CN': zhCN, 'zh-HK': zhHK, en } as Record<string, Record<string, unknown>>

  for (const [name, messages] of Object.entries(locales)) {
    it(`${name}：availableChannels 与 billingRules 里没有「套餐价」`, () => {
      const hits = [
        ...strings(messages.availableChannels, 'availableChannels'),
        ...strings(messages.billingRules, 'billingRules'),
      ].filter(([, text]) => FORBIDDEN.test(text))
      expect(hits).toEqual([])
    })

    it(`${name}：套餐倍率相关的键齐全`, () => {
      const ac = messages.availableChannels as Record<string, string>
      for (const key of ['planRate', 'yourPlanRate', 'planRateLead', 'rateNoteFiat', 'rateNoteFiatNoPlan']) {
        expect(typeof ac[key]).toBe('string')
      }
      for (const key of ['planPrice', 'yourPlanPrice', 'inOut']) expect(ac[key]).toBeUndefined()
      expect(ac.planRateLead).not.toContain('{')
      expect(ac.planRateCell).toBeUndefined()
      const fiat = (messages.billingRules as { fiat: Record<string, string> }).fiat
      expect(typeof fiat.rate).toBe('string')
      expect(fiat.balance).toBeUndefined()
    })
  }
})
