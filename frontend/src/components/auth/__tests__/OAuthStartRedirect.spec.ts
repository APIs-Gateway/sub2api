import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import DingTalkOAuthSection from '@/components/auth/DingTalkOAuthSection.vue'
import EmailOAuthButtons from '@/components/auth/EmailOAuthButtons.vue'
import LinuxDoOAuthSection from '@/components/auth/LinuxDoOAuthSection.vue'
import OidcOAuthSection from '@/components/auth/OidcOAuthSection.vue'

// 第三方登录发起时，redirect 只把站内路径交给后端；其余一律回 /dashboard。
// （微信的发起按钮要先注入公开设置，用例在 WechatOAuthSection.spec.ts 里。）

const routeState = vi.hoisted(() => ({
  query: {} as Record<string, unknown>,
}))

const locationState = vi.hoisted(() => ({
  current: { href: 'http://localhost/register' } as { href: string },
}))

vi.mock('vue-router', () => ({
  useRoute: () => routeState,
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

const emailStubs = { GitHubMark: true, GoogleMark: true }

const providers: Array<{ name: string; mountIt: () => ReturnType<typeof mount>; startPath: string }> = [
  {
    name: 'GitHub / Google',
    mountIt: () =>
      mount(EmailOAuthButtons, {
        props: { githubEnabled: true, googleEnabled: false },
        global: { stubs: emailStubs },
      }),
    startPath: '/api/v1/auth/oauth/github/start',
  },
  { name: 'LinuxDo', mountIt: () => mount(LinuxDoOAuthSection), startPath: '/api/v1/auth/oauth/linuxdo/start' },
  { name: 'OIDC', mountIt: () => mount(OidcOAuthSection), startPath: '/api/v1/auth/oauth/oidc/start' },
  { name: '钉钉', mountIt: () => mount(DingTalkOAuthSection), startPath: '/api/v1/auth/oauth/dingtalk/start' },
]

const UNSAFE_REDIRECTS = [
  '//evil.com',
  '/\\evil.com',
  'https://evil.com',
  'javascript:alert(1)',
  '/%2F%2Fevil.com',
  '/%5Cevil.com',
  '/%252F%252Fevil.com',
  '/\t/evil.com',
]

describe.each(providers)('$name 登录发起时的 redirect', ({ mountIt, startPath }) => {
  beforeEach(() => {
    routeState.query = {}
    locationState.current = { href: 'http://localhost/register' }
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: locationState.current,
    })
    window.localStorage.clear()
    window.sessionStorage.clear()
  })

  async function startAndReadRedirect(): Promise<string> {
    const wrapper = mountIt()
    await wrapper.get('button').trigger('click')
    const url = new URL(locationState.current.href, 'http://localhost')
    expect(url.pathname).toBe(startPath)
    return url.searchParams.get('redirect') as string
  }

  it('把站内 redirect 原样交给后端（首页注册入口带来的 /keys）', async () => {
    routeState.query = { redirect: '/keys' }
    expect(await startAndReadRedirect()).toBe('/keys')
  })

  it('带查询串的 redirect 保持完整', async () => {
    routeState.query = { redirect: '/usage?model=gpt-5&range=7d' }
    expect(await startAndReadRedirect()).toBe('/usage?model=gpt-5&range=7d')
  })

  it('没有 redirect 时是 /dashboard', async () => {
    expect(await startAndReadRedirect()).toBe('/dashboard')
  })

  it.each(UNSAFE_REDIRECTS)('不合法的 redirect %j 回落到 /dashboard', async (redirect) => {
    routeState.query = { redirect }
    expect(await startAndReadRedirect()).toBe('/dashboard')
  })

  it('redirect 重复出现（数组）时回落到 /dashboard', async () => {
    routeState.query = { redirect: ['/keys', '//evil.com'] }
    expect(await startAndReadRedirect()).toBe('/dashboard')
  })
})
