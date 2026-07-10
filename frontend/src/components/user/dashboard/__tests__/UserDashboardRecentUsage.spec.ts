import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import UserDashboardRecentUsage from '../UserDashboardRecentUsage.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

vi.mock('@/utils/format', () => ({
  formatDateTime: () => '2026-07-10 12:00',
}))

describe('UserDashboardRecentUsage', () => {
  it('includes cache reads and writes in the recent usage token total', () => {
    const wrapper = mount(UserDashboardRecentUsage, {
      props: {
        loading: false,
        data: [{
          id: 1,
          model: 'gpt-5.6-terra',
          created_at: '2026-07-10T04:00:00Z',
          actual_cost: 0.1,
          total_cost: 0.1,
          input_tokens: 100,
          output_tokens: 200,
          cache_creation_tokens: 300,
          cache_read_tokens: 700,
        }] as any[],
      },
      global: {
        stubs: {
          LoadingSpinner: true,
          EmptyState: true,
          Icon: true,
          RouterLink: { template: '<a><slot /></a>' },
        },
      },
    })

    expect(wrapper.text()).toContain('1,300 tokens')
  })
})
