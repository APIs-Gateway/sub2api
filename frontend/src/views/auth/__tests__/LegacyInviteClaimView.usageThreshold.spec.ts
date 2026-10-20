import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import LegacyInviteClaimView from '@/views/auth/LegacyInviteClaimView.vue'

const { getPublicSettingsMock, getLegacyInviteStatusMock, sendLegacyInviteCodeMock, showErrorMock } =
  vi.hoisted(() => ({
    getPublicSettingsMock: vi.fn(),
    getLegacyInviteStatusMock: vi.fn(),
    sendLegacyInviteCodeMock: vi.fn(),
    showErrorMock: vi.fn()
  }))

// 只替换 useI18n：i18n/index.ts 在模块加载时就要调 createI18n，整包 mock 会把它打掉。
// t 把插值参数一并吐出来，这样能同时断言用的是哪条 key、门槛数字有没有传对。
vi.mock('vue-i18n', async importOriginal => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}:${JSON.stringify(params)}` : key,
      locale: { value: 'zh-CN' }
    })
  }
})

vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError: showErrorMock, showSuccess: vi.fn() })
}))

vi.mock('@/api/auth', () => ({
  getPublicSettings: (...args: any[]) => getPublicSettingsMock(...args)
}))

vi.mock('@/api/legacyInvite', () => ({
  getLegacyInviteStatus: (...args: any[]) => getLegacyInviteStatusMock(...args),
  sendLegacyInviteCode: (...args: any[]) => sendLegacyInviteCodeMock(...args),
  claimLegacyInvite: vi.fn()
}))

const stubs = {
  AuthLayout: { template: '<div><slot /><slot name="footer" /></div>' },
  TurnstileWidget: true,
  Icon: true
}

async function mountView(status: Record<string, unknown>) {
  getLegacyInviteStatusMock.mockResolvedValue(status)
  getPublicSettingsMock.mockResolvedValue({ turnstile_enabled: false, turnstile_site_key: '' })
  const wrapper = mount(LegacyInviteClaimView, { global: { stubs } })
  await flushPromises()
  return wrapper
}

async function triggerNotEligible(wrapper: VueWrapper) {
  sendLegacyInviteCodeMock.mockRejectedValue({ code: 'LEGACY_INVITE_NOT_ELIGIBLE' })
  await wrapper.get('#legacy-email').setValue('user@example.com')
  await wrapper.get('button[type="button"]').trigger('click')
  await flushPromises()
}

describe('LegacyInviteClaimView 的用量口径门槛', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('min_usage_cost 大于 0 时副标题同时写出付费和用量两条门槛', async () => {
    // 付费没达标的重度用户如果只看到「满 300 元」，会以为自己没资格而直接走人。
    const wrapper = await mountView({ enabled: true, min_paid_amount: 300, min_usage_cost: 7500 })

    expect(wrapper.text()).toContain(
      'legacyInvite.subtitleWithUsage:{"amount":"300","usage":"7500"}'
    )
    expect(wrapper.text()).not.toContain('legacyInvite.subtitle:')
  })

  it('min_usage_cost 为 0 时只展示付费门槛', async () => {
    const wrapper = await mountView({ enabled: true, min_paid_amount: 300, min_usage_cost: 0 })

    expect(wrapper.text()).toContain('legacyInvite.subtitle:{"amount":"300"}')
    expect(wrapper.text()).not.toContain('subtitleWithUsage')
  })

  it('旧后端没返回 min_usage_cost 时按 0 处理，不会把 undefined 渲染进文案', async () => {
    const wrapper = await mountView({ enabled: true, min_paid_amount: 300 })

    expect(wrapper.text()).toContain('legacyInvite.subtitle:{"amount":"300"}')
    expect(wrapper.text()).not.toContain('subtitleWithUsage')
  })

  it('不达标报错在用量口径开着时把两条门槛都列出来', async () => {
    const wrapper = await mountView({ enabled: true, min_paid_amount: 300, min_usage_cost: 7500.5 })
    await triggerNotEligible(wrapper)

    expect(showErrorMock).toHaveBeenCalledWith(
      'legacyInvite.errors.notEligibleWithUsage:{"amount":"300","usage":"7500.50"}'
    )
  })

  it('不达标报错在用量口径关着时只提付费门槛', async () => {
    const wrapper = await mountView({ enabled: true, min_paid_amount: 300, min_usage_cost: 0 })
    await triggerNotEligible(wrapper)

    expect(showErrorMock).toHaveBeenCalledWith('legacyInvite.errors.notEligible:{"amount":"300"}')
  })
})
