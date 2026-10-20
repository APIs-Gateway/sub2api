import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import RedeemView from '@/views/user/RedeemView.vue'

const {
  getHistoryMock,
  getPublicSettingsMock,
  redeemMock,
  refreshUserMock,
  authState,
  showErrorMock,
  showSuccessMock,
  showWarningMock,
  fetchActiveSubscriptionsMock
} = vi.hoisted(() => ({
  getHistoryMock: vi.fn(),
  getPublicSettingsMock: vi.fn(),
  redeemMock: vi.fn(),
  refreshUserMock: vi.fn(),
  authState: {
    user: null as Record<string, unknown> | null,
    refreshUser: vi.fn()
  },
  showErrorMock: vi.fn(),
  showSuccessMock: vi.fn(),
  showWarningMock: vi.fn(),
  fetchActiveSubscriptionsMock: vi.fn()
}))

vi.mock('@/stores/auth', async () => {
  const { reactive } = await import('vue')
  return {
    useAuthStore: () => reactive(authState)
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: showErrorMock,
    showSuccess: showSuccessMock,
    showWarning: showWarningMock
  })
}))

vi.mock('@/stores/subscriptions', () => ({
  useSubscriptionStore: () => ({
    fetchActiveSubscriptions: fetchActiveSubscriptionsMock
  })
}))

vi.mock('@/api', () => ({
  redeemAPI: {
    getHistory: getHistoryMock,
    redeem: redeemMock
  },
  authAPI: {
    getPublicSettings: getPublicSettingsMock
  }
}))

vi.mock('@/utils/format', () => ({
  formatDateTime: () => 'April 2026'
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

function mountRedeemView() {
  return mount(RedeemView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        Icon: true,
        transition: false
      }
    }
  })
}

async function redeemCode(wrapper: VueWrapper) {
  await wrapper.get('#code').setValue('BALANCE-CODE')
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}

describe('RedeemView', () => {
  beforeEach(() => {
    window.localStorage.clear()
    getHistoryMock.mockReset()
    getPublicSettingsMock.mockReset()
    redeemMock.mockReset()
    refreshUserMock.mockReset()
    showErrorMock.mockReset()
    showSuccessMock.mockReset()
    showWarningMock.mockReset()
    fetchActiveSubscriptionsMock.mockReset()

    getHistoryMock.mockResolvedValue([])
    getPublicSettingsMock.mockResolvedValue({ contact_info: '' })
    redeemMock.mockResolvedValue({
      message: 'Balance added',
      type: 'balance',
      value: 15,
      new_balance: 25
    })
    refreshUserMock.mockResolvedValue(undefined)
    authState.refreshUser = refreshUserMock
    authState.user = {
      id: 1,
      username: 'alice',
      balance: 10,
      concurrency: 2
    }
  })

  it('keeps redemption successful and updates the session balance when profile refresh fails', async () => {
    const consoleErrorSpy = vi.spyOn(console, 'error').mockImplementation(() => undefined)
    refreshUserMock.mockRejectedValueOnce(new Error('profile refresh failed'))
    const wrapper = mountRedeemView()
    await flushPromises()

    await redeemCode(wrapper)

    expect(authState.user).toMatchObject({ balance: 25, concurrency: 2 })
    expect(JSON.parse(window.localStorage.getItem('auth_user') || '{}')).toMatchObject({ balance: 25 })
    expect(wrapper.text()).toContain('redeem.redeemSuccess')
    expect(wrapper.text()).toContain('redeem.newBalance')
    expect(wrapper.text()).toContain('$25.00')
    expect((wrapper.get('#code').element as HTMLInputElement).value).toBe('')
    expect(showSuccessMock).toHaveBeenCalledWith('redeem.codeRedeemSuccess')
    expect(showErrorMock).not.toHaveBeenCalled()
    expect(consoleErrorSpy).toHaveBeenCalledWith(
      'Failed to refresh user after redeem:',
      expect.any(Error)
    )

    consoleErrorSpy.mockRestore()
  })

  it('refreshes the profile after a successful redemption without losing the success result', async () => {
    const wrapper = mountRedeemView()
    await flushPromises()

    await redeemCode(wrapper)

    expect(refreshUserMock).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('redeem.redeemSuccess')
    expect(showSuccessMock).toHaveBeenCalledWith('redeem.codeRedeemSuccess')
    expect(showErrorMock).not.toHaveBeenCalled()
  })

  it('shows the server redemption error without mutating the session or refreshing the profile', async () => {
    redeemMock.mockRejectedValueOnce({
      response: {
        data: {
          detail: 'This code has already been redeemed'
        }
      }
    })
    const wrapper = mountRedeemView()
    await flushPromises()

    await redeemCode(wrapper)

    expect(authState.user).toMatchObject({ balance: 10, concurrency: 2 })
    expect(refreshUserMock).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('redeem.redeemFailed')
    expect(wrapper.text()).toContain('This code has already been redeemed')
    expect(showErrorMock).toHaveBeenCalledWith('redeem.redeemFailed')
    expect(showSuccessMock).not.toHaveBeenCalled()
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
  })
})
