import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import HomeView from '../HomeView.vue'

const { checkAuth, fetchPublicSettings, publicDocUrl, cachedPublicSettings } = vi.hoisted(() => ({
  checkAuth: vi.fn(),
  fetchPublicSettings: vi.fn(),
  publicDocUrl: { value: '' },
  cachedPublicSettings: { value: null as null | { doc_url?: string } },
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
      locale: { value: 'en' },
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
    docUrl: publicDocUrl.value,
    homeContent: '',
    publicSettingsLoaded: true,
    fetchPublicSettings,
  }),
}))

const mountHome = () => mount(HomeView, {
  global: {
    stubs: {
      RouterLink: { template: '<a><slot /></a>' },
      LocaleSwitcher: true,
      Icon: true,
      BrandMark: true,
    },
  },
})

describe('HomeView documentation link', () => {
  beforeEach(() => {
    checkAuth.mockReset()
    fetchPublicSettings.mockReset()
    publicDocUrl.value = ''
    cachedPublicSettings.value = null
    localStorage.clear()
    document.documentElement.classList.remove('dark')
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: vi.fn().mockReturnValue({ matches: false }),
    })
  })

  it('does not render unsafe documentation links', () => {
    publicDocUrl.value = 'javascript:alert(1)'

    const wrapper = mountHome()

    expect(wrapper.find('a[href="javascript:alert(1)"]').exists()).toBe(false)

    wrapper.unmount()
  })

  it('renders sanitized documentation links', () => {
    publicDocUrl.value = 'https://docs.example.com/guide'

    const wrapper = mountHome()

    expect(wrapper.find('a[href="https://docs.example.com/guide"]').exists()).toBe(true)

    wrapper.unmount()
  })

  it('prefers sanitized cached documentation links', () => {
    publicDocUrl.value = 'https://docs.example.com/fallback'
    cachedPublicSettings.value = { doc_url: 'https://docs.example.com/cached' }

    const wrapper = mountHome()

    expect(wrapper.find('a[href="https://docs.example.com/cached"]').exists()).toBe(true)
    expect(wrapper.find('a[href="https://docs.example.com/fallback"]').exists()).toBe(false)

    wrapper.unmount()
  })

  it('hides documentation links when no URL is configured', () => {
    const wrapper = mountHome()

    expect(wrapper.find('a[href]').exists()).toBe(false)

    wrapper.unmount()
  })
})
