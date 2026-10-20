import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import DingTalkEmailCompletionView from '../DingTalkEmailCompletionView.vue'

const { replace, setToken, apiClientPost, routeState } = vi.hoisted(() => ({
  replace: vi.fn(),
  setToken: vi.fn(),
  apiClientPost: vi.fn(),
  routeState: { query: {} as Record<string, unknown> }
}))

vi.mock('vue-router', () => ({
  useRoute: () => routeState,
  useRouter: () => ({ replace })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

vi.mock('@/stores', () => ({
  useAuthStore: () => ({ setToken }),
  useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn(), showInfo: vi.fn() })
}))

vi.mock('@/api/client', () => ({
  apiClient: { post: (...args: unknown[]) => apiClientPost(...args) }
}))

vi.mock('@/api/auth', async () => {
  const actual = await vi.importActual<typeof import('@/api/auth')>('@/api/auth')
  return { ...actual, persistOAuthTokenContext: vi.fn() }
})

// 只关心父组件拿到提交事件后怎么处理 redirect，表单本身用桩代替
const FormStub = {
  name: 'PendingOAuthCreateAccountFormStub',
  props: ['testIdPrefix', 'initialEmail', 'isSubmitting', 'errorMessage'],
  emits: ['submit', 'switch-to-bind'],
  template: '<div />'
}

function mountView() {
  return mount(DingTalkEmailCompletionView, {
    global: {
      stubs: {
        AuthLayout: { template: '<div><slot /></div>' },
        PendingOAuthCreateAccountForm: FormStub
      }
    }
  })
}

async function submitAccount(wrapper: ReturnType<typeof mountView>) {
  wrapper
    .findComponent({ name: 'PendingOAuthCreateAccountFormStub' })
    .vm.$emit('submit', { email: 'new@example.com', password: 'secret-123' })
  await flushPromises()
}

const UNSAFE = ['//evil.com', '/\\evil.com', 'https://evil.com', '/%2F%2Fevil.com', '/%5Cevil.com', '/.//evil.com']

describe('DingTalkEmailCompletionView 的 redirect', () => {
  beforeEach(() => {
    replace.mockReset()
    setToken.mockReset()
    apiClientPost.mockReset()
    setToken.mockResolvedValue({})
    routeState.query = {}
    localStorage.clear()
    sessionStorage.clear()
  })

  it('补完邮箱建号成功后回到后端返回的 redirect', async () => {
    apiClientPost.mockResolvedValue({ data: { access_token: 'token', redirect: '/keys' } })
    await submitAccount(mountView())

    expect(setToken).toHaveBeenCalledWith('token')
    expect(replace).toHaveBeenCalledTimes(1)
    expect(replace).toHaveBeenCalledWith('/keys')
  })

  it('后端没返回时用地址栏里的 redirect', async () => {
    routeState.query = { redirect: '/usage?range=7d' }
    apiClientPost.mockResolvedValue({ data: { access_token: 'token' } })
    await submitAccount(mountView())

    expect(replace).toHaveBeenCalledWith('/usage?range=7d')
  })

  it('都没有时回 /dashboard', async () => {
    apiClientPost.mockResolvedValue({ data: { access_token: 'token' } })
    await submitAccount(mountView())

    expect(replace).toHaveBeenCalledWith('/dashboard')
  })

  it.each(UNSAFE)('后端返回的 redirect 不合法（%j）时回落到 /dashboard', async (redirect) => {
    apiClientPost.mockResolvedValue({ data: { access_token: 'token', redirect } })
    await submitAccount(mountView())

    expect(replace).toHaveBeenCalledTimes(1)
    expect(replace).toHaveBeenCalledWith('/dashboard')
  })

  it.each(UNSAFE)('地址栏里的 redirect 不合法（%j）时回落到 /dashboard', async (redirect) => {
    routeState.query = { redirect }
    apiClientPost.mockResolvedValue({ data: { access_token: 'token' } })
    await submitAccount(mountView())

    expect(replace).toHaveBeenCalledWith('/dashboard')
  })

  it('转去绑定已有账号时把合法的 redirect 一起带过去', async () => {
    routeState.query = { redirect: '/keys' }
    const wrapper = mountView()
    wrapper.findComponent({ name: 'PendingOAuthCreateAccountFormStub' }).vm.$emit('switch-to-bind', 'a@b.com')
    await flushPromises()

    expect(replace).toHaveBeenCalledWith({
      path: '/auth/dingtalk/callback',
      query: { bind: '1', email: 'a@b.com', redirect: '/keys' }
    })
  })

  it.each(UNSAFE)('转去绑定已有账号时不带不合法的 redirect（%j）', async (redirect) => {
    routeState.query = { redirect }
    const wrapper = mountView()
    wrapper.findComponent({ name: 'PendingOAuthCreateAccountFormStub' }).vm.$emit('switch-to-bind', 'a@b.com')
    await flushPromises()

    expect(replace).toHaveBeenCalledWith({
      path: '/auth/dingtalk/callback',
      query: { bind: '1', email: 'a@b.com' }
    })
  })
})
