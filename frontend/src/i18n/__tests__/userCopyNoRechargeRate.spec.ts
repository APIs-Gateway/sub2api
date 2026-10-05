import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zhCN from '../locales/zh-CN'
import zhHK from '../locales/zh-HK'

// 用户端文案不得写出充值倍率、定价曲线这类换算比例：
// 例如「1 CNY = x USD」「当前倍率」「每刀单价」，或把「额度价值」与实付并列。
// 后台（admin）是给管理员看的账本口径，不在限制范围内。
const locales: Record<string, Record<string, unknown>> = {
  en: en as Record<string, unknown>,
  'zh-CN': zhCN as Record<string, unknown>,
  'zh-HK': zhHK as Record<string, unknown>
}

interface Forbidden {
  name: string
  pattern: RegExp
  /** 只扫这些前缀下的键；缺省扫整棵用户端文案树。 */
  onlyUnder?: string[]
  /** 刻意保留的键（带原因写在 FORBIDDEN 里）。 */
  allow?: string[]
}

const FORBIDDEN: Forbidden[] = [
  { name: '1 CNY = x USD 换算', pattern: /1\s*(?:CNY|\{currency\})\s*[=＝]/i },
  { name: '充值倍率 / 当前倍率', pattern: /充值倍率|當前倍率|当前倍率|recharge rate|current rate/i },
  { name: '每刀单价 / 每 N 刀一档', pattern: /每刀|每\s*\d+\s*刀|per dollar|\d+\s*USD steps/i },
  { name: '额度价值（与实付并列）', pattern: /额度价值|額度價值|quota value/i },
  // 「约为官方价的 0.25」「0.25 of official pricing」这类带具体数字的折算示例。
  { name: '折合官方价的具体比例', pattern: /官方[价價]的\s*\d*\.\d|\d*\.\d+\s*of\s+(?:the\s+)?official\s+pric/i },
  // 展示倍率改成 1:1 口径之后，倍率不再和官方价相乘，也不说「几折」「官方价的 N%」。
  {
    name: '「几折」',
    pattern: /[几幾]折/,
    // free 站（充值倍率为 1）的分组倍率悬停保留改动前的原文，一个字不改。
    allow: ['groups.rateMultiplierTip']
  },
  { name: '倍率 × 官方价 的算式', pattern: /官方[价價]\s*[×x]\s*倍率|倍率\s*[×x]\s*官方[价價]/i },
  { name: '官方价的 N%', pattern: /官方[价價]的\s*\d+\s*%/ },
  // 价格只有一套，套餐的优势只用倍率表达。这条先管分组倍率和密钥分组这两块；
  // 价格页、计费规则卡、续费对话框里的「套餐价」由各自的改版去掉后，再把范围放宽到整棵树。
  { name: '「套餐价」', pattern: /套餐[价價](?!值)|plan price/i, onlyUnder: ['groups.', 'keys.'] }
]

function collectMessages(value: unknown, path: string, out: Array<[string, string]>) {
  if (typeof value === 'string') {
    out.push([path, value])
    return
  }
  if (value === null || typeof value !== 'object') return
  for (const [key, child] of Object.entries(value as Record<string, unknown>)) {
    // 任意层级的 admin 子树（顶层 admin、points.admin 等）是后台页面，不限制。
    if (key === 'admin') continue
    collectMessages(child, path ? `${path}.${key}` : key, out)
  }
}

describe.each(Object.entries(locales))('用户端文案不出现换算比例（%s）', (_name, messages) => {
  const all: Array<[string, string]> = []
  collectMessages(messages, '', all)

  it('收集到了用户端文案', () => {
    expect(all.length).toBeGreaterThan(500)
  })

  it.each(FORBIDDEN)('不含「$name」', ({ pattern, onlyUnder, allow }) => {
    const hits = all
      .filter(([key]) => !onlyUnder || onlyUnder.some((prefix) => key.startsWith(prefix)))
      .filter(([key]) => !allow?.includes(key))
      .filter(([, text]) => pattern.test(text))
      .map(([key]) => key)
    expect(hits).toEqual([])
  })
})

// 例外是为了保留旧文案原样；键被删掉或改写后，这条例外就该一起去掉，不能一直留着当后门。
describe.each(FORBIDDEN.filter((rule) => rule.allow))('「$name」的例外', ({ pattern, allow }) => {
  const byLocale = Object.entries(locales).map(([name, messages]) => {
    const all: Array<[string, string]> = []
    collectMessages(messages, '', all)
    return { name, all }
  })

  it.each(allow ?? [])('%s 三语都还在，且至少一种语言里仍含该说法', (key) => {
    const texts = byLocale.map(({ name, all }) => {
      const hit = all.find(([path]) => path === key)
      expect(hit, `${name} 里没有 ${key}，请从例外里删掉`).toBeDefined()
      return hit![1]
    })
    expect(texts.some((text) => pattern.test(text)), `${key} 已经不含该说法，请从例外里删掉`).toBe(true)
  })
})
