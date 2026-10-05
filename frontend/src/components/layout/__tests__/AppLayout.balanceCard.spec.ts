import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'

import AppLayout from '../AppLayout.vue'

const state = vi.hoisted(() => ({
  app: { sidebarCollapsed: false },
  auth: { user: { role: 'user' } },
}))

vi.mock('@/stores', () => ({ useAppStore: () => state.app }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => state.auth }))
vi.mock('@/stores/onboarding', () => ({ useOnboardingStore: () => ({ setReplayCallback: vi.fn() }) }))
vi.mock('@/composables/useOnboardingTour', () => ({ useOnboardingTour: () => ({ replayTour: vi.fn() }) }))
vi.mock('@/styles/onboarding.css', () => ({}))

// 侧栏只负责把 balance-card 插槽渲染出来，并带上 collapsed 参数。
const SidebarStub = defineComponent({
  setup(_, { slots }) {
    return () => h('aside', { 'data-testid': 'sidebar' }, slots['balance-card']?.({ collapsed: true }))
  },
})
const CardStub = defineComponent({
  props: { collapsed: Boolean },
  setup(props) {
    return () => h('div', { 'data-testid': 'card', 'data-collapsed': String(props.collapsed) })
  },
})

describe('AppLayout 余额卡挂载', () => {
  it('把余额卡放进侧栏的 balance-card 插槽，并传入图标态', () => {
    const wrapper = mount(AppLayout, {
      slots: { default: '<p>page</p>' },
      global: {
        stubs: { AppSidebar: SidebarStub, AppHeader: true, SidebarBalanceCard: CardStub },
      },
    })

    const card = wrapper.find('[data-testid="sidebar"] [data-testid="card"]')
    expect(card.exists()).toBe(true)
    expect(card.attributes('data-collapsed')).toBe('true')
    expect(wrapper.text()).toContain('page')
  })
})
