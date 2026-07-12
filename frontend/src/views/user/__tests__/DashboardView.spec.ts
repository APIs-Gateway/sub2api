import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import DashboardView from '../DashboardView.vue'

const mocks = vi.hoisted(() => ({
  getDashboardStats: vi.fn(),
  getDashboardTrend: vi.fn(),
  getDashboardModels: vi.fn(),
  getByDateRange: vi.fn(),
  getMyPlatformQuotas: vi.fn()
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    user: { balance: 0 },
    isSimpleMode: false
  })
}))

vi.mock('@/api/usage', () => ({
  usageAPI: {
    getDashboardStats: mocks.getDashboardStats,
    getDashboardTrend: mocks.getDashboardTrend,
    getDashboardModels: mocks.getDashboardModels,
    getByDateRange: mocks.getByDateRange
  }
}))

vi.mock('@/api/user', () => ({
  getMyPlatformQuotas: mocks.getMyPlatformQuotas
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

describe('user DashboardView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.getDashboardStats.mockResolvedValue({ total_api_keys: 1 })
    mocks.getDashboardTrend.mockResolvedValue({ trend: [] })
    mocks.getDashboardModels.mockResolvedValue({ models: [] })
    mocks.getByDateRange.mockResolvedValue({ items: [] })
    mocks.getMyPlatformQuotas.mockResolvedValue({ platform_quotas: [] })
  })

  it('shows a persistent error and retries the primary statistics request', async () => {
    mocks.getDashboardStats
      .mockRejectedValueOnce(new Error('temporary failure'))
      .mockResolvedValueOnce({ total_api_keys: 1 })

    const wrapper = mount(DashboardView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          LoadingSpinner: true,
          UserDashboardStats: true,
          UserDashboardCharts: true,
          UserDashboardRecentUsage: true,
          UserDashboardQuickActions: true
        }
      }
    })

    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('dashboard.loadFailed')

    await wrapper.get('[role="alert"] button').trigger('click')
    await flushPromises()

    expect(mocks.getDashboardStats).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.findComponent({ name: 'UserDashboardStats' }).exists()).toBe(true)
  })
})

