import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import LoginView from '@/views/auth/LoginView.vue'

const { getPublicSettingsMock, pushMock, loginMock, login2FAMock, isTotpMock, routeState } = vi.hoisted(() => ({
  getPublicSettingsMock: vi.fn(),
  pushMock: vi.fn(),
  loginMock: vi.fn(),
  login2FAMock: vi.fn(),
  isTotpMock: vi.fn(),
  routeState: { query: {} as Record<string, unknown> }
}))

const publicSettings = {
  registration_enabled: true,
  turnstile_enabled: false,
  turnstile_site_key: '',
  tencent_captcha_enabled: false,
  tencent_captcha_app_id: '',
  aliyun_captcha_enabled: false,
  aliyun_captcha_scene_id: '',
  aliyun_captcha_prefix: '',
  linuxdo_oauth_enabled: false,
  dingtalk_oauth_enabled: false,
  wechat_oauth_enabled: false,
  backend_mode_enabled: false,
  oidc_oauth_enabled: false,
  oidc_oauth_provider_name: 'OIDC',
  github_oauth_enabled: false,
  google_oauth_enabled: false,
  password_reset_enabled: false,
  passkey_enabled: false,
  login_agreement_enabled: false,
  login_agreement_documents: []
}

vi.mock('vue-router', () => ({
  useRouter: () => ({
    push: pushMock,
    currentRoute: { value: routeState }
  })
}))

vi.mock('vue-i18n', () => ({
  createI18n: () => ({ global: { t: (key: string) => key } }),
  useI18n: () => ({ t: (key: string) => key })
}))

vi.mock('@/stores', () => ({
  useAuthStore: () => ({
    login: (...args: unknown[]) => loginMock(...args),
    login2FA: (...args: unknown[]) => login2FAMock(...args),
    loginWithPasskey: vi.fn()
  }),
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showWarning: vi.fn()
  })
}))

vi.mock('@/api/auth', () => ({
  buildOAuthLoginStartURL: vi.fn(),
  getPublicSettings: (...args: unknown[]) => getPublicSettingsMock(...args),
  isTotp2FARequired: (...args: unknown[]) => isTotpMock(...args),
  isWeChatWebOAuthEnabled: vi.fn(() => false),
  startOAuthLogin: vi.fn()
}))

const TotpLoginModalStub = {
  name: 'TotpLoginModalStub',
  props: ['tempToken', 'userEmailMasked'],
  emits: ['verify', 'cancel'],
  setup(_props: unknown, { expose }: { expose: (exposed: Record<string, unknown>) => void }) {
    expose({ setVerifying: vi.fn(), setError: vi.fn() })
    return () => null
  }
}

function mountLogin() {
  return mount(LoginView, {
    global: {
      stubs: {
        AuthLayout: { template: '<div><slot /><slot name="footer" /></div>' },
        DingTalkOAuthSection: true,
        EmailOAuthButtons: true,
        Icon: true,
        LinuxDoOAuthSection: true,
        LoginAgreementPrompt: true,
        OidcOAuthSection: true,
        // 把 to 序列化出来，才能断言切到注册页时 redirect 有没有带上
        RouterLink: { props: ['to'], template: '<a :data-to="JSON.stringify(to)"><slot /></a>' },
        TotpLoginModal: TotpLoginModalStub,
        TurnstileWidget: true,
        WechatOAuthSection: true,
        transition: false
      }
    }
  })
}

async function submitCredentials(wrapper: ReturnType<typeof mountLogin>) {
  await wrapper.get('#email').setValue('user@example.com')
  await wrapper.get('#password').setValue('secret-123')
  await wrapper.get('form').trigger('submit.prevent')
  await flushPromises()
}

function expectPushedOnly(target: string) {
  expect(pushMock).toHaveBeenCalledTimes(1)
  expect(pushMock).toHaveBeenCalledWith(target)
}

const UNSAFE_REDIRECTS = [
  '//evil.com',
  '/\\evil.com',
  'https://evil.com',
  'javascript:alert(1)',
  '/%2F%2Fevil.com',
  '/%5Cevil.com',
  '/%252F%252Fevil.com',
  '/\t/evil.com',
  '/login'
]

describe('LoginView 的 redirect', () => {
  beforeEach(() => {
    getPublicSettingsMock.mockReset()
    pushMock.mockReset()
    loginMock.mockReset()
    login2FAMock.mockReset()
    isTotpMock.mockReset()
    routeState.query = {}
    getPublicSettingsMock.mockResolvedValue(publicSettings)
    loginMock.mockResolvedValue({ access_token: 't' })
    isTotpMock.mockReturnValue(false)
  })

  it('登录成功后回到 redirect 指向的页面', async () => {
    routeState.query = { redirect: '/keys' }
    const wrapper = mountLogin()
    await flushPromises()
    await submitCredentials(wrapper)

    expectPushedOnly('/keys')
  })

  it('redirect 带查询串时原样保留（鉴权守卫写入的原路径）', async () => {
    routeState.query = { redirect: '/usage?model=gpt-5&range=7d' }
    const wrapper = mountLogin()
    await flushPromises()
    await submitCredentials(wrapper)

    expectPushedOnly('/usage?model=gpt-5&range=7d')
  })

  it('没有 redirect 时仍回 /dashboard', async () => {
    const wrapper = mountLogin()
    await flushPromises()
    await submitCredentials(wrapper)

    expectPushedOnly('/dashboard')
  })

  it.each(UNSAFE_REDIRECTS)('不合法的 redirect %j 回落到 /dashboard', async (redirect) => {
    routeState.query = { redirect }
    const wrapper = mountLogin()
    await flushPromises()
    await submitCredentials(wrapper)

    expectPushedOnly('/dashboard')
  })

  it('redirect 重复出现（数组）时回落到 /dashboard', async () => {
    routeState.query = { redirect: ['/keys', '//evil.com'] }
    const wrapper = mountLogin()
    await flushPromises()
    await submitCredentials(wrapper)

    expectPushedOnly('/dashboard')
  })

  describe('两步验证（2FA）', () => {
    async function loginThroughTotp(redirect: unknown) {
      routeState.query = redirect === undefined ? {} : { redirect }
      loginMock.mockResolvedValue({ requires_2fa: true, temp_token: 'tmp', user_email_masked: 'u***@example.com' })
      isTotpMock.mockReturnValue(true)
      login2FAMock.mockResolvedValue({})

      const wrapper = mountLogin()
      await flushPromises()
      await submitCredentials(wrapper)
      // 密码这一步不跳转，等验证码
      expect(pushMock).not.toHaveBeenCalled()

      wrapper.findComponent({ name: 'TotpLoginModalStub' }).vm.$emit('verify', '123456')
      await flushPromises()
      return wrapper
    }

    it('验证码通过后回到 redirect 指向的页面', async () => {
      await loginThroughTotp('/keys')

      expect(login2FAMock).toHaveBeenCalledWith('tmp', '123456')
      expectPushedOnly('/keys')
    })

    it('没有 redirect 时回 /dashboard', async () => {
      await loginThroughTotp(undefined)

      expectPushedOnly('/dashboard')
    })

    it.each(UNSAFE_REDIRECTS)('不合法的 redirect %j 回落到 /dashboard', async (redirect) => {
      await loginThroughTotp(redirect)

      expectPushedOnly('/dashboard')
    })
  })

  describe('切到注册页', () => {
    function signUpLinkTarget(wrapper: ReturnType<typeof mountLogin>) {
      const link = wrapper.findAll('a').find((a) => a.text().includes('auth.signUp'))
      expect(link).toBeDefined()
      return JSON.parse(link!.attributes('data-to') as string)
    }

    it('把 redirect 一起带过去', async () => {
      routeState.query = { redirect: '/keys' }
      const wrapper = mountLogin()
      await flushPromises()

      expect(signUpLinkTarget(wrapper)).toEqual({ path: '/register', query: { redirect: '/keys' } })
    })

    it('没有 redirect 或不合法时不带', async () => {
      const wrapper = mountLogin()
      await flushPromises()
      expect(signUpLinkTarget(wrapper)).toEqual({ path: '/register', query: {} })

      routeState.query = { redirect: '//evil.com' }
      const unsafe = mountLogin()
      await flushPromises()
      expect(signUpLinkTarget(unsafe)).toEqual({ path: '/register', query: {} })
    })
  })
})
