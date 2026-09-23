import { afterEach, describe, expect, it, vi, beforeEach } from 'vitest'
import { shallowMount } from '@vue/test-utils'
import UserSubscriptionCard from '../UserSubscriptionCard.vue'
import type { UserSubscription } from '@/types'

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
