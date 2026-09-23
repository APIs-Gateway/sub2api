import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import UserDashboardStats from '../UserDashboardStats.vue'

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

vi.mock('@/components/user/dashboard/CheckinCard.vue', () => ({
  default: { name: 'CheckinCard', template: '<div />' },
}))

describe('UserDashboardStats', () => {
  const mountStats = (stats: Record<string, unknown>) =>
    mount(UserDashboardStats, {
      props: {
        stats: stats as any,
        balance: 0,
        isSimple: false,
      },
      global: {
        stubs: {
          CheckinCard: true,
          Icon: true,
        },
      },
    })

  it('includes cache creation and read tokens in the token card breakdown', () => {
    const wrapper = mountStats({
      today_tokens: 370,
      today_input_tokens: 100,
      today_output_tokens: 200,
      today_cache_creation_tokens: 30,
      today_cache_read_tokens: 40,
      total_tokens: 911,
      total_input_tokens: 400,
      total_output_tokens: 500,
      total_cache_creation_tokens: 5,
      total_cache_read_tokens: 6,
    })

    const text = wrapper.text()
    expect(text).toContain('dashboard.input 100 · dashboard.output 200 · dashboard.cache 70')
    expect(text).toContain('dashboard.input 400 · dashboard.output 500 · dashboard.cache 11')
  })

  it('treats missing cache token fields as zero', () => {
    const wrapper = mountStats({
      today_input_tokens: 1,
      today_output_tokens: 2,
      total_input_tokens: 3,
      total_output_tokens: 4,
    })

    const text = wrapper.text()
    expect(text).toContain('dashboard.output 2 · dashboard.cache 0')
    expect(text).toContain('dashboard.output 4 · dashboard.cache 0')
  })
})
