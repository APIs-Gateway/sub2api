import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import RegisterView from '@/views/auth/RegisterView.vue'

const { routeState, pushMock, registerMock, getPublicSettingsMock, getLegacyInviteStatusMock } = vi.hoisted(() => ({
  routeState: { path: '/register', query: {} as Record<string, unknown> },
  pushMock: vi.fn(),
  registerMock: vi.fn(),
  getPublicSettingsMock: vi.fn(),
  getLegacyInviteStatusMock: vi.fn()
}))

vi.mock('vue-router', () => ({
  useRoute: () => routeState,
  useRouter: () => ({ push: pushMock, replace: vi.fn() })
}))

// 只替换 useI18n：i18n/index.ts 在模块加载时就要调 createI18n，整包 mock 会把它打掉。
vi.mock('vue-i18n', async importOriginal => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key, locale: { value: 'zh-CN' } })
  }
})

vi.mock('@/stores', () => ({
  useAuthStore: () => ({ setToken: vi.fn(), register: (...args: unknown[]) => registerMock(...args) }),
  useAppStore: () => ({
    cachedPublicSettings: null,
    showError: vi.fn(),
    showSuccess: vi.fn()
  })
}))

vi.mock('@/api/auth', async () => {
  const actual = await vi.importActual<typeof import('@/api/auth')>('@/api/auth')
  return {
    ...actual,
    getPublicSettings: (...args: unknown[]) => getPublicSettingsMock(...args),
    validatePromoCode: vi.fn(),
    validateInvitationCode: vi.fn(),
    validateAffiliateCode: vi.fn()
  }
})

vi.mock('@/api/legacyInvite', () => ({
  getLegacyInviteStatus: (...args: unknown[]) => getLegacyInviteStatusMock(...args)
}))

const stubs = {
  AuthLayout: { template: '<div><slot /><slot name="footer" /></div>' },
  LinuxDoOAuthSection: true,
  OidcOAuthSection: true,
  WechatOAuthSection: true,
  EmailOAuthButtons: true,
  LoginAgreementPrompt: true,
  TurnstileWidget: true,
  Icon: true,
  // 把 to 序列化出来，才能断言切到登录页时 redirect 有没有带上
  RouterLink: { props: ['to'], template: '<a :data-to="JSON.stringify(to)"><slot /></a>' }
}

function settings(overrides: Record<string, unknown> = {}) {
  return {
    registration_enabled: true,
    email_verify_enabled: false,
    promo_code_enabled: false,
    invitation_code_enabled: false,
    signup_source_enabled: { email: true },
    affiliate_code_admits_signup: false,
    turnstile_enabled: false,
    turnstile_site_key: '',
    site_name: 'Sub2API',
    linuxdo_oauth_enabled: false,
    oidc_oauth_enabled: false,
    oidc_oauth_provider_name: 'OIDC',
    github_oauth_enabled: false,
    google_oauth_enabled: false,
    registration_email_suffix_whitelist: [],
    ...overrides
  }
}

async function mountRegister(overrides: Record<string, unknown> = {}) {
  getPublicSettingsMock.mockResolvedValue(settings(overrides))
  const wrapper = mount(RegisterView, { global: { stubs } })
  await flushPromises()
  await flushPromises()
  return wrapper
}

async function submitForm(wrapper: Awaited<ReturnType<typeof mountRegister>>) {
  await wrapper.get('#email').setValue('new@example.com')
  await wrapper.get('#password').setValue('secret-123')
  await wrapper.get('form').trigger('submit.prevent')
  await flushPromises()
}

function expectPushedOnly(target: string) {
  expect(pushMock).toHaveBeenCalledTimes(1)
  expect(pushMock).toHaveBeenCalledWith(target)
}

function savedRegisterData(): Record<string, unknown> {
  return JSON.parse(sessionStorage.getItem('register_data') as string)
}

const UNSAFE_REDIRECTS = [
  '//evil.com',
  '/\\evil.com',
  'https://evil.com',
  'javascript:alert(1)',
  '/%2F%2Fevil.com',
  '/%5Cevil.com',
  '/%252F%252Fevil.com',
  '/\n/evil.com',
  '/register'
]

describe('RegisterView 的 redirect', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    routeState.query = {}
    sessionStorage.clear()
    localStorage.clear()
    getLegacyInviteStatusMock.mockResolvedValue({ enabled: false })
    registerMock.mockResolvedValue({})
  })

  describe('不需要邮箱验证：注册成功后直接跳转', () => {
    it('回到 redirect 指向的页面（首页「立即开始」带来的 /keys）', async () => {
      routeState.query = { redirect: '/keys' }
      const wrapper = await mountRegister()
      await submitForm(wrapper)

      expect(registerMock).toHaveBeenCalledTimes(1)
      expectPushedOnly('/keys')
    })

    it('没有 redirect 时仍回 /dashboard', async () => {
      const wrapper = await mountRegister()
      await submitForm(wrapper)

      expect(registerMock).toHaveBeenCalledTimes(1)
      expectPushedOnly('/dashboard')
    })

    it.each(UNSAFE_REDIRECTS)('不合法的 redirect %j 回落到 /dashboard', async (redirect) => {
      routeState.query = { redirect }
      const wrapper = await mountRegister()
      await submitForm(wrapper)

      expect(registerMock).toHaveBeenCalledTimes(1)
      expectPushedOnly('/dashboard')
    })

    it('redirect 重复出现（数组）时回落到 /dashboard', async () => {
      routeState.query = { redirect: ['/keys', '//evil.com'] }
      const wrapper = await mountRegister()
      await submitForm(wrapper)

      expectPushedOnly('/dashboard')
    })
  })

  describe('需要邮箱验证：redirect 随注册数据带到验证页', () => {
    it('把 redirect 存进 register_data，再去 /email-verify', async () => {
      routeState.query = { redirect: '/keys' }
      const wrapper = await mountRegister({ email_verify_enabled: true })
      await submitForm(wrapper)

      expect(registerMock).not.toHaveBeenCalled()
      expectPushedOnly('/email-verify')
      expect(savedRegisterData()).toMatchObject({ email: 'new@example.com', redirect: '/keys' })
    })

    it('没有 redirect 时 register_data 里不带这一项', async () => {
      const wrapper = await mountRegister({ email_verify_enabled: true })
      await submitForm(wrapper)

      expectPushedOnly('/email-verify')
      expect(savedRegisterData()).not.toHaveProperty('redirect')
    })

    it.each(UNSAFE_REDIRECTS)('不合法的 redirect %j 不会写进 register_data', async (redirect) => {
      routeState.query = { redirect }
      const wrapper = await mountRegister({ email_verify_enabled: true })
      await submitForm(wrapper)

      expectPushedOnly('/email-verify')
      expect(savedRegisterData()).not.toHaveProperty('redirect')
    })
  })

  describe('切到登录页', () => {
    function signInLinkTarget(wrapper: Awaited<ReturnType<typeof mountRegister>>) {
      const link = wrapper.findAll('a').find((a) => a.text().includes('auth.signIn'))
      expect(link).toBeDefined()
      return JSON.parse(link!.attributes('data-to') as string)
    }

    it('把 redirect 一起带过去', async () => {
      routeState.query = { redirect: '/keys' }
      const wrapper = await mountRegister()

      expect(signInLinkTarget(wrapper)).toEqual({ path: '/login', query: { redirect: '/keys' } })
    })

    it('没有 redirect 或不合法时不带', async () => {
      expect(signInLinkTarget(await mountRegister())).toEqual({ path: '/login', query: {} })

      routeState.query = { redirect: '//evil.com' }
      expect(signInLinkTarget(await mountRegister())).toEqual({ path: '/login', query: {} })
    })
  })
})
