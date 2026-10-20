import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { useCurrencyDisplay } from '@/composables/useCurrencyDisplay'
import { useAppStore } from '@/stores/app'
import type { PublicSettings, UserSubscription } from '@/types'
import SidebarBalanceCard from '../SidebarBalanceCard.vue'

const auth = vi.hoisted(() => ({
  user: null as Record<string, unknown> | null,
  isSimpleMode: false,
}))
const getActiveSubscriptions = vi.hoisted(() => vi.fn())

vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth }))
vi.mock('@/api/subscriptions', () => ({ default: { getActiveSubscriptions } }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => (params ? `${key}|${JSON.stringify(params)}` : key),
    }),
  }
})

const RouterLinkStub = {
  props: ['to'],
  template: '<a :data-to="typeof to === \'string\' ? to : JSON.stringify(to)"><slot /></a>',
}

function card(overrides: Partial<UserSubscription> = {}): UserSubscription {
  return {
    id: 1,
    user_id: 1,
    group_id: null,
    status: 'active',
    starts_at: '2026-10-01T00:00:00Z',
    daily_usage_usd: 4,
    weekly_usage_usd: 0,
    monthly_usage_usd: 0,
    daily_window_start: null,
    weekly_window_start: null,
    monthly_window_start: null,
    daily_limit_usd: 10,
    weekly_limit_usd: 70,
    monthly_limit_usd: 300,
    fiat_per_credit: 0.5,
    created_at: '2026-10-01T00:00:00Z',
    updated_at: '2026-10-01T00:00:00Z',
    expires_at: new Date(Date.now() + 10 * 24 * 60 * 60 * 1000).toISOString(),
    ...overrides,
  }
}

function setSettings(settings: Record<string, unknown>) {
  useAppStore().cachedPublicSettings = settings as unknown as PublicSettings
}

async function mountCard(props: { collapsed?: boolean } = {}): Promise<VueWrapper> {
  const wrapper = mount(SidebarBalanceCard, {
    props,
    global: { stubs: { RouterLink: RouterLinkStub } },
  })
  await flushPromises()
  return wrapper
}

const text = (w: VueWrapper, id: string) => w.find(`[data-testid="${id}"]`).text().replace(/[\u00a0\u202f\s]+/g, '')

beforeEach(() => {
  setActivePinia(createPinia())
  window.localStorage.clear()
  useCurrencyDisplay().setMode('fiat')
  auth.user = { id: 7, role: 'user', balance: 130 }
  auth.isSimpleMode = false
  getActiveSubscriptions.mockReset().mockResolvedValue([])
  // 充值倍率 13：¥1 买到 13 额度。
  setSettings({ balance_recharge_multiplier: 13, payment_enabled: true })
})

afterEach(() => {
  vi.useRealTimers()
})

describe('SidebarBalanceCard', () => {
  it('人民币模式：余额按充值倍率折成 ¥，带充值和使用记录入口', async () => {
    const wrapper = await mountCard()

    expect(text(wrapper, 'balance-card-amount')).toBe('¥10.00')
    expect(wrapper.find('[data-testid="balance-card-topup"]').attributes('data-to')).toBe(
      JSON.stringify({ path: '/purchase', query: { tab: 'recharge' } })
    )
    expect(wrapper.find('[data-testid="balance-card-usage"]').attributes('data-to')).toBe('/usage')
    expect(wrapper.find('[data-testid="balance-card-topup"]').text()).toBe('nav.topUp')
    expect(wrapper.find('[data-testid="balance-card-usage"]').text()).toBe('dashboard.viewUsage')
    expect(wrapper.find('[data-testid="balance-card-subscription"]').exists()).toBe(false)
  })

  it('美元模式：直接按 $ 显示额度，不出现 ¥，也不带换算比例', async () => {
    useCurrencyDisplay().setMode('usd')

    const wrapper = await mountCard()

    expect(text(wrapper, 'balance-card-amount')).toBe('$130.00')
    expect(wrapper.text()).not.toMatch(/¥|CNY|1\s*[:=]/)
  })

  it('free 站（倍率为 1、支付关闭）：强制按美元，没有充值入口', async () => {
    setSettings({ balance_recharge_multiplier: 1, payment_enabled: false })

    const wrapper = await mountCard()

    expect(text(wrapper, 'balance-card-amount')).toBe('$130.00')
    expect(wrapper.find('[data-testid="balance-card-topup"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="balance-card-usage"]').exists()).toBe(true)
  })

  it('支付关闭时隐藏充值按钮，其余照常', async () => {
    setSettings({ balance_recharge_multiplier: 13, payment_enabled: false })

    const wrapper = await mountCard()

    expect(wrapper.find('[data-testid="balance-card-topup"]').exists()).toBe(false)
    expect(text(wrapper, 'balance-card-amount')).toBe('¥10.00')
  })

  it('简易模式不渲染', async () => {
    auth.isSimpleMode = true

    const wrapper = await mountCard()

    expect(wrapper.find('[data-testid="sidebar-balance-card"]').exists()).toBe(false)
  })

  it('用户资料还没回来时显示占位，不闪 ¥0.00', async () => {
    auth.user = null

    const wrapper = await mountCard()

    expect(wrapper.find('[data-testid="balance-card-skeleton"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="balance-card-amount"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('0.00')
  })

  it('余额悬停给出精确值', async () => {
    auth.user = { id: 7, role: 'user', balance: 1.2345 }

    const wrapper = await mountCard()

    expect(wrapper.find('[data-testid="balance-card-amount"]').attributes('title')).toMatch(/0\.09496/)
  })

  describe('订阅用户', () => {
    it('余额为 0 照常显示，不带任何告警样式；订阅剩余另起一行', async () => {
      auth.user = { id: 7, role: 'user', balance: 0 }
      getActiveSubscriptions.mockResolvedValue([card()])

      const wrapper = await mountCard()

      const amount = wrapper.find('[data-testid="balance-card-amount"]')
      expect(amount.text().replace(/[\u00a0\u202f\s]+/g, '')).toBe('¥0.00')
      expect(amount.classes().join(' ')).not.toMatch(/text-primary|bg-primary|border-primary|red|amber|yellow/)
      expect(wrapper.find('[data-testid="sidebar-balance-card"]').classes().join(' ')).not.toMatch(/text-primary|bg-primary|border-primary|red|amber/)
      expect(wrapper.find('[data-testid="balance-card-subscription"]').exists()).toBe(true)
      expect(wrapper.find('[data-testid="balance-card-subscription"]').attributes('data-to')).toBe('/subscriptions')
    })

    it('人民币模式按这张卡自己的单价折算：剩余 6 额度 × 0.5 = ¥3.00', async () => {
      getActiveSubscriptions.mockResolvedValue([card()])

      const wrapper = await mountCard()
      const sub = wrapper.find('[data-testid="balance-card-subscription"]')

      expect(sub.text()).toContain('nav.balanceCard.leftDaily')
      expect(sub.text().replace(/[\u00a0\u202f\s]+/g, '')).toContain('¥3.00')
      expect(sub.text()).toContain('nav.balanceCard.subscription')
    })

    it('美元模式显示额度本身', async () => {
      useCurrencyDisplay().setMode('usd')
      getActiveSubscriptions.mockResolvedValue([card()])

      const wrapper = await mountCard()

      expect(text(wrapper, 'balance-card-subscription')).toContain('$6.00')
    })

    it('有卡拿不到单价时整体回落到美元，不混排', async () => {
      getActiveSubscriptions.mockResolvedValue([card({ fiat_per_credit: undefined })])

      const wrapper = await mountCard()

      expect(text(wrapper, 'balance-card-subscription')).toContain('$6.00')
    })

    it('不限额的订阅显示「无限制」', async () => {
      getActiveSubscriptions.mockResolvedValue([
        card({ daily_limit_usd: null, weekly_limit_usd: null, monthly_limit_usd: null }),
      ])

      const wrapper = await mountCard()
      const sub = wrapper.find('[data-testid="balance-card-subscription"]')

      expect(sub.text()).toContain('subscriptionProgress.unlimited')
      expect(sub.text()).not.toContain('nav.balanceCard.leftDaily')
    })

    it('多张订阅时标题改成张数', async () => {
      getActiveSubscriptions.mockResolvedValue([card({ id: 1 }), card({ id: 2 })])

      const wrapper = await mountCard()

      expect(wrapper.find('[data-testid="balance-card-subscription"]').text()).toContain(
        'subscriptionProgress.activeCount|{"count":2}'
      )
    })

    it('到期时间：几天后 / 明天；临近到期用陶土色，其余中性', async () => {
      getActiveSubscriptions.mockResolvedValue([card({ expires_at: new Date(Date.now() + 10 * 86400000).toISOString() })])
      let wrapper = await mountCard()
      let expiry = wrapper.find('[data-testid="balance-card-subscription"] .num')
      expect(expiry.text()).toBe('subscriptionProgress.daysRemaining|{"days":10}')
      expect(expiry.classes().join(' ')).not.toContain('primary')

      getActiveSubscriptions.mockResolvedValue([card({ expires_at: new Date(Date.now() + 2 * 86400000).toISOString() })])
      setActivePinia(createPinia())
      setSettings({ balance_recharge_multiplier: 13, payment_enabled: true })
      wrapper = await mountCard()
      expiry = wrapper.find('[data-testid="balance-card-subscription"] .num')
      expect(expiry.text()).toBe('subscriptionProgress.daysRemaining|{"days":2}')
      expect(expiry.classes().join(' ')).toContain('text-primary-700')
    })

    it('订阅接口失败时卡片照常显示余额，只是没有订阅行', async () => {
      const error = vi.spyOn(console, 'error').mockImplementation(() => {})
      getActiveSubscriptions.mockRejectedValue(new Error('boom'))

      const wrapper = await mountCard()

      expect(text(wrapper, 'balance-card-amount')).toBe('¥10.00')
      expect(wrapper.find('[data-testid="balance-card-subscription"]').exists()).toBe(false)
      error.mockRestore()
    })
  })

  describe('图标态', () => {
    it('只留一个入口：充值开启时去充值，余额放进悬停提示', async () => {
      const wrapper = await mountCard({ collapsed: true })
      const link = wrapper.find('[data-testid="balance-card-collapsed"]')

      expect(link.attributes('data-to')).toBe(JSON.stringify({ path: '/purchase', query: { tab: 'recharge' } }))
      expect(link.attributes('title')).toContain('¥')
      expect(link.attributes('title')).toContain('dashboard.balance')
      expect(wrapper.find('[data-testid="balance-card-amount"]').exists()).toBe(false)
      expect(wrapper.find('[data-testid="balance-card-topup"]').exists()).toBe(false)
    })

    it('支付关闭时入口改去使用记录', async () => {
      setSettings({ balance_recharge_multiplier: 13, payment_enabled: false })

      const wrapper = await mountCard({ collapsed: true })

      expect(wrapper.find('[data-testid="balance-card-collapsed"]').attributes('data-to')).toBe('/usage')
    })
  })

  describe('手机抽屉', () => {
    it('点任一入口后收起抽屉', async () => {
      vi.useFakeTimers()
      const app = useAppStore()
      app.setMobileOpen(true)

      const wrapper = mount(SidebarBalanceCard, { global: { stubs: { RouterLink: RouterLinkStub } } })
      await wrapper.find('[data-testid="balance-card-topup"]').trigger('click')
      expect(app.mobileOpen).toBe(true)
      vi.advanceTimersByTime(200)

      expect(app.mobileOpen).toBe(false)
    })

    it('抽屉没开时点击不去动它', async () => {
      vi.useFakeTimers()
      const app = useAppStore()
      const spy = vi.spyOn(app, 'setMobileOpen')

      const wrapper = mount(SidebarBalanceCard, { global: { stubs: { RouterLink: RouterLinkStub } } })
      await wrapper.find('[data-testid="balance-card-usage"]').trigger('click')
      vi.advanceTimersByTime(200)

      expect(spy).not.toHaveBeenCalled()
    })
  })
})
