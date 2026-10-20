import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const {
  listAccounts,
  listWithEtag,
  listIds,
  deleteAccount,
  exportData,
  batchClearError,
  getBatchTodayStats,
  getAllProxies,
  getAllGroups,
  showError,
  showSuccess
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  listIds: vi.fn(),
  deleteAccount: vi.fn(),
  exportData: vi.fn(),
  batchClearError: vi.fn(),
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
      listIds,
      getBatchTodayStats,
      delete: deleteAccount,
      exportData,
      batchClearError,
      batchRefresh: vi.fn(),
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
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}|${Object.values(params).join(',')}` : key
    })
  }
})

const PAGE_ROWS = 3
const TOTAL = 120

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

const allIds = Array.from({ length: TOTAL }, (_, index) => index + 1)

const AccountBulkActionsBarStub = {
  props: [
    'selectedIds',
    'pageSelectedCount',
    'totalCount',
    'canSelectAllFiltered',
    'allFilteredSelected',
    'selectingAllFiltered'
  ],
  emits: ['select-all-filtered', 'edit-selected', 'delete', 'reset-status', 'clear'],
  template: `
    <div>
      <button data-test="select-all-filtered" @click="$emit('select-all-filtered')">select all filtered</button>
      <button data-test="edit-selected" @click="$emit('edit-selected')">edit selected</button>
      <button data-test="delete" @click="$emit('delete')">delete</button>
      <button data-test="reset-status" @click="$emit('reset-status')">reset status</button>
    </div>
  `
}

const BulkEditAccountModalStub = {
  props: ['show', 'accountIds', 'selectedPlatforms', 'selectedTypes', 'target'],
  template: '<div data-test="bulk-edit-modal"></div>'
}

const DataTableStub = {
  props: ['data'],
  template: `
    <div data-test="data-table">
      <div data-test="header-select"><slot name="header-select" /></div>
      <div v-for="row in data" :key="row.id" data-test="row"><slot name="cell-select" :row="row" /></div>
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
      DataTable: DataTableStub,
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
      BulkEditAccountModal: BulkEditAccountModalStub,
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

type Wrapper = ReturnType<typeof mountView>

const bar = (wrapper: Wrapper) => wrapper.getComponent(AccountBulkActionsBarStub)
const modal = (wrapper: Wrapper) => wrapper.getComponent(BulkEditAccountModalStub)

const checkHeader = async (wrapper: Wrapper, checked = true) => {
  const input = wrapper.get<HTMLInputElement>('[data-test="header-select"] input')
  input.element.checked = checked
  await input.trigger('change')
}

describe('admin AccountsView select all filtered results', () => {
  beforeEach(() => {
    localStorage.clear()
    for (const mock of [
      listAccounts,
      listWithEtag,
      listIds,
      deleteAccount,
      exportData,
      batchClearError,
      getBatchTodayStats,
      getAllProxies,
      getAllGroups,
      showError,
      showSuccess
    ]) {
      mock.mockReset()
    }

    listAccounts.mockResolvedValue({
      items: makeAccounts(PAGE_ROWS),
      total: TOTAL,
      page: 1,
      page_size: PAGE_ROWS,
      pages: Math.ceil(TOTAL / PAGE_ROWS)
    })
    listWithEtag.mockResolvedValue({ notModified: true, etag: null, data: null })
    listIds.mockResolvedValue({ ids: allIds, total: TOTAL, platforms: ['anthropic', 'openai'], types: ['apikey', 'oauth'] })
    deleteAccount.mockResolvedValue({ message: 'ok' })
    exportData.mockResolvedValue({ accounts: [], proxies: [] })
    batchClearError.mockResolvedValue({ total: TOTAL, success: TOTAL, failed: 0 })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getAllProxies.mockResolvedValue([])
    getAllGroups.mockResolvedValue([])
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('only offers select-all-filtered once the header checkbox selects the page and more results exist', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(bar(wrapper).props('canSelectAllFiltered')).toBe(false)

    await checkHeader(wrapper)

    expect(bar(wrapper).props('canSelectAllFiltered')).toBe(true)
    expect(bar(wrapper).props('pageSelectedCount')).toBe(PAGE_ROWS)
    expect(bar(wrapper).props('totalCount')).toBe(TOTAL)
    expect(bar(wrapper).props('allFilteredSelected')).toBe(false)
    expect(listIds).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('does not offer it when the filtered result fits in the current page', async () => {
    listAccounts.mockResolvedValue({
      items: makeAccounts(PAGE_ROWS),
      total: PAGE_ROWS,
      page: 1,
      page_size: 20,
      pages: 1
    })
    const wrapper = mountView()
    await flushPromises()
    await checkHeader(wrapper)

    expect(bar(wrapper).props('canSelectAllFiltered')).toBe(false)
    wrapper.unmount()
  })

  it('fetches the ID set with the current filters and selects every matching account', async () => {
    const wrapper = mountView()
    await flushPromises()
    await checkHeader(wrapper)

    await wrapper.get('[data-test="select-all-filtered"]').trigger('click')
    await flushPromises()

    expect(listIds).toHaveBeenCalledTimes(1)
    expect(listIds).toHaveBeenCalledWith({
      platform: '',
      type: '',
      status: '',
      group: '',
      privacy_mode: '',
      search: ''
    })
    expect(bar(wrapper).props('selectedIds')).toEqual(allIds)
    expect(bar(wrapper).props('allFilteredSelected')).toBe(true)
    expect(bar(wrapper).props('canSelectAllFiltered')).toBe(false)
    wrapper.unmount()
  })

  it('bulk edit gets every selected ID and the platforms/types of the whole selection', async () => {
    const wrapper = mountView()
    await flushPromises()
    await checkHeader(wrapper)
    await wrapper.get('[data-test="select-all-filtered"]').trigger('click')
    await flushPromises()

    await wrapper.get('[data-test="edit-selected"]').trigger('click')
    await flushPromises()

    expect(modal(wrapper).props('show')).toBe(true)
    expect(modal(wrapper).props('accountIds')).toEqual(allIds)
    expect([...(modal(wrapper).props('selectedPlatforms') as string[])].sort()).toEqual(['anthropic', 'openai'])
    expect([...(modal(wrapper).props('selectedTypes') as string[])].sort()).toEqual(['apikey', 'oauth'])
    const target = modal(wrapper).props('target') as {
      mode: string
      accountIds: number[]
      selectedPlatforms: string[]
      selectedTypes: string[]
    }
    expect(target.mode).toBe('selected')
    expect(target.accountIds).toEqual(allIds)
    expect([...target.selectedPlatforms].sort()).toEqual(['anthropic', 'openai'])
    expect([...target.selectedTypes].sort()).toEqual(['apikey', 'oauth'])
    wrapper.unmount()
  })

  it('bulk delete states the number of accounts and deletes every selected ID', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = mountView()
    await flushPromises()
    await checkHeader(wrapper)
    await wrapper.get('[data-test="select-all-filtered"]').trigger('click')
    await flushPromises()

    await wrapper.get('[data-test="delete"]').trigger('click')
    await flushPromises()

    expect(confirmSpy).toHaveBeenCalledWith(`admin.accounts.bulkDeleteConfirm|${TOTAL}`)
    expect(deleteAccount).toHaveBeenCalledTimes(TOTAL)
    expect(deleteAccount.mock.calls.map(call => call[0]).sort((a, b) => a - b)).toEqual(allIds)
    expect(showSuccess).toHaveBeenCalledWith(`admin.accounts.bulkDeleteSuccess|${TOTAL}`)
    expect(bar(wrapper).props('selectedIds')).toEqual([])
    wrapper.unmount()
  })

  it('bulk delete keeps only the failed accounts selected', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    deleteAccount.mockImplementation(async (id: number) => {
      if (id === 7 || id === 99) throw new Error('boom')
      return { message: 'ok' }
    })
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const wrapper = mountView()
    await flushPromises()
    await checkHeader(wrapper)
    await wrapper.get('[data-test="select-all-filtered"]').trigger('click')
    await flushPromises()

    await wrapper.get('[data-test="delete"]').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith(`admin.accounts.bulkDeletePartial|${TOTAL - 2},2`)
    expect([...(bar(wrapper).props('selectedIds') as number[])].sort((a, b) => a - b)).toEqual([7, 99])
    wrapper.unmount()
  })

  it('bulk delete does nothing when the confirmation is declined', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    const wrapper = mountView()
    await flushPromises()
    await checkHeader(wrapper)
    await wrapper.get('[data-test="select-all-filtered"]').trigger('click')
    await flushPromises()

    await wrapper.get('[data-test="delete"]').trigger('click')
    await flushPromises()

    expect(deleteAccount).not.toHaveBeenCalled()
    expect(bar(wrapper).props('selectedIds')).toEqual(allIds)
    wrapper.unmount()
  })

  it('other bulk operations also state how many accounts they will touch', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = mountView()
    await flushPromises()
    await checkHeader(wrapper)
    await wrapper.get('[data-test="select-all-filtered"]').trigger('click')
    await flushPromises()

    await wrapper.get('[data-test="reset-status"]').trigger('click')
    await flushPromises()

    expect(confirmSpy).toHaveBeenCalledWith(`admin.accounts.bulkActions.confirmCount|${TOTAL}`)
    expect(batchClearError).toHaveBeenCalledWith(allIds)
    wrapper.unmount()
  })

  it('reports when too many accounts match and keeps the selection unchanged', async () => {
    listIds.mockRejectedValue({
      reason: 'ACCOUNT_IDS_LIMIT_EXCEEDED',
      metadata: { total: '7321', limit: '5000' }
    })
    const wrapper = mountView()
    await flushPromises()
    await checkHeader(wrapper)

    await wrapper.get('[data-test="select-all-filtered"]').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('admin.accounts.bulkActions.selectAllFilteredTooMany|7321,5000')
    expect(bar(wrapper).props('selectedIds')).toEqual([1, 2, 3])
    expect(bar(wrapper).props('allFilteredSelected')).toBe(false)
    expect(bar(wrapper).props('canSelectAllFiltered')).toBe(true)
    wrapper.unmount()
  })

  it('falls back to the page selection state when one account is deselected', async () => {
    const wrapper = mountView()
    await flushPromises()
    await checkHeader(wrapper)
    await wrapper.get('[data-test="select-all-filtered"]').trigger('click')
    await flushPromises()

    const firstRowCheckbox = wrapper.get<HTMLInputElement>('[data-test="row"] input')
    firstRowCheckbox.element.checked = false
    await firstRowCheckbox.trigger('change')

    expect(bar(wrapper).props('allFilteredSelected')).toBe(false)
    expect(bar(wrapper).props('selectedIds')).toHaveLength(TOTAL - 1)
    wrapper.unmount()
  })

  it('exports by filters instead of a huge ids query string when everything filtered is selected', async () => {
    const wrapper = mountView()
    await flushPromises()
    await checkHeader(wrapper)
    await wrapper.get('[data-test="select-all-filtered"]').trigger('click')
    await flushPromises()

    expect(bar(wrapper).props('allFilteredSelected')).toBe(true)
    const instance = wrapper.vm as unknown as { handleExportData?: () => Promise<void> }
    // handleExportData 在 <script setup> 里，通过 vm 暴露的代理访问
    await instance.handleExportData?.()
    await flushPromises()

    expect(exportData).toHaveBeenCalledTimes(1)
    const options = exportData.mock.calls[0][0]
    expect(options.ids).toBeUndefined()
    expect(options.filters).toBeDefined()
    wrapper.unmount()
  })
})
