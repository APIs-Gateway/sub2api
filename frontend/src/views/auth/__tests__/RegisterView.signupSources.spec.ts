import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import RegisterView from '@/views/auth/RegisterView.vue'

const { routeState, getPublicSettingsMock, getLegacyInviteStatusMock } = vi.hoisted(() => ({
  routeState: {
    path: '/register',
    query: {} as Record<string, unknown>
  },
  getPublicSettingsMock: vi.fn(),
  getLegacyInviteStatusMock: vi.fn()
}))

vi.mock('vue-router', () => ({
  useRoute: () => routeState,
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() })
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
  useAuthStore: () => ({ setToken: vi.fn() }),
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() })
}))

vi.mock('@/api/auth', async () => {
  const actual = await vi.importActual<typeof import('@/api/auth')>('@/api/auth')
  return {
    ...actual,
    getPublicSettings: (...args: any[]) => getPublicSettingsMock(...args),
    validatePromoCode: vi.fn(),
    validateInvitationCode: vi.fn(),
    validateAffiliateCode: vi.fn()
  }
})

vi.mock('@/api/legacyInvite', () => ({
  getLegacyInviteStatus: (...args: any[]) => getLegacyInviteStatusMock(...args)
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
  RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' }
}

function baseSettings(overrides: Record<string, unknown> = {}) {
  return {
    registration_enabled: true,
    email_verify_enabled: false,
    promo_code_enabled: false,
    invitation_code_enabled: true,
    signup_source_enabled: { email: true, github: true },
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

async function mountRegisterView(settings: Record<string, unknown>) {
  getPublicSettingsMock.mockResolvedValue(settings)
  const wrapper = mount(RegisterView, { global: { stubs } })
  // 两次 flush：一次等公开设置，一次等它触发的领码入口探测
  await flushPromises()
  await flushPromises()
  return wrapper
}

describe('RegisterView 的注册来源开关', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    routeState.query = {}
    getLegacyInviteStatusMock.mockResolvedValue({ enabled: false })
  })

  it('账号密码注册被关掉时不渲染表单', async () => {
    // 这正是线上出过的问题：来源开关关了，注册页照旧把整张表单画出来，
    // 用户一路填完才撞上 SIGNUP_SOURCE_DISABLED。
    const wrapper = await mountRegisterView(
      baseSettings({ signup_source_enabled: { email: false, github: true } })
    )
    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.find('#email').exists()).toBe(false)
  })

  it('账号密码关了但第三方还开着时，仍然渲染第三方入口', async () => {
    const wrapper = await mountRegisterView(
      baseSettings({ signup_source_enabled: { email: false }, github_oauth_enabled: true })
    )
    expect(wrapper.find('form').exists()).toBe(false)
    // 按组件名找而不是按 stub 标签名：EmailOAuthButtons 转成 kebab 是 email-o-auth-buttons，
    // 写错标签名的话 exists() 会恒为 false，断言 false 的用例就成了假通过。
    expect(wrapper.findComponent({ name: 'EmailOAuthButtons' }).exists()).toBe(true)
  })

  it('所有来源都关掉时等价于不能注册，表单和第三方入口都不出现', async () => {
    const wrapper = await mountRegisterView(
      baseSettings({ signup_source_enabled: { email: false } })
    )
    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.findComponent({ name: 'EmailOAuthButtons' }).exists()).toBe(false)
  })

  it('后端没报告 email 这一项时按可用处理，避免旧版本后端把注册页整个锁死', async () => {
    const wrapper = await mountRegisterView(baseSettings({ signup_source_enabled: {} }))
    expect(wrapper.find('form').exists()).toBe(true)
  })
})

describe('RegisterView 的邀请码准入提示', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    routeState.query = {}
    getLegacyInviteStatusMock.mockResolvedValue({ enabled: false })
  })

  it('开启邀请人邀请码准入后，注册码那栏标成可选并给出说明', async () => {
    const wrapper = await mountRegisterView(baseSettings({ affiliate_code_admits_signup: true }))
    expect(wrapper.text()).toContain('auth.invitationOrAffiliateCodeHint')
    expect(wrapper.text()).toContain('common.optional')
  })

  it('未开启时不出现那段说明，免得用户以为可以不填', async () => {
    const wrapper = await mountRegisterView(baseSettings({ affiliate_code_admits_signup: false }))
    expect(wrapper.text()).not.toContain('auth.invitationOrAffiliateCodeHint')
  })
})

describe('RegisterView 的老用户领码入口', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    routeState.query = {}
  })

  it('领码开放时给出入口链接', async () => {
    getLegacyInviteStatusMock.mockResolvedValue({ enabled: true })
    const wrapper = await mountRegisterView(baseSettings())
    expect(wrapper.text()).toContain('auth.claimInvitationCodeLink')
  })

  it('账号密码注册关掉后领码入口依然可见', async () => {
    // 这条最关键：入口一度被放在表单内部，而账号密码注册一关整个表单就不渲染——
    // 偏偏那种情况下用户最需要它，走第三方注册同样得先有一张邀请码。
    getLegacyInviteStatusMock.mockResolvedValue({ enabled: true })
    const wrapper = await mountRegisterView(
      baseSettings({ signup_source_enabled: { email: false }, github_oauth_enabled: true })
    )
    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.text()).toContain('auth.claimInvitationCodeLink')
  })

  it('领码未开放时不给链接', async () => {
    getLegacyInviteStatusMock.mockResolvedValue({ enabled: false })
    const wrapper = await mountRegisterView(baseSettings())
    expect(wrapper.text()).not.toContain('auth.claimInvitationCodeLink')
  })

  it('探测失败时按未开放处理', async () => {
    // 给出一个点进去只会看到「暂未开放」的链接，比不给还糟
    getLegacyInviteStatusMock.mockRejectedValue(new Error('network down'))
    const wrapper = await mountRegisterView(baseSettings())
    expect(wrapper.text()).not.toContain('auth.claimInvitationCodeLink')
  })

  it('注册码功能本身没开时不去探测领码状态', async () => {
    await mountRegisterView(baseSettings({ invitation_code_enabled: false }))
    expect(getLegacyInviteStatusMock).not.toHaveBeenCalled()
  })

  it('注册总闸关闭时不去探测领码状态', async () => {
    await mountRegisterView(baseSettings({ registration_enabled: false }))
    expect(getLegacyInviteStatusMock).not.toHaveBeenCalled()
  })
})
