import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import UserSubscriptionCard from '@/components/subscription/UserSubscriptionCard.vue'
import subscriptionsAPI from '@/api/subscriptions'
import type { UserSubscription } from '@/types'

const appStoreMock = vi.hoisted(() => ({
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({
    push: vi.fn()
  })
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, number>) =>
        params?.max ? `${key}:${params.max}` : key
    })
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => appStoreMock
}))

vi.mock('@/api/subscriptions', () => ({
  default: {
    setOverdraftDays: vi.fn()
  }
}))

function subscription(overrides: Partial<UserSubscription> = {}): UserSubscription {
  return {
    id: 42,
    user_id: 7,
    group_id: 9,
    status: 'active',
    starts_at: '2026-07-01T00:00:00Z',
    expires_at: '2026-07-31T00:00:00Z',
    daily_usage_usd: 0,
    weekly_usage_usd: 0,
    monthly_usage_usd: 0,
    daily_window_start: null,
    weekly_window_start: null,
    monthly_window_start: null,
    granted_total_usd: 500,
    daily_amount_usd: 100,
    consumed_usd: 100,
    clawed_usd: 0,
    remaining_usd: 400,
    consumption_day: 1,
    max_overdraft_days: null,
    max_overdraft_uses: 5,
    total_overdraft_count: 1,
    remaining_overdraft_uses: 4,
    can_enable_overdraft: true,
    activated_at: '2026-07-01T00:00:00Z',
    created_at: '2026-07-01T00:00:00Z',
    updated_at: '2026-07-01T00:00:00Z',
    group: {
      id: 9,
      name: 'Pro',
      platform: 'anthropic',
      status: 'active',
      rate_multiplier: 1,
      rate_multipliers: [],
      allowed_models: [],
      blocked_models: [],
      claude_code_only: false,
      fallback_group_id: null,
      openai_messages_enabled: false,
      openai_messages_allowed_models: [],
      rpm_limit: 0,
      rpm_mode: 'fixed',
      rpm_overrides: [],
      model_rate_multipliers: [],
      created_at: '2026-07-01T00:00:00Z',
      updated_at: '2026-07-01T00:00:00Z'
    },
    ...overrides
  }
}

function mountCard(overrides: Partial<UserSubscription> = {}) {
  return mount(UserSubscriptionCard, {
    props: {
      subscription: subscription(overrides)
    }
  })
}

describe('UserSubscriptionCard overdraft setting', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders max from remaining overdraft uses', () => {
    const wrapper = mountCard({ max_overdraft_uses: 5 })
    const input = wrapper.get('input[type="number"]')

    expect(input.attributes('max')).toBe('5')
  })

  it('treats zero as off and sends null', async () => {
    const wrapper = mountCard()

    await wrapper.get('input[type="number"]').setValue('0')
    await wrapper.get('button.btn-secondary').trigger('click')

    expect(subscriptionsAPI.setOverdraftDays).toHaveBeenCalledWith(42, null)
    expect(appStoreMock.showSuccess).toHaveBeenCalledWith('userSubscriptions.overdraft.saved')
    expect(wrapper.emitted('saved')).toHaveLength(1)
  })

  it('rejects non-integer and above-limit values before calling API', async () => {
    const wrapper = mountCard()

    await wrapper.get('input[type="number"]').setValue('6')
    await wrapper.get('button.btn-secondary').trigger('click')

    expect(subscriptionsAPI.setOverdraftDays).not.toHaveBeenCalled()
    expect(appStoreMock.showError).toHaveBeenCalledWith('userSubscriptions.overdraft.invalid:5')

    appStoreMock.showError.mockClear()
    await wrapper.get('input[type="number"]').setValue('1.5')
    await wrapper.get('button.btn-secondary').trigger('click')

    expect(subscriptionsAPI.setOverdraftDays).not.toHaveBeenCalled()
    expect(appStoreMock.showError).toHaveBeenCalledWith('userSubscriptions.overdraft.invalid:5')
  })

  it('saves a valid enabled depth', async () => {
    const wrapper = mountCard()

    await wrapper.get('input[type="number"]').setValue('3')
    await wrapper.get('button.btn-secondary').trigger('click')

    expect(subscriptionsAPI.setOverdraftDays).toHaveBeenCalledWith(42, 3)
  })
})
