import { mount } from '@vue/test-utils'
import { nextTick, reactive } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { CustomMenuItem } from '@/types'

type RouteMock = {
  fullPath: string
  name: string
  params: Record<string, string>
  meta: Record<string, unknown>
}

type AppStoreMock = {
  siteName: string
  siteLogo: string
  cachedPublicSettings: { custom_menu_items?: CustomMenuItem[] } | null
  fetchPublicSettings: ReturnType<typeof vi.fn>
}

const route = reactive<RouteMock>({
  fullPath: '/custom/scheduler',
  name: 'CustomPage',
  params: { id: 'scheduler' },
  meta: {
    title: 'Custom Page',
    titleKey: 'customPage.title'
  }
})

const appStore = reactive<AppStoreMock>({
  siteName: 'Demo Site',
  siteLogo: '',
  cachedPublicSettings: {
    custom_menu_items: []
  },
  fetchPublicSettings: vi.fn().mockResolvedValue(undefined)
})

const authStore = reactive({
  isAdmin: false,
  isAuthenticated: false
})

const subscriptionStore = {
  fetchActiveSubscriptions: vi.fn().mockResolvedValue(undefined),
  startPolling: vi.fn(),
  clear: vi.fn()
}

const announcementStore = {
  fetchAnnouncements: vi.fn().mockResolvedValue(undefined),
  reset: vi.fn()
}

const adminComplianceStore = {
  fetchStatus: vi.fn().mockResolvedValue(undefined),
  requireAcknowledgement: vi.fn(),
  reset: vi.fn()
}

const adminSettingsStore = reactive({
  customMenuItems: [] as CustomMenuItem[]
})

const router = {
  afterEach: vi.fn(),
  replace: vi.fn()
}

vi.mock('vue-router', () => ({
  RouterView: { template: '<div />' },
  useRouter: () => router,
  useRoute: () => route
}))

vi.mock('@/api/setup', () => ({
  getSetupStatus: vi.fn().mockResolvedValue({ needs_setup: false })
}))

vi.mock('@/stores', () => ({
  useAppStore: () => appStore,
  useAuthStore: () => authStore,
  useSubscriptionStore: () => subscriptionStore,
  useAnnouncementStore: () => announcementStore,
  useAdminComplianceStore: () => adminComplianceStore,
  useAdminSettingsStore: () => adminSettingsStore
}))

vi.mock('@/components/common/Toast.vue', () => ({
  default: { template: '<div />' }
}))

vi.mock('@/components/common/NavigationProgress.vue', () => ({
  default: { template: '<div />' }
}))

vi.mock('@/components/admin/AdminComplianceDialog.vue', () => ({
  default: { template: '<div />' }
}))

vi.mock('@/components/common/AnnouncementPopup.vue', () => ({
  default: { template: '<div />' }
}))

describe('App document title refresh', () => {
  beforeEach(() => {
    route.fullPath = '/custom/scheduler'
    route.name = 'CustomPage'
    route.params = { id: 'scheduler' }
    route.meta = {
      title: 'Custom Page',
      titleKey: 'customPage.title'
    }
    appStore.siteName = 'Demo Site'
    appStore.siteLogo = ''
    appStore.cachedPublicSettings = {
      custom_menu_items: []
    }
    appStore.fetchPublicSettings.mockClear()
    authStore.isAdmin = false
    authStore.isAuthenticated = false
    adminSettingsStore.customMenuItems = []
    document.title = ''
  })

  afterEach(() => {
    vi.clearAllMocks()
  })

  it('refreshes custom page titles when menu settings load after route navigation', async () => {
    const { default: App } = await import('../App.vue')
    const wrapper = mount(App)

    await nextTick()
    await nextTick()

    expect(document.title).toBe('Custom Page - Demo Site')

    appStore.cachedPublicSettings = {
      custom_menu_items: [
        {
          id: 'scheduler',
          label: '账号调度器',
          icon_svg: '',
          url: 'https://example.com',
          visibility: 'public',
          sort_order: 0
        }
      ]
    }
    await nextTick()

    expect(document.title).toBe('账号调度器 - Demo Site')

    wrapper.unmount()
  })
})
