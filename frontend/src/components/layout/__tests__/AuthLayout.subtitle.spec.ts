import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'

import AuthLayout from '../AuthLayout.vue'

const { fetchPublicSettings, cachedPublicSettings, i18nState } = vi.hoisted(() => ({
  fetchPublicSettings: vi.fn(),
  cachedPublicSettings: { value: null as null | { site_subtitle?: string } },
  i18nState: { lang: 'en' as 'en' | 'zh-CN' },
}))

// 测试环境的 vue-i18n 是 runtime-only 构建，不能编译消息；这里按键路径直接读取真实的语言包文案
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const { default: en } = await import('@/i18n/locales/en')
  const { default: zhCN } = await import('@/i18n/locales/zh-CN')
  const lookup = (key: string): string | undefined => {
    let cur: unknown = i18nState.lang === 'zh-CN' ? zhCN : en
    for (const seg of key.split('.')) {
      if (typeof cur !== 'object' || cur === null) return undefined
      cur = (cur as Record<string, unknown>)[seg]
    }
    return typeof cur === 'string' ? cur : undefined
  }
  return {
    ...actual,
    useI18n: () => ({
      locale: ref(i18nState.lang),
      t: (key: string) => lookup(key) ?? key,
    }),
  }
})

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    cachedPublicSettings: cachedPublicSettings.value,
    siteName: 'Sub2API',
    siteLogo: '',
    publicSettingsLoaded: true,
    fetchPublicSettings,
  }),
}))

const mountLayout = () => mount(AuthLayout, { global: { stubs: { BrandMark: true } } })

// 副标题紧跟在站点名称 h1 之后
const subtitleText = (wrapper: ReturnType<typeof mountLayout>) =>
  wrapper.find('h1').element.nextElementSibling?.textContent?.trim() ?? ''

describe('AuthLayout site subtitle', () => {
  beforeEach(() => {
    fetchPublicSettings.mockReset()
    cachedPublicSettings.value = null
    i18nState.lang = 'en'
  })

  it('shows the configured subtitle as is', () => {
    cachedPublicSettings.value = { site_subtitle: 'Example subtitle' }

    const wrapper = mountLayout()

    expect(subtitleText(wrapper)).toBe('Example subtitle')
    wrapper.unmount()
  })

  it.each([
    ['en', 'Fast, reliable LLM API service'],
    ['zh-CN', '快速稳定的大模型 API 服务'],
  ] as const)('falls back to the neutral localized default when the subtitle is empty (%s)', (lang, expected) => {
    i18nState.lang = lang
    cachedPublicSettings.value = { site_subtitle: '' }

    const wrapper = mountLayout()

    expect(subtitleText(wrapper)).toBe(expected)
    expect(wrapper.text()).not.toContain('Subscription to API')
    expect(wrapper.text()).not.toContain('订阅转')
    wrapper.unmount()
  })

  it('uses the same neutral default when the public settings are not cached yet', () => {
    cachedPublicSettings.value = null

    const wrapper = mountLayout()

    expect(subtitleText(wrapper)).toBe('Fast, reliable LLM API service')
    wrapper.unmount()
  })
})
