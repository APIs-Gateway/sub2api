import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { reactive } from 'vue'

import RedeemView from '@/views/user/RedeemView.vue'
import { resetFiatDataMissingForTest, useCurrencyDisplay } from '@/composables/useCurrencyDisplay'

// 兑换页按人民币展示：当前余额、兑换到账、兑换记录里的余额类面值；兑换请求本身不变。

const { getHistory, redeem, authState } = vi.hoisted(() => ({
  getHistory: vi.fn(),
  redeem: vi.fn(),
  authState: { user: null as Record<string, unknown> | null, refreshUser: vi.fn() },
}))
// 可变的假设置：改它就能模拟 codex 站（倍率 13）和 free 站（倍率 1）。
const publicSettings = vi.hoisted(() => ({ value: {} as Record<string, unknown> }))

vi.mock('@/stores/auth', () => ({ useAuthStore: () => reactive(authState) }))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showWarning: vi.fn(),
    get cachedPublicSettings() {
      return publicSettings.value
    },
  }),
}))
vi.mock('@/stores/subscriptions', () => ({ useSubscriptionStore: () => ({ fetchActiveSubscriptions: vi.fn() }) }))
vi.mock('@/api', () => ({
  redeemAPI: { getHistory, redeem },
  authAPI: { getPublicSettings: vi.fn().mockResolvedValue({ contact_info: '' }) },
}))
vi.mock('@/utils/format', () => ({ formatDateTime: () => 'April 2026' }))
vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

function mountRedeem() {
  return mount(RedeemView, {
    global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, Icon: true, NumText: false, transition: false } },
  })
}

beforeEach(() => {
  window.localStorage.clear()
  resetFiatDataMissingForTest()
  useCurrencyDisplay().setMode('fiat')
  publicSettings.value = { balance_recharge_multiplier: 13 }
  getHistory.mockReset().mockResolvedValue([
    { id: 1, code: 'AAA', type: 'balance', value: 130, status: 'used', used_at: '', created_at: '' },
    { id: 2, code: 'BBB', type: 'admin_balance', value: -26, status: 'used', used_at: '', created_at: '', notes: '' },
  ])
  redeem.mockReset().mockResolvedValue({ message: 'ok', type: 'balance', value: 130, new_balance: 780 })
  authState.refreshUser = vi.fn().mockResolvedValue(undefined)
  authState.user = { id: 1, username: 'alice', balance: 650, concurrency: 2 }
})

describe('兑换页按人民币展示', () => {
  it('当前余额和兑换记录里的面值按充值倍率折回人民币', async () => {
    const wrapper = mountRedeem()
    await flushPromises()
    const text = wrapper.text().replace(/\s+/g, '')

    // 余额 650 ÷ 13 = ¥50.00；兑换码面值 130 ÷ 13 = +¥10.00；管理员扣减 -26 ÷ 13 = -¥2.00。
    expect(text).toContain('¥50.00')
    expect(text).toContain('+¥10.00')
    expect(text).toContain('-¥2.00')
    expect(text).not.toContain('$')
  })

  it('兑换成功后到账金额和新余额也是人民币，请求只带兑换码', async () => {
    const wrapper = mountRedeem()
    await flushPromises()
    await wrapper.get('#code').setValue('BALANCE-CODE')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    const text = wrapper.text().replace(/\s+/g, '')

    expect(redeem).toHaveBeenCalledWith('BALANCE-CODE')
    // 到账 130 ÷ 13 = ¥10.00；新余额 780 ÷ 13 = ¥60.00。
    expect(text).toContain('¥10.00')
    expect(text).toContain('¥60.00')
    expect(text).not.toContain('$')
  })

  it('free 站（倍率 1）保持美元', async () => {
    publicSettings.value = { balance_recharge_multiplier: 1 }
    const wrapper = mountRedeem()
    await flushPromises()
    const text = wrapper.text().replace(/\s+/g, '')

    expect(text).toContain('$650.00')
    expect(text).toContain('+$130.00')
    expect(text).not.toContain('¥')
  })
})
