import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zhCN from '../locales/zh-CN'
import zhHK from '../locales/zh-HK'

// 用户端的美元金额统一写成 `$1,300.00`（符号在前、无空格），和余额同一写法。
// 不得出现 `USD1,300.00` / `USD 1,300.00` 这种代码前缀，也不得在文案模板里给金额占位符再拼一个 USD。
// 币种名称只能当标签用（如「到账余额（USD）」），不能贴在数字前面。
// 后台（admin）是账本口径，不在限制范围内。

const locales: Record<string, Record<string, unknown>> = {
  en: en as Record<string, unknown>,
  'zh-CN': zhCN as Record<string, unknown>,
  'zh-HK': zhHK as Record<string, unknown>
}

/** USD 后面直接跟占位符或数字：`USD {d}`、`USD 72.60`、`USD1,300.00`。 */
const COPY_USD_PREFIX = /\bUSD\s*(?:\{|\d)/

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

describe.each(Object.entries(locales))('用户端文案不给金额加 USD 前缀（%s）', (_name, messages) => {
  const all: Array<[string, string]> = []
  collectMessages(messages, '', all)

  it('收集到了用户端文案', () => {
    expect(all.length).toBeGreaterThan(500)
  })

  it('没有「USD {金额}」「USD 72.60」这类前缀写法', () => {
    const hits = all.filter(([, text]) => COPY_USD_PREFIX.test(text)).map(([key]) => key)
    expect(hits).toEqual([])
  })
})

// 用户端源码里不能自己拼「USD ${金额}」：金额一律走 useCurrencyDisplay / utils/numberFormat 的统一格式函数。
const sources = import.meta.glob(
  [
    '/src/views/user/**/*.{vue,ts}',
    '/src/components/{payment,subscription,user,keys,common,layout}/**/*.{vue,ts}',
    '!/src/**/__tests__/**'
  ],
  { query: '?raw', import: 'default', eager: true }
) as Record<string, string>

/** 模板字符串 / Vue 插值里把 USD 贴在金额前面，或字符串拼接 'USD ' + 金额。 */
const SOURCE_USD_PREFIX = /\bUSD\s*(?:\$\{|\{\{)|['"]USD\s['"]\s*\+/

describe('用户端源码不手拼 USD 前缀金额', () => {
  it('扫描到了用户端源码', () => {
    expect(Object.keys(sources).length).toBeGreaterThan(50)
  })

  it('没有 `USD ${...}` / `USD {{ ... }}` / `\'USD \' + ...`', () => {
    const hits = Object.entries(sources)
      .filter(([, text]) => SOURCE_USD_PREFIX.test(text))
      .map(([file]) => file)
    expect(hits).toEqual([])
  })
})
