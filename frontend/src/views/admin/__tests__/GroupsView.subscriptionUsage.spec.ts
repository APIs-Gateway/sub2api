import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { AdminGroup } from '@/types'
import GroupsView from '../GroupsView.vue'

const {
  listGroups,
  getUsageSummary,
  getCapacitySummary,
  getModelsListCandidates
} = vi.hoisted(() => ({
  listGroups: vi.fn(),
  getUsageSummary: vi.fn(),
  getCapacitySummary: vi.fn(),
  getModelsListCandidates: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: {
      list: listGroups,
      getUsageSummary,
      getCapacitySummary,
      getModelsListCandidates,
      create: vi.fn(),
      update: vi.fn(),
      delete: vi.fn(),
      getAll: vi.fn(),
      updateSortOrder: vi.fn()
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn()
  })
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({
    isCurrentStep: vi.fn(),
    nextStep: vi.fn()
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const messages: Record<string, string> = {
    'admin.groups.subscription.subscription': 'Subscription',
    'admin.groups.subscription.standard': 'Standard',
    'admin.groups.subscription.noLimit': 'No limit',
    'admin.groups.limitDay': 'day',
    'admin.groups.limitWeek': 'week',
    'admin.groups.limitMonth': 'month',
    'admin.groups.usageTotal': 'Total'
  }
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key
    })
  }
})

const DataTableStub = {
  props: ['data'],
  template:
    '<div><div v-for="row in data" :key="row.id" :data-test="' +
    "'group-row-' + row.id" +
    '"><slot name="cell-billing_type" :row="row" /></div></div>'
}

const group = (id: number, overrides: Partial<AdminGroup> = {}): AdminGroup =>
  ({
    id,
    name: 'Group ' + id,
    description: null,
    platform: 'anthropic',
    rate_multiplier: 1,
    rpm_limit: 0,
    is_exclusive: false,
    status: 'active',
    subscription_type: 'subscription',
    daily_limit_usd: 10,
    weekly_limit_usd: null,
    monthly_limit_usd: null,
    allow_image_generation: false,
    image_rate_independent: false,
    image_rate_multiplier: 1,
    image_price_1k: null,
    image_price_2k: null,
    image_price_4k: null,
    claude_code_only: false,
    fallback_group_id: null,
    fallback_group_id_on_invalid_request: null,
    stable_priority_fallback_group_id: null,
    require_oauth_only: false,
    require_privacy_set: false,
    created_at: '2026-07-01T00:00:00Z',
    updated_at: '2026-07-01T00:00:00Z',
    model_routing: null,
    model_routing_enabled: false,
    mcp_xml_inject: false,
    ...overrides
  }) as AdminGroup

function mountView() {
  return mount(GroupsView, {
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
        PlatformIcon: true,
        Icon: true,
        GroupRateMultipliersModal: true,
        GroupRPMOverridesModal: true,
        GroupCapacityBadge: true,
        VueDraggable: true
      }
    }
  })
}

describe('admin GroupsView subscription usage', () => {
  beforeEach(() => {
    listGroups.mockReset()
    getUsageSummary.mockReset()
    getCapacitySummary.mockReset()
    getModelsListCandidates.mockReset()

    listGroups.mockResolvedValue({
      items: [
        group(1),
        group(2),
        group(3),
        group(4, {
          daily_limit_usd: null,
          weekly_limit_usd: null,
          monthly_limit_usd: null
        }),
        group(5, { subscription_type: 'standard' })
      ],
      total: 5,
      page: 1,
      page_size: 20,
      pages: 1
    })
    getUsageSummary.mockResolvedValue([
      { group_id: 1, today_cost: 5, total_cost: 123.456 },
      { group_id: 2, today_cost: 8, total_cost: 0 },
      { group_id: 3, today_cost: 10, total_cost: 0 },
      { group_id: 4, today_cost: 2, total_cost: 5.678 }
    ])
    getCapacitySummary.mockResolvedValue([])
    getModelsListCandidates.mockResolvedValue([])
  })

  it('renders subscription daily usage, limits, totals, and no-limit state', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-test="group-row-1"]').text()).toContain('$5.00 / $10.00/day')
    expect(wrapper.get('[data-test="group-row-1"]').text()).toContain('Total $123.5')
    expect(wrapper.get('[data-test="group-row-1"]').find('.text-gray-700').exists()).toBe(true)
    expect(wrapper.get('[data-test="group-row-2"]').find('.text-amber-600').exists()).toBe(true)
    expect(wrapper.get('[data-test="group-row-3"]').find('.text-red-600').exists()).toBe(true)
    expect(wrapper.get('[data-test="group-row-4"]').text()).toContain('No limit')
    expect(wrapper.get('[data-test="group-row-4"]').text()).toContain('Total $5.68')
    expect(wrapper.get('[data-test="group-row-5"]').text()).toBe('Standard')
  })
})
