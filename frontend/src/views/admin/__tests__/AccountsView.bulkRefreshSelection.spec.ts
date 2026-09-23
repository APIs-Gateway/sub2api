import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const {
  listAccounts,
  listWithEtag,
  batchRefresh,
  getBatchTodayStats,
  getAllProxies,
  getAllGroups,
  showError,
  showSuccess
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  batchRefresh: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag,
      getBatchTodayStats,
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh,
      toggleSchedulable: vi.fn()
    },
    proxies: {
      getAll: getAllProxies
    },
    groups: {
      getAll: getAllGroups
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
    showInfo: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    token: 'test-token'
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

const makeAccounts = (count: number) => Array.from({ length: count }, (_, index) => ({
  id: index + 1,
  name: `account-${index + 1}`,
  platform: 'anthropic',
  type: 'oauth',
  status: 'active',
  schedulable: true,
  created_at: '2026-03-07T10:00:00Z',
  updated_at: '2026-03-07T10:00:00Z'
}))

const AccountBulkActionsBarStub = {
  props: ['selectedIds'],
  emits: ['select-page', 'clear', 'refresh-token'],
  template: `
    <div>
      <button data-test="select-page" @click="$emit('select-page')">select page</button>
      <button data-test="refresh-token" @click="$emit('refresh-token')">refresh token</button>
    </div>
  `
}

const mountView = () => mount(AccountsView, {
  global: {
    stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      TablePageLayout: {
        template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
      },
      DataTable: {
        props: ['data'],
        template: '<div data-test="data-table"><div v-for="row in data" :key="row.id"><slot name="cell-select" :row="row" /></div></div>'
      },
      Pagination: true,
      ConfirmDialog: true,
      AccountTableActions: { template: '<div><slot name="beforeCreate" /><slot name="after" /></div>' },
      AccountTableFilters: { template: '<div></div>' },
      AccountBulkActionsBar: AccountBulkActionsBarStub,
      AccountActionMenu: true,
      ImportDataModal: true,
      ReAuthAccountModal: true,
      AccountTestModal: true,
      AccountStatsModal: true,
      ScheduledTestsPanel: true,
      SyncFromCrsModal: true,
      TempUnschedStatusModal: true,
      ErrorPassthroughRulesModal: true,
      TLSFingerprintProfilesModal: true,
      CreateAccountModal: true,
      EditAccountModal: true,
      BulkEditAccountModal: true,
      PlatformTypeBadge: true,
      AccountCapacityCell: true,
      AccountStatusIndicator: true,
      AccountTodayStatsCell: true,
      AccountGroupsCell: true,
      AccountUsageCell: true,
      Icon: true
    }
  }
})

describe('admin AccountsView bulk token refresh selection', () => {
  beforeEach(() => {
    localStorage.clear()
    listAccounts.mockReset()
    listWithEtag.mockReset()
    batchRefresh.mockReset()
    getBatchTodayStats.mockReset()
    getAllProxies.mockReset()
    getAllGroups.mockReset()
    showError.mockReset()
    showSuccess.mockReset()

    listAccounts.mockResolvedValue({ items: makeAccounts(3), total: 3, page: 1, page_size: 20, pages: 1 })
    listWithEtag.mockResolvedValue({ notModified: true, etag: null, data: null })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getAllProxies.mockResolvedValue([])
    getAllGroups.mockResolvedValue([])
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it.each([
    { name: 'keeps only failed accounts selected', result: { total: 3, success: 2, failed: 1, errors: [{ account_id: 2, error: 'no refresh token available' }] }, expectedIds: [2] },
    { name: 'clears the selection after every account succeeds', result: { total: 3, success: 3, failed: 0 }, expectedIds: [] },
    { name: 'keeps the original selection when failure details are missing', result: { total: 3, success: 2, failed: 1 }, expectedIds: [1, 2, 3] },
  ])('$name after a batch token refresh', async ({ result, expectedIds }) => {
    batchRefresh.mockResolvedValue(result)
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="select-page"]').trigger('click')
    await wrapper.get('[data-test="refresh-token"]').trigger('click')
    await flushPromises()

    expect(batchRefresh).toHaveBeenCalledWith([1, 2, 3])
    expect(wrapper.getComponent(AccountBulkActionsBarStub).props('selectedIds')).toEqual(expectedIds)
    expect(wrapper.findAll<HTMLInputElement>('[data-test="data-table"] input').map(input => input.element.checked))
      .toEqual([1, 2, 3].map(id => expectedIds.includes(id)))
    if (result.failed > 0) {
      expect(showError).toHaveBeenCalledWith('admin.accounts.bulkActions.partialSuccess')
      await wrapper.get('[data-test="refresh-token"]').trigger('click')
      await flushPromises()
      expect(batchRefresh).toHaveBeenLastCalledWith(expectedIds)
    }
    wrapper.unmount()
  })
})
