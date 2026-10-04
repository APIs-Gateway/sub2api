import { describe, expect, it } from 'vitest'
import postcss from 'postcss'
import tailwindcss from 'tailwindcss'
import tailwindConfig, { lightGrayText } from '../../tailwind.config.js'

// 浅色模式 text-gray-400 / text-gray-500 必须达到 WCAG AA 的 4.5:1。
// 这两档文字色由 tailwind.config.js 里的 CSS 变量提供，深色模式仍取色板原值。

const gray = tailwindConfig.theme.extend.colors.gray as Record<string, string>

function luminance(hex: string): number {
  const channel = (i: number) => {
    const v = parseInt(hex.slice(i, i + 2), 16) / 255
    return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4)
  }
  return 0.2126 * channel(1) + 0.7152 * channel(3) + 0.0722 * channel(5)
}

function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

// 次要文字实际会落在的浅色底：卡片白、页面底 gray-50、悬停行 / 禁用底 gray-100
const surfaces: Record<string, string> = {
  white: '#ffffff',
  'gray-50': gray['50'],
  'gray-100': gray['100']
}

const channels = (hex: string) =>
  [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16)).join(' ')

describe('浅色模式次要文字对比度', () => {
  for (const shade of [400, 500] as const) {
    for (const [name, bg] of Object.entries(surfaces)) {
      it(`text-gray-${shade} 在 ${name} 底上 >= 4.5:1`, () => {
        expect(contrast(lightGrayText[shade], bg)).toBeGreaterThanOrEqual(4.5)
      })
    }
  }

  it('保留 400 < 500 < gray-600 的由浅到深顺序', () => {
    const onWhite = (hex: string) => contrast(hex, '#ffffff')
    expect(onWhite(lightGrayText[400])).toBeLessThanOrEqual(onWhite(lightGrayText[500]))
    expect(onWhite(lightGrayText[500])).toBeLessThanOrEqual(onWhite(gray['600']))
  })

  it('色板原值不变（深色模式、背景、边框仍用它）', () => {
    expect(gray['400']).toBe('#b8b1a1')
    expect(gray['500']).toBe('#918a7c')
  })

  it('变量：浅色取加深值，深色 / 占位符 / 禁用 / 深底取色板原值', () => {
    const rules: Record<string, Record<string, string>> = {}
    const pluginDef = tailwindConfig.plugins[0] as unknown as {
      handler: (api: unknown) => void
    }
    pluginDef.handler({
      addBase: (r: Record<string, Record<string, string>>) => Object.assign(rules, r),
      theme: (path: string) => path.split('.').reduce<any>((o, k) => o[k], tailwindConfig.theme.extend)
    })

    expect(rules[':root:not(.dark)']).toEqual({
      '--text-gray-400': channels(lightGrayText[400]),
      '--text-gray-500': channels(lightGrayText[500])
    })

    const resetKey = Object.keys(rules).find((k) => k.includes('.dark,'))!
    for (const selector of ['.dark', 'input', 'textarea', ':disabled', '.bg-gray-900', "[class~='bg-gray-800/80']"]) {
      expect(resetKey.split(', ')).toContain(selector)
    }
    expect(rules[resetKey]).toEqual({
      '--text-gray-400': channels(gray['400']),
      '--text-gray-500': channels(gray['500'])
    })
  })

  it('只有文字色走变量，背景 / 边框仍是色板原值', async () => {
    const result = await postcss([
      tailwindcss({
        ...tailwindConfig,
        content: [
          {
            raw: 'text-gray-400 text-gray-500/70 placeholder:text-gray-400 dark:text-gray-400 bg-gray-400 border-gray-500'
          }
        ]
      })
    ]).process('@tailwind utilities;', { from: undefined })
    const css = result.css

    expect(css).toMatch(/\.text-gray-400\s*{[^}]*rgb\(var\(--text-gray-400\)/)
    expect(css).toMatch(/\.text-gray-500\\\/70\s*{[^}]*rgb\(var\(--text-gray-500\) \/ 0\.7\)/)
    expect(css).toMatch(/\.placeholder\\:text-gray-400::placeholder\s*{[^}]*var\(--text-gray-400\)/)
    expect(css).toMatch(/\.dark\\:text-gray-400[^{]*{[^}]*var\(--text-gray-400\)/)
    expect(css).toMatch(/\.bg-gray-400\s*{[^}]*rgb\(184 177 161/)
    expect(css).toMatch(/\.border-gray-500\s*{[^}]*rgb\(145 138 124/)
  })
})
