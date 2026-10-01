import { mount, shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import UserSubscriptionCard from '../UserSubscriptionCard.vue'
import SubscriptionProgressMini from '@/components/common/SubscriptionProgressMini.vue'
import { resetFiatDataMissingForTest } from '@/composables/useCurrencyDisplay'
import type { UserSubscription } from '@/types'

// 可变的假设置：改它就能模拟 codex 站（倍率 13）和 free 站（倍率 1）。
const publicSettings: { value: Record<string, unknown> } = { value: {} }
const subscriptionStore = vi.hoisted(() => ({
  activeSubscriptions: [] as unknown[],
  hasActiveSubscriptions: true,
  fetchActiveSubscriptions: vi.fn().mockResolvedValue(undefined)
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    get cachedPublicSettings() {
      return publicSettings.value
    }
  })
}))
vi.mock('@/stores', () => ({
  useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
  useSubscriptionStore: () => subscriptionStore
}))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})
vi.mock('@/api/subscriptions', () => ({
  default: {
    borrowOverdraftDay: vi.fn()
  }
}))

// 去掉千分位和空白，方便断言金额。
function plain(text: string): string {
  return text.replace(/\s+/g, '')
}

function cardFixture(overrides: Partial<UserSubscription> = {}): UserSubscription {
  return {
    id: 1,
    user_id: 1,
    status: 'active',
    daily_amount_usd: 60,
    daily_limit_usd: 60,
    weekly_limit_usd: 420,
    monthly_limit_usd: 1800,
    daily_usage_usd: 20,
    weekly_usage_usd: 100,
    monthly_usage_usd: 300,
    starts_at: '2026-06-06T04:39:05.088Z',
    expires_at: '2099-07-10T04:39:05.088Z',
    group: null,
    ...overrides
  } as unknown as UserSubscription
}

function mountCard(subscription: UserSubscription) {
  return shallowMount(UserSubscriptionCard, {
    props: { subscription },
    global: { stubs: { ConfirmDialog: true, NumText: false } }
  })
}

beforeEach(() => {
  window.localStorage.clear()
  resetFiatDataMissingForTest()
  publicSettings.value = { balance_recharge_multiplier: 13 }
})

describe('订阅卡片按人民币展示', () => {
  it('每个窗口的已用和上限都按这张卡自己的单价折算，且不出现 $', () => {
    const text = plain(mountCard(cardFixture({ fiat_per_credit: 0.045 } as Partial<UserSubscription>)).text())

    // 每日 20 × 0.045 = 0.90，上限 60 × 0.045 = 2.70；每周 100 × 0.045 = 4.50 / 420 × 0.045 = 18.90。
    expect(text).toContain('¥0.90/¥2.70')
    expect(text).toContain('¥4.50/¥18.90')
    expect(text).toContain('¥13.50/¥81.00')
    expect(text).not.toContain('$')
  })

  it('拿不到这张卡的单价时只回落到美元，不按钱包单价去猜', () => {
    const text = plain(mountCard(cardFixture()).text())

    expect(text).toContain('$20.00/$60.00')
    expect(text).not.toContain('¥')
  })

  it('free 站（倍率 1）保持美元，即使卡上带了单价', () => {
    publicSettings.value = { balance_recharge_multiplier: 1 }
    const text = plain(mountCard(cardFixture({ fiat_per_credit: 0.045 } as Partial<UserSubscription>)).text())

    expect(text).toContain('$20.00/$60.00')
    expect(text).not.toContain('¥')
  })
})

describe('迷你订阅进度', () => {
  it('人民币模式按卡单价折算，不限额的窗口不带 $', async () => {
    subscriptionStore.activeSubscriptions = [
      cardFixture({ fiat_per_credit: 0.045, weekly_limit_usd: null } as Partial<UserSubscription>)
    ]
    const wrapper = mount(SubscriptionProgressMini, { global: { stubs: { Icon: true, RouterLink: true } } })
    await wrapper.get('button').trigger('click')
    const text = plain(wrapper.text())

    expect(text).toContain('¥0.90/¥2.70')
    expect(text).not.toContain('$')
  })

  it('free 站保持美元', async () => {
    publicSettings.value = { balance_recharge_multiplier: 1 }
    subscriptionStore.activeSubscriptions = [cardFixture({ fiat_per_credit: 0.045 } as Partial<UserSubscription>)]
    const wrapper = mount(SubscriptionProgressMini, { global: { stubs: { Icon: true, RouterLink: true } } })
    await wrapper.get('button').trigger('click')

    expect(plain(wrapper.text())).toContain('$20.00/$60.00')
  })
})
