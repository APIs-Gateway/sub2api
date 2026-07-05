import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { UserSubscription } from '@/types'
import SubscriptionsView from '../SubscriptionsView.vue'

const { listSubscriptions, getAllGroups } = vi.hoisted(() => ({
  listSubscriptions: vi.fn(),
  getAllGroups: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    subscriptions: {
      list: listSubscriptions,
      assign: vi.fn(),
      extend: vi.fn(),
      revoke: vi.fn(),
      resetQuota: vi.fn()
    },
    groups: {
      getAll: getAllGroups
    },
    usage: {
      searchUsers: vi.fn()
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn()
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, number>) => {
        if (key === 'admin.subscriptions.plan.nameWithDays') {
          return `订阅 ${params?.daily} / 天，共 ${params?.days} 天`
        }
        if (key === 'admin.subscriptions.plan.name') {
          return `订阅 ${params?.daily} / 天`
        }
        if (key === 'admin.subscriptions.plan.dailyAmount') {
          return '每日额度'
        }
        if (key === 'admin.subscriptions.plan.remaining') {
          return '剩余'
        }
        if (key === 'admin.subscriptions.plan.total') {
          return '总额'
        }
        if (key === 'admin.subscriptions.daily') {
          return '每日'
        }
        if (key === 'admin.subscriptions.weekly') {
          return '每周'
        }
        if (key === 'admin.subscriptions.monthly') {
          return '每月'
        }
        return key
      }
    })
  }
})

const DataTableStub = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.id" data-test="subscription-row">
        <div data-test="plan-cell">
          <slot name="cell-plan" :row="row" />
        </div>
        <div data-test="usage-cell">
          <slot name="cell-usage" :row="row" />
        </div>
      </div>
    </div>
  `
}

function subscription(overrides: Partial<UserSubscription> = {}): UserSubscription {
  return {
    id: 100,
    user_id: 1,
    group_id: 10,
    status: 'active',
    starts_at: '2026-07-01T00:00:00Z',
    expires_at: '2026-07-31T00:00:00Z',
    daily_window_start: null,
    weekly_window_start: null,
    monthly_window_start: null,
    daily_usage_usd: 0,
    weekly_usage_usd: 0,
    monthly_usage_usd: 0,
    granted_total_usd: 300,
    daily_amount_usd: 10,
    consumed_usd: 50,
    clawed_usd: 0,
    remaining_usd: 250,
    consumption_day: 5,
    calendar_day: 2,
    created_at: '2026-07-01T00:00:00Z',
    updated_at: '2026-07-01T00:00:00Z',
    group: {
      id: 10,
      name: 'Pro',
      platform: 'anthropic',
      status: 'active',
      rate_multiplier: 1,
      rate_multipliers: [],
      allowed_models: [],
      blocked_models: [],
      claude_code_only: false,
      fallback_group_id: null,
      openai_messages_enabled: false,
      openai_messages_allowed_models: [],
      rpm_limit: 0,
      rpm_mode: 'fixed',
      rpm_overrides: [],
      model_rate_multipliers: [],
      created_at: '2026-07-01T00:00:00Z',
      updated_at: '2026-07-01T00:00:00Z'
    },
    ...overrides
  }
}

describe('admin SubscriptionsView usage progress', () => {
  beforeEach(() => {
    listSubscriptions.mockReset()
    getAllGroups.mockReset()
    getAllGroups.mockResolvedValue([])
  })

  it('renders window usage for D/T subscription cards instead of stale burn-down counters', async () => {
    listSubscriptions.mockResolvedValue({
      items: [
        subscription({
          id: 103,
          daily_amount_usd: 60,
          granted_total_usd: 1800,
          consumed_usd: 0,
          remaining_usd: 1800,
          daily_usage_usd: 22.11428095,
          weekly_usage_usd: 139.7676467,
          monthly_usage_usd: 139.7676467,
          daily_limit_usd: 60,
          weekly_limit_usd: 420,
          monthly_limit_usd: 1800
        })
      ],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })

    const wrapper = mount(SubscriptionsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: {
            template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
          },
          DataTable: DataTableStub,
          Pagination: true,
          BaseDialog: true,
          ConfirmDialog: true,
          EmptyState: true,
          Select: true,
          GroupBadge: true,
          GroupOptionItem: true,
          Icon: true,
          Teleport: true
        }
      }
    })

    await flushPromises()

    const text = wrapper.text()
    expect(text).toContain('$22.11 / $60.00')
    expect(text).toContain('$139.77 / $420.00')
    expect(text).toContain('$139.77 / $1800.00')
    expect(text).not.toContain('$0.00 / $1800.00')
  })

  it('lists subscriptions without group or platform filters and renders plan details from card fields', async () => {
    listSubscriptions.mockResolvedValue({
      items: [
        subscription({
          id: 102,
          group_id: null,
          group: undefined,
          daily_amount_usd: 0,
          daily_limit_usd: 30,
          weekly_limit_usd: 210,
          monthly_limit_usd: 900,
          daily_usage_usd: 12,
          weekly_usage_usd: 40,
          monthly_usage_usd: 80,
          granted_total_usd: 0,
          remaining_usd: null
        })
      ],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })

    const wrapper = mount(SubscriptionsView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: {
            template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
          },
          DataTable: DataTableStub,
          Pagination: true,
          BaseDialog: true,
          ConfirmDialog: true,
          EmptyState: true,
          Select: true,
          GroupBadge: true,
          GroupOptionItem: true,
          Icon: true,
          Teleport: true
        }
      }
    })

    await flushPromises()

    expect(getAllGroups).not.toHaveBeenCalled()
    expect(listSubscriptions).toHaveBeenCalledWith(
      1,
      expect.any(Number),
      expect.not.objectContaining({
        group_id: expect.anything(),
        platform: expect.anything()
      }),
      expect.any(Object)
    )
    expect(wrapper.text()).toContain('订阅 30.00 / 天')
    expect(wrapper.text()).toContain('每日额度 $30.00')
    expect(wrapper.text()).toContain('$12.00')
    expect(wrapper.text()).toContain('$210.00')
    expect(wrapper.text()).toContain('$900.00')
  })
})
