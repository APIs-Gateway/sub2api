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

const REDIRECT_CASES: Array<[string, string, string]> = [
  ['站内路径原样保留', '/keys', '/keys'],
  ['带查询串的站内路径', '/usage?model=gpt-5&range=7d', '/usage?model=gpt-5&range=7d'],
  ['协议相对地址', '//evil.com', '/dashboard'],
  ['反斜杠', '/\\evil.com', '/dashboard'],
  ['绝对地址', 'https://evil.com', '/dashboard'],
  ['脚本协议', 'javascript:alert(1)', '/dashboard'],
  ['编码的双斜杠', '/%2F%2Fevil.com', '/dashboard'],
  ['编码的反斜杠', '/%5Cevil.com', '/dashboard'],
  ['多重编码', '/%252F%252Fevil.com', '/dashboard'],
  ['Tab 夹在斜杠之间', '/\t/evil.com', '/dashboard'],
  ['点段', '/.//evil.com', '/dashboard'],
]

describe('DingTalkCallbackView 的 redirect 校验', () => {
  const stubs = {
    AuthLayout: { template: '<div><slot /></div>' },
    Icon: true,
    RouterLink: { template: '<a><slot /></a>' },
    transition: false
  }

  beforeEach(() => {
    replace.mockReset()
    setToken.mockReset()
    exchangePendingOAuthCompletion.mockReset()
    setToken.mockResolvedValue({})
    window.location.hash = ''
    localStorage.clear()
    sessionStorage.clear()
  })

  it.each(REDIRECT_CASES)('回调带 token 的 fragment：%s', async (_name, redirect, expected) => {
    window.location.hash = `#access_token=legacy-access-token&redirect=${encodeURIComponent(redirect)}`

    mount(DingTalkCallbackView, { global: { stubs } })
    await flushPromises()

    expect(setToken).toHaveBeenCalledWith('legacy-access-token')
    expect(replace).toHaveBeenCalledTimes(1)
    expect(replace).toHaveBeenCalledWith(expected)
  })

  it.each(REDIRECT_CASES)('后端换回登录结果里的 redirect：%s', async (_name, redirect, expected) => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_in: 3600,
      redirect
    })

    mount(DingTalkCallbackView, { global: { stubs } })
    await flushPromises()

    expect(setToken).toHaveBeenCalledWith('access-token')
    expect(replace).toHaveBeenCalledTimes(1)
    expect(replace).toHaveBeenCalledWith(expected)
  })
})
