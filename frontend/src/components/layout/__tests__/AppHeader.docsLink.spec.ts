import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import AppHeader from '../AppHeader.vue'

const stores = vi.hoisted(() => ({
  app: {
    cachedPublicSettings: null,
    contactInfo: '',
    docUrl: '',
    toggleMobileSidebar: vi.fn(),
  },
  auth: {
    isAdmin: false,
    isSimpleMode: false,
    logout: vi.fn(),
    user: {
      email: 'user@example.com',
      role: 'user',
      username: 'User',
    },
  },
  adminSettings: {
    customMenuItems: [],
  },
  onboarding: {
    replay: vi.fn(),
  },
}))

vi.mock('@/stores', () => ({
  useAppStore: () => stores.app,
  useAuthStore: () => stores.auth,
  useOnboardingStore: () => stores.onboarding,
}))

vi.mock('@/stores/adminSettings', () => ({
  useAdminSettingsStore: () => stores.adminSettings,
}))

vi.mock('@/composables/useCurrencyDisplay', () => ({
  useCurrencyDisplay: () => ({ canSwitch: { value: false } }),
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ meta: {}, name: 'Dashboard', params: {}, path: '/dashboard' }),
  useRouter: () => ({ push: vi.fn() }),
}))

// 保留真实的 vue-i18n（LocaleSwitcher 间接依赖 createI18n），只替换 useI18n
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

const mountHeader = () =>
  mount(AppHeader, {
    global: {
      stubs: {
        AnnouncementBell: true,
        CurrencyModeSwitch: true,
        Icon: true,
        LocaleSwitcher: true,
        RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
        SubscriptionProgressMini: true,
      },
    },
  })

describe('AppHeader documentation entry', () => {
  it('links to the built-in docs page when no external documentation URL is configured', () => {
    stores.app.docUrl = ''

    const wrapper = mountHeader()

    expect(wrapper.find('a[href="/docs"]').exists()).toBe(true)

    wrapper.unmount()
  })

  it('still links to the built-in docs page when an external documentation URL is configured', () => {
    stores.app.docUrl = 'https://docs.example.com/guide'

    const wrapper = mountHeader()

    expect(wrapper.find('a[href="/docs"]').exists()).toBe(true)
    expect(wrapper.find('a[href="https://docs.example.com/guide"]').exists()).toBe(false)

    wrapper.unmount()
  })
})
