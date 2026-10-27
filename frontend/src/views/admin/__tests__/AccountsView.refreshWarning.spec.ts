import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const {
  listAccounts,
  listWithEtag,
  batchRefresh,
  refreshCredentials,
  getBatchTodayStats,
  getAllProxies,
  getAllGroups,
  showError,
  showSuccess,
  showWarning
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  batchRefresh: vi.fn(),
  refreshCredentials: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  showWarning: vi.fn()
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
      refreshCredentials,
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
    showWarning,
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

const AccountActionMenuStub = {
  props: ['show', 'account', 'anchorRect'],
  emits: ['refresh-token'],
  template: '<div data-test="action-menu"></div>'
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
        template: '<div data-test="data-table"><div v-for="row in data" :key="row.id" data-test="row" :data-account-name="row.name"></div></div>'
      },
      Pagination: true,
      ConfirmDialog: true,
      AccountTableActions: { template: '<div><slot name="beforeCreate" /><slot name="after" /></div>' },
      AccountTableFilters: { template: '<div></div>' },
      AccountBulkActionsBar: AccountBulkActionsBarStub,
      AccountActionMenu: AccountActionMenuStub,
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

describe('admin AccountsView single account refresh', () => {
  beforeEach(() => {
    localStorage.clear()
    listAccounts.mockReset()
    listWithEtag.mockReset()
    batchRefresh.mockReset()
    refreshCredentials.mockReset()
    showWarning.mockReset()
    getBatchTodayStats.mockReset()
    getAllProxies.mockReset()
    getAllGroups.mockReset()
    showError.mockReset()
    showSuccess.mockReset()

    listWithEtag.mockResolvedValue({ notModified: true, etag: null, data: null })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getAllProxies.mockResolvedValue([])
    getAllGroups.mockResolvedValue([])
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it.each([
    {
      name: 'shows the warning and patches the account after a partial Antigravity refresh',
      result: {
        account: { ...makeAccounts(1)[0], name: 'refreshed account' },
        message: 'Token refreshed, but project_id is temporarily unavailable',
        warning: 'missing_project_id_temporary'
      },
      warning: 'Token refreshed, but project_id is temporarily unavailable'
    },
    {
      name: 'patches the account without a warning after a full refresh',
      result: { account: { ...makeAccounts(1)[0], name: 'refreshed account' } },
      warning: null
    },
  ])('$name', async ({ result, warning }) => {
    listAccounts.mockResolvedValue({ items: makeAccounts(1), total: 1, page: 1, page_size: 20, pages: 1 })
    refreshCredentials.mockResolvedValue(result)
    const wrapper = mountView()
    await flushPromises()

    wrapper.getComponent(AccountActionMenuStub).vm.$emit('refresh-token', makeAccounts(1)[0])
    await flushPromises()

    expect(refreshCredentials).toHaveBeenCalledWith(1)
    expect(wrapper.get('[data-test="row"]').attributes('data-account-name')).toBe('refreshed account')
    if (warning) {
      expect(showWarning).toHaveBeenCalledWith(warning)
    } else {
      expect(showWarning).not.toHaveBeenCalled()
    }
    wrapper.unmount()
  })
})
