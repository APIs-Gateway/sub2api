import { describe, expect, it } from 'vitest'

import en from '@/i18n/locales/en'
import zhCN from '@/i18n/locales/zh-CN'
import zhHK from '@/i18n/locales/zh-HK'
import { FeatureFlags } from '@/utils/featureFlags'

import { USER_NAV_ENTRIES } from '../userNav'

function lookup(messages: unknown, key: string): unknown {
  return key.split('.').reduce<unknown>((node, part) => (node as Record<string, unknown> | undefined)?.[part], messages)
}

describe('USER_NAV_ENTRIES', () => {
  it('keeps the order both the user sidebar and the admin "my account" list render', () => {
    expect(USER_NAV_ENTRIES.map((entry) => entry.path)).toEqual([
      '/dashboard',
      '/keys',
      '/usage',
      '/available-channels',
      '/monitor',
      '/subscriptions',
      '/purchase',
      '/orders',
      '/redeem',
      '/points',
      '/docs',
      '/profile',
    ])
  })

  it('only the dashboard is user-sidebar only', () => {
    expect(USER_NAV_ENTRIES.filter((entry) => entry.userOnly).map((entry) => entry.path)).toEqual(['/dashboard'])
  })

  it('declares the same feature gates the sidebar used before', () => {
    const flags = Object.fromEntries(USER_NAV_ENTRIES.filter((e) => e.flag).map((e) => [e.path, e.flag]))

    expect(flags).toEqual({
      '/available-channels': FeatureFlags.availableChannels,
      '/monitor': FeatureFlags.channelMonitor,
      '/purchase': FeatureFlags.payment,
      '/orders': FeatureFlags.payment,
    })
    expect(FeatureFlags.payment.mode).toBe('opt-out')
    expect(FeatureFlags.availableChannels.mode).toBe('opt-in')
  })

  it('declares the same simple-mode exclusions as before', () => {
    expect(USER_NAV_ENTRIES.filter((e) => e.hideInSimpleMode).map((e) => e.path)).toEqual([
      '/usage',
      '/available-channels',
      '/subscriptions',
      '/purchase',
      '/orders',
      '/redeem',
      '/points',
    ])
  })

  it('has a translation in every locale for each entry', () => {
    const keys = USER_NAV_ENTRIES.map((e) => e.labelKey)

    for (const [name, messages] of Object.entries({ en, 'zh-CN': zhCN, 'zh-HK': zhHK })) {
      for (const key of keys) {
        const value = lookup(messages, key)
        expect(typeof value === 'string' && value.length > 0, `${name} is missing ${key}`).toBe(true)
      }
    }
  })
})
