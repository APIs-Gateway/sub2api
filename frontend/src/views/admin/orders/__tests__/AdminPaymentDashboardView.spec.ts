import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AdminPaymentDashboardView from '../AdminPaymentDashboardView.vue'

const { getDashboard } = vi.hoisted(() => ({
  getDashboard: vi.fn()
}))

vi.mock('@/api/admin/payment', () => ({
  adminPaymentAPI: { getDashboard },
  default: { getDashboard }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn() })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const dashboardData = {
  payment_methods: [],
  top_users: [],
  daily_series: []
}

function mountView() {
  return mount(AdminPaymentDashboardView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        LoadingSpinner: true,
        Icon: true,
        OrderStatsCards: true,
        DailyRevenueChart: true
      }
    }
  })
}

describe('AdminPaymentDashboardView', () => {
  beforeEach(() => {
    getDashboard.mockReset()
    getDashboard.mockResolvedValue({ data: dashboardData })
  })

  it('shows a persistent error and retries after the initial request fails', async () => {
    getDashboard
      .mockRejectedValueOnce(new Error('temporary failure'))
      .mockResolvedValueOnce({ data: dashboardData })

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('dashboard.loadFailed')

    await wrapper.get('[role="alert"] button').trigger('click')
    await flushPromises()

    expect(getDashboard).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.findComponent({ name: 'OrderStatsCards' }).exists()).toBe(true)
  })

  it('keeps the previous dashboard visible when a refresh fails', async () => {
    const wrapper = mountView()
    await flushPromises()

    getDashboard.mockRejectedValueOnce(new Error('refresh failure'))
    await wrapper.get('button[title="common.refresh"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[role="alert"]').exists()).toBe(true)
    expect(wrapper.findComponent({ name: 'OrderStatsCards' }).exists()).toBe(true)
  })
})
