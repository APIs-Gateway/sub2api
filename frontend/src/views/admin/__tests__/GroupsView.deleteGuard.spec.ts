import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { AdminGroup } from '@/types'
import GroupsView from '../GroupsView.vue'

// Hoist mocks so vi.mock factories can reference them.
const {
  listGroups,
  deleteGroup,
  getUsageSummary,
  getCapacitySummary,
  getModelsListCandidates,
  showError,
  showSuccess
} = vi.hoisted(() => ({
  listGroups: vi.fn(),
  deleteGroup: vi.fn(),
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
      duplicate: vi.fn(),
      getUsageSummary,
      getCapacitySummary,
      getModelsListCandidates,
      create: vi.fn(),
      update: vi.fn(),
      delete: deleteGroup,
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
      t: (key: string, params?: Record<string, unknown>) => {
        if (params && 'count' in params) return `${key}:${params.count}`
        if (params && 'name' in params) return `${key}:${params.name}`
        return key
      }
    })
  }
})

const makeGroup = (id: number, status: 'active' | 'inactive' = 'active'): AdminGroup =>
  ({
    id,
    name: `Group ${id}`,
    description: null,
    platform: 'openai',
    rate_multiplier: 1,
    rpm_limit: 0,
    is_exclusive: false,
    status,
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
    mcp_xml_inject: false,
    sort_order: 0
  }) as AdminGroup

const DataTableStub = {
  props: ['data'],
  template:
    '<div><div v-for="row in data" :key="row.id" :data-row="row.id"><slot name="cell-actions" :row="row" /></div></div>'
}

// Renders the ConfirmDialog @confirm handler so we can trigger it directly.
const ConfirmDialogStub = {
  props: ['show', 'message'],
  emits: ['confirm', 'cancel'],
  template: '<div v-if="show" data-test="delete-confirm"><button data-test="confirm-btn" @click="$emit(\'confirm\')">confirm</button></div>'
}

function mountView(groups: AdminGroup[]) {
  listGroups.mockResolvedValue({ items: groups, total: groups.length, page: 1, page_size: 20, pages: 1 })
  return mount(GroupsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
        },
        DataTable: DataTableStub,
        Pagination: true,
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' },
        ConfirmDialog: ConfirmDialogStub,
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

describe('GroupsView delete guard', () => {
  beforeEach(() => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    for (const fn of [listGroups, deleteGroup, getUsageSummary, getCapacitySummary, getModelsListCandidates, showError, showSuccess]) {
      fn.mockReset()
    }
    getUsageSummary.mockResolvedValue([])
    getCapacitySummary.mockResolvedValue([])
    getModelsListCandidates.mockResolvedValue([])
  })

  function getDeleteButton(wrapper: ReturnType<typeof mountView>, rowId: number) {
    const row = wrapper.get(`[data-row="${rowId}"]`)
    return row.findAll('button').find((b) => b.text() === 'common.delete')
  }

  // ── button state ────────────────────────────────────────────────────────────

  it('disables the delete button for an active group', async () => {
    const wrapper = mountView([makeGroup(1, 'active')])
    await flushPromises()

    const btn = getDeleteButton(wrapper, 1)
    expect(btn).toBeTruthy()
    expect(btn!.attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('sets a title tooltip on the disabled delete button', async () => {
    const wrapper = mountView([makeGroup(1, 'active')])
    await flushPromises()

    const btn = getDeleteButton(wrapper, 1)
    expect(btn!.attributes('title')).toBe('admin.groups.deleteBlockedNotInactive')
    wrapper.unmount()
  })

  it('enables the delete button for an inactive group', async () => {
    const wrapper = mountView([makeGroup(2, 'inactive')])
    await flushPromises()

    const btn = getDeleteButton(wrapper, 2)
    expect(btn).toBeTruthy()
    expect(btn!.attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('clicking a disabled delete button does not open the confirm dialog', async () => {
    const wrapper = mountView([makeGroup(1, 'active')])
    await flushPromises()

    const btn = getDeleteButton(wrapper, 1)
    await btn!.trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-test="delete-confirm"]').exists()).toBe(false)
    expect(deleteGroup).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  // ── successful delete ────────────────────────────────────────────────────────

  it('calls deleteGroup and shows success on confirm', async () => {
    deleteGroup.mockResolvedValue({ message: 'ok' })
    listGroups
      .mockResolvedValueOnce({ items: [makeGroup(2, 'inactive')], total: 1, page: 1, page_size: 20, pages: 1 })
      .mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 1 })

    const wrapper = mountView([makeGroup(2, 'inactive')])
    await flushPromises()

    const btn = getDeleteButton(wrapper, 2)
    await btn!.trigger('click')
    await flushPromises()

    await wrapper.get('[data-test="confirm-btn"]').trigger('click')
    await flushPromises()

    expect(deleteGroup).toHaveBeenCalledWith(2)
    expect(showSuccess).toHaveBeenCalledWith('admin.groups.groupDeleted')
    wrapper.unmount()
  })

  // ── structured error reasons ─────────────────────────────────────────────────

  it('shows deleteBlockedNotInactive message when reason is GROUP_DELETE_NOT_INACTIVE', async () => {
    deleteGroup.mockRejectedValueOnce({ response: { data: { reason: 'GROUP_DELETE_NOT_INACTIVE', message: 'raw message' } } })
    const wrapper = mountView([makeGroup(2, 'inactive')])
    await flushPromises()

    await getDeleteButton(wrapper, 2)!.trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="confirm-btn"]').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('admin.groups.deleteBlockedNotInactive')
    wrapper.unmount()
  })

  it('shows deleteBlockedHasKeys with key_count when reason is GROUP_DELETE_HAS_KEYS', async () => {
    deleteGroup.mockRejectedValueOnce({
      response: { data: { reason: 'GROUP_DELETE_HAS_KEYS', metadata: { key_count: '3' } } }
    })
    const wrapper = mountView([makeGroup(2, 'inactive')])
    await flushPromises()

    await getDeleteButton(wrapper, 2)!.trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="confirm-btn"]').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('admin.groups.deleteBlockedHasKeys:3')
    wrapper.unmount()
  })

  it('shows deleteBlockedHasPlans with plan_count when reason is GROUP_DELETE_HAS_PLANS', async () => {
    deleteGroup.mockRejectedValueOnce({
      response: { data: { reason: 'GROUP_DELETE_HAS_PLANS', metadata: { plan_count: '2' } } }
    })
    const wrapper = mountView([makeGroup(2, 'inactive')])
    await flushPromises()

    await getDeleteButton(wrapper, 2)!.trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="confirm-btn"]').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('admin.groups.deleteBlockedHasPlans:2')
    wrapper.unmount()
  })

  it('falls back to response message for an unknown reason', async () => {
    deleteGroup.mockRejectedValueOnce({
      response: { data: { reason: 'SOME_OTHER_ERROR', message: 'something went wrong' } }
    })
    const wrapper = mountView([makeGroup(2, 'inactive')])
    await flushPromises()

    await getDeleteButton(wrapper, 2)!.trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="confirm-btn"]').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('something went wrong')
    wrapper.unmount()
  })

  it('falls back to failedToDelete i18n key when error has no message', async () => {
    deleteGroup.mockRejectedValueOnce({ response: { data: {} } })
    const wrapper = mountView([makeGroup(2, 'inactive')])
    await flushPromises()

    await getDeleteButton(wrapper, 2)!.trigger('click')
    await flushPromises()
    await wrapper.get('[data-test="confirm-btn"]').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('admin.groups.failedToDelete')
    wrapper.unmount()
  })
})
