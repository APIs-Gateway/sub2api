import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import AppLayout from '../AppLayout.vue'

vi.mock('@/stores', () => ({ useAppStore: () => ({ sidebarCollapsed: false }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { role: 'user' } }) }))
vi.mock('@/stores/onboarding', () => ({ useOnboardingStore: () => ({ setReplayCallback: vi.fn() }) }))
vi.mock('@/composables/useOnboardingTour', () => ({ useOnboardingTour: () => ({ replayTour: vi.fn() }) }))
vi.mock('@/styles/onboarding.css', () => ({}))

describe('AppLayout 低余额横幅挂载', () => {
  it('横幅在顶栏之后、页面内容之前', () => {
    const wrapper = mount(AppLayout, {
      slots: { default: '<p data-testid="page">page</p>' },
      global: {
        stubs: {
          AppSidebar: true,
          AppHeader: { template: '<header data-testid="header" />' },
          SidebarBalanceCard: true,
          LowBalanceBanner: { template: '<div data-testid="banner" />' },
        },
      },
    })

    const order = Array.from(wrapper.element.querySelectorAll('[data-testid]')).map((el) =>
      el.getAttribute('data-testid')
    )
    expect(order).toEqual(['header', 'banner', 'page'])
  })
})
