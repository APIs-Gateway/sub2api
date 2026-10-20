import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import SubscriptionsView from '@/views/user/SubscriptionsView.vue'
import { useAppStore } from '@/stores/app'
import type { PublicSettings } from '@/types'

const { getMySubscriptions, getCheckoutInfo } = vi.hoisted(() => ({
  getMySubscriptions: vi.fn(),
  getCheckoutInfo: vi.fn(),
}))

vi.mock('@/api/subscriptions', () => ({
  default: { getMySubscriptions },
}))

vi.mock('@/api/payment', () => ({
  paymentAPI: { getCheckoutInfo },
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key, locale: ref('zh') }),
  }
})

function mountView() {
  return mount(SubscriptionsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        Icon: true,
        UserSubscriptionCard: true,
        RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' },
      },
    },
  })
}

function setPublicSettings(settings: Partial<PublicSettings> | null, loaded = settings !== null) {
  const appStore = useAppStore()
  appStore.cachedPublicSettings = settings as PublicSettings | null
  appStore.publicSettingsLoaded = loaded
}

describe('SubscriptionsView empty state', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    getMySubscriptions.mockReset().mockResolvedValue([])
    getCheckoutInfo.mockReset().mockResolvedValue({ data: { methods: {}, subscription_payment_multiplier: 1 } })
  })

  it('points to the purchase page when payment is enabled', async () => {
    setPublicSettings({ payment_enabled: true })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('userSubscriptions.noActiveSubscriptions')
    expect(wrapper.text()).toContain('userSubscriptions.noActiveSubscriptionsDesc')
    expect(wrapper.text()).not.toContain('userSubscriptions.noActiveSubscriptionsNoPaymentDesc')

    const purchase = wrapper.get('[data-testid="subscriptions-empty-purchase"]')
    expect(purchase.attributes('href')).toBe('/purchase?tab=subscription')
    expect(purchase.text()).toBe('subscriptionPurchase.title')

    const redeem = wrapper.get('[data-testid="subscriptions-empty-redeem"]')
    expect(redeem.attributes('href')).toBe('/redeem')
    expect(redeem.classes()).toContain('btn-secondary')
    expect(purchase.classes()).toContain('btn-primary')
  })

  it('shows the purchase entry while public settings are still unknown', async () => {
    setPublicSettings(null)
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-testid="subscriptions-empty-purchase"]').exists()).toBe(true)
  })

  it('hides the purchase entry and uses neutral copy when payment is disabled', async () => {
    setPublicSettings({ payment_enabled: false })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('userSubscriptions.noActiveSubscriptionsNoPaymentDesc')
    expect(wrapper.text()).not.toContain('userSubscriptions.noActiveSubscriptionsDesc')
    expect(wrapper.find('[data-testid="subscriptions-empty-purchase"]').exists()).toBe(false)
    expect(wrapper.find('a[href^="/purchase"]').exists()).toBe(false)

    // 兑换码不依赖支付，支付关闭时仍然给入口，并且成为唯一的主按钮。
    const redeem = wrapper.get('[data-testid="subscriptions-empty-redeem"]')
    expect(redeem.attributes('href')).toBe('/redeem')
    expect(redeem.classes()).toContain('btn-primary')
  })

  it('does not render the empty state when the user has subscriptions', async () => {
    setPublicSettings({ payment_enabled: false })
    getMySubscriptions.mockResolvedValue([
      { id: 1, status: 'active', expires_at: null, group: null },
    ])
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-testid="subscriptions-empty-redeem"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('userSubscriptions.noActiveSubscriptions')
  })
})

// 只剩已过期 / 已撤销订阅的用户到不了上面的空态（subscriptions.length 不为 0），
// 「生效中」下面那条虚线提示就是他们看到的全部，购买入口要放在这里。
describe('SubscriptionsView 只剩已结束订阅时的购买入口', () => {
  const endedOnly = [
    { id: 3, status: 'expired', expires_at: '2026-09-01T00:00:00Z', group: null },
    { id: 4, status: 'revoked', expires_at: '2026-08-01T00:00:00Z', group: null },
  ]

  beforeEach(() => {
    setActivePinia(createPinia())
    getMySubscriptions.mockReset().mockResolvedValue(endedOnly)
    getCheckoutInfo.mockReset().mockResolvedValue({ data: { methods: {}, subscription_payment_multiplier: 1 } })
  })

  it('支付开启时，在「当前没有生效中的订阅」下给出购买订阅入口', async () => {
    setPublicSettings({ payment_enabled: true })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('userSubscriptions.noActiveNow')
    const purchase = wrapper.get('[data-testid="subscriptions-ended-purchase"]')
    expect(purchase.attributes('href')).toBe('/purchase?tab=subscription')
    expect(purchase.text()).toBe('subscriptionPurchase.title')
    expect(purchase.classes()).toContain('btn-primary')
    // 从没有订阅的那套空态不出现，已结束的历史仍然照常展示。
    expect(wrapper.find('[data-testid="subscriptions-empty-purchase"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('userSubscriptions.sectionEnded')
  })

  it('设置还没加载时也给入口（与侧栏同一口径）', async () => {
    setPublicSettings(null)
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-testid="subscriptions-ended-purchase"]').exists()).toBe(true)
  })

  it('支付关闭时不出现购买入口', async () => {
    setPublicSettings({ payment_enabled: false })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('userSubscriptions.noActiveNow')
    expect(wrapper.find('[data-testid="subscriptions-ended-purchase"]').exists()).toBe(false)
    expect(wrapper.find('a[href^="/purchase"]').exists()).toBe(false)
  })

  it('有生效中的订阅时不显示这条提示，也就没有这个入口', async () => {
    setPublicSettings({ payment_enabled: true })
    getMySubscriptions.mockResolvedValue([
      { id: 1, status: 'active', expires_at: null, group: null },
      ...endedOnly,
    ])
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).not.toContain('userSubscriptions.noActiveNow')
    expect(wrapper.find('[data-testid="subscriptions-ended-purchase"]').exists()).toBe(false)
  })
})
