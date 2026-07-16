import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import type { CheckinStatus } from '@/api/user'
import CheckinCard from '../CheckinCard.vue'

const { getCheckinStatus, claimCheckin, refreshUser, setBalance, showSuccess, showError } = vi.hoisted(() => ({
  getCheckinStatus: vi.fn(),
  claimCheckin: vi.fn(),
  refreshUser: vi.fn(),
  setBalance: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api/user', () => ({
  getCheckinStatus,
  claimCheckin
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    cachedPublicSettings: { turnstile_enabled: false },
    showSuccess,
    showError
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ refreshUser, setBalance })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: { amount?: string }) =>
        params?.amount ? `${key}:${params.amount}` : key
    })
  }
})

const claimableStatus: CheckinStatus = {
  enabled: true,
  amount_min: 0.1,
  amount_max: 0.5,
  daily_claimed: false,
  daily_available: true,
  bonus_available: 0,
  bonus_earned_today: 0,
  bonus_claimed_today: 0,
  spend_per_extra: 0,
  today_spend: 0,
  spend_to_next_bonus: 0,
  can_claim: true,
  next_reset_at: '2026-07-13T00:00:00+08:00',
  min_tokens: 0,
  today_tokens: 0,
  tokens_met: true
}

const claimedStatus: CheckinStatus = {
  ...claimableStatus,
  daily_claimed: true,
  daily_available: false,
  can_claim: false
}

describe('CheckinCard', () => {
  beforeEach(() => {
    getCheckinStatus.mockReset()
    claimCheckin.mockReset()
    refreshUser.mockReset()
    setBalance.mockReset()
    showSuccess.mockReset()
    showError.mockReset()
    getCheckinStatus.mockResolvedValue(claimableStatus)
    claimCheckin.mockResolvedValue({ type: 'daily', amount: 0.25, status: claimedStatus })
    refreshUser.mockResolvedValue(undefined)
  })

  it('keeps a committed checkin successful when the follow-up user refresh fails', async () => {
    refreshUser.mockRejectedValueOnce(new Error('refresh timeout'))
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)

    const wrapper = mount(CheckinCard, {
      global: { stubs: { TurnstileWidget: true } }
    })
    await flushPromises()

    await wrapper.get('button').trigger('click')
    await flushPromises()

    expect(claimCheckin).toHaveBeenCalledTimes(1)
    expect(showSuccess).toHaveBeenCalledWith('checkin.claimedToast:0.25')
    expect(showError).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('checkin.doneToday')
    warn.mockRestore()
  })

  it('updates the displayed balance from the successful claim response', async () => {
    claimCheckin.mockResolvedValueOnce({
      type: 'daily',
      amount: 0.25,
      balance: 12.75,
      status: claimedStatus
    })

    const wrapper = mount(CheckinCard, {
      global: { stubs: { TurnstileWidget: true } }
    })
    await flushPromises()

    await wrapper.get('button').trigger('click')
    await flushPromises()

    expect(setBalance).toHaveBeenCalledWith(12.75)
  })

  it('still reports an actual claim request failure', async () => {
    claimCheckin.mockRejectedValueOnce(new Error('claim rejected'))
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)

    const wrapper = mount(CheckinCard, {
      global: { stubs: { TurnstileWidget: true } }
    })
    await flushPromises()

    await wrapper.get('button').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('checkin.claimFailed')
    expect(showSuccess).not.toHaveBeenCalled()
    warn.mockRestore()
  })

  it('reconciles a lost success response without retrying the claim', async () => {
    claimCheckin.mockRejectedValueOnce(new Error('response lost'))
    getCheckinStatus
      .mockResolvedValueOnce(claimableStatus)
      .mockResolvedValueOnce(claimedStatus)
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)

    const wrapper = mount(CheckinCard, {
      global: { stubs: { TurnstileWidget: true } }
    })
    await flushPromises()

    await wrapper.get('button').trigger('click')
    await flushPromises()

    expect(claimCheckin).toHaveBeenCalledTimes(1)
    expect(getCheckinStatus).toHaveBeenCalledTimes(2)
    expect(showSuccess).toHaveBeenCalledWith('checkin.claimRecoveredToast')
    expect(showError).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('checkin.doneToday')
    warn.mockRestore()
  })
})
