import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zhCN from '../locales/zh-CN'
import zhHK from '../locales/zh-HK'

// 首页只讲订阅和余额：不放没有依据的数字，不描述内部机制，也不把「按量计费」当成与订阅对立的卖点。
const FORBIDDEN: Array<[string, RegExp]> = [
  ['placeholder price / quota figures', /[$¥]\s?\d/],
  ['routing / scheduling internals', /智能调度|智能調度|高可用|high-availability|smart routing|账号池|帳號池|account pool/i],
  ['pay-as-you-go as a foil to subscription', /按量(?:计费|計費|付费|付費)|传统按量|傳統按量|pay-as-you-go/i],
]

describe('landing page copy', () => {
  for (const [name, locale] of [
    ['en', en],
    ['zh-CN', zhCN],
    ['zh-HK', zhHK],
  ] as const) {
    it(`${name}: keeps to subscription and balance only`, () => {
      const text = JSON.stringify(locale.home)
      for (const [label, pattern] of FORBIDDEN) {
        expect(text, `${name} home copy must not contain ${label}`).not.toMatch(pattern)
      }
    })
  }

  it('keeps the overdraft refresh wording aligned with the subscription page', () => {
    expect(zhCN.home.marketing.overdraftDesc).toContain(zhCN.userSubscriptions.overdraftBtn.label)
    expect(zhHK.home.marketing.overdraftDesc).toContain(zhHK.userSubscriptions.overdraftBtn.label)
    expect(en.home.marketing.overdraftDesc).toContain(en.userSubscriptions.overdraftBtn.label)
  })
})
