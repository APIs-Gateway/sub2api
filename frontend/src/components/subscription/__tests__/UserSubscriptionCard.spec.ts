import { afterEach, describe, expect, it, vi, beforeEach } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { shallowMount } from '@vue/test-utils'
import UserSubscriptionCard from '../UserSubscriptionCard.vue'
import { useAppStore as useRealAppStore } from '@/stores/app'
import type { PublicSettings, UserSubscription } from '@/types'

const routerPush = vi.hoisted(() => vi.fn())

vi.mock('vue-router', () => ({
  useRouter: () => ({
    push: routerPush,
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

vi.mock('@/api/subscriptions', () => ({
  default: {
    borrowOverdraftDay: vi.fn(),
  },
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    showSuccess: vi.fn(),
    showError: vi.fn(),
  }),
}))

function activeSubscriptionFixture(): UserSubscription {
  return {
    id: 376,
    user_id: 345,
    status: 'active',
    daily_amount_usd: 60,
    daily_limit_usd: 60,
    weekly_limit_usd: 420,
    monthly_limit_usd: 1800,
    daily_used_usd: 0,
    weekly_used_usd: 0,
    monthly_used_usd: 0,
    today_remaining: 60,
    starts_at: '2026-06-06T04:39:05.088Z',
    expires_at: '2026-07-10T04:39:05.088Z',
    group: null,
  } as unknown as UserSubscription
}

describe('UserSubscriptionCard lifecycle checkout', () => {
  beforeEach(() => {
    routerPush.mockReset()
  })

  it('routes renew/change-plan checkout to the purchase page with a rounded charge', async () => {
    const wrapper = shallowMount(UserSubscriptionCard, {
      props: {
        subscription: activeSubscriptionFixture(),
      },
      global: {
        stubs: {
          ConfirmDialog: true,
        },
      },
    })

    const changeButton = wrapper.findAll('button').find(button => button.text() === 'userSubscriptions.lifecycle.changeTitle')
    expect(changeButton).toBeTruthy()
    await changeButton!.trigger('click')

    const dialog = wrapper.findComponent({ name: 'SubscriptionLifecycleDialog' })
    expect(dialog.exists()).toBe(true)
    dialog.vm.$emit('purchase', {
      intent: 'change_plan',
      dailyAmountUsd: 60,
      validityDays: 308,
      charge: 72.60000000000001,
    })

    expect(routerPush).toHaveBeenCalledWith({
      path: '/purchase',
      query: {
        tab: 'subscription',
        intent: 'change_plan',
        daily_amount_usd: '60',
        validity_days: '308',
        charge: '72.60',
      },
    })
  })
})

describe('UserSubscriptionCard lifecycle checkout unit price', () => {
  beforeEach(() => {
    routerPush.mockReset()
  })

  async function mountAndOpen() {
    const wrapper = shallowMount(UserSubscriptionCard, {
      props: { subscription: activeSubscriptionFixture() },
      global: { stubs: { ConfirmDialog: true } },
    })
    const changeButton = wrapper.findAll('button').find(button => button.text() === 'userSubscriptions.lifecycle.changeTitle')
    await changeButton!.trigger('click')
    return wrapper
  }

  it('带了报价单价就写进结账 query，供结账页把每日额度写成人民币', async () => {
    const wrapper = await mountAndOpen()
    wrapper.findComponent({ name: 'SubscriptionLifecycleDialog' }).vm.$emit('purchase', {
      intent: 'renew',
      dailyAmountUsd: 90,
      validityDays: 30,
      charge: 121.5,
      unitPrice: 0.045,
    })

    expect(routerPush).toHaveBeenCalledWith({
      path: '/purchase',
      query: {
        tab: 'subscription',
        intent: 'renew',
        daily_amount_usd: '90',
        validity_days: '30',
        charge: '121.50',
        unit_price: '0.045',
      },
    })
  })

  it('单价缺失或为 0 时不带 unit_price', async () => {
    const wrapper = await mountAndOpen()
    wrapper.findComponent({ name: 'SubscriptionLifecycleDialog' }).vm.$emit('purchase', {
      intent: 'renew',
      dailyAmountUsd: 90,
      validityDays: 30,
      charge: 121.5,
      unitPrice: 0,
    })

    const query = routerPush.mock.calls[0][0].query
    expect(query).not.toHaveProperty('unit_price')
  })
})

describe('UserSubscriptionCard expiry labels', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date(2026, 6, 30, 9, 0))
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  function mountWithExpiry(expiresAt: string) {
    return shallowMount(UserSubscriptionCard, {
      props: {
        subscription: { ...activeSubscriptionFixture(), expires_at: expiresAt },
      },
      global: {
        stubs: {
          ConfirmDialog: true,
        },
      },
    })
  }

  it('labels a same-day expiry under 24 hours away as today, not tomorrow', () => {
    const text = mountWithExpiry(new Date(2026, 6, 30, 18, 0).toISOString()).text()

    expect(text).toContain('common.today')
    expect(text).not.toContain('common.tomorrow')
  })

  it('labels a next-calendar-day expiry as tomorrow', () => {
    const text = mountWithExpiry(new Date(2026, 6, 31, 8, 0).toISOString()).text()

    expect(text).toContain('common.tomorrow')
  })

  it('labels an expiry that elapsed less than a day ago as expired', () => {
    const wrapper = mountWithExpiry(new Date(2026, 6, 30, 8, 0).toISOString())

    expect(wrapper.text()).toContain('userSubscriptions.status.expired')
    expect(wrapper.text()).not.toContain('common.today')
    expect(wrapper.find('span.font-medium.text-primary-700').exists()).toBe(true)
  })

  it('shows remaining days for expiries further out', () => {
    const text = mountWithExpiry(new Date(2026, 7, 4, 9, 0).toISOString()).text()

    expect(text).toContain('userSubscriptions.daysRemaining')
    expect(text).not.toContain('common.today')
    expect(text).not.toContain('common.tomorrow')
  })

  it('renders no expiry label for an invalid expiry timestamp', () => {
    const text = mountWithExpiry('not-a-date').text()

    expect(text).not.toContain('userSubscriptions.status.expired')
    expect(text).not.toContain('common.today')
    expect(text).not.toContain('common.tomorrow')
  })
})

describe('UserSubscriptionCard payment gating', () => {
  function mountActive() {
    return shallowMount(UserSubscriptionCard, {
      props: { subscription: activeSubscriptionFixture() },
      global: { stubs: { ConfirmDialog: true } },
    })
  }

  function buttonLabels(wrapper: ReturnType<typeof mountActive>) {
    return wrapper.findAll('button').map(button => button.text())
  }

  it('shows renew and change-plan while public settings are unknown', () => {
    const labels = buttonLabels(mountActive())

    expect(labels).toContain('payment.renewNow')
    expect(labels).toContain('userSubscriptions.lifecycle.changeTitle')
  })

  it('shows renew and change-plan when payment is enabled', () => {
    useRealAppStore().cachedPublicSettings = { payment_enabled: true } as PublicSettings
    const labels = buttonLabels(mountActive())

    expect(labels).toContain('payment.renewNow')
    expect(labels).toContain('userSubscriptions.lifecycle.changeTitle')
  })

  it('hides renew and change-plan when payment is disabled, keeping the card usable', () => {
    useRealAppStore().cachedPublicSettings = { payment_enabled: false } as PublicSettings
    const wrapper = mountActive()
    const labels = buttonLabels(wrapper)

    expect(labels).not.toContain('payment.renewNow')
    expect(labels).not.toContain('userSubscriptions.lifecycle.changeTitle')
    expect(wrapper.text()).toContain('userSubscriptions.status.active')
  })
})

// 组件里用到的金额口径依赖 app store；未配置充值倍率时按美元展示（旧行为）。
beforeEach(() => {
  setActivePinia(createPinia())
})
