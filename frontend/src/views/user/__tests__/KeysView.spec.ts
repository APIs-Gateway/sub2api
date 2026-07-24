import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'

import type { ApiKey } from '@/types'
import KeysView from '../KeysView.vue'

const {
  listKeys,
  getPublicSettings,
  getDashboardApiKeysUsage,
  getAvailableGroups,
  getUserGroupRates,
  showError,
  showSuccess,
  copyToClipboard,
  isCurrentStep,
  nextStep,
} = vi.hoisted(() => ({
  listKeys: vi.fn(),
  getPublicSettings: vi.fn(),
  getDashboardApiKeysUsage: vi.fn(),
  getAvailableGroups: vi.fn(),
  getUserGroupRates: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  copyToClipboard: vi.fn(),
  isCurrentStep: vi.fn(),
  nextStep: vi.fn(),
}))

const messages: Record<string, string> = {
  'common.actions': 'Actions',
  'common.name': 'Name',
  'common.refresh': 'Refresh',
  'common.status': 'Status',
  'keys.allGroups': 'All Groups',
  'keys.allStatus': 'All Status',
  'keys.apiKey': 'API Key',
  'keys.columnSettings': 'Column Settings',
  'keys.createKey': 'Create API Key',
  'keys.created': 'Created',
  'keys.currentConcurrency': 'Current Concurrency',
  'keys.expiresAt': 'Expires',
  'keys.group': 'Group',
  'keys.id': 'ID',
  'keys.lastUsedAt': 'Last Used',
  'keys.lastUsedIP': 'Last Used IP',
  'keys.rateLimitColumn': 'Rate Limit',
  'keys.searchPlaceholder': 'Search name or key...',
  'keys.status.active': 'Active',
  'keys.status.expired': 'Expired',
  'keys.status.inactive': 'Inactive',
  'keys.status.quota_exhausted': 'Quota exhausted',
  'keys.usage': 'Usage',
}

vi.mock('@/api', () => ({
  keysAPI: {
    list: listKeys,
    create: vi.fn(),
    update: vi.fn(),
    delete: vi.fn(),
    toggleStatus: vi.fn(),
  },
  authAPI: { getPublicSettings },
  usageAPI: { getDashboardApiKeysUsage },
  userGroupsAPI: {
    getAvailable: getAvailableGroups,
    getUserGroupRates,
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess }),
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({ isCurrentStep, nextStep }),
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => messages[key] ?? key }),
  }
})

const createApiKey = (): ApiKey => ({
  id: 1,
  user_id: 1,
  key: 'sk-test-key',
  name: 'test-key',
  group_id: null,
  status: 'active',
  ip_whitelist: [],
  ip_blacklist: [],
  last_used_at: null,
  last_used_ip: null,
  quota: 0,
  quota_used: 0,
  expires_at: null,
  created_at: '2026-06-27T00:00:00Z',
  updated_at: '2026-06-27T00:00:00Z',
  current_concurrency: 0,
  rate_limit_5h: 0,
  rate_limit_1d: 0,
  rate_limit_7d: 0,
  usage_5h: 0,
  usage_1d: 0,
  usage_7d: 0,
  window_5h_start: null,
  window_1d_start: null,
  window_7d_start: null,
  reset_5h_at: null,
  reset_1d_at: null,
  reset_7d_at: null,
  stable_priority_enabled: false,
})

const DataTableStub = {
  props: ['columns', 'data'],
  template: `
    <div>
      <div data-test="columns">{{ columns.map((col) => col.key).join(',') }}</div>
      <div v-for="row in data" :key="row.id">
        <div v-if="columns.some((col) => col.key === 'id')" data-test="key-id">
          <slot name="cell-id" :value="row.id" :row="row" />
        </div>
      </div>
    </div>
  `,
}

const mountView = async () => {
  const wrapper = mount(KeysView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template: '<div><slot name="filters" /><slot name="actions" /><slot name="table" /><slot name="pagination" /></div>',
        },
        DataTable: DataTableStub,
        Pagination: true,
        BaseDialog: true,
        ConfirmDialog: true,
        EmptyState: true,
        Select: true,
        SearchInput: true,
        Icon: { template: '<span />' },
        UseKeyModal: true,
        EndpointPopover: true,
        GroupBadge: true,
        GroupOptionItem: true,
        Teleport: true,
      },
    },
  })
  await flushPromises()
  await nextTick()
  return wrapper
}

const visibleColumnKeys = (wrapper: VueWrapper) =>
  wrapper.get('[data-test="columns"]').text().split(',').filter(Boolean)

const getButtonByText = (wrapper: VueWrapper, text: string) => {
  const button = wrapper.findAll('button').find((item) => item.text().includes(text))
  if (!button) throw new Error(`Button not found: ${text}`)
  return button
}

describe('user KeysView column settings', () => {
  beforeEach(() => {
    localStorage.clear()
    listKeys.mockResolvedValue({
      items: [createApiKey()],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    })
    getPublicSettings.mockResolvedValue({})
    getDashboardApiKeysUsage.mockResolvedValue({ stats: {} })
    getAvailableGroups.mockResolvedValue([])
    getUserGroupRates.mockResolvedValue({})
    isCurrentStep.mockReturnValue(false)
  })

  it('hides the API key ID column by default', async () => {
    const wrapper = await mountView()

    expect(visibleColumnKeys(wrapper)).not.toContain('id')
    expect(visibleColumnKeys(wrapper)).not.toContain('rate_limit')
    expect(visibleColumnKeys(wrapper)).not.toContain('last_used_at')
    expect(visibleColumnKeys(wrapper)).not.toContain('last_used_ip')
  })

  it('shows the API key ID column after the user enables it', async () => {
    const wrapper = await mountView()

    await wrapper.get('button[title="Column Settings"]').trigger('click')
    await getButtonByText(wrapper, 'ID').trigger('click')
    await nextTick()

    expect(visibleColumnKeys(wrapper)).toContain('id')
    expect(wrapper.get('[data-test="key-id"]').text()).toBe('#1')
    expect(JSON.parse(localStorage.getItem('api-key-hidden-columns') ?? '[]')).not.toContain('id')
    expect(localStorage.getItem('api-key-column-settings-version')).toBe('3')
  })

  it('migrates prior column preferences by keeping newly introduced columns hidden', async () => {
    localStorage.setItem('api-key-hidden-columns', JSON.stringify(['group', 'created_at']))
    localStorage.setItem('api-key-column-settings-version', '1')

    const wrapper = await mountView()

    expect(visibleColumnKeys(wrapper)).not.toContain('group')
    expect(visibleColumnKeys(wrapper)).not.toContain('created_at')
    expect(visibleColumnKeys(wrapper)).not.toContain('last_used_ip')
    expect(visibleColumnKeys(wrapper)).not.toContain('id')
    expect(JSON.parse(localStorage.getItem('api-key-hidden-columns') ?? '[]')).toEqual([
      'group',
      'created_at',
      'last_used_ip',
      'id',
    ])
    expect(localStorage.getItem('api-key-column-settings-version')).toBe('3')
  })
})
