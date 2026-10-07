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

const { listChains } = vi.hoisted(() => ({ listChains: vi.fn() }))
vi.mock('@/api/keyFallback', () => ({
  keyFallbackAPI: { listChains, getChain: vi.fn(), replaceChain: vi.fn() },
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
  'keys.rateLimit5h': '5-Hour Limit ({currency})',
  'keys.rateLimit1d': 'Daily Limit ({currency})',
  'keys.rateLimit7d': '7-Day Limit ({currency})',
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

// 路由：只需要 query 与 replace；测试里直接改 routeState.query 模拟带参数打开
const routeState = vi.hoisted(() => ({
  query: {} as Record<string, unknown>,
  replace: vi.fn(),
}))
vi.mock('vue-router', () => ({
  useRoute: () => routeState,
  useRouter: () => ({ replace: routeState.replace }),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
    get cachedPublicSettings() {
      return publicSettings.value
    },
  }),
}))

// 分组倍率（useRateDisplay）会读登录状态：这里按未登录，不去请求套餐定价。
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ isAuthenticated: false }),
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
      // 只对带 {name} 占位符的文案做替换，其余文案原样返回。
      t: (key: string, params?: Record<string, unknown>) => {
        const message = messages[key] ?? key
        return params ? message.replace(/\{(\w+)\}/g, (_, name: string) => String(params[name] ?? '')) : message
      },
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

const mountView = async (table: object = DataTableStub) => {
  const wrapper = mount(KeysView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template: '<div><slot name="filters" /><slot name="actions" /><slot name="table" /><slot name="pagination" /></div>',
        },
        DataTable: table,
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
    listChains.mockReset()
    listChains.mockResolvedValue({ platforms: [] })
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

// 限额标签的币种要和输入框前面的符号一致：人民币模式写 CNY，美元模式和 free 站写 USD。
describe('user KeysView limit labels follow the currency', () => {
  beforeEach(() => {
    localStorage.clear()
    updateKey.mockReset()
    publicSettings.value = { balance_recharge_multiplier: 10 }
    activeSubscriptions.value = []
    useCurrencyDisplay().setMode('fiat')
    getPublicSettings.mockResolvedValue({})
    getDashboardApiKeysUsage.mockResolvedValue({ stats: {} })
    getAvailableGroups.mockResolvedValue([])
    getUserGroupRates.mockResolvedValue({})
    isCurrentStep.mockReturnValue(false)
  })

  async function openLimitForm() {
    const key: ApiKey = { ...createApiKey(), group_id: 1, rate_limit_5h: 5, rate_limit_1d: 10, rate_limit_7d: 20 }
    listKeys.mockResolvedValue({ items: [key], total: 1, page: 1, page_size: 20, pages: 1 })
    const wrapper = await mountView()
    await getButtonByText(wrapper, 'common.edit').trigger('click')
    await nextTick()
    const labels = wrapper
      .findAll('label.input-label')
      .map((label) => label.text())
      .filter((text) => /Limit \(/.test(text))
    return { wrapper, labels }
  }

  it('人民币模式：三个限额标签写 CNY，不再写 USD', async () => {
    const { wrapper, labels } = await openLimitForm()

    expect(labels).toEqual(['5-Hour Limit (CNY)', 'Daily Limit (CNY)', '7-Day Limit (CNY)'])
    expect(wrapper.find('input[placeholder="keys.quotaAmountPlaceholderFiat"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('美元模式：三个限额标签写 USD', async () => {
    useCurrencyDisplay().setMode('usd')
    const { wrapper, labels } = await openLimitForm()

    expect(labels).toEqual(['5-Hour Limit (USD)', 'Daily Limit (USD)', '7-Day Limit (USD)'])
    wrapper.unmount()
  })

  it('free 站（倍率 1）按美元：标签写 USD', async () => {
    publicSettings.value = { balance_recharge_multiplier: 1 }
    const { wrapper, labels } = await openLimitForm()

    expect(labels).toEqual(['5-Hour Limit (USD)', 'Daily Limit (USD)', '7-Day Limit (USD)'])
    wrapper.unmount()
  })
})

// 概览与行内接入入口
describe('user KeysView overview and connect actions', () => {
  const RowsTableStub = {
    props: ['columns', 'data'],
    template: `
      <div>
        <div v-for="row in data" :key="row.id" :data-row="row.id">
          <slot name="cell-usage" :row="row" />
          <slot name="cell-actions" :row="row" />
        </div>
      </div>
    `,
  }

  const keyA: ApiKey = { ...createApiKey(), id: 1, name: 'a', status: 'active', group_id: 1, quota: 100, quota_used: 40 }
  const keyB: ApiKey = { ...createApiKey(), id: 2, name: 'b', status: 'inactive', group_id: 1 }

  beforeEach(() => {
    localStorage.clear()
    publicSettings.value = { balance_recharge_multiplier: 10 }
    activeSubscriptions.value = []
    useCurrencyDisplay().setMode('fiat')
    listKeys.mockResolvedValue({ items: [keyA, keyB], total: 2, page: 1, page_size: 20, pages: 1 })
    getPublicSettings.mockResolvedValue({ api_base_url: 'https://codex.hiyo.top' })
    getDashboardApiKeysUsage.mockResolvedValue({
      stats: {
        1: { api_key_id: 1, today_actual_cost: 1, total_actual_cost: 20, today_actual_cost_fiat: 0.1, total_actual_cost_fiat: 2 },
        2: { api_key_id: 2, today_actual_cost: 0, total_actual_cost: 15, today_actual_cost_fiat: 0, total_actual_cost_fiat: 1.5 },
      },
    })
    getAvailableGroups.mockResolvedValue([])
    getUserGroupRates.mockResolvedValue({})
    isCurrentStep.mockReturnValue(false)
  })

  it('概览：已启用数、近 30 天消费合计、接入地址', async () => {
    const wrapper = await mountView(RowsTableStub)
    expect(wrapper.get('[data-test="overview-address"]').text()).toBe('https://codex.hiyo.top')
    expect(wrapper.get('[data-test="overview-enabled"]').text()).toBe('keys.overview.enabled')
    expect(wrapper.get('[data-test="overview-spent"]').text()).toBe('¥3.50')
    wrapper.unmount()
  })

  it('概览：美元口径下按额度合计', async () => {
    useCurrencyDisplay().setMode('usd')
    const wrapper = await mountView(RowsTableStub)
    expect(wrapper.get('[data-test="overview-spent"]').text()).toBe('$35.00')
    wrapper.unmount()
    useCurrencyDisplay().setMode('fiat')
  })

  it('概览：有 Key 缺人民币值时不拿部分合计冒充总数', async () => {
    getDashboardApiKeysUsage.mockResolvedValue({
      stats: {
        1: { api_key_id: 1, today_actual_cost: 0, total_actual_cost: 20, total_actual_cost_fiat: 2 },
        2: { api_key_id: 2, today_actual_cost: 0, total_actual_cost: 15 },
      },
    })
    const wrapper = await mountView(RowsTableStub)
    expect(wrapper.get('[data-test="overview-spent"]').text()).toBe('$35.00')
    wrapper.unmount()
  })

  it('概览：分页后只覆盖当前页，标签写明', async () => {
    listKeys.mockResolvedValue({ items: [keyA, keyB], total: 30, page: 1, page_size: 2, pages: 15 })
    const wrapper = await mountView(RowsTableStub)
    expect(wrapper.get('[data-test="overview-enabled"]').text()).toBe('keys.overview.enabledList')
    expect(wrapper.get('[data-test="overview-stats"]').text()).toContain('keys.overview.spentList')
    wrapper.unmount()
  })

  it('概览：没有筛选、只有一页时按全部密钥统计', async () => {
    const wrapper = await mountView(RowsTableStub)
    expect(wrapper.get('[data-test="overview-enabled"]').text()).toBe('keys.overview.enabled')
    expect(wrapper.get('[data-test="overview-stats"]').text()).not.toContain('keys.overview.spentList')
    wrapper.unmount()
  })

  // 带筛选条件时，列表只是全部密钥的一部分：即使结果不超过一页，也不能写成「共 N 个密钥」
  describe('概览：带筛选条件时按当前列表统计', () => {
    const filterSelect = (wrapper: VueWrapper, firstLabel: string) =>
      wrapper.findAllComponents({ name: 'Select' }).find((select) => select.props('options')?.[0]?.label === firstLabel)!

    const expectListLabels = (wrapper: VueWrapper) => {
      expect(wrapper.get('[data-test="overview-enabled"]').text()).toBe('keys.overview.enabledList')
      expect(wrapper.get('[data-test="overview-stats"]').text()).toContain('keys.overview.spentList')
    }

    it('状态筛选', async () => {
      const wrapper = await mountView(RowsTableStub)
      expect(wrapper.get('[data-test="overview-enabled"]').text()).toBe('keys.overview.enabled')
      filterSelect(wrapper, 'All Status').vm.$emit('update:modelValue', 'active')
      await flushPromises()
      expect(listKeys).toHaveBeenLastCalledWith(1, expect.any(Number), expect.objectContaining({ status: 'active' }), expect.anything())
      expectListLabels(wrapper)
      wrapper.unmount()
    })

    it('分组筛选，包括「无分组」（值为 0）', async () => {
      const wrapper = await mountView(RowsTableStub)
      filterSelect(wrapper, 'All Groups').vm.$emit('update:modelValue', 0)
      await flushPromises()
      expect(listKeys).toHaveBeenLastCalledWith(1, expect.any(Number), expect.objectContaining({ group_id: 0 }), expect.anything())
      expectListLabels(wrapper)
      wrapper.unmount()
    })

    it('搜索', async () => {
      const wrapper = await mountView(RowsTableStub)
      const search = wrapper.findComponent({ name: 'SearchInput' })
      search.vm.$emit('update:modelValue', 'abc')
      search.vm.$emit('search')
      await flushPromises()
      expect(listKeys).toHaveBeenLastCalledWith(1, expect.any(Number), expect.objectContaining({ search: 'abc' }), expect.anything())
      expectListLabels(wrapper)
      wrapper.unmount()
    })

    it('筛选条件清掉后恢复成全部密钥的统计', async () => {
      const wrapper = await mountView(RowsTableStub)
      const status = filterSelect(wrapper, 'All Status')
      status.vm.$emit('update:modelValue', 'active')
      await flushPromises()
      expectListLabels(wrapper)
      status.vm.$emit('update:modelValue', '')
      await flushPromises()
      expect(wrapper.get('[data-test="overview-enabled"]').text()).toBe('keys.overview.enabled')
      wrapper.unmount()
    })
  })

  it('复制接入地址', async () => {
    copyToClipboard.mockResolvedValue(true)
    const wrapper = await mountView(RowsTableStub)
    await wrapper.get('[data-test="overview-copy"]').trigger('click')
    expect(copyToClipboard).toHaveBeenCalledWith('https://codex.hiyo.top', 'keys.endpoints.copied')
    await flushPromises()
    expect(wrapper.get('[data-test="overview-copy"]').text()).toBe('keys.overview.copied')
    wrapper.unmount()
  })

  it('每行（有上限）：已用和上限同口径，都是额度折算；近 30 天消费单独一行', async () => {
    const wrapper = await mountView(RowsTableStub)
    const rowA = wrapper.get('[data-row="1"]')
    // quota=100、quota_used=40、倍率 10：已用 ¥4.00，上限 ¥10.00（不是近 30 天消费 ¥2.00）
    expect(rowA.get('[data-test="row-used"]').text()).toBe('¥4.00')
    expect(rowA.get('[data-test="row-limit"]').text()).toBe('¥10.00')
    expect(rowA.get('[data-test="row-recent"]').text()).toBe('¥2.00')
    expect(rowA.text()).not.toContain('≈')
    // 「累计已用」「近 30 天」「今日」标签直接写在行里，不靠悬停提示
    expect(rowA.text()).toContain('keys.usedLabel')
    expect(rowA.text()).toContain('keys.total')
    expect(rowA.text()).toContain('keys.today')
    expect(rowA.findAll('[title]').map((el) => el.attributes('title'))).not.toContain('keys.total')
    wrapper.unmount()
  })

  it('每行：近 30 天消费与 quota_used 不同时，已用仍取 quota_used，近 30 天取用量统计', async () => {
    getDashboardApiKeysUsage.mockResolvedValue({
      stats: {
        1: { api_key_id: 1, today_actual_cost: 5, total_actual_cost: 999, today_actual_cost_fiat: 0.5, total_actual_cost_fiat: 99.9 },
        2: { api_key_id: 2, today_actual_cost: 0, total_actual_cost: 0, today_actual_cost_fiat: 0, total_actual_cost_fiat: 0 },
      },
    })
    const wrapper = await mountView(RowsTableStub)
    const rowA = wrapper.get('[data-row="1"]')
    expect(rowA.get('[data-test="row-used"]').text()).toBe('¥4.00')
    expect(rowA.get('[data-test="row-recent"]').text()).toBe('¥99.90')
    wrapper.unmount()
  })

  it('每行：额度用完时已用和上限一起变色', async () => {
    const full: ApiKey = { ...keyA, quota: 100, quota_used: 100 }
    listKeys.mockResolvedValue({ items: [full, keyB], total: 2, page: 1, page_size: 20, pages: 1 })
    const wrapper = await mountView(RowsTableStub)
    const rowA = wrapper.get('[data-row="1"]')
    expect(rowA.get('[data-test="row-used"]').text()).toBe('¥10.00')
    expect(rowA.get('[data-test="row-used"]').classes()).toContain('text-primary-700')
    expect(rowA.get('[data-test="row-limit"]').classes()).toContain('text-primary-700')
    wrapper.unmount()
  })

  it('每行：美元口径下已用和上限都是额度', async () => {
    useCurrencyDisplay().setMode('usd')
    const wrapper = await mountView(RowsTableStub)
    const rowA = wrapper.get('[data-row="1"]')
    expect(rowA.get('[data-test="row-used"]').text()).toBe('$40.00')
    expect(rowA.get('[data-test="row-limit"]').text()).toBe('$100.00')
    expect(rowA.get('[data-test="row-recent"]').text()).toBe('$20.00')
    wrapper.unmount()
    useCurrencyDisplay().setMode('fiat')
  })

  it('每行（无上限）：写近 30 天消费并标明不限额，不写「已用 x / 不限」', async () => {
    const wrapper = await mountView(RowsTableStub)
    const rowB = wrapper.get('[data-row="2"]')
    expect(rowB.find('[data-test="row-used"]').exists()).toBe(false)
    expect(rowB.get('[data-test="row-recent"]').text()).toBe('¥1.50')
    expect(rowB.get('[data-test="row-limit"]').text()).toBe('keys.unlimited')
    expect(rowB.text()).toContain('keys.total')
    expect(rowB.text()).not.toContain('keys.usedLabel')
    wrapper.unmount()
  })

  it('「接入」打开一键安装页签，「CC Switch」打开 CC Switch 页签', async () => {
    const wrapper = await mountView(RowsTableStub)
    const modal = () => wrapper.findComponent({ name: 'KeyOnboardingModal' })
    expect(modal().props('show')).toBe(false)

    await wrapper.get('[data-row="1"] [data-test="action-ccswitch"]').trigger('click')
    expect(modal().props('show')).toBe(true)
    expect(modal().props('initialTab')).toBe('ccswitch')
    expect(modal().props('apiKey')).toMatchObject({ id: 1 })
    expect(modal().props('baseUrl')).toBe('https://codex.hiyo.top')

    modal().vm.$emit('close')
    await nextTick()
    await wrapper.get('[data-row="2"] [data-test="action-connect"]').trigger('click')
    expect(modal().props('initialTab')).toBe('install')
    expect(modal().props('apiKey')).toMatchObject({ id: 2 })
    wrapper.unmount()
  })

  it('操作列的停用/启用、编辑、删除按钮带可见文字，不是纯图标', async () => {
    const wrapper = await mountView(RowsTableStub)
    const labelsOf = (id: number) =>
      wrapper.findAll(`[data-row="${id}"] button`).map((b) => b.text())
    // 行 1 启用中显示「停用」，行 2 已停用显示「启用」
    expect(labelsOf(1)).toEqual(expect.arrayContaining(['keys.disable', 'common.edit', 'common.delete']))
    expect(labelsOf(2)).toEqual(expect.arrayContaining(['keys.enable', 'common.edit', 'common.delete']))
    for (const sel of ['action-toggle', 'action-edit', 'action-delete']) {
      const btn = wrapper.get(`[data-row="1"] [data-test="${sel}"]`)
      expect(btn.text()).not.toBe('')
      expect(btn.find('.sr-only').exists()).toBe(false)
    }
    wrapper.unmount()
  })
})

// 没有可加入链的分组（同平台、已授权、启用中、不是主分组）时，行内的「兜底」入口不显示；
// 链里已有项时照常显示，用户才能看到并删掉。
describe('user KeysView fallback entry visibility', () => {
  const summaryOf = (keys: Array<{ key_id: number; has_available: boolean; fallback: boolean }>) => ({
    platforms: [
      {
        platform: 'openai',
        keys: keys.map((k) => ({
          key_id: k.key_id,
          name: `k${k.key_id}`,
          has_available: k.has_available,
          items: [
            { group_id: 1, name: 'main', role: 'primary', position: 0, status: 'active', usable: true },
            ...(k.fallback
              ? [{ group_id: 2, name: 'fb', role: 'fallback', position: 1, status: 'active', usable: true }]
              : []),
          ],
        })),
      },
    ],
  })

  beforeEach(() => {
    localStorage.clear()
    publicSettings.value = null
    activeSubscriptions.value = []
    listKeys.mockResolvedValue({
      items: [{ ...createApiKey(), id: 1, group_id: 1 }, { ...createApiKey(), id: 2, group_id: 1 }],
      total: 2, page: 1, page_size: 20, pages: 1,
    })
    getPublicSettings.mockResolvedValue({})
    getDashboardApiKeysUsage.mockResolvedValue({ stats: {} })
    getAvailableGroups.mockResolvedValue([])
    getUserGroupRates.mockResolvedValue({})
    isCurrentStep.mockReturnValue(false)
    listChains.mockReset()
  })

  it('有可添加分组的 Key 显示入口，没有的不显示', async () => {
    listChains.mockResolvedValue(summaryOf([
      { key_id: 1, has_available: true, fallback: false },
      { key_id: 2, has_available: false, fallback: false },
    ]))
    const wrapper = await mountView()
    expect(wrapper.findAll('[data-test="fallback-entry"]')).toHaveLength(1)
    expect(wrapper.get('[data-test="fallback-entry"]').text()).toBe('keyFallback.entry')
    wrapper.unmount()
  })

  it('所有 Key 都没有可添加的分组（如 free 站只有一个分组）：入口整体不显示', async () => {
    listChains.mockResolvedValue(summaryOf([
      { key_id: 1, has_available: false, fallback: false },
      { key_id: 2, has_available: false, fallback: false },
    ]))
    const wrapper = await mountView()
    expect(wrapper.find('[data-test="fallback-entry"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('已有兜底项时，即使没有可添加的分组入口也照常显示', async () => {
    listChains.mockResolvedValue(summaryOf([
      { key_id: 1, has_available: false, fallback: true },
      { key_id: 2, has_available: false, fallback: false },
    ]))
    const wrapper = await mountView()
    expect(wrapper.findAll('[data-test="fallback-entry"]')).toHaveLength(1)
    wrapper.unmount()
  })

  it('摘要读取失败时不把入口藏起来', async () => {
    listChains.mockRejectedValue(new Error('boom'))
    const wrapper = await mountView()
    expect(wrapper.findAll('[data-test="fallback-entry"]')).toHaveLength(2)
    wrapper.unmount()
  })

  it('摘要还没回来时先不显示，回来后再显示', async () => {
    let resolve!: (v: unknown) => void
    listChains.mockReturnValue(new Promise((r) => { resolve = r }))
    const wrapper = await mountView()
    expect(wrapper.find('[data-test="fallback-entry"]').exists()).toBe(false)
    resolve(summaryOf([{ key_id: 1, has_available: true, fallback: false }, { key_id: 2, has_available: true, fallback: false }]))
    await flushPromises()
    expect(wrapper.findAll('[data-test="fallback-entry"]')).toHaveLength(2)
    wrapper.unmount()
  })

  it('没有绑定分组的 Key 仍然没有入口', async () => {
    listKeys.mockResolvedValue({ items: [createApiKey()], total: 1, page: 1, page_size: 20, pages: 1 })
    listChains.mockResolvedValue({ platforms: [] })
    const wrapper = await mountView()
    expect(wrapper.find('[data-test="fallback-entry"]').exists()).toBe(false)
    wrapper.unmount()
  })
})

describe('user KeysView ?new=1 与创建成功确认态', () => {
  const created: ApiKey = { ...createApiKey(), id: 9, name: 'fresh', key: 'sk-fresh-full-key', group_id: 5 }

  beforeEach(() => {
    localStorage.clear()
    publicSettings.value = null
    activeSubscriptions.value = []
    routeState.query = {}
    routeState.replace.mockReset()
    createKey.mockReset()
    copyToClipboard.mockReset()
    copyToClipboard.mockResolvedValue(true)
    listKeys.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 1 })
    getPublicSettings.mockResolvedValue({})
    getDashboardApiKeysUsage.mockResolvedValue({ stats: {} })
    getAvailableGroups.mockResolvedValue([])
    getUserGroupRates.mockResolvedValue({})
    isCurrentStep.mockReturnValue(false)
    nextStep.mockReset()
  })

  const submitCreate = async (wrapper: VueWrapper) => {
    await wrapper.get('[data-tour="key-form-name"]').setValue('fresh')
    const groupSelect = wrapper.findAllComponents({ name: 'Select' })
      .find((select) => select.attributes('data-tour') === 'key-form-group')!
    groupSelect.vm.$emit('update:modelValue', 5)
    await nextTick()
    await wrapper.get('#key-form').trigger('submit')
    await flushPromises()
  }

  it('打开 /keys?new=1 直接弹出创建弹窗，并把参数清掉', async () => {
    routeState.query = { new: '1', foo: 'bar' }
    const wrapper = await mountView()
    expect(wrapper.find('#key-form').exists()).toBe(true)
    expect(routeState.replace).toHaveBeenCalledTimes(1)
    expect(routeState.replace).toHaveBeenCalledWith({ query: { foo: 'bar' } })
    wrapper.unmount()
  })

  it('没有 new 参数时不弹窗，也不改地址', async () => {
    const wrapper = await mountView()
    expect(wrapper.find('#key-form').exists()).toBe(false)
    expect(routeState.replace).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('new 不是 1 时只清参数，不弹窗', async () => {
    routeState.query = { new: '0' }
    const wrapper = await mountView()
    expect(wrapper.find('#key-form').exists()).toBe(false)
    expect(routeState.replace).toHaveBeenCalledWith({ query: {} })
    wrapper.unmount()
  })

  it('创建成功后弹窗原地变成确认态：完整密钥、复制、提示、去接入', async () => {
    routeState.query = { new: '1' }
    createKey.mockResolvedValue(created)
    const wrapper = await mountView()
    const listCallsBefore = listKeys.mock.calls.length
    await submitCreate(wrapper)

    expect(createKey).toHaveBeenCalledTimes(1)
    expect(wrapper.find('#key-form').exists()).toBe(false)
    expect(wrapper.get('[data-test="key-created-value"]').text()).toBe('sk-fresh-full-key')
    expect(wrapper.text()).toContain('keys.createdHint')
    expect(wrapper.find('[data-test="key-created-connect"]').exists()).toBe(true)
    // 列表在后台刷新
    expect(listKeys.mock.calls.length).toBe(listCallsBefore + 1)

    await wrapper.get('[data-test="key-created-copy"]').trigger('click')
    expect(copyToClipboard).toHaveBeenCalledWith('sk-fresh-full-key', 'keys.copied')
    wrapper.unmount()
  })

  it('创建成功时导览仍在最后一步推进', async () => {
    isCurrentStep.mockReturnValue(true)
    createKey.mockResolvedValue(created)
    const wrapper = await mountView()
    await getButtonByText(wrapper, 'Create API Key').trigger('click')
    await submitCreate(wrapper)
    expect(isCurrentStep).toHaveBeenCalledWith('[data-tour="key-form-submit"]')
    expect(nextStep).toHaveBeenCalledWith(500)
    wrapper.unmount()
  })

  it('创建失败时留在表单，不进确认态', async () => {
    createKey.mockRejectedValue({ response: { data: { detail: 'boom' } } })
    const wrapper = await mountView()
    await getButtonByText(wrapper, 'Create API Key').trigger('click')
    await submitCreate(wrapper)
    expect(wrapper.find('#key-form').exists()).toBe(true)
    expect(wrapper.find('[data-test="key-created"]').exists()).toBe(false)
    expect(showError).toHaveBeenCalledWith('boom')
    wrapper.unmount()
  })

  it('点「去接入」关掉确认态，并用新密钥打开接入弹窗', async () => {
    createKey.mockResolvedValue(created)
    const wrapper = await mountView()
    await getButtonByText(wrapper, 'Create API Key').trigger('click')
    await submitCreate(wrapper)
    await wrapper.get('[data-test="key-created-connect"]').trigger('click')

    const modal = wrapper.findComponent({ name: 'KeyOnboardingModal' })
    expect(wrapper.find('[data-test="key-created"]').exists()).toBe(false)
    expect(modal.props('show')).toBe(true)
    expect(modal.props('initialTab')).toBe('install')
    expect(modal.props('apiKey')).toMatchObject({ id: 9, key: 'sk-fresh-full-key' })
    wrapper.unmount()
  })

  it('关闭确认态后再打开创建弹窗，回到空白表单', async () => {
    createKey.mockResolvedValue(created)
    const wrapper = await mountView()
    await getButtonByText(wrapper, 'Create API Key').trigger('click')
    await submitCreate(wrapper)
    await wrapper.get('[data-test="key-created-close"]').trigger('click')
    expect(wrapper.find('[data-test="key-created"]').exists()).toBe(false)
    await getButtonByText(wrapper, 'Create API Key').trigger('click')
    expect((wrapper.get('[data-tour="key-form-name"]').element as HTMLInputElement).value).toBe('')
    wrapper.unmount()
  })
})
