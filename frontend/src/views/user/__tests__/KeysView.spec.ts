import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { nextTick, ref } from 'vue'

import type { ApiKey } from '@/types'
import KeysView from '../KeysView.vue'
import { useCurrencyDisplay } from '@/composables/useCurrencyDisplay'

const {
  listKeys,
  createKey,
  updateKey,
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
  createKey: vi.fn(),
  updateKey: vi.fn(),
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
    create: createKey,
    update: updateKey,
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

// 可变的假设置与订阅卡：测试里改它们就能模拟不同的充值倍率和扣费来源。
const publicSettings: { value: Record<string, unknown> | null } = { value: null }
const activeSubscriptions: { value: Array<Record<string, unknown>> } = { value: [] }

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
    get cachedPublicSettings() {
      return publicSettings.value
    },
  }),
}))

vi.mock('@/stores/subscriptions', () => ({
  useSubscriptionStore: () => ({
    get activeSubscriptions() {
      return activeSubscriptions.value
    },
  }),
}))

vi.mock('@/i18n', () => ({
  getLocale: () => 'zh-CN',
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
    useI18n: () => ({
      locale: ref('en'),
      t: (key: string) => messages[key] ?? key,
    }),
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
        <slot name="cell-actions" :row="row" />
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
        BaseDialog: {
          props: ['show'],
          template: '<div v-if="show"><slot /><slot name="footer" /></div>',
        },
        ConfirmDialog: true,
        EmptyState: true,
        Select: true,
        SearchInput: true,
        Icon: { template: '<span />' },
        KeyOnboardingModal: true,
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
    publicSettings.value = null
    activeSubscriptions.value = []
    updateKey.mockReset()
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

  it.each([
    { initialStatus: 'quota_exhausted', status: 'active', formStatus: 'active' },
    { initialStatus: 'inactive', status: 'inactive', formStatus: 'inactive' },
    { initialStatus: 'active', status: 'active', formStatus: 'inactive' },
  ] as const)('syncs quota reset from $initialStatus to $status with form status $formStatus', async ({ initialStatus, status, formStatus }) => {
    const key: ApiKey = {
      ...createApiKey(), group_id: 1, quota: 10, quota_used: 10,
      status: initialStatus,
    }
    listKeys.mockResolvedValueOnce({ items: [key], total: 1, page: 1, page_size: 20, pages: 1 })
    updateKey.mockResolvedValue({ ...key, status, quota_used: 0 })
    const wrapper = await mountView()
    await getButtonByText(wrapper, 'common.edit').trigger('click')
    await wrapper.get('[data-tour="key-form-name"]').setValue('Unsaved name')
    const statusSelect = wrapper.findAllComponents({ name: 'Select' })
      .find((select) => select.props('options')?.length === 2 &&
        select.props('options')[0].value === 'active')!
    statusSelect.vm.$emit('update:modelValue', 'inactive')
    await wrapper.get('button[title="keys.resetQuotaUsed"]').trigger('click')
    const confirmation = wrapper.findAllComponents({ name: 'ConfirmDialog' })
      .find((dialog) => dialog.props('title') === 'keys.resetQuotaTitle')!
    confirmation.vm.$emit('confirm')
    await flushPromises()

    expect(updateKey).toHaveBeenNthCalledWith(1, key.id, { reset_quota: true })
    expect(wrapper.findComponent(DataTableStub).props('data')[0])
      .toMatchObject({ status, quota_used: 0 })
    expect(statusSelect.props('modelValue')).toBe(formStatus)
    expect((wrapper.get('[data-tour="key-form-name"]').element as HTMLInputElement).value)
      .toBe('Unsaved name')

    await wrapper.get('#key-form').trigger('submit')
    await flushPromises()
    expect(updateKey).toHaveBeenNthCalledWith(2, key.id, expect.objectContaining({ name: 'Unsaved name', status: formStatus }))
    wrapper.unmount()
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

    await getButtonByText(wrapper, 'ID').trigger('click')
    await nextTick()

    expect(visibleColumnKeys(wrapper)).not.toContain('id')
    expect(JSON.parse(localStorage.getItem('api-key-hidden-columns') ?? '[]')).toContain('id')
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

  it('keeps a current-version user preference without applying the migration defaults again', async () => {
    localStorage.setItem('api-key-hidden-columns', JSON.stringify(['group']))
    localStorage.setItem('api-key-column-settings-version', '3')

    const wrapper = await mountView()

    expect(visibleColumnKeys(wrapper)).not.toContain('group')
    expect(visibleColumnKeys(wrapper)).toContain('id')
    expect(localStorage.getItem('api-key-hidden-columns')).toBe(JSON.stringify(['group']))
    expect(localStorage.getItem('api-key-column-settings-version')).toBe('3')
  })
})

// 方案 K2：人民币模式下额度上限按「当前扣费来源」单价填写与回显，提交时换算回额度。
describe('user KeysView fiat limit input', () => {
  const FIAT_INPUT = 'input[placeholder="keys.quotaAmountPlaceholderFiat"]'

  beforeEach(() => {
    localStorage.clear()
    updateKey.mockReset()
    publicSettings.value = { balance_recharge_multiplier: 10 }
    activeSubscriptions.value = []
    useCurrencyDisplay().setMode('fiat')
    createKey.mockReset()
    createKey.mockResolvedValue({})
    getPublicSettings.mockResolvedValue({})
    getDashboardApiKeysUsage.mockResolvedValue({ stats: {} })
    getAvailableGroups.mockResolvedValue([])
    getUserGroupRates.mockResolvedValue({})
    isCurrentStep.mockReturnValue(false)
  })

  async function editKeyWithQuota(quota: number) {
    const key: ApiKey = { ...createApiKey(), group_id: 1, quota }
    listKeys.mockResolvedValue({ items: [key], total: 1, page: 1, page_size: 20, pages: 1 })
    updateKey.mockResolvedValue(key)
    const wrapper = await mountView()
    await getButtonByText(wrapper, 'common.edit').trigger('click')
    await nextTick()
    return { wrapper, key }
  }

  it('无订阅卡时按余额单价 1/m 回显人民币；未修改则原样提交额度', async () => {
    const { wrapper, key } = await editKeyWithQuota(100)

    const input = wrapper.get(FIAT_INPUT).element as HTMLInputElement
    expect(Number(input.value)).toBe(10)

    await wrapper.get('#key-form').trigger('submit')
    await flushPromises()
    expect(updateKey).toHaveBeenCalledWith(key.id, expect.objectContaining({ quota: 100 }))
    wrapper.unmount()
  })

  it('用户填人民币时按余额单价换算回额度', async () => {
    const { wrapper, key } = await editKeyWithQuota(100)

    await wrapper.get(FIAT_INPUT).setValue('20')
    await wrapper.get('#key-form').trigger('submit')
    await flushPromises()
    expect(updateKey).toHaveBeenCalledWith(key.id, expect.objectContaining({ quota: 200 }))
    wrapper.unmount()
  })

  it('有生效中的订阅卡时按该卡单价折算', async () => {
    activeSubscriptions.value = [{ status: 'active', fiat_per_credit: 0.05 }]
    const { wrapper, key } = await editKeyWithQuota(100)

    expect(Number((wrapper.get(FIAT_INPUT).element as HTMLInputElement).value)).toBe(5)
    await wrapper.get(FIAT_INPUT).setValue('6')
    await wrapper.get('#key-form').trigger('submit')
    await flushPromises()
    expect(updateKey).toHaveBeenCalledWith(key.id, expect.objectContaining({ quota: 120 }))
    wrapper.unmount()
  })

  it('倍率为 1 时仍按额度填写，不做换算', async () => {
    publicSettings.value = { balance_recharge_multiplier: 1 }
    const { wrapper, key } = await editKeyWithQuota(100)

    expect(wrapper.find(FIAT_INPUT).exists()).toBe(false)
    await wrapper.get('#key-form').trigger('submit')
    await flushPromises()
    expect(updateKey).toHaveBeenCalledWith(key.id, expect.objectContaining({ quota: 100 }))
    wrapper.unmount()
  })

  it('极小的上限不会被四舍五入成 0 而清成不限；未修改时原样提交', async () => {
    const { wrapper, key } = await editKeyWithQuota(0.03)

    // 0.03 ÷ 10 = ¥0.003，按分取整会变成 0
    expect(Number((wrapper.get(FIAT_INPUT).element as HTMLInputElement).value)).toBe(0.003)
    await wrapper.get('#key-form').trigger('submit')
    await flushPromises()
    expect(updateKey).toHaveBeenCalledWith(key.id, expect.objectContaining({ quota: 0.03 }))
    wrapper.unmount()
  })

  it('弹窗打开后切换计价口径，已填的数字仍按打开时的单位换算', async () => {
    const { wrapper, key } = await editKeyWithQuota(100)

    useCurrencyDisplay().setMode('usd')
    await nextTick()
    await wrapper.get(FIAT_INPUT).setValue('20')
    await wrapper.get('#key-form').trigger('submit')
    await flushPromises()
    expect(updateKey).toHaveBeenCalledWith(key.id, expect.objectContaining({ quota: 200 }))
    useCurrencyDisplay().setMode('fiat')
    wrapper.unmount()
  })

  it('弹窗打开后订阅卡才加载回来，提交仍按打开时的单价换算', async () => {
    const { wrapper, key } = await editKeyWithQuota(100)

    activeSubscriptions.value = [{ status: 'active', fiat_per_credit: 0.05 }]
    await nextTick()
    await wrapper.get(FIAT_INPUT).setValue('20')
    await wrapper.get('#key-form').trigger('submit')
    await flushPromises()
    expect(updateKey).toHaveBeenCalledWith(key.id, expect.objectContaining({ quota: 200 }))
    wrapper.unmount()
  })

  it('创建弹窗按人民币填写并换算回额度', async () => {
    listKeys.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
    const wrapper = await mountView()
    const setupState = (wrapper.vm as any).$?.setupState
    setupState.showCreateModal = true
    await nextTick()
    setupState.formData.group_id = 1
    setupState.formData.name = 'new-key'
    setupState.formData.enable_quota = true
    await nextTick()

    await wrapper.get(FIAT_INPUT).setValue('5')
    await wrapper.get('#key-form').trigger('submit')
    await flushPromises()
    expect(createKey).toHaveBeenCalledWith(
      'new-key', 1, undefined, [], [], 50, undefined, expect.anything(), false
    )
    wrapper.unmount()
  })

  it('关闭后再编辑另一个 Key，不会沿用上一个 Key 的原值', async () => {
    const a: ApiKey = { ...createApiKey(), id: 1, group_id: 1, quota: 100 }
    const b: ApiKey = { ...createApiKey(), id: 2, name: 'b', group_id: 1, quota: 300 }
    listKeys.mockResolvedValue({ items: [a, b], total: 2, page: 1, page_size: 20, pages: 1 })
    updateKey.mockResolvedValue(a)
    const wrapper = await mountView()
    const setupState = (wrapper.vm as any).$?.setupState

    setupState.editKey(a)
    await nextTick()
    setupState.closeModals()
    await nextTick()
    setupState.editKey(b)
    await nextTick()
    // b 回显 ¥30；改成 ¥10（恰好等于 a 的回显值）必须按单价换算，而不是提交 a 的原额度 100
    await wrapper.get(FIAT_INPUT).setValue('10')
    await wrapper.get('#key-form').trigger('submit')
    await flushPromises()
    expect(updateKey).toHaveBeenCalledWith(b.id, expect.objectContaining({ quota: 100 }))
    // 再验证没改动时 b 原样提交
    setupState.editKey(b)
    await nextTick()
    await wrapper.get('#key-form').trigger('submit')
    await flushPromises()
    expect(updateKey).toHaveBeenLastCalledWith(b.id, expect.objectContaining({ quota: 300 }))
    wrapper.unmount()
  })
})
