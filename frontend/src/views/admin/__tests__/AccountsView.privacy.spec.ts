import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const {
  listAccounts,
  listWithEtag,
  getBatchTodayStats,
  getAllProxies,
  getAllGroups,
  setPrivacy,
  showError,
  showSuccess,
  showInfo
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn(),
  setPrivacy: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  showInfo: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag,
      getBatchTodayStats,
      setPrivacy,
      delete: vi.fn(),
      batchClearError: vi.fn(),
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
    showInfo
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

const baseAccount = {
  id: 101,
  name: 'privacy-account',
  platform: 'openai',
  type: 'oauth',
  status: 'active',
  schedulable: true,
  proxy_id: null,
  concurrency: 1,
  priority: 1,
  error_message: null,
  last_used_at: null,
  expires_at: null,
  auto_pause_on_expired: false,
  created_at: '2026-07-01T00:00:00Z',
  updated_at: '2026-07-01T00:00:00Z',
  rate_limited_at: null,
  rate_limit_reset_at: null,
  overload_until: null,
  temp_unschedulable_until: null,
  temp_unschedulable_reason: null,
  session_window_start: null,
  session_window_end: null,
  session_window_status: null,
  extra: {}
}

function mountView() {
  return mount(AccountsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
        },
        DataTable: { props: ['columns', 'data'], template: '<div></div>' },
        Pagination: true,
        ConfirmDialog: true,
        AccountTableActions: { template: '<div><slot name="beforeCreate" /><slot name="after" /></div>' },
        AccountTableFilters: { template: '<div></div>' },
        AccountBulkActionsBar: true,
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
}

describe('admin AccountsView privacy result messaging', () => {
  beforeEach(() => {
    localStorage.clear()
    listAccounts.mockReset().mockResolvedValue({
      items: [baseAccount],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1
    })
    listWithEtag.mockReset().mockResolvedValue({ notModified: true, etag: null, data: null })
    getBatchTodayStats.mockReset().mockResolvedValue({ stats: {} })
    getAllProxies.mockReset().mockResolvedValue([])
    getAllGroups.mockReset().mockResolvedValue([])
    setPrivacy.mockReset()
    showError.mockReset()
    showSuccess.mockReset()
    showInfo.mockReset()
  })

  it.each([
    ['openai training disabled', { platform: 'openai', extra: { privacy_mode: 'training_off' } }, 'success', 'admin.accounts.privacyTrainingOff'],
    ['openai Cloudflare blocked', { platform: 'openai', extra: { privacy_mode: 'training_set_cf_blocked' } }, 'error', 'admin.accounts.privacyCfBlocked'],
    ['antigravity privacy set', { platform: 'antigravity', extra: { privacy_mode: 'privacy_set' } }, 'success', 'admin.accounts.privacyAntigravitySet'],
    ['antigravity privacy failed', { platform: 'antigravity', extra: { privacy_mode: '' } }, 'error', 'admin.accounts.privacyAntigravityFailed'],
  ])('shows accurate result for %s', async (_label, updatedPatch, expectedType, expectedKey) => {
    const updated = { ...baseAccount, ...updatedPatch, id: baseAccount.id }
    setPrivacy.mockResolvedValue(updated)

    const wrapper = mountView()
    await flushPromises()

    await (wrapper.vm as any).$?.setupState.handleSetPrivacy(baseAccount)
    await flushPromises()

    if (expectedType === 'success') {
      expect(showSuccess).toHaveBeenCalledWith(expectedKey)
      expect(showError).not.toHaveBeenCalledWith(expectedKey)
    } else {
      expect(showError).toHaveBeenCalledWith(expectedKey)
      expect(showSuccess).not.toHaveBeenCalledWith(expectedKey)
    }
    expect(showSuccess).not.toHaveBeenCalledWith('common.success')
  })
})
