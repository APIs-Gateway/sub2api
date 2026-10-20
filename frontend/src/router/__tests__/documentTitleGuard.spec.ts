import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

vi.mock('@/api/setup', () => ({
  getSetupStatus: vi.fn().mockResolvedValue({ needs_setup: false })
}))

vi.mock('@/api', () => ({
  authAPI: {
    getCurrentUser: vi.fn().mockResolvedValue({
      data: {
        id: 1,
        email: 'user@example.com',
        role: 'user'
      }
    })
  },
  isTotp2FARequired: () => false
}))

vi.mock('@/composables/useNavigationLoading', () => ({
  useNavigationLoadingState: () => ({
    startNavigation: vi.fn(),
    endNavigation: vi.fn(),
    isLoading: { value: false }
  })
}))

vi.mock('@/composables/useRoutePrefetch', () => ({
  useRoutePrefetch: () => ({
    triggerPrefetch: vi.fn(),
    cancelPendingPrefetch: vi.fn(),
    resetPrefetchState: vi.fn()
  })
}))

vi.mock('@/views/user/CustomPageView.vue', () => ({
  default: { template: '<div />' }
}))

describe('router document title guard', () => {
  beforeEach(() => {
    vi.resetModules()
    setActivePinia(createPinia())
    localStorage.clear()
    localStorage.setItem('auth_token', 'test-token')
    localStorage.setItem('auth_user', JSON.stringify({
      id: 1,
      email: 'user@example.com',
      role: 'user'
    }))
    document.title = ''
    window.scrollTo = vi.fn()
    window.history.pushState(null, '', '/')
  })

  it('uses loaded custom menu labels when navigating to custom pages', async () => {
    const [{ default: router }, { useAppStore }] = await Promise.all([
      import('../index'),
      import('@/stores/app')
    ])
    const appStore = useAppStore()
    appStore.siteName = 'Demo Site'
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

    await router.push('/custom/scheduler')
    await router.isReady()

    expect(document.title).toBe('账号调度器 - Demo Site')
  })
})
