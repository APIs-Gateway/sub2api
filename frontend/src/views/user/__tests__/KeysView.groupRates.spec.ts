/**
 * 密钥页的分组倍率：列表里的分组徽标、创建 / 编辑弹窗里分组下拉的「已选」和「选项」、
 * 点徽标弹出的切换分组浮层，这四处都要按站点和展示口径给出同一套倍率。
 * 用真实的 GroupBadge / GroupOptionItem 和 zh-CN 文案渲染，断言的是用户真正看到的字。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { DOMWrapper, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { defineComponent, nextTick, ref } from 'vue'

import type { ApiKey } from '@/types'
import KeysView from '../KeysView.vue'
import { resetFiatDataMissingForTest, useCurrencyDisplay } from '@/composables/useCurrencyDisplay'
import { resetPlanPricingForTest } from '@/composables/useRateDisplay'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'

const {
  listKeys,
  getDashboardApiKeysUsage,
  getAvailableGroups,
  getUserGroupRates,
  getSubscriptionPricing
} = vi.hoisted(() => ({
  listKeys: vi.fn(),
  getDashboardApiKeysUsage: vi.fn(),
  getAvailableGroups: vi.fn(),
  getUserGroupRates: vi.fn(),
  getSubscriptionPricing: vi.fn()
}))

vi.mock('@/api', () => ({
  keysAPI: { list: listKeys, create: vi.fn(), update: vi.fn(), delete: vi.fn(), toggleStatus: vi.fn() },
  authAPI: { getPublicSettings: vi.fn().mockResolvedValue({}) },
  usageAPI: { getDashboardApiKeysUsage },
  userGroupsAPI: { getAvailable: getAvailableGroups, getUserGroupRates }
}))
vi.mock('@/api/subscriptions', () => ({ default: { getSubscriptionPricing } }))
vi.mock('@/i18n', () => ({ getLocale: () => 'zh-CN' }))
vi.mock('@/stores/onboarding', () => ({ useOnboardingStore: () => ({ isCurrentStep: () => false, nextStep: vi.fn() }) }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: vi.fn() }) }))

// 测试环境用的是 vue-i18n 的 runtime 构建，不能现场编译消息；这里按 zh-CN 语言包取字再替换 {占位符}。
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const { default: zhCN } = await import('@/i18n/locales/zh-CN')
  const translate = (key: string, params: Record<string, unknown> = {}) => {
    const hit = key.split('.').reduce<unknown>((o, k) => (o as Record<string, unknown> | undefined)?.[k], zhCN)
    if (typeof hit !== 'string') return key
    return hit.replace(/\{(\w+)\}/g, (_, name) => String(params[name] ?? `{${name}}`))
  }
  return { ...actual, useI18n: () => ({ locale: ref('zh-CN'), t: translate }) }
})

const PROD_PRICING = { d_min: 30, d_max: 510, u_min: 0.04, u_max: 0.05, t_min: 30, t_max: 360, t_step: 30, d_floor: 210 }

/** 改动前 free 站药丸的悬停原文（zh-CN），一个字不能变。 */
const LEGACY_TIP =
  '「Nx 倍率」是该分组相对官方价的扣额度速度：数字越大、池子越稳定，同样的用量扣得也越多；它是系统扣费的系数，不是最终价格。叠加套餐折扣后你实际相当于官方价几折，见分组描述。'

const G16 = {
  id: 16,
  name: 'codex特惠分组',
  description: '不保证稳定性，挂了切其它分组。出问题会积极修复。',
  platform: 'openai',
  subscription_type: 'standard',
  rate_multiplier: 1.4
}
const G30 = { id: 30, name: 'codex pro+plus', description: '适合日常工作/下游接入。', platform: 'openai', subscription_type: 'standard', rate_multiplier: 3 }
const G26 = { id: 26, name: 'kiro cc', description: 'kiro claude', platform: 'anthropic', subscription_type: 'standard', rate_multiplier: 2 }

const keyRow = (extra: Partial<ApiKey> = {}): ApiKey =>
  ({
    id: 1,
    user_id: 1,
    key: 'sk-test-key',
    name: 'test-key',
    group_id: 16,
    group: G16,
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
    ...extra
  }) as ApiKey

/** 渲染列表里的分组徽标和用量列。 */
const TableStub = defineComponent({
  props: ['columns', 'data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.id" :data-row="row.id">
        <div data-test="cell-group"><slot name="cell-group" :row="row" /></div>
        <div data-test="cell-usage"><slot name="cell-usage" :row="row" /></div>
      </div>
    </div>`
})

/** 把 Select 的 selected / option 两个槽都渲染出来。 */
const SelectStub = defineComponent({
  props: ['modelValue', 'options'],
  template: `
    <div>
      <div data-test="selected"><slot name="selected" :option="(options || []).find((o) => o.value === modelValue) ?? null" /></div>
      <div v-for="o in (options || [])" :key="o.value" data-test="option"><slot name="option" :option="o" :selected="o.value === modelValue" /></div>
    </div>`
})

async function mountView(options: { keys?: ApiKey[]; userRates?: Record<number, number> } = {}) {
  listKeys.mockResolvedValue({ items: options.keys ?? [keyRow()], total: 1, page: 1, page_size: 20, pages: 1 })
  getUserGroupRates.mockResolvedValue(options.userRates ?? {})
  const wrapper = mount(KeysView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="actions" /><slot name="table" /><slot name="pagination" /></div>' },
        DataTable: TableStub,
        Pagination: true,
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
        ConfirmDialog: true,
        EmptyState: true,
        Select: SelectStub,
        SearchInput: true,
        HelpTooltip: { template: '<div data-test="help"><slot /></div>' },
        Icon: { template: '<span />' },
        KeyOnboardingModal: true,
        EndpointPopover: true,
        Teleport: true
      }
    }
  })
  await flushPromises()
  await nextTick()
  return wrapper
}

function setSettings(settings: Record<string, unknown>) {
  useAppStore().cachedPublicSettings = settings as never
}

/** 打开「创建密钥」弹窗并选中 16 号分组，让「已选」槽和「选项」槽都渲染出来。 */
async function openCreateModal(wrapper: VueWrapper) {
  const state = (wrapper.vm as any).$?.setupState
  state.showCreateModal = true
  await nextTick()
  state.formData.group_id = 16
  await nextTick()
  return wrapper.get('[data-tour="key-form-group"]')
}

/** 点列表里的分组徽标，弹出切换分组浮层。 */
async function openSwitchPopup(wrapper: VueWrapper) {
  await wrapper.get('[data-test="cell-group"] button').trigger('click')
  await nextTick()
  // 浮层以搜索框为标志（Teleport 在测试里就地渲染成 teleport-stub）
  const popup = wrapper.get('input[placeholder="搜索分组..."]').element.closest('div.fixed')
  if (!popup) throw new Error('切换分组浮层没有出现')
  return new DOMWrapper(popup)
}

const texts = (els: Array<{ text(): string }>) => els.map((el) => el.text())
const pill = (root: VueWrapper | ReturnType<VueWrapper['get']>) => root.findAll('span.rounded-full')
const planTexts = (root: VueWrapper | ReturnType<VueWrapper['get']>) => texts(root.findAll('[data-test="plan-rate"]'))

describe('密钥页的分组倍率', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    window.localStorage.clear()
    resetFiatDataMissingForTest()
    resetPlanPricingForTest()
    useCurrencyDisplay().setMode('fiat')
    listKeys.mockReset()
    getDashboardApiKeysUsage.mockReset().mockResolvedValue({ stats: {} })
    getAvailableGroups.mockReset().mockResolvedValue([G16, G30, G26])
    getSubscriptionPricing.mockReset().mockResolvedValue(PROD_PRICING)
    const auth = useAuthStore()
    auth.token = 'test-token'
    auth.user = { id: 1, role: 'user' } as never
    setSettings({ balance_recharge_multiplier: 13, payment_enabled: true })
  })

  describe('人民币模式（充值倍率 13）', () => {
    it('列表里的分组徽标：0.108x，旁边是「套餐低至 0.056x」', async () => {
      const wrapper = await mountView()
      const cell = wrapper.get('[data-test="cell-group"]')

      expect(cell.get('span.truncate').text()).toBe('codex特惠分组')
      expect(cell.get('span.rounded.text-\\[10px\\]').text()).toBe('0.108x')
      expect(planTexts(cell)).toEqual(['套餐低至 0.056x'])
      expect(cell.get('span.inline-flex').attributes('title')).toContain('官方价每 $1 的用量，实付 ¥0.108')
    })

    it('列表里「套餐低至」在徽标下一行，不占徽标的宽度；窄屏只留悬停', async () => {
      const wrapper = await mountView()
      const cell = wrapper.get('[data-test="cell-group"]')
      const badge = cell.get('span.inline-flex')

      expect(badge.find('[data-test="plan-rate"]').exists()).toBe(false)
      const plan = cell.get('[data-test="plan-rate"]')
      expect(plan.text()).toBe('套餐低至 0.056x')
      expect(plan.classes()).toEqual(expect.arrayContaining(['hidden', 'sm:block']))
      // 悬停提示：徽标和下一行是同一段
      expect(plan.attributes('title')).toBe(badge.attributes('title'))

      // 创建弹窗里的已选槽宽度够，套餐低至仍在徽标里面
      const select = await openCreateModal(wrapper)
      expect(select.get('[data-test="selected"] span.inline-flex').find('[data-test="plan-rate"]').exists()).toBe(true)
    })

    it('列表里的订阅分组：徽标显示「订阅」，下面没有套餐低至', async () => {
      const wrapper = await mountView({ keys: [keyRow({ group: { ...G16, subscription_type: 'subscription' } as never })] })
      const cell = wrapper.get('[data-test="cell-group"]')

      expect(cell.get('span.inline-flex').text()).toContain('订阅')
      expect(cell.findAll('[data-test="plan-rate"]')).toHaveLength(0)
    })

    it('列表里的徽标：有专属倍率时两对都划线', async () => {
      const wrapper = await mountView({ userRates: { 16: 0.585 } })
      const cell = wrapper.get('[data-test="cell-group"]')

      expect(texts(cell.findAll('.line-through'))).toEqual(['0.108x', '0.056x'])
      expect(texts(cell.findAll('.font-bold'))).toEqual(['0.045x', '0.0234x'])
    })

    it('创建弹窗分组下拉：已选槽和三个选项都用等效倍率', async () => {
      const wrapper = await mountView()
      const select = await openCreateModal(wrapper)

      const selected = select.get('[data-test="selected"]')
      expect(selected.get('span.rounded.text-\\[10px\\]').text()).toBe('0.108x')
      expect(planTexts(selected)).toEqual(['套餐低至 0.056x'])

      const options = select.findAll('[data-test="option"]')
      expect(options).toHaveLength(3)
      expect(options.map((o) => o.get('span.rounded-full').text())).toEqual(['0.108x 倍率', '0.231x 倍率', '0.154x 倍率'])
      expect(options.map((o) => planTexts(o))).toEqual([['套餐低至 0.056x'], ['套餐低至 0.12x'], ['套餐低至 0.08x']])
      expect(options.map((o) => o.get('span.rounded-full').attributes('title'))).toEqual([
        '倍率 0.108x：官方价每 $1 的用量，实付 ¥0.108。倍率越低越省。\n开通套餐后，同样的用量最低按 0.056x 扣费。',
        '倍率 0.231x：官方价每 $1 的用量，实付 ¥0.231。倍率越低越省。\n开通套餐后，同样的用量最低按 0.12x 扣费。',
        '倍率 0.154x：官方价每 $1 的用量，实付 ¥0.154。倍率越低越省。\n开通套餐后，同样的用量最低按 0.08x 扣费。'
      ])
      // 分组描述原样显示（描述里的第二个倍率由运营在发版时去掉，不在前端处理）
      expect(options[0].text()).toContain(G16.description)
    })

    it('点徽标弹出的切换分组浮层：同样三个选项', async () => {
      const wrapper = await mountView()
      const popup = await openSwitchPopup(wrapper)

      expect(texts(pill(popup))).toEqual(['0.108x 倍率', '0.231x 倍率', '0.154x 倍率'])
      expect(planTexts(popup)).toEqual(['套餐低至 0.056x', '套餐低至 0.12x', '套餐低至 0.08x'])
    })

    it('套餐定价取不到：四处都只剩主倍率', async () => {
      getSubscriptionPricing.mockRejectedValue(new Error('boom'))
      const wrapper = await mountView()
      const select = await openCreateModal(wrapper)
      const popup = await openSwitchPopup(wrapper)

      expect(wrapper.findAll('[data-test="plan-rate"]')).toHaveLength(0)
      expect(texts(pill(popup))).toEqual(['0.108x 倍率', '0.231x 倍率', '0.154x 倍率'])
      expect(select.findAll('[data-test="option"]')).toHaveLength(3)
    })

    it('支付关闭：不请求套餐定价，也没有套餐低至', async () => {
      setSettings({ balance_recharge_multiplier: 13, payment_enabled: false })
      const wrapper = await mountView()

      expect(getSubscriptionPricing).not.toHaveBeenCalled()
      expect(wrapper.findAll('[data-test="plan-rate"]')).toHaveLength(0)
      expect(wrapper.get('[data-test="cell-group"] span.rounded.text-\\[10px\\]').text()).toBe('0.108x')
    })
  })

  describe('美元模式（充值倍率 13，选了 $）：原始倍率，没有套餐低至', () => {
    beforeEach(() => {
      useCurrencyDisplay().setMode('usd')
    })

    it('四处都是原始倍率', async () => {
      const wrapper = await mountView()
      const select = await openCreateModal(wrapper)
      const popup = await openSwitchPopup(wrapper)

      expect(wrapper.get('[data-test="cell-group"] span.rounded.text-\\[10px\\]').text()).toBe('1.4x')
      expect(select.get('[data-test="selected"] span.rounded.text-\\[10px\\]').text()).toBe('1.4x')
      expect(texts(select.findAll('[data-test="option"] span.rounded-full'))).toEqual(['1.4x 倍率', '3x 倍率', '2x 倍率'])
      expect(texts(pill(popup))).toEqual(['1.4x 倍率', '3x 倍率', '2x 倍率'])
      expect(wrapper.findAll('[data-test="plan-rate"]')).toHaveLength(0)
    })

    it('药丸悬停说明每 $1 官方价扣多少额度', async () => {
      const wrapper = await mountView()
      const select = await openCreateModal(wrapper)

      expect(select.findAll('[data-test="option"] span.rounded-full').map((p) => p.attributes('title'))).toEqual([
        '倍率 1.4x：官方价每 $1 的用量，扣 $1.4 额度。倍率越低越省。',
        '倍率 3x：官方价每 $1 的用量，扣 $3 额度。倍率越低越省。',
        '倍率 2x：官方价每 $1 的用量，扣 $2 额度。倍率越低越省。'
      ])
    })

    it('专属倍率照旧划线，数字是原始倍率', async () => {
      const wrapper = await mountView({ userRates: { 16: 0.585 } })
      const cell = wrapper.get('[data-test="cell-group"]')

      expect(texts(cell.findAll('.line-through'))).toEqual(['1.4x'])
      expect(texts(cell.findAll('.font-bold'))).toEqual(['0.585x'])
    })
  })

  describe('free 站（充值倍率 1）：与改动前逐字相同', () => {
    beforeEach(() => {
      setSettings({ balance_recharge_multiplier: 1, payment_enabled: true })
      // 即使浏览器里存着 ¥ 偏好，free 站也只按美元显示。
      window.localStorage.setItem('currency-display-mode', 'fiat')
    })

    it('徽标、已选、选项、浮层都是原始倍率，悬停是原文案，没有套餐低至，也不请求套餐定价', async () => {
      const wrapper = await mountView()
      const select = await openCreateModal(wrapper)
      const popup = await openSwitchPopup(wrapper)

      expect(wrapper.get('[data-test="cell-group"] span.rounded.text-\\[10px\\]').text()).toBe('1.4x')
      expect(wrapper.get('[data-test="cell-group"] span.inline-flex').attributes('title')).toBeUndefined()
      expect(select.get('[data-test="selected"] span.rounded.text-\\[10px\\]').text()).toBe('1.4x')
      expect(texts(select.findAll('[data-test="option"] span.rounded-full'))).toEqual(['1.4x 倍率', '3x 倍率', '2x 倍率'])
      expect(texts(pill(popup))).toEqual(['1.4x 倍率', '3x 倍率', '2x 倍率'])
      for (const p of [...select.findAll('[data-test="option"] span.rounded-full'), ...pill(popup)]) {
        expect(p.attributes('title')).toBe(LEGACY_TIP)
      }
      expect(wrapper.findAll('[data-test="plan-rate"]')).toHaveLength(0)
      expect(getSubscriptionPricing).not.toHaveBeenCalled()
    })

    it('专属倍率照旧：划掉 1.4x，高亮 0.585x', async () => {
      const wrapper = await mountView({ userRates: { 16: 0.585 } })
      const cell = wrapper.get('[data-test="cell-group"]')

      expect(texts(cell.findAll('.line-through'))).toEqual(['1.4x'])
      expect(texts(cell.findAll('.font-bold'))).toEqual(['0.585x'])
    })
  })

  describe('创建 / 编辑弹窗里的「计费怎么算？」', () => {
    it.each([
      ['人民币模式', () => undefined],
      ['美元模式', () => useCurrencyDisplay().setMode('usd')],
      ['free 站', () => setSettings({ balance_recharge_multiplier: 1, payment_enabled: true })]
    ] as const)('%s：只讲余额、套餐、倍率越低越省，不出现美元额度和折扣算式', async (_name, arrange) => {
      arrange()
      const wrapper = await mountView()
      await openCreateModal(wrapper)

      const help = wrapper.get('[data-test="help"]')
      expect(help.findAll('p').map((p) => p.text())).toEqual([
        '计费怎么算？',
        '本站按量从余额扣费。开通套餐后，用量会优先从套餐扣，同样的用量更划算。',
        '分组右侧的倍率越低越省：同样的用量，倍率低的分组扣得更少。'
      ])
      expect(help.text()).not.toMatch(/USD|\$|几折|套餐折扣|¥39|2700|0\.X/)
    })
  })

  describe('密钥列表用量列', () => {
    it('有上限的密钥：「累计已用」在前，「近30天」单独一行', async () => {
      const wrapper = await mountView({ keys: [keyRow({ quota: 100, quota_used: 40 })] })
      const usage = wrapper.get('[data-test="cell-usage"]').text()

      expect(usage).toContain('累计已用')
      expect(usage).toContain('近30天')
      expect(usage.indexOf('累计已用')).toBeLessThan(usage.indexOf('近30天'))
      expect(usage).not.toMatch(/(^|[^计])已用/)
    })

    it('没有上限的密钥只有近 30 天消费，不出现「累计已用」', async () => {
      const wrapper = await mountView({ keys: [keyRow({ quota: 0 })] })
      const usage = wrapper.get('[data-test="cell-usage"]').text()

      expect(usage).toContain('近30天')
      expect(usage).not.toContain('已用')
    })
  })
})
