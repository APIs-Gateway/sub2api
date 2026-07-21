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

vi.mock('vue-router', () => ({
  useRoute: () => ({ meta: {}, name: 'Dashboard', params: {} }),
  useRouter: () => ({ push: vi.fn() }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) =>
      ({
        'common.toggleMenu': 'Toggle menu',
        'common.userMenu': 'User menu',
      })[key] ?? key,
  }),
}))

describe('AppHeader accessibility labels', () => {
  it('renders localized labels for the mobile and user menu buttons', () => {
    const wrapper = mount(AppHeader, {
      global: {
        stubs: {
          AnnouncementBell: true,
          Icon: true,
          LocaleSwitcher: true,
          RouterLink: true,
          SubscriptionProgressMini: true,
        },
      },
    })

    expect(wrapper.find('button[aria-label="Toggle menu"]').exists()).toBe(true)
    expect(wrapper.find('button[aria-label="User menu"]').exists()).toBe(true)
  })
})
