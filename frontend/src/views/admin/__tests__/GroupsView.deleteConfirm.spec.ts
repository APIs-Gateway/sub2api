import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { AdminGroup } from '@/types'
import GroupsView from '../GroupsView.vue'

const { listGroups, deleteGroup, getUsageSummary, getCapacitySummary, getModelsListCandidates } =
  vi.hoisted(() => ({
    listGroups: vi.fn(),
    deleteGroup: vi.fn(),
    getUsageSummary: vi.fn(),
    getCapacitySummary: vi.fn(),
    getModelsListCandidates: vi.fn()
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
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() })
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

const group = (id: number, subscriptionType: 'standard' | 'subscription'): AdminGroup =>
  ({
    id,
    name: `Group ${id}`,
    description: null,
    platform: 'openai',
    rate_multiplier: 1,
    rpm_limit: 0,
    is_exclusive: subscriptionType === 'subscription',
    status: 'active',
    subscription_type: subscriptionType,
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
  template:
    '<div><div v-for="row in data" :key="row.id" :data-row="row.id"><slot name="cell-actions" :row="row" /></div></div>'
}

// 只关心确认框收到的文案，把 message 直接渲染出来。
const ConfirmDialogStub = {
  props: ['show', 'message'],
  template: '<div v-if="show" data-test="delete-confirm">{{ message }}</div>'
}

function mountView() {
  return mount(GroupsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template:
            '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
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

describe('admin GroupsView delete confirmation', () => {
  beforeEach(() => {
    for (const fn of [listGroups, deleteGroup, getUsageSummary, getCapacitySummary, getModelsListCandidates]) {
      fn.mockReset()
    }
    listGroups.mockResolvedValue({
      items: [group(7, 'standard'), group(8, 'subscription')],
      total: 2,
      page: 1,
      page_size: 20,
      pages: 1
    })
    getUsageSummary.mockResolvedValue([])
    getCapacitySummary.mockResolvedValue([])
    getModelsListCandidates.mockResolvedValue([])
  })

  async function openDeleteDialog(rowId: number) {
    const wrapper = mountView()
    await flushPromises()
    const row = wrapper.get(`[data-row="${rowId}"]`)
    const deleteButton = row.findAll('button').find((button) => button.text() === 'common.delete')
    expect(deleteButton).toBeTruthy()
    await deleteButton!.trigger('click')
    await flushPromises()
    return wrapper
  }

  it.each([
    [7, 'standard'],
    [8, 'subscription']
  ])('uses the single delete message for a %s-id %s group', async (id) => {
    const wrapper = await openDeleteDialog(id)

    // 订阅卡不随分组删除，不再按分组类型换一条「会删订阅记录」的文案。
    expect(wrapper.get('[data-test="delete-confirm"]').text()).toBe(
      `admin.groups.deleteConfirm:Group ${id}`
    )
    expect(deleteGroup).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
