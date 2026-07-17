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
  showSuccess
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn(),
  setPrivacy: vi.fn(),
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
      batchRefresh: vi.fn(),
      toggleSchedulable: vi.fn(),
      setPrivacy
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

// Render the per-column header slots so we can assert the usage-window header hint.
const DataTableStub = {
  props: ['columns', 'data'],
  template: `
    <div data-test="data-table">
      <template v-for="column in columns" :key="column.key">
        <div v-if="column.key === 'usage'" data-test="usage-header">
          <slot :name="'header-' + column.key" :column="column" />
        </div>
      </template>
      <div v-for="row in data" :key="row.id" data-test="account-row">
        <slot name="cell-name" :row="row" :value="row.name" />
      </div>
    </div>
  `
}

// Expose the content passed to HelpTooltip without dealing with its <Teleport>.
const HelpTooltipStub = {
  props: ['content', 'widthClass'],
  template: '<span data-test="help-tooltip">{{ content }}<slot name="trigger" /></span>'
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
        HelpTooltip: HelpTooltipStub,
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

describe('admin AccountsView usage windows hint', () => {
  beforeEach(() => {
    localStorage.clear()

    listAccounts.mockReset()
    listWithEtag.mockReset()
    getBatchTodayStats.mockReset()
    getAllProxies.mockReset()
    getAllGroups.mockReset()
    setPrivacy.mockReset()
    showError.mockReset()
    showSuccess.mockReset()

    listAccounts.mockResolvedValue({
      items: [],
      total: 0,
      page: 1,
      page_size: 20,
      pages: 0
    })
    listWithEtag.mockResolvedValue({
      notModified: true,
      etag: null,
      data: null
    })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getAllProxies.mockResolvedValue([])
    getAllGroups.mockResolvedValue([])
  })

  it('renders an explanatory tooltip next to the usage windows column header', async () => {
    const wrapper = mountView()
    await flushPromises()

    const header = wrapper.find('[data-test="usage-header"]')
    expect(header.exists()).toBe(true)
    // Column label is still shown alongside the help icon.
    expect(header.text()).toContain('admin.accounts.columns.usageWindows')

    const hint = wrapper.find('[data-test="help-tooltip"]')
    expect(hint.exists()).toBe(true)
    expect(hint.text()).toBe('admin.accounts.usageWindowsHint')
  })

  it('shows privacy result toast based on returned privacy mode', async () => {
    const wrapper = mountView()
    await flushPromises()

    setPrivacy.mockResolvedValueOnce({
      id: 42,
      platform: 'openai',
      extra: { privacy_mode: 'training_set_cf_blocked' }
    })
    await (wrapper.vm as any).handleSetPrivacy({
      id: 42,
      platform: 'openai',
      extra: {}
    })

    expect(showError).toHaveBeenCalledWith('admin.accounts.privacyCfBlocked')
    expect(showSuccess).not.toHaveBeenCalled()

    setPrivacy.mockResolvedValueOnce({
      id: 43,
      platform: 'antigravity',
      extra: { privacy_mode: 'privacy_set' }
    })
    await (wrapper.vm as any).handleSetPrivacy({
      id: 43,
      platform: 'antigravity',
      extra: {}
    })

    expect(showSuccess).toHaveBeenCalledWith('admin.accounts.privacyAntigravitySet')
  })

  it('links only API Key account names with a safe base_url to the upstream origin', async () => {
    listAccounts.mockResolvedValue({
      items: [
        { id: 101, name: 'relay-account', platform: 'openai', type: 'apikey', credentials: { base_url: 'https://relay.example.com/api/v1/' } },
        { id: 102, name: 'oauth-account', platform: 'openai', type: 'oauth', credentials: { base_url: 'https://oauth.example.com/v1' } },
        { id: 103, name: 'invalid-url', platform: 'openai', type: 'apikey', credentials: { base_url: 'javascript:alert(1)' } }
      ],
      total: 3,
      page: 1,
      page_size: 20,
      pages: 1
    })

    const wrapper = mountView()
    await flushPromises()

    const links = wrapper.findAll('a')
    expect(links).toHaveLength(1)
    expect(links[0].text()).toBe('relay-account')
    expect(links[0].attributes()).toMatchObject({
      href: 'https://relay.example.com',
      target: '_blank',
      rel: 'noopener noreferrer'
    })
    expect(wrapper.text()).toContain('oauth-account')
    expect(wrapper.text()).toContain('invalid-url')

    const tooltip = wrapper.findAllComponents(HelpTooltipStub).find(
      component => component.props('content') === 'https://relay.example.com'
    )
    expect(tooltip).toBeDefined()
    expect(tooltip?.props('widthClass')).toBe('w-max max-w-sm break-all')
    expect(tooltip?.classes()).toEqual(expect.arrayContaining(['self-start']))

    wrapper.unmount()
  })
})
