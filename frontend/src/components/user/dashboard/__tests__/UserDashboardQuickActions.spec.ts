import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}:${JSON.stringify(params)}` : key,
    }),
  }
})

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    cachedPublicSettings: {
      api_base_url: 'https://api.example.com',
      payment_enabled: false,
      custom_endpoints: [],
    },
  }),
}))

import UserDashboardQuickActions from '../UserDashboardQuickActions.vue'

const stubs = { Icon: true }

describe('UserDashboardQuickActions copyAiPrompt', () => {
  beforeEach(() => {
    // Reset clipboard mock between tests
    Object.defineProperty(navigator, 'clipboard', {
      writable: true,
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
    })
  })

  it('shows the copied confirmation when clipboard write succeeds', async () => {
    const wrapper = mount(UserDashboardQuickActions, { global: { stubs } })

    const copyBtn = wrapper.findAll('button').find((b) => b.text().includes('dashboard.copyAiPrompt'))
    expect(copyBtn).toBeDefined()
    await copyBtn!.trigger('click')
    await flushPromises()

    // Should show the "copied" label, not the failure notice
    expect(wrapper.text()).toContain('dashboard.aiPromptCopied')
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
  })

  it('shows a failure notice and NOT the copied label when clipboard write fails', async () => {
    Object.defineProperty(navigator, 'clipboard', {
      writable: true,
      value: { writeText: vi.fn().mockRejectedValue(new Error('NotAllowedError')) },
    })

    const wrapper = mount(UserDashboardQuickActions, { global: { stubs } })

    const copyBtn = wrapper.findAll('button').find((b) => b.text().includes('dashboard.copyAiPrompt'))
    expect(copyBtn).toBeDefined()
    await copyBtn!.trigger('click')
    await flushPromises()

    // Must NOT show the success label — that would mislead the user
    expect(wrapper.text()).not.toContain('dashboard.aiPromptCopied')
    // Must show the failure alert
    const alert = wrapper.find('[role="alert"]')
    expect(alert.exists()).toBe(true)
    expect(alert.text()).toContain('dashboard.apiEntry.copyFailed')
  })
})
