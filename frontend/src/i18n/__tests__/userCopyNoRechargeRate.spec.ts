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

const FORBIDDEN: Array<{ name: string; pattern: RegExp }> = [
  { name: '1 CNY = x USD 换算', pattern: /1\s*(?:CNY|\{currency\})\s*[=＝]/i },
  { name: '充值倍率 / 当前倍率', pattern: /充值倍率|當前倍率|当前倍率|recharge rate|current rate/i },
  { name: '每刀单价 / 每 N 刀一档', pattern: /每刀|每\s*\d+\s*刀|per dollar|\d+\s*USD steps/i },
  { name: '额度价值（与实付并列）', pattern: /额度价值|額度價值|quota value/i },
  // 「约为官方价的 0.25」「0.25 of official pricing」这类带具体数字的折算示例。
  { name: '折合官方价的具体比例', pattern: /官方[价價]的\s*\d*\.\d|\d*\.\d+\s*of\s+(?:the\s+)?official\s+pric/i }
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

  it.each(FORBIDDEN)('不含「$name」', ({ pattern }) => {
    const hits = all.filter(([, text]) => pattern.test(text)).map(([key]) => key)
    expect(hits).toEqual([])
  })
})
