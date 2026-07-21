import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { AdminGroup } from '@/types'
import GroupsView from '../GroupsView.vue'

const {
  listGroups,
  duplicate,
  getUsageSummary,
  getCapacitySummary,
  getModelsListCandidates,
  showError,
  showSuccess
} = vi.hoisted(() => ({
    listGroups: vi.fn(),
    duplicate: vi.fn(),
    getUsageSummary: vi.fn(),
    getCapacitySummary: vi.fn(),
    getModelsListCandidates: vi.fn(),
    showError: vi.fn(),
    showSuccess: vi.fn()
  }))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: {
      list: listGroups,
      duplicate,
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
  useAppStore: () => ({ showError, showSuccess })
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({ isCurrentStep: vi.fn(), nextStep: vi.fn() })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params?.name ? `${key}:${params.name}` : key
    })
  }
})

const group = (id: number): AdminGroup =>
  ({
    id,
    name: `Group ${id}`,
    description: null,
    platform: 'openai',
    rate_multiplier: 1,
    rpm_limit: 0,
    is_exclusive: false,
    status: 'active',
    subscription_type: 'standard',
    daily_limit_usd: null,
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
    mcp_xml_inject: false
  }) as AdminGroup

const DataTableStub = {
  props: ['data'],
  template: '<div><div v-for="row in data" :key="row.id"><slot name="cell-actions" :row="row" /></div></div>'
}

function mountView() {
  return mount(GroupsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
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

describe('admin GroupsView duplicate action', () => {
  beforeEach(() => {
    listGroups.mockReset()
    duplicate.mockReset()
    showError.mockReset()
    showSuccess.mockReset()
    getUsageSummary.mockReset()
    getCapacitySummary.mockReset()
    getModelsListCandidates.mockReset()
    listGroups.mockResolvedValue({ items: [group(7)], total: 1, page: 1, page_size: 20, pages: 1 })
    duplicate.mockResolvedValue({ ...group(8), name: 'Group 7 (Copy)', status: 'inactive' })
    getUsageSummary.mockResolvedValue([])
    getCapacitySummary.mockResolvedValue([])
    getModelsListCandidates.mockResolvedValue([])
  })

  it('calls the duplicate API and reloads the group list', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="group-duplicate"]').trigger('click')
    await flushPromises()

    expect(duplicate).toHaveBeenCalledWith(7)
    expect(listGroups).toHaveBeenCalledTimes(2)
  })

  it('reports a duplicate failure and releases the busy state', async () => {
    duplicate.mockRejectedValueOnce(new Error('network timeout'))
    const wrapper = mountView()
    await flushPromises()

    const button = wrapper.get('[data-testid="group-duplicate"]')
    await button.trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalled()
    expect(button.attributes('disabled')).toBeUndefined()
    expect(listGroups).toHaveBeenCalledTimes(1)
  })

  it('ignores a second click while duplication is in progress', async () => {
    let resolveDuplicate: (value: AdminGroup) => void = () => undefined
    duplicate.mockImplementationOnce(
      () => new Promise<AdminGroup>((resolve) => {
        resolveDuplicate = resolve
      })
    )
    const wrapper = mountView()
    await flushPromises()

    const button = wrapper.get('[data-testid="group-duplicate"]')
    await button.trigger('click')
    await flushPromises()
    expect(button.attributes('disabled')).toBeDefined()

    await button.trigger('click')
    expect(duplicate).toHaveBeenCalledTimes(1)

    resolveDuplicate({ ...group(8), name: 'Group 7 (Copy)', status: 'inactive' })
    await flushPromises()
    expect(button.attributes('disabled')).toBeUndefined()
  })
})
