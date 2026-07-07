import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

const {
  authStore,
  appStore,
  adminSettingsStore,
  adminComplianceStore,
  navigationLoading,
  routePrefetch,
  getSetupStatus,
} = vi.hoisted(() => ({
  authStore: {
    isAuthenticated: true,
    isAdmin: true,
    isSimpleMode: false,
    hasPendingAuthSession: false,
    checkAuth: vi.fn(),
  },
  appStore: {
    siteName: 'Test Site',
    backendModeEnabled: false,
    cachedPublicSettings: {
      payment_enabled: true,
      risk_control_enabled: true,
      custom_menu_items: [
        {
          id: 'public-docs',
          label: 'Public Docs',
          icon_svg: '',
          url: 'https://example.test/docs',
          visibility: 'public',
          sort_order: 0,
        },
      ],
    },
  },
  adminSettingsStore: {
    customMenuItems: [
      {
        id: 'admin-docs',
        label: 'Admin Docs',
        icon_svg: '',
        url: 'https://example.test/admin-docs',
        visibility: 'admin',
        sort_order: 1,
      },
    ],
  },
  adminComplianceStore: {
    initialized: true,
    fetchStatus: vi.fn(),
    requireAcknowledgement: vi.fn(),
  },
  navigationLoading: {
    startNavigation: vi.fn(),
    endNavigation: vi.fn(),
    isLoading: { value: false },
  },
  routePrefetch: {
    triggerPrefetch: vi.fn(),
    cancelPendingPrefetch: vi.fn(),
    resetPrefetchState: vi.fn(),
  },
  getSetupStatus: vi.fn(),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => authStore,
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => appStore,
}))

vi.mock('@/stores/adminSettings', () => ({
  useAdminSettingsStore: () => adminSettingsStore,
}))

vi.mock('@/stores/adminCompliance', () => ({
  useAdminComplianceStore: () => adminComplianceStore,
}))

vi.mock('@/composables/useNavigationLoading', () => ({
  useNavigationLoadingState: () => navigationLoading,
}))

vi.mock('@/composables/useRoutePrefetch', () => ({
  useRoutePrefetch: () => routePrefetch,
}))

vi.mock('@/api/setup', () => ({
  getSetupStatus,
}))

vi.mock('@/i18n', () => ({
  i18n: {
    global: {
      t: (key: string) => key,
    },
  },
}))

describe('router document title watcher', () => {
  beforeEach(() => {
    vi.resetModules()
    localStorage.clear()
    sessionStorage.clear()
    document.title = ''
    vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
    authStore.isAuthenticated = true
    authStore.isAdmin = true
    authStore.isSimpleMode = false
    authStore.hasPendingAuthSession = false
    authStore.checkAuth.mockReset()
    appStore.siteName = 'Test Site'
    appStore.backendModeEnabled = false
    appStore.cachedPublicSettings = {
      payment_enabled: true,
      risk_control_enabled: true,
      custom_menu_items: [
        {
          id: 'public-docs',
          label: 'Public Docs',
          icon_svg: '',
          url: 'https://example.test/docs',
          visibility: 'public',
          sort_order: 0,
        },
      ],
    }
    adminSettingsStore.customMenuItems = [
      {
        id: 'admin-docs',
        label: 'Admin Docs',
        icon_svg: '',
        url: 'https://example.test/admin-docs',
        visibility: 'admin',
        sort_order: 1,
      },
    ]
    navigationLoading.startNavigation.mockReset()
    navigationLoading.endNavigation.mockReset()
    routePrefetch.triggerPrefetch.mockReset()
    adminComplianceStore.fetchStatus.mockReset()
    adminComplianceStore.requireAcknowledgement.mockReset()
    getSetupStatus.mockReset()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('refreshes custom page titles from public and admin menu items during navigation', async () => {
    const router = (await import('../index')).default

    await router.push('/custom/admin-docs')
    await router.isReady()
    await nextTick()

    expect(document.title).toBe('Admin Docs - Test Site')
    expect(authStore.checkAuth).toHaveBeenCalledTimes(1)
    expect(navigationLoading.startNavigation).toHaveBeenCalled()
    expect(routePrefetch.triggerPrefetch).toHaveBeenCalled()

    authStore.isAdmin = false
    appStore.siteName = 'Public Site'
    await router.push('/custom/public-docs')
    await router.isReady()
    await nextTick()

    expect(document.title).toBe('Public Docs - Public Site')
  })
})
