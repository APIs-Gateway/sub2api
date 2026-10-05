import { describe, expect, it, vi } from 'vitest'
import { deriveOverallStatus, isHardFailureStatus, useChannelMonitorFormat } from '../useChannelMonitorFormat'

// 用真实的 zh-CN / en 文案渲染，断言页面上到底出现哪些字。
const locale = vi.hoisted(() => ({ current: 'zh-CN' as 'zh-CN' | 'zh-HK' | 'en' }))
vi.mock('vue-i18n', async () => {
  const messages = {
    'zh-CN': (await import('@/i18n/locales/zh-CN')).default as Record<string, unknown>,
    'zh-HK': (await import('@/i18n/locales/zh-HK')).default as Record<string, unknown>,
    en: (await import('@/i18n/locales/en')).default as Record<string, unknown>,
  }
  const t = (key: string) => {
    const hit = key.split('.').reduce<unknown>((o, k) => (o as Record<string, unknown> | undefined)?.[k], messages[locale.current])
    return typeof hit === 'string' ? hit : key
  }
  return { useI18n: () => ({ t }) }
})

describe('isHardFailureStatus：只有 failed / error 算红', () => {
  it.each([
    ['operational', false],
    ['degraded', false],
    ['failed', true],
    ['error', true],
    ['', false],
    [undefined, false],
    [null, false],
  ] as const)('%s → %s', (status, expected) => {
    expect(isHardFailureStatus(status)).toBe(expected)
  })
})

describe('deriveOverallStatus：总状态只看红卡', () => {
  it('没有渠道 → 正常', () => {
    expect(deriveOverallStatus([])).toBe('operational')
  })

  it('全绿 → 正常', () => {
    expect(deriveOverallStatus(['operational', 'operational', 'operational'])).toBe('operational')
  })

  it('有黄（降级）但没有红 → 仍是正常，黄只在卡片上显示', () => {
    expect(deriveOverallStatus(['operational', 'degraded', 'operational'])).toBe('operational')
    expect(deriveOverallStatus(['degraded', 'degraded'])).toBe('operational')
  })

  it('还没有历史的卡既不算红也不算绿', () => {
    expect(deriveOverallStatus(['', 'operational'])).toBe('operational')
    expect(deriveOverallStatus(['', 'failed'])).toBe('degraded')
  })

  it('有红卡 → 降级（failed 和 error 都是红）', () => {
    expect(deriveOverallStatus(['operational', 'failed', 'operational'])).toBe('degraded')
    expect(deriveOverallStatus(['error', 'operational'])).toBe('degraded')
    expect(deriveOverallStatus(['degraded', 'failed', 'operational'])).toBe('degraded')
  })

  it('红卡和黄卡混在一起、但不是每张都红 → 降级，不是不可用', () => {
    expect(deriveOverallStatus(['failed', 'degraded'])).toBe('degraded')
    expect(deriveOverallStatus(['failed', 'failed', 'degraded'])).toBe('degraded')
  })

  it('每张卡都是红的 → 不可用', () => {
    expect(deriveOverallStatus(['failed', 'error', 'failed'])).toBe('unavailable')
    expect(deriveOverallStatus(['error'])).toBe('unavailable')
  })
})

describe('用户端状态文案：普通用户硬失败统一是「不可用」，管理员保留细分', () => {
  it.each([
    ['zh-CN', '不可用', '失败', '错误'],
    ['zh-HK', '不可用', '失敗', '錯誤'],
    ['en', 'Unavailable', 'Failed', 'Error'],
  ] as const)('%s', (lang, unavailable, failed, error) => {
    locale.current = lang
    const { viewStatusLabel, viewStatusBadgeClass, statusBadgeClass } = useChannelMonitorFormat()

    // 普通用户
    expect(viewStatusLabel('failed', false)).toBe(unavailable)
    expect(viewStatusLabel('error', false)).toBe(unavailable)
    // 管理员仍然看到具体的状态
    expect(viewStatusLabel('failed', true)).toBe(failed)
    expect(viewStatusLabel('error', true)).toBe(error)
    // 正常 / 降级 / 未知对谁都一样
    expect(viewStatusLabel('operational', false)).toBe(viewStatusLabel('operational', true))
    expect(viewStatusLabel('degraded', false)).toBe(viewStatusLabel('degraded', true))
    expect(viewStatusLabel('', false)).toBe('-')

    // 普通用户的 error 与 failed 一样是红色，不落到灰色的「未知」样式；管理员保持原样
    expect(viewStatusBadgeClass('error', false)).toBe(statusBadgeClass('failed'))
    expect(viewStatusBadgeClass('error', true)).toBe(statusBadgeClass('error'))
    expect(viewStatusBadgeClass('error', true)).not.toBe(statusBadgeClass('failed'))
  })
})
