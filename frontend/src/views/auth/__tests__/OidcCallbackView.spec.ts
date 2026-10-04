import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import OidcCallbackView from '../OidcCallbackView.vue'

const replace = vi.fn()
const showSuccess = vi.fn()
const showError = vi.fn()
const setToken = vi.fn()
const setPendingAuthSession = vi.fn()
const clearPendingAuthSession = vi.fn()
const exchangePendingOAuthCompletion = vi.fn()
const completeOIDCOAuthRegistration = vi.fn()
const getPublicSettings = vi.fn()
const login2FA = vi.fn()
const apiClientPost = vi.fn()
const sendVerifyCode = vi.fn()
const sendPendingOAuthVerifyCode = vi.fn()

vi.mock('vue-router', () => ({
  useRoute: () => ({
    query: {}
  }),
  useRouter: () => ({
    replace
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string>) => {
        if (key === 'auth.oauthFlow.totpHint') {
          return `verify ${params?.account ?? ''}`.trim()
        }
        if (!params?.providerName) {
          return key
        }
        return `${key}:${params.providerName}`
      }
    })
  }
})

vi.mock('@/stores', () => ({
  useAuthStore: () => ({
    authSessionVersion: 0,
    setToken,
    setPendingAuthSession,
    clearPendingAuthSession
  }),
  useAppStore: () => ({
    showSuccess,
    showError
  })
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    post: (...args: any[]) => apiClientPost(...args)
  }
}))

vi.mock('@/api/auth', async () => {
  const actual = await vi.importActual<typeof import('@/api/auth')>('@/api/auth')
  return {
    ...actual,
    exchangePendingOAuthCompletion: (...args: any[]) => exchangePendingOAuthCompletion(...args),
    completeOIDCOAuthRegistration: (...args: any[]) => completeOIDCOAuthRegistration(...args),
    getPublicSettings: (...args: any[]) => getPublicSettings(...args),
    login2FA: (...args: any[]) => login2FA(...args),
    sendVerifyCode: (...args: any[]) => sendVerifyCode(...args),
    sendPendingOAuthVerifyCode: (...args: any[]) => sendPendingOAuthVerifyCode(...args)
  }
})

describe('OidcCallbackView', () => {
  beforeEach(() => {
    replace.mockReset()
    showSuccess.mockReset()
    showError.mockReset()
    setToken.mockReset()
    setPendingAuthSession.mockReset()
    clearPendingAuthSession.mockReset()
    exchangePendingOAuthCompletion.mockReset()
    completeOIDCOAuthRegistration.mockReset()
    getPublicSettings.mockReset()
    login2FA.mockReset()
    apiClientPost.mockReset()
    sendVerifyCode.mockReset()
    sendPendingOAuthVerifyCode.mockReset()
    getPublicSettings.mockResolvedValue({
      oidc_oauth_provider_name: 'ExampleID',
      turnstile_enabled: false,
      turnstile_site_key: ''
    })
    window.location.hash = ''
    localStorage.clear()
    sessionStorage.clear()
  })

  it('accepts the legacy fragment token success callback without pending-session exchange', async () => {
    window.location.hash =
      '#access_token=legacy-access-token&refresh_token=legacy-refresh-token&expires_in=3600&token_type=Bearer&redirect=%2Flegacy-dashboard'
    setToken.mockResolvedValue({})

    mount(OidcCallbackView, {
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

    expect(exchangePendingOAuthCompletion).not.toHaveBeenCalled()
    expect(setToken).toHaveBeenCalledWith('legacy-access-token')
    expect(localStorage.getItem('refresh_token')).toBe('legacy-refresh-token')
    expect(localStorage.getItem('token_expires_at')).not.toBeNull()
    expect(showSuccess).toHaveBeenCalledWith('auth.loginSuccess')
    expect(replace).toHaveBeenCalledWith('/legacy-dashboard')
  })

  it('accepts the legacy pending oauth invitation fragment without pending-session exchange', async () => {
    window.location.hash = '#error=invitation_required&pending_oauth_token=legacy-pending-token&redirect=%2Flegacy-invite'
    apiClientPost.mockResolvedValue({
      data: {
        access_token: 'legacy-access-token',
        refresh_token: 'legacy-refresh-token',
        expires_in: 3600,
        token_type: 'Bearer'
      }
    })
    setToken.mockResolvedValue({})

    const wrapper = mount(OidcCallbackView, {
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

    expect(exchangePendingOAuthCompletion).not.toHaveBeenCalled()
    await wrapper.find('input[type="text"]').setValue('invite-code')
    await wrapper.find('button').trigger('click')
    await flushPromises()

    expect(apiClientPost).toHaveBeenCalledWith('/auth/oauth/oidc/complete-registration', {
      adopt_display_name: true,
      adopt_avatar: true,
      pending_oauth_token: 'legacy-pending-token',
      invitation_code: 'invite-code'
    })
    expect(setToken).toHaveBeenCalledWith('legacy-access-token')
    expect(replace).toHaveBeenCalledWith('/legacy-invite')
  })

  it('does not send adoption decisions during the initial exchange', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_in: 3600,
      redirect: '/dashboard',
      adoption_required: true
    })
    setToken.mockResolvedValue({})

    mount(OidcCallbackView, {
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

    expect(exchangePendingOAuthCompletion).toHaveBeenCalledTimes(1)
    expect(exchangePendingOAuthCompletion).toHaveBeenCalledWith()
  })

  it('waits for explicit adoption confirmation before finishing a non-invitation login', async () => {
    exchangePendingOAuthCompletion
      .mockResolvedValueOnce({
        redirect: '/dashboard',
        adoption_required: true,
        suggested_display_name: 'OIDC Nick',
        suggested_avatar_url: 'https://cdn.example/oidc.png'
      })
      .mockResolvedValueOnce({
        access_token: 'access-token',
        refresh_token: 'refresh-token',
        expires_in: 3600,
        redirect: '/dashboard'
      })
    setToken.mockResolvedValue({})

    const wrapper = mount(OidcCallbackView, {
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

    expect(wrapper.text()).toContain('OIDC Nick')
    expect(setToken).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()

    const checkboxes = wrapper.findAll('input[type="checkbox"]')
    await checkboxes[0].setValue(false)

    await wrapper.findAll('button')[0].trigger('click')
    await flushPromises()

    expect(exchangePendingOAuthCompletion).toHaveBeenCalledTimes(2)
    expect(exchangePendingOAuthCompletion).toHaveBeenNthCalledWith(1)
    expect(exchangePendingOAuthCompletion).toHaveBeenNthCalledWith(2, {
      adoptDisplayName: false,
      adoptAvatar: true
    })
    expect(setToken).toHaveBeenCalledWith('access-token')
    expect(replace).toHaveBeenCalledWith('/dashboard')
  })

  it('supports bind completion after adoption confirmation', async () => {
    exchangePendingOAuthCompletion
      .mockResolvedValueOnce({
        redirect: '/dashboard',
        adoption_required: true,
        suggested_display_name: 'OIDC Nick',
        suggested_avatar_url: 'https://cdn.example/oidc.png'
      })
      .mockResolvedValueOnce({
        redirect: '/profile'
      })

    const wrapper = mount(OidcCallbackView, {
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

    await wrapper.findAll('button')[0].trigger('click')
    await flushPromises()

    expect(exchangePendingOAuthCompletion).toHaveBeenNthCalledWith(2, {
      adoptDisplayName: true,
      adoptAvatar: true
    })
    expect(setToken).not.toHaveBeenCalled()
    expect(showSuccess).toHaveBeenCalledWith('profile.authBindings.bindSuccess')
    expect(replace).toHaveBeenCalledWith('/profile')
  })

  it('keeps rendering pending bind-login UI when adoption confirmation leads to another pending step', async () => {
    exchangePendingOAuthCompletion
      .mockResolvedValueOnce({
        redirect: '/profile',
        adoption_required: true,
        suggested_display_name: 'OIDC Nick',
        suggested_avatar_url: 'https://cdn.example/oidc.png'
      })
      .mockResolvedValueOnce({
        step: 'bind_login_required',
        redirect: '/profile',
        email: 'existing@example.com',
        adoption_required: true,
        suggested_display_name: 'OIDC Nick',
        suggested_avatar_url: 'https://cdn.example/oidc.png'
      })

    const wrapper = mount(OidcCallbackView, {
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
    await wrapper.findAll('button')[0].trigger('click')
    await flushPromises()

    expect(showSuccess).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
    expect((wrapper.get('[data-testid="oidc-bind-login-email"]').element as HTMLInputElement).value).toBe(
      'existing@example.com'
    )
  })

  it('persists a pending auth session when the oauth flow still needs account creation', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      error: 'email_required',
      redirect: '/welcome'
    })

    mount(OidcCallbackView, {
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

    expect(setPendingAuthSession).toHaveBeenCalledWith({
      token: '',
      token_field: 'pending_oauth_token',
      provider: 'oidc',
      redirect: '/welcome'
    })
  })

  it('renders adoption choices for invitation flow and submits the selected values', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      error: 'invitation_required',
      redirect: '/dashboard',
      adoption_required: true,
      suggested_display_name: 'OIDC Nick',
      suggested_avatar_url: 'https://cdn.example/oidc.png'
    })
    completeOIDCOAuthRegistration.mockResolvedValue({
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_in: 3600,
      token_type: 'Bearer'
    })
    setToken.mockResolvedValue({})

    const wrapper = mount(OidcCallbackView, {
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

    const checkboxes = wrapper.findAll('input[type="checkbox"]')
    expect(checkboxes).toHaveLength(2)
    await checkboxes[1].setValue(false)
    await wrapper.find('input[type="text"]').setValue('invite-code')
    await wrapper.find('button').trigger('click')

    expect(completeOIDCOAuthRegistration).toHaveBeenCalledWith('invite-code', {
      adoptDisplayName: true,
      adoptAvatar: false
    })
  })

  it('keeps the oauth flow active when complete-registration returns another pending step', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      error: 'invitation_required',
      redirect: '/dashboard',
      adoption_required: true,
      suggested_display_name: 'OIDC Nick',
      suggested_avatar_url: 'https://cdn.example/oidc.png'
    })
    completeOIDCOAuthRegistration.mockResolvedValue({
      auth_result: 'pending_session',
      step: 'choose_account_action_required',
      redirect: '/dashboard',
      email: 'fresh@example.com',
      resolved_email: 'fresh@example.com',
      force_email_on_signup: true,
      adoption_required: true
    })

    const wrapper = mount(OidcCallbackView, {
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
    await wrapper.find('input[type="text"]').setValue('invite-code')
    await wrapper.find('button').trigger('click')
    await flushPromises()

    expect(completeOIDCOAuthRegistration).toHaveBeenCalledWith('invite-code', {
      adoptDisplayName: true,
      adoptAvatar: true
    })
    expect(setToken).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('auth.oauthFlow.bindExistingAccount')
    expect(wrapper.text()).toContain('auth.oauthFlow.createNewAccount')
  })

  it('collects email, password, and verify code for pending oauth account creation and submits adoption decisions', async () => {
    getPublicSettings.mockResolvedValue({
      oidc_oauth_provider_name: 'ExampleID',
      invitation_code_enabled: true,
      turnstile_enabled: false,
      turnstile_site_key: ''
    })
    exchangePendingOAuthCompletion.mockResolvedValue({
      error: 'email_required',
      redirect: '/welcome',
      adoption_required: true,
      suggested_display_name: 'OIDC Nick',
      suggested_avatar_url: 'https://cdn.example/oidc.png'
    })
    apiClientPost.mockResolvedValue({
      data: {
        access_token: 'new-access-token',
        refresh_token: 'new-refresh-token',
        expires_in: 3600,
        token_type: 'Bearer'
      }
    })
    setToken.mockResolvedValue({})

    const wrapper = mount(OidcCallbackView, {
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

    const checkboxes = wrapper.findAll('input[type="checkbox"]')
    expect(checkboxes).toHaveLength(2)
    await checkboxes[1].setValue(false)
    await wrapper.get('[data-testid="oidc-create-account-email"]').setValue('  new@example.com  ')
    await wrapper.get('[data-testid="oidc-create-account-password"]').setValue('secret-123')
    await wrapper.get('[data-testid="oidc-create-account-verify-code"]').setValue('246810')
    await wrapper.get('[data-testid="oidc-create-account-invitation-code"]').setValue(' INVITE123 ')
    await wrapper.get('[data-testid="oidc-create-account-submit"]').trigger('click')
    await flushPromises()

    expect(apiClientPost).toHaveBeenCalledWith('/auth/oauth/pending/create-account', {
      email: 'new@example.com',
      password: 'secret-123',
      verify_code: '246810',
      invitation_code: 'INVITE123',
      adopt_display_name: true,
      adopt_avatar: false
    })
    expect(setToken).toHaveBeenCalledWith('new-access-token')
    expect(replace).toHaveBeenCalledWith('/welcome')
  })

  it('switches to bind-login when create-account returns EMAIL_EXISTS', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      error: 'email_required',
      redirect: '/welcome'
    })
    apiClientPost.mockRejectedValue({
      response: {
        data: {
          reason: 'EMAIL_EXISTS',
          message: 'email already exists'
        }
      }
    })

    const wrapper = mount(OidcCallbackView, {
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
    await wrapper.get('[data-testid="oidc-create-account-email"]').setValue('existing@example.com')
    await wrapper.get('[data-testid="oidc-create-account-password"]').setValue('secret-123')
    await wrapper.get('[data-testid="oidc-create-account-submit"]').trigger('click')
    await flushPromises()

    expect((wrapper.get('[data-testid="oidc-bind-login-email"]').element as HTMLInputElement).value).toBe(
      'existing@example.com'
    )
  })

  it('shows create-account failures through toast without inline error text', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      error: 'email_required',
      redirect: '/welcome'
    })
    apiClientPost.mockRejectedValue(new Error('create failed'))

    const wrapper = mount(OidcCallbackView, {
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
    await wrapper.get('[data-testid="oidc-create-account-email"]').setValue('new@example.com')
    await wrapper.get('[data-testid="oidc-create-account-password"]').setValue('secret-123')
    await wrapper.get('[data-testid="oidc-create-account-submit"]').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('create failed')
    expect(wrapper.text()).not.toContain('create failed')
  })

  it('sends a verify code for pending oauth account creation', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      error: 'email_required',
      redirect: '/welcome'
    })
    sendPendingOAuthVerifyCode.mockResolvedValue({
      message: 'sent',
      countdown: 60
    })

    const wrapper = mount(OidcCallbackView, {
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

    await wrapper.get('[data-testid="oidc-create-account-email"]').setValue('  new@example.com  ')
    await wrapper.get('[data-testid="oidc-create-account-send-code"]').trigger('click')
    await flushPromises()

    expect(sendPendingOAuthVerifyCode).toHaveBeenCalledWith({
      email: 'new@example.com'
    })
  })

  it('shows bind-login form for existing account binding and submits credentials with adoption decisions', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      error: 'adopt_existing_user_by_email',
      redirect: '/profile/security',
      email: 'existing@example.com',
      adoption_required: true,
      suggested_display_name: 'OIDC Nick',
      suggested_avatar_url: 'https://cdn.example/oidc.png'
    })
    apiClientPost.mockResolvedValue({
      data: {
        access_token: 'bind-access-token',
        refresh_token: 'bind-refresh-token',
        expires_in: 3600,
        token_type: 'Bearer'
      }
    })
    setToken.mockResolvedValue({})

    const wrapper = mount(OidcCallbackView, {
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

    const checkboxes = wrapper.findAll('input[type="checkbox"]')
    expect(checkboxes).toHaveLength(2)
    await checkboxes[0].setValue(false)
    await wrapper.get('[data-testid="oidc-bind-login-email"]').setValue('existing@example.com')
    await wrapper.get('[data-testid="oidc-bind-login-password"]').setValue('secret-password')
    await wrapper.get('[data-testid="oidc-bind-login-submit"]').trigger('click')
    await flushPromises()

    expect(apiClientPost).toHaveBeenCalledWith('/auth/oauth/pending/bind-login', {
      email: 'existing@example.com',
      password: 'secret-password',
      adopt_display_name: false,
      adopt_avatar: true
    })
    expect(setToken).toHaveBeenCalledWith('bind-access-token')
    expect(replace).toHaveBeenCalledWith('/profile/security')
  })

  it('handles bind-login 2FA challenge before redirecting', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      error: 'adopt_existing_user_by_email',
      redirect: '/profile',
      email: 'existing@example.com',
      adoption_required: true,
      suggested_display_name: 'OIDC Nick',
      suggested_avatar_url: 'https://cdn.example/oidc.png'
    })
    apiClientPost.mockResolvedValue({
      data: {
        requires_2fa: true,
        temp_token: 'temp-123',
        user_email_masked: 'o***g@example.com'
      }
    })
    login2FA.mockResolvedValue({
      access_token: '2fa-access-token',
      refresh_token: '2fa-refresh-token',
      expires_in: 3600
    })
    setToken.mockResolvedValue({})

    const wrapper = mount(OidcCallbackView, {
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

    await wrapper.get('[data-testid="oidc-bind-login-password"]').setValue('secret-password')
    await wrapper.get('[data-testid="oidc-bind-login-submit"]').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('o***g@example.com')
    expect(login2FA).not.toHaveBeenCalled()

    await wrapper.get('[data-testid="oidc-bind-login-totp"]').setValue('123456')
    await wrapper.get('[data-testid="oidc-bind-login-totp-submit"]').trigger('click')
    await flushPromises()

    expect(login2FA).toHaveBeenCalledWith({
      temp_token: 'temp-123',
      totp_code: '123456'
    })
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

describe('OidcCallbackView 的 redirect 校验', () => {
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
    getPublicSettings.mockReset()
    getPublicSettings.mockResolvedValue({ turnstile_enabled: false, turnstile_site_key: '' })
    setToken.mockResolvedValue({})
    window.location.hash = ''
    localStorage.clear()
    sessionStorage.clear()
  })

  it.each(REDIRECT_CASES)('回调带 token 的 fragment：%s', async (_name, redirect, expected) => {
    window.location.hash = `#access_token=legacy-access-token&redirect=${encodeURIComponent(redirect)}`

    mount(OidcCallbackView, { global: { stubs } })
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

    mount(OidcCallbackView, { global: { stubs } })
    await flushPromises()

    expect(setToken).toHaveBeenCalledWith('access-token')
    expect(replace).toHaveBeenCalledTimes(1)
    expect(replace).toHaveBeenCalledWith(expected)
  })
})
