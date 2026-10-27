import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'

import ProfileBalanceNotifyCard from '../ProfileBalanceNotifyCard.vue'
import ProfileInfoCard from '../ProfileInfoCard.vue'
import { resetFiatDataMissingForTest, useCurrencyDisplay } from '@/composables/useCurrencyDisplay'
import type { User } from '@/types'

// 个人资料页按人民币展示：账户余额、余额不足提醒的阈值。阈值在后端始终是余额额度，提交前换算回来。

const updateProfile = vi.hoisted(() => vi.fn())
// 可变的假设置：改它就能模拟 codex 站（倍率 13）和 free 站（倍率 1）。
const publicSettings = vi.hoisted(() => ({ value: {} as Record<string, unknown> }))

vi.mock('@/api', () => ({ userAPI: { updateProfile } }))
vi.mock('vue-router', () => ({ useRoute: () => ({ fullPath: '/profile' }) }))
const authStore = reactive({
  user: null,
  profileRefreshVersion: 0,
  authSessionVersion: 0,
  applyUserProfile: vi.fn(),
  invalidateUserRefresh: vi.fn(),
  refreshUser: vi.fn(),
})
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authStore }))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess: vi.fn(),
    showError: vi.fn(),
    get cachedPublicSettings() {
      return publicSettings.value
    },
  }),
}))
vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}${JSON.stringify(params)}` : key) }),
  }
})

function user(overrides: Partial<User> = {}): User {
  return {
    id: 5,
    username: 'alice',
    email: 'alice@example.com',
    avatar_url: null,
    role: 'user',
    balance: 650,
    concurrency: 2,
    status: 'active',
    allowed_groups: null,
    balance_notify_enabled: true,
    balance_notify_threshold: null,
    balance_notify_extra_emails: [],
    created_at: '2026-04-20T00:00:00Z',
    updated_at: '2026-04-20T00:00:00Z',
    ...overrides,
  }
}

function notifyCard(props: Record<string, unknown> = {}) {
  return mount(ProfileBalanceNotifyCard, {
    props: { enabled: true, threshold: null, systemDefaultThreshold: 0, userEmail: '', extraEmails: [], ...props },
  })
}

beforeEach(() => {
  window.localStorage.clear()
  resetFiatDataMissingForTest()
  useCurrencyDisplay().setMode('fiat')
  publicSettings.value = { balance_recharge_multiplier: 13 }
  updateProfile.mockReset().mockResolvedValue({})
  authStore.authSessionVersion = 0
})

describe('个人资料页的账户余额', () => {
  function mountInfo() {
    return mount(ProfileInfoCard, {
      props: { user: user(), linuxdoEnabled: false, oidcEnabled: false, oidcProviderName: 'OIDC' } as never,
      global: { stubs: { Icon: true, NumText: false } },
    })
  }

  it('人民币模式下按充值倍率折回人民币，不显示 $', () => {
    const text = mountInfo().get('[data-testid="profile-overview-metric-balance"]').text().replace(/\s+/g, '')

    // 650 个额度 ÷ 13 = ¥50.00。
    expect(text).toContain('¥50.00')
    expect(text).not.toContain('$')
  })

  it('free 站（倍率 1）保持美元', () => {
    publicSettings.value = { balance_recharge_multiplier: 1 }
    const text = mountInfo().get('[data-testid="profile-overview-metric-balance"]').text().replace(/\s+/g, '')

    expect(text).toContain('$650.00')
  })
})

describe('余额不足提醒的阈值', () => {
  it('人民币模式下输入框前缀是 ¥，回显按倍率折算，系统默认值也是人民币', () => {
    const wrapper = notifyCard({ threshold: 6.5, systemDefaultThreshold: 13 })
    const input = wrapper.get('input[type="number"]')

    expect((input.element as HTMLInputElement).value).toBe('0.5')
    expect(wrapper.text()).toContain('¥')
    expect(wrapper.text()).not.toContain('$')
    expect(input.attributes('placeholder')).toContain('¥1.00')
  })

  it('没改动阈值直接保存，原样提交原始额度，不经过换算往返', async () => {
    // 0.4615 元 ×13 不是整数额度；没改动时必须原样提交 6。
    const wrapper = notifyCard({ threshold: 6 })
    const saveButton = wrapper.findAll('button').find((b) => b.text() === 'common.save')!
    await saveButton.trigger('click')
    await flushPromises()

    expect(updateProfile).toHaveBeenCalledWith({ balance_notify_threshold: 6 })
  })

  it('填人民币后按充值倍率换算成额度提交', async () => {
    const wrapper = notifyCard()
    await wrapper.get('input[type="number"]').setValue('0.5')
    const saveButton = wrapper.findAll('button').find((b) => b.text() === 'common.save')!
    await saveButton.trigger('click')
    await flushPromises()

    // ¥0.5 × 13 = 6.5 个额度。
    expect(updateProfile).toHaveBeenCalledWith({ balance_notify_threshold: 6.5 })
  })

  it('free 站（倍率 1）保持美元，输入和提交都是额度本身', async () => {
    publicSettings.value = { balance_recharge_multiplier: 1 }
    const wrapper = notifyCard({ systemDefaultThreshold: 5 })
    await wrapper.get('input[type="number"]').setValue('2.5')
    const saveButton = wrapper.findAll('button').find((b) => b.text() === 'common.save')!
    await saveButton.trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('$')
    expect(wrapper.get('input[type="number"]').attributes('placeholder')).toContain('$5.00')
    expect(updateProfile).toHaveBeenCalledWith({ balance_notify_threshold: 2.5 })
  })
})
