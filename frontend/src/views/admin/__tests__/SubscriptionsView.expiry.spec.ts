import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

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
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}:${JSON.stringify(params)}` : key
    })
  }
})

const DataTableStub = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.id" :data-test="'expires-' + row.id">
        <slot name="cell-expires_at" :row="row" :value="row.expires_at" />
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

describe('admin SubscriptionsView remaining expiry labels', () => {
  const now = new Date(2026, 6, 30, 9, 0)

  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(now)
    listSubscriptions.mockReset()
    getAllGroups.mockReset()
    getAllGroups.mockResolvedValue([])
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('shows days, hours/minutes or minutes remaining and nothing once expired', async () => {
    const at = (ms: number) => new Date(now.getTime() + ms).toISOString()
    listSubscriptions.mockResolvedValue({
      items: [
        subscription({ id: 1, expires_at: at(2 * 24 * 60 * 60 * 1000 + 1) }),
        subscription({ id: 2, expires_at: at((2 * 60 + 30) * 60 * 1000) }),
        subscription({ id: 3, expires_at: at(45 * 60 * 1000) }),
        subscription({ id: 4, expires_at: at(-60 * 1000) })
      ],
      total: 4,
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

    expect(wrapper.get('[data-test="expires-1"]').text()).toContain(
      'admin.subscriptions.daysRemaining:{"days":3}'
    )
    expect(wrapper.get('[data-test="expires-2"]').text()).toContain(
      'admin.subscriptions.hoursMinutesRemaining:{"hours":2,"minutes":30}'
    )
    expect(wrapper.get('[data-test="expires-3"]').text()).toContain(
      'admin.subscriptions.minutesRemaining:{"minutes":45}'
    )
    const expired = wrapper.get('[data-test="expires-4"]').text()
    expect(expired).not.toContain('Remaining')
    expect(expired).not.toContain('daysRemaining')
  })
})
