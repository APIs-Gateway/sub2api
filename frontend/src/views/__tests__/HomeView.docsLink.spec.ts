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
      RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
      LocaleSwitcher: true,
      Icon: true,
      BrandMark: true,
    },
  },
})

describe('HomeView documentation entries', () => {
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

  it('links the header icon, the hero link and the footer link to the built-in docs page', () => {
    const wrapper = mountHome()

    expect(wrapper.findAll('a[href="/docs"]')).toHaveLength(3)

    wrapper.unmount()
  })

  it('ignores the external documentation URL setting', () => {
    publicDocUrl.value = 'https://docs.example.com/fallback'
    cachedPublicSettings.value = { doc_url: 'https://docs.example.com/cached' }

    const wrapper = mountHome()

    expect(wrapper.findAll('a[href="/docs"]')).toHaveLength(3)
    expect(wrapper.find('a[href^="https://docs.example.com"]').exists()).toBe(false)
    expect(wrapper.find('a[target="_blank"]').exists()).toBe(false)

    wrapper.unmount()
  })
})
