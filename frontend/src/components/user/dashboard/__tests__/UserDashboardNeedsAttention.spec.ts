import { describe, it, expect, vi } from 'vitest'
import { mount } from '@vue/test-utils'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}:${JSON.stringify(params)}` : key,
    }),
  }
})

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn() }),
}))

vi.mock('@/composables/useCurrencyDisplay', () => ({
  useCurrencyDisplay: () => ({
    formatWallet: (v: number) => `$${v.toFixed(2)}`,
  }),
}))

// Auth store: no low-balance threshold so only key-quota alerts are relevant.
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    user: {
      balance: 0,
      balance_notify_threshold: null,
      balance_notify_threshold_type: 'fixed',
    },
  }),
}))

import UserDashboardNeedsAttention from '../UserDashboardNeedsAttention.vue'
import type { ApiKey } from '@/types'

function makeKey(over: Partial<ApiKey> & { id: number }): ApiKey {
  return {
    name: `key-${over.id}`,
    key: `sk-${over.id}`,
    created_at: '',
    quota: 100,
    quota_used: 90,
    status: 'active',
    ...over,
  } as ApiKey
}

const stubs = { Icon: true }

describe('UserDashboardNeedsAttention key quota alerts', () => {
  it('shows a quota alert for an active key at ≥80% usage', () => {
    const wrapper = mount(UserDashboardNeedsAttention, {
      props: { apiKeys: [makeKey({ id: 1, status: 'active', quota: 100, quota_used: 80 })] },
      global: { stubs },
    })
    expect(wrapper.text()).toContain('dashboard.needsAttention.keyNearLimit')
    expect(wrapper.text()).not.toContain('dashboard.needsAttention.allGood')
  })

  it('does not alert on an inactive key even at 100% quota usage', () => {
    const wrapper = mount(UserDashboardNeedsAttention, {
      props: { apiKeys: [makeKey({ id: 2, status: 'inactive', quota: 100, quota_used: 100 })] },
      global: { stubs },
    })
    expect(wrapper.text()).toContain('dashboard.needsAttention.allGood')
    expect(wrapper.text()).not.toContain('dashboard.needsAttention.keyNearLimit')
    expect(wrapper.text()).not.toContain('dashboard.needsAttention.keyExhausted')
  })

  it('does not alert on an expired key even at 100% quota usage', () => {
    const wrapper = mount(UserDashboardNeedsAttention, {
      props: { apiKeys: [makeKey({ id: 3, status: 'expired', quota: 100, quota_used: 100 })] },
      global: { stubs },
    })
    expect(wrapper.text()).toContain('dashboard.needsAttention.allGood')
    expect(wrapper.text()).not.toContain('dashboard.needsAttention.keyExhausted')
  })

  it('does not alert on a quota_exhausted key', () => {
    const wrapper = mount(UserDashboardNeedsAttention, {
      props: { apiKeys: [makeKey({ id: 4, status: 'quota_exhausted', quota: 100, quota_used: 100 })] },
      global: { stubs },
    })
    expect(wrapper.text()).toContain('dashboard.needsAttention.allGood')
    expect(wrapper.text()).not.toContain('dashboard.needsAttention.keyExhausted')
  })

  it('only alerts on active keys when mixed statuses are present', () => {
    const wrapper = mount(UserDashboardNeedsAttention, {
      props: {
        apiKeys: [
          makeKey({ id: 5, status: 'inactive', quota: 100, quota_used: 100 }),
          makeKey({ id: 6, status: 'active',   quota: 100, quota_used: 85  }),
          makeKey({ id: 7, status: 'expired',   quota: 100, quota_used: 100 }),
        ],
      },
      global: { stubs },
    })
    // Only key-6 should produce an alert
    expect(wrapper.text()).toContain('dashboard.needsAttention.keyNearLimit')
    const items = wrapper.findAll('li')
    expect(items).toHaveLength(1)
  })
})
