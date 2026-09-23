import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { AdminGroup } from '@/types'
import GroupsView from '../GroupsView.vue'

const {
  listGroups,
  duplicate,
  updateGroup,
  getUsageSummary,
  getCapacitySummary,
  getModelsListCandidates,
  showError,
  showSuccess
} = vi.hoisted(() => ({
    listGroups: vi.fn(),
    duplicate: vi.fn(),
    updateGroup: vi.fn(),
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
      update: updateGroup,
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

const BaseDialogStub = {
  props: { show: { type: Boolean, default: false } },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
}

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
        BaseDialog: BaseDialogStub,
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

// 上游 706b5676a：分组创建 / 更新失败时展示后端标准化错误的 message（extractApiErrorMessage），
// 而不是读不存在的 detail 字段退回通用文案。fork 的 duplicate spec 结构与上游不同，单独成文件。
describe('admin GroupsView API error messages', () => {
  beforeEach(() => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    for (const fn of [
      listGroups,
      duplicate,
      updateGroup,
      showError,
      showSuccess,
      getUsageSummary,
      getCapacitySummary,
      getModelsListCandidates
    ]) {
      fn.mockReset()
    }
    listGroups.mockResolvedValue({ items: [group(7)], total: 1, page: 1, page_size: 20, pages: 1 })
    getUsageSummary.mockResolvedValue([])
    getCapacitySummary.mockResolvedValue([])
    getModelsListCandidates.mockResolvedValue([])
  })

  it('shows the standardized API message when updating a group fails', async () => {
    updateGroup.mockRejectedValueOnce({
      status: 409,
      code: 409,
      message: 'group name already exists',
      reason: 'GROUP_EXISTS'
    })
    const wrapper = mountView()
    await flushPromises()

    const editButton = wrapper.findAll('button').find((button) => button.text() === 'common.edit')
    expect(editButton).toBeTruthy()
    await editButton!.trigger('click')
    await flushPromises()
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()

    expect(updateGroup).toHaveBeenCalledTimes(1)
    expect(showError).toHaveBeenCalledWith('group name already exists')
    wrapper.unmount()
  })
})
