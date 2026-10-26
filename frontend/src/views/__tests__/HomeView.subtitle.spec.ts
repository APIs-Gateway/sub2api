import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'

import HomeView from '../HomeView.vue'

const { checkAuth, fetchPublicSettings, cachedPublicSettings, i18nState } = vi.hoisted(() => ({
  checkAuth: vi.fn(),
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
  useAuthStore: () => ({
    isAuthenticated: false,
    isAdmin: false,
    checkAuth,
  }),
  useAppStore: () => ({
    cachedPublicSettings: cachedPublicSettings.value,
    siteName: 'Sub2API',
    siteLogo: '',
    docUrl: '',
    homeContent: '',
    publicSettingsLoaded: true,
    fetchPublicSettings,
  }),
}))

const mountHome = () =>
  mount(HomeView, {
    global: {
      stubs: {
        RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
        LocaleSwitcher: true,
        Icon: true,
        BrandMark: true,
      },
    },
  })

// 首屏描述紧跟在主标题 h1 之后
const heroDescription = (wrapper: ReturnType<typeof mountHome>) =>
  wrapper.find('h1').element.nextElementSibling?.textContent?.trim() ?? ''

describe('HomeView hero description', () => {
  beforeEach(() => {
    checkAuth.mockReset()
    fetchPublicSettings.mockReset()
    cachedPublicSettings.value = null
    i18nState.lang = 'en'
    localStorage.clear()
    document.documentElement.classList.remove('dark')
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: vi.fn().mockReturnValue({ matches: false }),
    })
  })

  it('shows the configured site subtitle as is', () => {
    cachedPublicSettings.value = { site_subtitle: 'Example subtitle' }

    const wrapper = mountHome()

    expect(heroDescription(wrapper)).toBe('Example subtitle')
    wrapper.unmount()
  })

  it.each([
    ['en', 'A fast, reliable API for powerful AI models. Sign up and create a key to connect.'],
    ['zh-CN', '稳定、快速的大模型 API 服务。注册后创建密钥即可接入。'],
  ] as const)('falls back to the localized hero copy when the subtitle is empty (%s)', (lang, expected) => {
    i18nState.lang = lang
    cachedPublicSettings.value = { site_subtitle: '' }

    const wrapper = mountHome()

    expect(heroDescription(wrapper)).toBe(expected)
    expect(wrapper.text()).not.toContain('Subscription to API')
    expect(wrapper.text()).not.toContain('订阅转')
    wrapper.unmount()
  })
})
