import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { useCurrencyDisplay } from '@/composables/useCurrencyDisplay'
import { useAppStore } from '@/stores/app'
import type { PublicSettings, UserSubscription } from '@/types'
import LowBalanceBanner from '../LowBalanceBanner.vue'

const auth = vi.hoisted(() => ({
  user: null as Record<string, unknown> | null,
  isAdmin: false,
  isSimpleMode: false,
}))
const getActiveSubscriptions = vi.hoisted(() => vi.fn())
const usageQuery = vi.hoisted(() => vi.fn())

vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth }))
vi.mock('@/api/subscriptions', () => ({ default: { getActiveSubscriptions } }))
vi.mock('@/api/usage', () => ({ usageAPI: { query: usageQuery } }))
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

const page = (count: number) => ({ items: Array.from({ length: count }, (_, i) => ({ id: i + 1 })), total: count, page: 1, page_size: 1, pages: 1 })

function sub(overrides: Partial<UserSubscription> = {}): UserSubscription {
  return {
    id: 1,
    user_id: 1,
    group_id: null,
    status: 'active',
    starts_at: '2026-10-01T00:00:00Z',
    daily_usage_usd: 0,
    weekly_usage_usd: 0,
    monthly_usage_usd: 0,
    daily_window_start: null,
    weekly_window_start: null,
    monthly_window_start: null,
    created_at: '2026-10-01T00:00:00Z',
    updated_at: '2026-10-01T00:00:00Z',
    expires_at: null,
    ...overrides,
  }
}

function setSettings(settings: Record<string, unknown>) {
  useAppStore().cachedPublicSettings = settings as unknown as PublicSettings
}

async function mountBanner(): Promise<VueWrapper> {
  const wrapper = mount(LowBalanceBanner, { global: { stubs: { RouterLink: RouterLinkStub } } })
  await flushPromises()
  return wrapper
}

const banner = (w: VueWrapper) => w.find('[data-testid="low-balance-banner"]')
const messageOf = (w: VueWrapper) => w.find('[data-testid="low-balance-message"]').text()

beforeEach(() => {
  setActivePinia(createPinia())
  window.localStorage.clear()
  window.sessionStorage.clear()
  useCurrencyDisplay().setMode('fiat')
  // 充值倍率 13：¥1 = 13 额度；站点默认阈值 13 额度（= ¥1），用户没自己设。
  setSettings({ balance_recharge_multiplier: 13, payment_enabled: true, balance_low_notify_threshold: 13 })
  auth.user = { id: 7, role: 'user', balance: 6.5, balance_notify_enabled: true, balance_notify_threshold: null }
  auth.isAdmin = false
  auth.isSimpleMode = false
  getActiveSubscriptions.mockReset().mockResolvedValue([])
  usageQuery.mockReset().mockResolvedValue(page(0))
})

describe('LowBalanceBanner：没有订阅的用户', () => {
  it('余额低于站点默认阈值时显示，金额按人民币，带去充值标签的入口', async () => {
    const wrapper = await mountBanner()

    expect(banner(wrapper).exists()).toBe(true)
    // 6.5 额度 ÷ 13 = ¥0.50
    expect(messageOf(wrapper)).toContain('lowBalance.low')
    expect(messageOf(wrapper).replace(/[\u00a0\u202f\s]+/g, '')).toContain('¥0.50')
    expect(wrapper.find('[data-testid="low-balance-topup"]').attributes('data-to')).toBe(
      JSON.stringify({ path: '/purchase', query: { tab: 'recharge' } })
    )
    expect(wrapper.find('[data-testid="low-balance-topup"]').text()).toBe('nav.topUp')
    // 没有订阅就不需要去查扣费记录
    expect(usageQuery).not.toHaveBeenCalled()
  })

  it('美元模式：金额是 $，文案里没有换算比例', async () => {
    useCurrencyDisplay().setMode('usd')

    const wrapper = await mountBanner()

    expect(messageOf(wrapper)).toContain('$6.50')
    expect(wrapper.text()).not.toMatch(/¥|1\s*[:=]|13/)
  })

  it('余额用完（<= 0）用「已用完」的说法', async () => {
    auth.user = { ...auth.user, balance: 0 }

    const wrapper = await mountBanner()

    expect(messageOf(wrapper)).toBe('lowBalance.depleted')
  })

  it('余额不低于阈值时不显示', async () => {
    auth.user = { ...auth.user, balance: 13 }

    expect(banner(await mountBanner()).exists()).toBe(false)
  })

  it('用户自己设的阈值优先于站点默认', async () => {
    // 站点默认 13；用户设了 5：余额 6.5 不低
    auth.user = { ...auth.user, balance_notify_threshold: 5 }
    expect(banner(await mountBanner()).exists()).toBe(false)

    // 用户设了 50：余额 6.5 低
    auth.user = { ...auth.user, balance_notify_threshold: 50 }
    expect(banner(await mountBanner()).exists()).toBe(true)
  })

  it('用户和站点都没有阈值时不显示', async () => {
    setSettings({ balance_recharge_multiplier: 13, payment_enabled: true, balance_low_notify_threshold: 0 })

    expect(banner(await mountBanner()).exists()).toBe(false)
  })

  it('用户在余额不足提醒里关掉了提醒就不显示', async () => {
    auth.user = { ...auth.user, balance_notify_enabled: false }

    expect(banner(await mountBanner()).exists()).toBe(false)
  })

  it('累计充值百分比类型的阈值', async () => {
    auth.user = {
      ...auth.user,
      balance: 90,
      balance_notify_threshold: 10,
      balance_notify_threshold_type: 'percentage',
      total_recharged: 1000,
    }
    expect(banner(await mountBanner()).exists()).toBe(true) // 阈值 100

    auth.user = { ...auth.user, balance: 110 }
    expect(banner(await mountBanner()).exists()).toBe(false)
  })
})

describe('LowBalanceBanner：没有支付、管理员、简易模式', () => {
  it('free 站（倍率为 1、支付关闭）不催充值：不显示', async () => {
    setSettings({ balance_recharge_multiplier: 1, payment_enabled: false, balance_low_notify_threshold: 13 })

    expect(banner(await mountBanner()).exists()).toBe(false)
  })

  it('后台关掉支付时不显示', async () => {
    setSettings({ balance_recharge_multiplier: 13, payment_enabled: false, balance_low_notify_threshold: 13 })

    expect(banner(await mountBanner()).exists()).toBe(false)
  })

  it('管理员不显示', async () => {
    auth.isAdmin = true

    expect(banner(await mountBanner()).exists()).toBe(false)
  })

  it('简易模式不显示', async () => {
    auth.isSimpleMode = true

    expect(banner(await mountBanner()).exists()).toBe(false)
  })

  it('后台模式不显示', async () => {
    setSettings({
      balance_recharge_multiplier: 13,
      payment_enabled: true,
      balance_low_notify_threshold: 13,
      backend_mode_enabled: true,
    })

    expect(banner(await mountBanner()).exists()).toBe(false)
  })

  it('用户资料还没回来时不显示', async () => {
    auth.user = null

    expect(banner(await mountBanner()).exists()).toBe(false)
  })
})

describe('LowBalanceBanner：有生效订阅的用户', () => {
  beforeEach(() => {
    auth.user = { ...auth.user, balance: 0 }
    getActiveSubscriptions.mockResolvedValue([sub()])
  })

  it('余额为 0 但近 7 天没有从钱包扣过费：不显示（订阅用户的常态）', async () => {
    usageQuery.mockResolvedValue(page(0))

    expect(banner(await mountBanner()).exists()).toBe(false)
    expect(usageQuery).toHaveBeenCalledTimes(1)
  })

  it('近 7 天从钱包扣过费：显示', async () => {
    usageQuery.mockResolvedValue(page(1))

    const wrapper = await mountBanner()

    expect(banner(wrapper).exists()).toBe(true)
    expect(usageQuery).toHaveBeenCalledWith(
      expect.objectContaining({ billing_type: 0, page: 1, page_size: 1 })
    )
    const params = usageQuery.mock.calls[0][0] as { start_date: string; end_date: string }
    const days = (Date.parse(params.end_date) - Date.parse(params.start_date)) / 86400000
    expect(days).toBe(6)
  })

  it('查扣费记录失败时不显示（宁可漏提醒，不误报）', async () => {
    const error = vi.spyOn(console, 'error').mockImplementation(() => {})
    usageQuery.mockRejectedValue(new Error('boom'))

    expect(banner(await mountBanner()).exists()).toBe(false)
    error.mockRestore()
  })

  it('余额不低时不去查扣费记录', async () => {
    auth.user = { ...auth.user, balance: 1000 }

    await mountBanner()

    expect(usageQuery).not.toHaveBeenCalled()
  })

  it('订阅信息没回来之前不显示，避免先闪一下', async () => {
    let resolve!: (value: UserSubscription[]) => void
    getActiveSubscriptions.mockReturnValue(new Promise((r) => { resolve = r }))
    usageQuery.mockResolvedValue(page(1))

    const wrapper = mount(LowBalanceBanner, { global: { stubs: { RouterLink: RouterLinkStub } } })
    await flushPromises()
    expect(banner(wrapper).exists()).toBe(false)

    resolve([sub()])
    await flushPromises()
    expect(banner(wrapper).exists()).toBe(true)
  })

  it('订阅接口失败时不显示', async () => {
    const error = vi.spyOn(console, 'error').mockImplementation(() => {})
    getActiveSubscriptions.mockRejectedValue(new Error('boom'))

    expect(banner(await mountBanner()).exists()).toBe(false)
    error.mockRestore()
  })
})

describe('LowBalanceBanner：关闭', () => {
  it('点 X 后消失，同一会话里重新挂载（换页、刷新）也不再出现', async () => {
    const wrapper = await mountBanner()
    expect(banner(wrapper).exists()).toBe(true)

    await wrapper.find('[data-testid="low-balance-dismiss"]').trigger('click')
    expect(banner(wrapper).exists()).toBe(false)

    expect(banner(await mountBanner()).exists()).toBe(false)
    setActivePinia(createPinia())
    expect(banner(await mountBanner()).exists()).toBe(false)
  })

  it('余额回到阈值以上之后再跌破，会重新提醒', async () => {
    const wrapper = await mountBanner()
    await wrapper.find('[data-testid="low-balance-dismiss"]').trigger('click')
    expect(banner(wrapper).exists()).toBe(false)

    // 充值后余额回升
    auth.user = { ...auth.user, balance: 500 }
    expect(banner(await mountBanner()).exists()).toBe(false)

    // 又用到了阈值以下
    auth.user = { ...auth.user, balance: 1 }
    expect(banner(await mountBanner()).exists()).toBe(true)
  })

  it('关闭按钮有可读的名字', async () => {
    const wrapper = await mountBanner()

    expect(wrapper.find('[data-testid="low-balance-dismiss"]').attributes('aria-label')).toBe('common.close')
  })

  it('关闭只对当前用户有效', async () => {
    const first = await mountBanner()
    await first.find('[data-testid="low-balance-dismiss"]').trigger('click')

    auth.user = { ...auth.user, id: 8 }

    expect(banner(await mountBanner()).exists()).toBe(true)
  })
})
