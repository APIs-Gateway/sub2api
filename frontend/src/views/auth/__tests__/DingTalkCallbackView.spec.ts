import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import DingTalkCallbackView from '../DingTalkCallbackView.vue'

const replace = vi.fn()
const setToken = vi.fn()
const exchangePendingOAuthCompletion = vi.fn()
const login2FA = vi.fn()
const apiClientPost = vi.fn()

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ replace })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key, te: () => false })
  }
})

vi.mock('@/stores', () => ({
  useAuthStore: () => ({
    authSessionVersion: 0,
    setToken,
    setPendingAuthSession: vi.fn(),
    clearPendingAuthSession: vi.fn()
  }),
  useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() })
}))

vi.mock('@/api/client', () => ({
  apiClient: { post: (...args: any[]) => apiClientPost(...args) }
}))

vi.mock('@/api/auth', async () => {
  const actual = await vi.importActual<typeof import('@/api/auth')>('@/api/auth')
  return {
    ...actual,
    exchangePendingOAuthCompletion: (...args: any[]) => exchangePendingOAuthCompletion(...args),
    login2FA: (...args: any[]) => login2FA(...args)
  }
})

describe('DingTalkCallbackView', () => {
  beforeEach(() => {
    replace.mockReset()
    setToken.mockReset()
    exchangePendingOAuthCompletion.mockReset()
    login2FA.mockReset()
    apiClientPost.mockReset()
    window.location.hash = ''
    localStorage.clear()
    sessionStorage.clear()
  })

  it('passes the full 2FA token context to the current auth session', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      error: 'bind_login_required', redirect: '/profile', email: 'existing@example.com'
    })
    apiClientPost.mockResolvedValue({
      data: { requires_2fa: true, temp_token: 'temp-123', user_email_masked: 'o***g@example.com' }
    })
    login2FA.mockResolvedValue({
      access_token: '2fa-access-token', refresh_token: '2fa-refresh-token', expires_in: 3600
    })
    setToken.mockResolvedValue({})

    const wrapper = mount(DingTalkCallbackView, {
      global: {
        stubs: {
          AuthLayout: { template: '<div><slot /></div>' },
          Icon: true,
          RouterLink: { template: '<a><slot /></a>' },
          transition: false
        }
      }
    })
    await flushPromises()
    await wrapper.get('[data-testid="dingtalk-bind-login-email"]').setValue('existing@example.com')
    await wrapper.get('[data-testid="dingtalk-bind-login-password"]').setValue('secret-password')
    await wrapper.get('[data-testid="dingtalk-bind-login-submit"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="dingtalk-bind-login-totp"]').setValue('123456')
    await wrapper.get('[data-testid="dingtalk-bind-login-totp-submit"]').trigger('click')
    await flushPromises()

    expect(login2FA).toHaveBeenCalledWith({ temp_token: 'temp-123', totp_code: '123456' })
    expect(setToken).toHaveBeenCalledWith('2fa-access-token', {
      refreshToken: '2fa-refresh-token', expiresIn: 3600, expectedSessionVersion: 0
    })
    expect(replace).toHaveBeenCalledWith('/profile')
  })
})
