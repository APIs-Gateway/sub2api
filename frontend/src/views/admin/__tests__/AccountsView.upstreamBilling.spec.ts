import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const {
  listAccounts,
  listWithEtag,
  getBatchTodayStats,
  getAllProxies,
  getAllGroups,
  getProbeSettings,
  probeOne,
  probeBatch,
  showError,
  showSuccess
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn(),
  getProbeSettings: vi.fn(),
  probeOne: vi.fn(),
  probeBatch: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag,
      getBatchTodayStats,
      getUpstreamBillingProbeSettings: getProbeSettings,
      probeUpstreamBilling: probeOne,
      probeUpstreamBillingBatch: probeBatch,
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
    showInfo: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ token: 'test-token' })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) =>
      params ? `${key}:${Object.values(params).join(',')}` : key
  })
}))

const account = {
  id: 1,
  name: 'openai-key',
  platform: 'openai',
  type: 'apikey',
  status: 'active',
  schedulable: true,
  created_at: '2026-07-13T00:00:00Z',
  updated_at: '2026-07-13T00:00:00Z'
}

const snapshot = {
  status: 'ok',
  last_attempt_at: '2026-07-13T00:00:00Z',
  next_probe_at: '2026-07-13T00:30:00Z'
}

const DataTableStub = {
  props: ['columns', 'data'],
  template: `
    <div data-test="data-table">
      <button data-test="sort-rate" @click="$emit('sort', 'upstream_billing_rate', 'asc')">sort</button>
      <div v-for="row in data" :key="row.id" data-test="account-row">
        <slot name="cell-select" :row="row" />
        <slot name="cell-upstream_billing_rate" :row="row" />
      </div>
    </div>
  `,
  emits: ['sort']
}

const AccountBulkActionsBarStub = {
  emits: ['probe-upstream-billing'],
  template: '<button data-test="probe-batch" @click="$emit(\'probe-upstream-billing\')">probe batch</button>'
}

const BillingCellStub = {
  emits: ['probe'],
  template: '<button data-test="probe-one" @click="$emit(\'probe\')">probe one</button>'
}

function mountView() {
  return mount(AccountsView, {
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
        BulkEditAccountModal: true,
        PlatformTypeBadge: true,
        AccountCapacityCell: true,
        AccountStatusIndicator: true,
        AccountTodayStatsCell: true,
        AccountGroupsCell: true,
        AccountUsageCell: true,
        UpstreamBillingRateCell: BillingCellStub,
        Icon: true
      }
    }
  })
}

describe('admin AccountsView upstream billing controls', () => {
  beforeEach(() => {
    localStorage.clear()
    listAccounts.mockReset()
    listWithEtag.mockReset()
    getBatchTodayStats.mockReset()
    getAllProxies.mockReset()
    getAllGroups.mockReset()
    getProbeSettings.mockReset()
    probeOne.mockReset()
    probeBatch.mockReset()
    showError.mockReset()
    showSuccess.mockReset()

    listAccounts.mockResolvedValue({ items: [account], total: 1, page: 1, page_size: 20, pages: 1 })
    listWithEtag.mockResolvedValue({ notModified: true, etag: null, data: null })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getAllProxies.mockResolvedValue([])
    getAllGroups.mockResolvedValue([])
    getProbeSettings.mockResolvedValue({ enabled: true, interval_minutes: 30 })
    probeOne.mockResolvedValue({ account_id: 1, snapshot })
  })

  it('loads the global setting and handles a single account probe', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(getProbeSettings).toHaveBeenCalled()
    await wrapper.get('[data-test="probe-one"]').trigger('click')
    await flushPromises()

    expect(probeOne).toHaveBeenCalledWith(1)
  })

  it('rejects an empty batch and reports successful results', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="probe-batch"]').trigger('click')
    expect(showError).toHaveBeenCalledWith('admin.accounts.upstreamBilling.noEligibleAccounts')

    await wrapper.get('input[type="checkbox"]').trigger('change')
    probeBatch.mockResolvedValueOnce([{ account_id: 1, snapshot }])
    await wrapper.get('[data-test="probe-batch"]').trigger('click')
    await flushPromises()

    expect(probeBatch).toHaveBeenCalledWith([1])
    expect(showSuccess).toHaveBeenCalledWith('admin.accounts.upstreamBilling.batchCompleted:1')
  })

  it('reports partial and failed batch probes', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('input[type="checkbox"]').trigger('change')

    probeBatch.mockResolvedValueOnce([
      { account_id: 1, snapshot },
      { account_id: 2, error: 'unsupported' }
    ])
    await wrapper.get('[data-test="probe-batch"]').trigger('click')
    await flushPromises()
    expect(showError).toHaveBeenCalledWith('admin.accounts.upstreamBilling.batchPartial:1,1')

    probeBatch.mockRejectedValueOnce(new Error('probe unavailable'))
    await wrapper.get('[data-test="probe-batch"]').trigger('click')
    await flushPromises()
    expect(showError).toHaveBeenCalledWith('probe unavailable')
  })

  it('keeps the view usable when global probe settings cannot be loaded', async () => {
    getProbeSettings.mockRejectedValueOnce(new Error('settings unavailable'))
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-test="data-table"]').exists()).toBe(true)
  })
})
