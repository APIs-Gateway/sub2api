import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import DingTalkCallbackView from '../DingTalkCallbackView.vue'

const replace = vi.fn()
const showSuccess = vi.fn()
const showError = vi.fn()
const setToken = vi.fn()
const setPendingAuthSession = vi.fn()
const clearPendingAuthSession = vi.fn()
const exchangePendingOAuthCompletion = vi.fn()
const apiClientPost = vi.fn()
const authState = { authSessionVersion: 0, setToken, setPendingAuthSession, clearPendingAuthSession }

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ replace })
}))

vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key, te: () => false })
}))

vi.mock('@/stores', () => ({
  useAuthStore: () => authState,
  useAppStore: () => ({ showSuccess, showError })
}))

vi.mock('@/api/client', () => ({
  apiClient: { post: (...args: unknown[]) => apiClientPost(...args) }
}))

vi.mock('@/api/auth', async () => ({
  ...await vi.importActual<typeof import('@/api/auth')>('@/api/auth'),
  exchangePendingOAuthCompletion: (...args: unknown[]) => exchangePendingOAuthCompletion(...args)
}))

const pendingSignup = {
  redirect: '/dashboard',
  synthetic_email: 'dingtalk-test@dingtalk.local',
  adoption_required: false
}

const tokens = {
  access_token: 'test-access-token',
  refresh_token: 'test-refresh-token',
  expires_in: 3600,
  token_type: 'Bearer'
}

const tokenContext = { refreshToken: tokens.refresh_token, expiresIn: tokens.expires_in, expectedSessionVersion: 0 }

const pendingProfileSignup = {
  ...pendingSignup,
  adoption_required: true,
  suggested_display_name: 'Provider Display Name',
  suggested_avatar_url: 'https://cdn.example/avatar.png'
}

function mountCallback() {
  return mount(DingTalkCallbackView, {
    global: {
      stubs: {
        AuthLayout: { template: '<div><slot /></div>' },
        Icon: true,
        RouterLink: { template: '<a><slot /></a>' },
        transition: false
      }
    }
  })
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

function changeToAnotherSession() {
  authState.authSessionVersion++
  localStorage.setItem('auth_token', 'other-account-access')
  localStorage.setItem('refresh_token', 'other-account-refresh')
  localStorage.setItem('token_expires_at', '9876543210000')
  sessionStorage.setItem('oauth_aff_code', 'OTHERREFERRAL')
  setPendingAuthSession.mockClear()
  clearPendingAuthSession.mockClear()
}

function expectOtherSessionPreserved() {
  expect(localStorage.getItem('auth_token')).toBe('other-account-access')
  expect(localStorage.getItem('refresh_token')).toBe('other-account-refresh')
  expect(localStorage.getItem('token_expires_at')).toBe('9876543210000')
  expect(sessionStorage.getItem('oauth_aff_code')).toBe('OTHERREFERRAL')
  expect(setPendingAuthSession).not.toHaveBeenCalled()
  expect(clearPendingAuthSession).not.toHaveBeenCalled()
  expect(showSuccess).not.toHaveBeenCalled()
  expect(showError).not.toHaveBeenCalled()
  expect(replace).not.toHaveBeenCalled()
}

describe('DingTalkCallbackView no-email registration', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    authState.authSessionVersion = 0
    window.location.hash = ''
    localStorage.clear()
    sessionStorage.clear()
    exchangePendingOAuthCompletion.mockResolvedValue({ ...pendingSignup })
    apiClientPost.mockResolvedValue({ data: { ...tokens } })
    setToken.mockResolvedValue({})
  })

  it('completes a new cookie-bound no-email signup and enters the dashboard without email/password input', async () => {
    const wrapper = mountCallback()
    await flushPromises()

    expect(exchangePendingOAuthCompletion).toHaveBeenCalledTimes(1)
    expect(exchangePendingOAuthCompletion).toHaveBeenCalledWith()
    expect(apiClientPost).toHaveBeenCalledTimes(1)
    expect(apiClientPost).toHaveBeenCalledWith('/auth/oauth/dingtalk/complete-registration', {
      adopt_display_name: false,
      adopt_avatar: false
    })
    expect(setToken).toHaveBeenCalledTimes(1)
    expect(setToken).toHaveBeenCalledWith(tokens.access_token, tokenContext)
    expect(localStorage.getItem('refresh_token')).toBeNull()
    expect(localStorage.getItem('token_expires_at')).toBeNull()
    expect(replace).toHaveBeenCalledTimes(1)
    expect(replace).toHaveBeenCalledWith('/dashboard')
    expect(showSuccess).toHaveBeenCalledTimes(1)
    expect(showSuccess).toHaveBeenCalledWith('auth.loginSuccess')
    expect(wrapper.find('input[type="email"]').exists()).toBe(false)
    expect(wrapper.find('input[type="password"]').exists()).toBe(false)
  })

  it('does not register again when an existing account already has login tokens', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({ ...pendingSignup, ...tokens, adoption_required: false })
    mountCallback()
    await flushPromises()

    expect(apiClientPost).not.toHaveBeenCalled()
    expect(setToken).toHaveBeenCalledWith(tokens.access_token)
    expect(replace).toHaveBeenCalledWith('/dashboard')
  })

  it.each([[false, false], [true, false], [false, true]])('shows profile choices before no-email signup and submits the explicit choices %s/%s', async (name, avatar) => {
    exchangePendingOAuthCompletion.mockResolvedValue({ ...pendingProfileSignup })
    const wrapper = mountCallback()
    await flushPromises()

    expect(apiClientPost).not.toHaveBeenCalled()
    expect(setToken).not.toHaveBeenCalled()
    expect(showSuccess).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('auth.oauthFlow.reviewProfileBeforeContinue')
    const choices = wrapper.findAll('input[type="checkbox"]')
    expect(choices).toHaveLength(2)
    await choices[0].setValue(name)
    await choices[1].setValue(avatar)
    await wrapper.find('button').trigger('click')
    await flushPromises()

    expect(exchangePendingOAuthCompletion).toHaveBeenCalledTimes(1)
    expect(apiClientPost).toHaveBeenCalledTimes(1)
    expect(apiClientPost).toHaveBeenCalledWith('/auth/oauth/dingtalk/complete-registration', {
      adopt_display_name: name, adopt_avatar: avatar
    })
    expect(setToken).toHaveBeenCalledWith(tokens.access_token, tokenContext)
    expect(showSuccess).toHaveBeenCalledWith('auth.loginSuccess')
    expect(replace).toHaveBeenCalledWith('/dashboard')
  })

  it('retains declined profile choices through invitation fallback and retry', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({ ...pendingProfileSignup })
    apiClientPost.mockRejectedValueOnce({ reason: 'OAUTH_INVITATION_REQUIRED' })
    const wrapper = mountCallback()
    await flushPromises()
    const choices = wrapper.findAll('input[type="checkbox"]')
    await choices[0].setValue(false)
    await choices[1].setValue(false)
    await wrapper.find('button').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('auth.dingtalk.invitationRequired')
    expect(wrapper.findAll<HTMLInputElement>('input[type="checkbox"]').every(input => !input.element.checked)).toBe(true)
    expect(setToken).not.toHaveBeenCalled()
    expect(showSuccess).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
    await wrapper.find('input[type="text"]').setValue('valid-invite')
    await wrapper.find('button').trigger('click')
    await flushPromises()

    expect(apiClientPost).toHaveBeenCalledTimes(2)
    expect(apiClientPost.mock.calls.every(([, body]) => body.adopt_display_name === false && body.adopt_avatar === false)).toBe(true)
    expect(apiClientPost).toHaveBeenLastCalledWith('/auth/oauth/dingtalk/complete-registration', {
      pending_oauth_token: undefined, invitation_code: 'valid-invite',
      adopt_display_name: false, adopt_avatar: false
    })
    expect(setToken).toHaveBeenCalledWith(tokens.access_token, tokenContext)
    expect(replace).toHaveBeenCalledWith('/dashboard')
  })

  it('does not report bind success when confirmed no-email registration returns no token', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({ ...pendingProfileSignup })
    apiClientPost.mockResolvedValue({ data: {} })
    const wrapper = mountCallback()
    await flushPromises()
    await wrapper.find('button').trigger('click')
    await flushPromises()

    expect(exchangePendingOAuthCompletion).toHaveBeenCalledTimes(1)
    expect(apiClientPost).toHaveBeenCalledTimes(1)
    expect(setToken).not.toHaveBeenCalled()
    expect(showSuccess).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('auth.dingtalk.callbackMissingToken')
  })

  it.each([undefined, '', '   ', 42])('does not initiate signup for an invalid synthetic marker: %s', async marker => {
    exchangePendingOAuthCompletion.mockResolvedValue({ ...pendingSignup, synthetic_email: marker })
    mountCallback()
    await flushPromises()

    expect(apiClientPost).not.toHaveBeenCalled()
    expect(setToken).not.toHaveBeenCalled()
    expect(showSuccess).not.toHaveBeenCalledWith('auth.loginSuccess')
    expect(replace).not.toHaveBeenCalledWith('/dashboard')
  })

  it.each(['bind_login', 'choose_account_action', 'create_account'])('preserves explicit %s account actions instead of auto-registering', async step => {
    exchangePendingOAuthCompletion.mockResolvedValue({ ...pendingSignup, step })
    mountCallback()
    await flushPromises()

    expect(apiClientPost).not.toHaveBeenCalled()
    expect(setToken).not.toHaveBeenCalled()
    expect(setPendingAuthSession).toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
  })

  it('preserves the email-completion flow', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({ ...pendingSignup, step: 'email_completion' })
    mountCallback()
    await flushPromises()

    expect(apiClientPost).not.toHaveBeenCalled()
    expect(setToken).not.toHaveBeenCalled()
    expect(replace).toHaveBeenCalledTimes(1)
    expect(replace).toHaveBeenCalledWith('/auth/dingtalk/email-completion?redirect=%2Fdashboard')
  })

  it('does not bypass invitation requirements', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({ ...pendingSignup, error: 'invitation_required' })
    const wrapper = mountCallback()
    await flushPromises()

    expect(apiClientPost).not.toHaveBeenCalled()
    expect(setToken).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('auth.dingtalk.invitationRequired')
  })

  it('keeps the pending session and lets the user submit an invitation required by the registration endpoint', async () => {
    apiClientPost.mockRejectedValueOnce({
      status: 403,
      reason: 'OAUTH_INVITATION_REQUIRED',
      message: 'invitation code required to complete oauth registration'
    })
    const wrapper = mountCallback()
    await flushPromises()

    expect(wrapper.text()).toContain('auth.dingtalk.invitationRequired')
    expect(clearPendingAuthSession).not.toHaveBeenCalled()
    expect(setPendingAuthSession).toHaveBeenCalledWith(expect.objectContaining({ provider: 'dingtalk' }))
    expect(setToken).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()

    await wrapper.find('input[type="text"]').setValue(' test-invitation ')
    await wrapper.find('button').trigger('click')
    await flushPromises()

    expect(apiClientPost).toHaveBeenCalledTimes(2)
    expect(apiClientPost).toHaveBeenLastCalledWith('/auth/oauth/dingtalk/complete-registration', {
      pending_oauth_token: undefined,
      invitation_code: 'test-invitation',
      adopt_display_name: false,
      adopt_avatar: false
    })
    expect(setToken).toHaveBeenCalledWith(tokens.access_token, tokenContext)
    expect(replace).toHaveBeenCalledWith('/dashboard')
  })

  it('preserves the two-factor challenge without registering or logging in', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({ ...pendingSignup, requires_2fa: true, temp_token: 'test-challenge' })
    mountCallback()
    await flushPromises()

    expect(apiClientPost).not.toHaveBeenCalled()
    expect(setToken).not.toHaveBeenCalled()
    expect(setPendingAuthSession).toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
  })

  it('shows a registration failure and never issues a login success or navigation', async () => {
    apiClientPost.mockRejectedValue({ response: { data: { message: 'Registration rejected' } } })
    mountCallback()
    await flushPromises()

    expect(apiClientPost).toHaveBeenCalledTimes(1)
    expect(setToken).not.toHaveBeenCalled()
    expect(localStorage.getItem('refresh_token')).toBeNull()
    expect(showSuccess).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('Registration rejected')
  })

  it('handles another pending step from the server without repeating signup', async () => {
    apiClientPost.mockResolvedValue({ data: { ...pendingSignup, step: 'bind_login' } })
    const wrapper = mountCallback()
    await flushPromises()

    expect(apiClientPost).toHaveBeenCalledTimes(1)
    expect(setToken).not.toHaveBeenCalled()
    expect(showSuccess).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
    expect(wrapper.find('input[type="password"]').exists()).toBe(true)
    expect(setPendingAuthSession).toHaveBeenCalled()
  })

  it('sanitizes an external redirect after successful registration', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({ ...pendingSignup, redirect: '//outside.example' })
    apiClientPost.mockResolvedValue({ data: { ...tokens, redirect: 'https://outside.example' } })
    mountCallback()
    await flushPromises()

    expect(setToken).toHaveBeenCalledWith(tokens.access_token, tokenContext)
    expect(replace).toHaveBeenCalledTimes(1)
    expect(replace).toHaveBeenCalledWith('/dashboard')
  })

  it.each([{}, { access_token: '' }, { access_token: '   ' }, { access_token: 42 }])('rejects a no-token registration response instead of reporting bind success: %s', async data => {
    apiClientPost.mockResolvedValue({ data })
    mountCallback()
    await flushPromises()

    expect(apiClientPost).toHaveBeenCalledTimes(1)
    expect(setToken).not.toHaveBeenCalled()
    expect(showSuccess).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('auth.dingtalk.callbackMissingToken')
    expect(localStorage.getItem('refresh_token')).toBeNull()
  })

  it('rejects a malformed invitation retry response without bind-success navigation', async () => {
    apiClientPost.mockRejectedValueOnce({ reason: 'OAUTH_INVITATION_REQUIRED' })
    apiClientPost.mockResolvedValueOnce({ data: {} })
    const wrapper = mountCallback()
    await flushPromises()
    await wrapper.find('input[type="text"]').setValue('test-invitation')
    await wrapper.find('button').trigger('click')
    await flushPromises()

    expect(apiClientPost).toHaveBeenCalledTimes(2)
    expect(setToken).not.toHaveBeenCalled()
    expect(showSuccess).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('auth.dingtalk.callbackMissingToken')
  })

  it('keeps an explicit pending session before interpreting its synthetic marker', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({ ...pendingSignup, auth_result: 'pending_session' })
    mountCallback()
    await flushPromises()

    expect(apiClientPost).not.toHaveBeenCalled()
    expect(setToken).not.toHaveBeenCalled()
    expect(setPendingAuthSession).toHaveBeenCalledWith(expect.objectContaining({ provider: 'dingtalk' }))
    expect(showSuccess).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
  })

  it('retains an explicit pending result returned by registration without inventing a successful bind', async () => {
    apiClientPost.mockResolvedValue({ data: { auth_result: 'pending_session', redirect: '/keys' } })
    mountCallback()
    await flushPromises()

    expect(apiClientPost).toHaveBeenCalledTimes(1)
    expect(setPendingAuthSession).toHaveBeenCalledWith(expect.objectContaining({ redirect: '/keys' }))
    expect(setToken).not.toHaveBeenCalled()
    expect(showSuccess).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
  })

  it('preserves a real current-user bind completion with no synthetic signup marker', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({ redirect: '/profile' })
    mountCallback()
    await flushPromises()

    expect(apiClientPost).not.toHaveBeenCalled()
    expect(setToken).not.toHaveBeenCalled()
    expect(showSuccess).toHaveBeenCalledWith('profile.authBindings.bindSuccess')
    expect(replace).toHaveBeenCalledWith('/profile')
  })

  it('forwards the referral and adoption decision without client identity fields', async () => {
    sessionStorage.setItem('oauth_aff_code', '  FORKREF  ')
    mountCallback()
    await flushPromises()

    expect(apiClientPost).toHaveBeenCalledWith('/auth/oauth/dingtalk/complete-registration', {
      aff_code: 'FORKREF', adopt_display_name: false, adopt_avatar: false
    })
    expect(sessionStorage.getItem('oauth_aff_code')).toBeNull()
    expect(setToken).toHaveBeenCalledWith(tokens.access_token, tokenContext)
  })

  it.each(['automatic', 'profile-confirmation', 'invitation-retry'])('ignores old registration tokens after the auth session changes during %s', async mode => {
    const pending = deferred<{ data: typeof tokens }>()
    if (mode === 'profile-confirmation') {
      exchangePendingOAuthCompletion.mockResolvedValue({ ...pendingProfileSignup })
    }
    if (mode === 'invitation-retry') {
      apiClientPost.mockRejectedValueOnce({ reason: 'OAUTH_INVITATION_REQUIRED' })
      apiClientPost.mockImplementationOnce(() => pending.promise)
    } else {
      apiClientPost.mockImplementationOnce(() => pending.promise)
    }
    const wrapper = mountCallback()
    await flushPromises()
    if (mode === 'profile-confirmation') {
      await wrapper.find('button').trigger('click')
      await flushPromises()
    }
    if (mode === 'invitation-retry') {
      await wrapper.find('input[type="text"]').setValue('valid-invite')
      await wrapper.find('button').trigger('click')
      await flushPromises()
    }
    expect(apiClientPost).toHaveBeenCalledTimes(mode === 'invitation-retry' ? 2 : 1)
    changeToAnotherSession()
    pending.resolve({ data: { ...tokens } })
    await flushPromises()

    expect(setToken).not.toHaveBeenCalled()
    expectOtherSessionPreserved()
  })

  it('does not start no-email registration from an exchange response for an older auth session', async () => {
    const pending = deferred<typeof pendingSignup>()
    exchangePendingOAuthCompletion.mockImplementationOnce(() => pending.promise)
    mountCallback()
    await flushPromises()
    changeToAnotherSession()
    pending.resolve({ ...pendingSignup })
    await flushPromises()

    expect(apiClientPost).not.toHaveBeenCalled()
    expect(setToken).not.toHaveBeenCalled()
    expectOtherSessionPreserved()
  })

  it('does not replace another session pending state or referral when a stale request requires an invitation', async () => {
    const pending = deferred<unknown>()
    apiClientPost.mockImplementationOnce(() => pending.promise)
    mountCallback()
    await flushPromises()
    changeToAnotherSession()
    pending.reject({ reason: 'OAUTH_INVITATION_REQUIRED' })
    await flushPromises()

    expect(setToken).not.toHaveBeenCalled()
    expectOtherSessionPreserved()
  })

  it('keeps the new session when atomic token publication rejects AUTH_SESSION_CHANGED', async () => {
    const pending = deferred<unknown>()
    setToken.mockImplementationOnce(() => pending.promise)
    mountCallback()
    await flushPromises()
    expect(setToken).toHaveBeenCalledWith(tokens.access_token, tokenContext)
    changeToAnotherSession()
    pending.reject({ code: 'AUTH_SESSION_CHANGED', message: 'Login session changed.' })
    await flushPromises()

    expectOtherSessionPreserved()
  })

  it('reports an ordinary token publication failure even though the auth store advanced its own version', async () => {
    setToken.mockImplementationOnce(async () => {
      authState.authSessionVersion++
      throw new Error('Current profile unavailable')
    })
    mountCallback()
    await flushPromises()

    expect(setToken).toHaveBeenCalledWith(tokens.access_token, tokenContext)
    expect(showError).toHaveBeenCalledWith('Current profile unavailable')
    expect(showSuccess).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
  })
})
