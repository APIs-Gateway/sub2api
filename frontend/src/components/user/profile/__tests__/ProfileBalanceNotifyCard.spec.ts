import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { reactive } from 'vue'
import ProfileBalanceNotifyCard from '../ProfileBalanceNotifyCard.vue'

const { sendNotifyEmailCode, verifyNotifyEmail, getProfile, removeNotifyEmail, updateProfile, toggleNotifyEmail, showSuccess, showError } = vi.hoisted(() => ({
  sendNotifyEmailCode: vi.fn(),
  verifyNotifyEmail: vi.fn(),
  getProfile: vi.fn(),
  removeNotifyEmail: vi.fn(),
  updateProfile: vi.fn(),
  toggleNotifyEmail: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api', () => ({
  userAPI: { sendNotifyEmailCode, verifyNotifyEmail, getProfile, removeNotifyEmail, updateProfile, toggleNotifyEmail }
}))
const authStore = reactive({
  user: null,
  profileRefreshVersion: 0,
  authSessionVersion: 0,
  applyUserProfile: vi.fn(),
  invalidateUserRefresh: vi.fn(),
  refreshUser: vi.fn()
})
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authStore }))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError })
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

enableAutoUnmount(afterEach)

const deferred = () => {
  let resolve!: () => void
  let reject!: (error: Error) => void
  const promise = new Promise<void>((done, fail) => { resolve = done; reject = fail })
  return { promise, resolve, reject }
}

const pendingRows = (wrapper: VueWrapper) => wrapper.findAll('.bg-yellow-50')
const pendingEmails = (wrapper: VueWrapper) => pendingRows(wrapper).map(row => row.get('span').text())

const button = (wrapper: VueWrapper, text: string) =>
  wrapper.findAll('button').find(item => item.text() === text)!

describe('ProfileBalanceNotifyCard', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.resetAllMocks()
    authStore.profileRefreshVersion = 0
    authStore.authSessionVersion = 0
    sendNotifyEmailCode.mockResolvedValue({})
    getProfile.mockResolvedValue({ balance_notify_extra_emails: [] })
    removeNotifyEmail.mockResolvedValue({})
  })

  afterEach(() => {
    vi.clearAllTimers()
    vi.useRealTimers()
  })

  it.each([
    ['pending', 'success'], ['pending', 'failure'],
    ['saved', 'success'], ['saved', 'failure']
  ] as const)('drops an old %s send %s after the auth session changes', async (kind, outcome) => {
    const entry = { email: 'shared@example.com', disabled: false, verified: false }
    const request = deferred()
    sendNotifyEmailCode.mockReturnValueOnce(request.promise)
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: {
        enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '',
        extraEmails: kind === 'saved' ? [entry] : []
      }
    })
    if (kind === 'pending') {
      await wrapper.get('input[type="email"]').setValue(entry.email)
      await button(wrapper, 'common.add').trigger('click')
      await button(wrapper, 'profile.balanceNotify.sendCode').trigger('click')
    } else {
      await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    }

    // Keep this component mounted and reuse the same address for B's saved row.
    authStore.authSessionVersion++
    await wrapper.setProps({ extraEmails: kind === 'saved' ? [{ ...entry }] : [] })
    expect(pendingRows(wrapper)).toHaveLength(0)
    expect(vi.getTimerCount()).toBe(0)

    if (outcome === 'success') request.resolve()
    else request.reject(new Error('old send failed'))
    await flushPromises()

    expect(pendingRows(wrapper)).toHaveLength(0)
    expect(wrapper.find('input[maxlength="6"]').exists()).toBe(false)
    expect(vi.getTimerCount()).toBe(0)
    expect(showSuccess).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
    if (kind === 'saved') {
      expect(wrapper.text()).toContain(entry.email)
      expect(wrapper.text()).toContain('profile.balanceNotify.unverified')
    } else {
      expect(wrapper.text()).not.toContain(entry.email)
    }
  })

  it.each([
    ['enable toggle', 'success'], ['enable toggle', 'failure'],
    ['threshold save', 'success'], ['threshold save', 'failure'],
    ['saved email toggle', 'success'], ['saved email toggle', 'failure'],
  ] as const)('ignores an old %s %s after account B replaces A', async (operation, outcome) => {
    const oldEntry = { email: 'account-a@example.com', disabled: false, verified: true }
    const newEntry = { email: 'account-b@example.com', disabled: false, verified: true }
    const request = deferred()
    if (operation === 'saved email toggle') toggleNotifyEmail.mockReturnValueOnce(request.promise)
    else updateProfile.mockReturnValueOnce(request.promise)
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: 5, systemDefaultThreshold: 5, userEmail: '', extraEmails: [oldEntry] }
    })

    if (operation === 'enable toggle') {
      await wrapper.findAll('input[type="checkbox"]')[0]!.setValue(false)
      expect(updateProfile).toHaveBeenCalledWith({ balance_notify_enabled: false })
    } else if (operation === 'threshold save') {
      await wrapper.get('input[type="number"]').setValue('9')
      await button(wrapper, 'common.save').trigger('click')
      expect(updateProfile).toHaveBeenCalledWith({ balance_notify_threshold: 9 })
    } else {
      await wrapper.findAll('input[type="checkbox"]')[1]!.setValue(false)
      expect(toggleNotifyEmail).toHaveBeenCalledWith(oldEntry.email, true)
    }

    authStore.authSessionVersion++
    await wrapper.setProps({ extraEmails: [newEntry], threshold: 11 })
    if (outcome === 'success') request.resolve()
    else request.reject(new Error('old account request failed'))
    await flushPromises()

    expect(authStore.applyUserProfile).not.toHaveBeenCalled()
    expect(showSuccess).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
    if (operation !== 'enable toggle') {
      expect(wrapper.text()).toContain(newEntry.email)
      expect(wrapper.text()).not.toContain(oldEntry.email)
    }
  })

  it.each(['success', 'failure'] as const)('ignores a removed pending email\'s late %s', async (outcome) => {
    const request = deferred()
    sendNotifyEmailCode.mockReturnValueOnce(request.promise)
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, extraEmails: [], systemDefaultThreshold: 5, userEmail: '' }
    })
    await wrapper.get('input[type="email"]').setValue('new@example.com')
    await button(wrapper, 'common.add').trigger('click')
    await button(wrapper, 'profile.balanceNotify.sendCode').trigger('click')
    await button(wrapper, 'profile.balanceNotify.removeEmail').trigger('click')
    // Reusing the address must not let the old request mutate its new row.
    await wrapper.get('input[type="email"]').setValue('new@example.com')
    await button(wrapper, 'common.add').trigger('click')

    if (outcome === 'success') request.resolve()
    else request.reject(new Error('obsolete send'))
    await flushPromises()
    expect(button(wrapper, 'profile.balanceNotify.sendCode').exists()).toBe(true)
    expect(vi.getTimerCount()).toBe(0)
    expect(showSuccess).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
  })

  it.each(['success', 'failure'] as const)('ignores a pending email send after unmount: %s', async (outcome) => {
    const request = deferred()
    sendNotifyEmailCode.mockReturnValueOnce(request.promise)
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, extraEmails: [], systemDefaultThreshold: 5, userEmail: '' }
    })
    await wrapper.get('input[type="email"]').setValue('new@example.com')
    await button(wrapper, 'common.add').trigger('click')
    await button(wrapper, 'profile.balanceNotify.sendCode').trigger('click')
    wrapper.unmount()

    if (outcome === 'success') request.resolve()
    else request.reject(new Error('obsolete send'))
    await flushPromises()
    expect(vi.getTimerCount()).toBe(0)
    expect(showSuccess).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
  })

  it.each(['success', 'failure'] as const)('ignores a saved email send after removal: %s', async (outcome) => {
    const request = deferred()
    sendNotifyEmailCode.mockReturnValueOnce(request.promise)
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: {
        enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '',
        extraEmails: [{ email: 'saved@example.com', disabled: false, verified: false }]
      }
    })
    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    await button(wrapper, 'profile.balanceNotify.removeEmail').trigger('click')
    await flushPromises()
    showSuccess.mockClear()
    showError.mockClear()

    if (outcome === 'success') request.resolve()
    else request.reject(new Error('obsolete send'))
    await flushPromises()
    expect(vi.getTimerCount()).toBe(0)
    expect(showSuccess).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
  })

  it.each(['success', 'failure'] as const)('ignores a saved email send after unmount: %s', async (outcome) => {
    const request = deferred()
    sendNotifyEmailCode.mockReturnValueOnce(request.promise)
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: {
        enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '',
        extraEmails: [{ email: 'saved@example.com', disabled: false, verified: false }]
      }
    })
    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    wrapper.unmount()

    if (outcome === 'success') request.resolve()
    else request.reject(new Error('obsolete send'))
    await flushPromises()
    expect(vi.getTimerCount()).toBe(0)
    expect(showSuccess).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
  })

  it('keeps a saved send valid when the parent refreshes the same email entry', async () => {
    const request = deferred()
    sendNotifyEmailCode.mockReturnValueOnce(request.promise)
    const entry = { email: 'saved@example.com', disabled: false, verified: false }
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '', extraEmails: [entry] }
    })
    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    await wrapper.setProps({ extraEmails: [{ ...entry }] })
    request.resolve()
    await flushPromises()
    expect(wrapper.find('input[maxlength="6"]').exists()).toBe(true)
    expect(vi.getTimerCount()).toBe(1)
    expect(showSuccess).toHaveBeenCalledWith('profile.balanceNotify.codeSent')
  })

  it('invalidates a saved send if its email disappears and then reappears via props', async () => {
    const request = deferred()
    sendNotifyEmailCode.mockReturnValueOnce(request.promise)
    const entry = { email: 'saved@example.com', disabled: false, verified: false }
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '', extraEmails: [entry] }
    })
    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    await wrapper.setProps({ extraEmails: [] })
    await wrapper.setProps({ extraEmails: [{ ...entry }] })
    request.resolve()
    await flushPromises()
    expect(wrapper.find('input[maxlength="6"]').exists()).toBe(false)
    expect(vi.getTimerCount()).toBe(0)
    expect(showSuccess).not.toHaveBeenCalled()
  })

  it('clears an active saved verification and ignores a stale same-address parent row', async () => {
    const entry = { email: 'saved@example.com', disabled: false, verified: false }
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '', extraEmails: [entry] }
    })
    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    await flushPromises()
    await wrapper.get('input[maxlength="6"]').setValue('123456')
    expect(vi.getTimerCount()).toBe(1)

    await button(wrapper, 'profile.balanceNotify.removeEmail').trigger('click')
    await flushPromises()
    expect(vi.getTimerCount()).toBe(0)
    await wrapper.setProps({ extraEmails: [] })
    await wrapper.setProps({ extraEmails: [{ ...entry }] })
    expect(wrapper.find('input[maxlength="6"]').exists()).toBe(false)
    expect(button(wrapper, 'profile.balanceNotify.verify')).toBeUndefined()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('keeps an in-flight saved send valid when removing its email fails', async () => {
    const request = deferred()
    sendNotifyEmailCode.mockReturnValueOnce(request.promise)
    removeNotifyEmail.mockRejectedValueOnce(new Error('remove failed'))
    const entry = { email: 'saved@example.com', disabled: false, verified: false }
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '', extraEmails: [entry] }
    })
    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    await button(wrapper, 'profile.balanceNotify.removeEmail').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('saved@example.com')

    request.resolve()
    await flushPromises()
    expect(wrapper.find('input[maxlength="6"]').exists()).toBe(true)
    expect(vi.getTimerCount()).toBe(1)
    expect(showSuccess).toHaveBeenCalledWith('profile.balanceNotify.codeSent')
  })

  it('ignores a saved send started while removal is pending after removal succeeds', async () => {
    const removal = deferred()
    const send = deferred()
    const entry = { email: 'saved@example.com', disabled: false, verified: false }
    let finishProfile!: (value: { balance_notify_extra_emails: typeof entry[] }) => void
    const profile = new Promise<{ balance_notify_extra_emails: typeof entry[] }>(resolve => { finishProfile = resolve })
    removeNotifyEmail.mockReturnValueOnce(removal.promise)
    sendNotifyEmailCode.mockReturnValueOnce(send.promise)
    getProfile.mockReturnValueOnce(profile)
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '', extraEmails: [entry] }
    })
    await button(wrapper, 'profile.balanceNotify.removeEmail').trigger('click')
    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    expect(sendNotifyEmailCode).toHaveBeenCalledWith(entry.email)

    removal.resolve()
    await flushPromises()
    expect(wrapper.text()).not.toContain(entry.email)
    // An older parent profile refresh must not restore the confirmed deletion.
    await wrapper.setProps({ extraEmails: [{ ...entry }] })
    expect(wrapper.text()).not.toContain(entry.email)
    expect(button(wrapper, 'profile.balanceNotify.verify')).toBeUndefined()
    showSuccess.mockClear()

    send.resolve()
    await flushPromises()
    expect(wrapper.find('input[maxlength="6"]').exists()).toBe(false)
    expect(vi.getTimerCount()).toBe(0)
    expect(showSuccess).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()

    finishProfile({ balance_notify_extra_emails: [] })
    await flushPromises()
    await wrapper.setProps({ extraEmails: [{ ...entry }] })
    expect(wrapper.text()).not.toContain(entry.email)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('allows a deliberately re-added email through the pending verification flow', async () => {
    const entry = { email: 'saved@example.com', disabled: false, verified: false }
    const verified = { ...entry, verified: true }
    let finishOldProfile!: (value: { balance_notify_extra_emails: typeof entry[] }) => void
    const oldProfile = new Promise<{ balance_notify_extra_emails: typeof entry[] }>(resolve => { finishOldProfile = resolve })
    getProfile.mockReturnValueOnce(oldProfile)
      .mockResolvedValueOnce({ balance_notify_extra_emails: [verified] })
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '', extraEmails: [entry] }
    })
    await button(wrapper, 'profile.balanceNotify.removeEmail').trigger('click')
    await flushPromises()
    await wrapper.get('input[type="email"]').setValue(entry.email)
    await button(wrapper, 'common.add').trigger('click')
    await button(wrapper, 'profile.balanceNotify.sendCode').trigger('click')
    await flushPromises()
    await pendingRows(wrapper)[0]!.get('input').setValue('123456')
    await pendingRows(wrapper)[0]!.findAll('button').find(item => item.text() === 'profile.balanceNotify.verify')!.trigger('click')
    await flushPromises()

    expect(pendingRows(wrapper)).toHaveLength(0)
    expect(wrapper.text()).toContain(entry.email)
    expect(wrapper.text()).toContain('profile.balanceNotify.verified')

    finishOldProfile({ balance_notify_extra_emails: [] })
    await flushPromises()
    expect(wrapper.text()).toContain(entry.email)
    expect(wrapper.text()).toContain('profile.balanceNotify.verified')
  })

  it('keeps an explicitly re-verified address visible when the follow-up profile refresh fails', async () => {
    const entry = { email: 'saved@example.com', disabled: false, verified: false }
    getProfile.mockResolvedValueOnce({ balance_notify_extra_emails: [] })
      .mockRejectedValueOnce(new Error('profile offline'))
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '', extraEmails: [entry] }
    })
    await button(wrapper, 'profile.balanceNotify.removeEmail').trigger('click')
    await flushPromises()
    await wrapper.get('input[type="email"]').setValue(entry.email)
    await button(wrapper, 'common.add').trigger('click')
    await button(wrapper, 'profile.balanceNotify.sendCode').trigger('click')
    await flushPromises()
    await pendingRows(wrapper)[0]!.get('input').setValue('123456')
    await pendingRows(wrapper)[0]!.findAll('button').find(item => item.text() === 'profile.balanceNotify.verify')!.trigger('click')
    await flushPromises()

    expect(pendingRows(wrapper)).toHaveLength(0)
    expect(wrapper.text()).toContain(entry.email)
    expect(wrapper.text()).toContain('profile.balanceNotify.verified')
    expect(showSuccess).toHaveBeenCalledWith('profile.balanceNotify.verifySuccess')
    expect(showError).not.toHaveBeenCalled()

    await wrapper.setProps({ extraEmails: [] })
    expect(wrapper.text()).toContain('profile.balanceNotify.verified')
    await wrapper.setProps({ extraEmails: [{ ...entry }] })
    expect(wrapper.text()).toContain('profile.balanceNotify.verified')
    expect(button(wrapper, 'profile.balanceNotify.verify')).toBeUndefined()

    // The next successful auth refresh is authoritative, even if another tab removed A.
    await wrapper.setProps({ extraEmails: [] })
    authStore.profileRefreshVersion++
    await flushPromises()
    expect(wrapper.text()).not.toContain(entry.email)
    expect(button(wrapper, 'common.add').exists()).toBe(true)
  })

  it('drops a confirmed email after a later authoritative removal', async () => {
    const entry = { email: 'saved@example.com', disabled: false, verified: false }
    const verified = { ...entry, verified: true }
    getProfile.mockResolvedValueOnce({ balance_notify_extra_emails: [verified] })
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '', extraEmails: [entry] }
    })
    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    await flushPromises()
    await wrapper.get('input[maxlength="6"]').setValue('123456')
    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('profile.balanceNotify.verified')

    await wrapper.setProps({ extraEmails: [] })
    expect(wrapper.text()).not.toContain(entry.email)
    expect(button(wrapper, 'common.add').exists()).toBe(true)

    wrapper.unmount()
    const remounted = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '', extraEmails: [] }
    })
    expect(remounted.text()).not.toContain(entry.email)
  })

  it('does not restore an email from verification profile data older than a fresh auth request', async () => {
    const entry = { email: 'saved@example.com', disabled: false, verified: false }
    const verified = { ...entry, verified: true }
    let finishOldProfile!: (value: { balance_notify_extra_emails: typeof entry[] }) => void
    getProfile.mockReturnValueOnce(new Promise(resolve => { finishOldProfile = resolve }))
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '', extraEmails: [entry] }
    })
    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    await flushPromises()
    await wrapper.get('input[maxlength="6"]').setValue('123456')
    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('profile.balanceNotify.verified')

    // Another tab removes A; its later auth request completes before ours.
    await wrapper.setProps({ extraEmails: [] })
    authStore.profileRefreshVersion++
    await flushPromises()
    expect(wrapper.text()).not.toContain(entry.email)

    finishOldProfile({ balance_notify_extra_emails: [verified] })
    await flushPromises()
    expect(wrapper.text()).not.toContain(entry.email)
    expect(authStore.applyUserProfile).not.toHaveBeenCalled()
  })

  it('uses a successful verification profile after a newer auth refresh fails', async () => {
    const entry = { email: 'saved@example.com', disabled: false, verified: false }
    const verified = { ...entry, verified: true }
    let finishProfile!: (value: { balance_notify_extra_emails: typeof entry[] }) => void
    getProfile.mockReturnValueOnce(new Promise(resolve => { finishProfile = resolve }))
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '', extraEmails: [entry] }
    })
    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    await flushPromises()
    await wrapper.get('input[maxlength="6"]').setValue('123456')
    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    await flushPromises()

    // An auth refresh started later but failed, so it never became authority.
    authStore.refreshUser.mockRejectedValueOnce(new Error('offline'))
    await expect(authStore.refreshUser()).rejects.toThrow('offline')
    finishProfile({ balance_notify_extra_emails: [verified] })
    await flushPromises()
    expect(authStore.applyUserProfile).toHaveBeenCalledWith(
      { balance_notify_extra_emails: [verified] }, 0, true
    )
    expect(wrapper.text()).toContain('profile.balanceNotify.verified')
  })

  it('ignores an old account verification profile after a new login', async () => {
    const entry = { email: 'saved@example.com', disabled: false, verified: false }
    let finishOldProfile!: (value: { balance_notify_extra_emails: typeof entry[] }) => void
    getProfile.mockReturnValueOnce(new Promise(resolve => { finishOldProfile = resolve }))
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '', extraEmails: [entry] }
    })
    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    await flushPromises()
    await wrapper.get('input[maxlength="6"]').setValue('123456')
    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    await flushPromises()

    authStore.authSessionVersion++
    await wrapper.setProps({ extraEmails: [] })
    finishOldProfile({ balance_notify_extra_emails: [{ ...entry, verified: true }] })
    await flushPromises()
    expect(authStore.applyUserProfile).not.toHaveBeenCalled()
  })

  it('ignores a verification profile that resolves after unmount', async () => {
    const entry = { email: 'saved@example.com', disabled: false, verified: false }
    let finishOldProfile!: (value: { balance_notify_extra_emails: typeof entry[] }) => void
    getProfile.mockReturnValueOnce(new Promise(resolve => { finishOldProfile = resolve }))
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '', extraEmails: [entry] }
    })
    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    await flushPromises()
    await wrapper.get('input[maxlength="6"]').setValue('123456')
    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    await flushPromises()

    wrapper.unmount()
    finishOldProfile({ balance_notify_extra_emails: [{ ...entry, verified: true }] })
    await flushPromises()
    expect(authStore.applyUserProfile).not.toHaveBeenCalled()
  })

  it.each(['pending verification', 'saved removal'] as const)(
    'ignores an old account profile after %s and a new login', async (operation) => {
      const entry = { email: 'saved@example.com', disabled: false, verified: false }
      let finishOldProfile!: (value: { balance_notify_extra_emails: typeof entry[] }) => void
      getProfile.mockReturnValueOnce(new Promise(resolve => { finishOldProfile = resolve }))
      const wrapper = mount(ProfileBalanceNotifyCard, {
        props: {
          enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '',
          extraEmails: operation === 'saved removal' ? [entry] : []
        }
      })
      if (operation === 'pending verification') {
        await wrapper.get('input[type="email"]').setValue(entry.email)
        await button(wrapper, 'common.add').trigger('click')
        await button(wrapper, 'profile.balanceNotify.sendCode').trigger('click')
        await flushPromises()
        await pendingRows(wrapper)[0]!.get('input').setValue('123456')
        await pendingRows(wrapper)[0]!.findAll('button').find(item => item.text() === 'profile.balanceNotify.verify')!.trigger('click')
      } else {
        await button(wrapper, 'profile.balanceNotify.removeEmail').trigger('click')
      }
      await flushPromises()

      authStore.authSessionVersion++
      await wrapper.setProps({ extraEmails: [] })
      finishOldProfile({ balance_notify_extra_emails: [{ ...entry, verified: true }] })
      await flushPromises()
      expect(authStore.applyUserProfile).not.toHaveBeenCalled()
    }
  )

  it('ignores an old parent profile after a newer empty list confirmed deletion', async () => {
    const entry = { email: 'saved@example.com', disabled: false, verified: false }
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '', extraEmails: [entry] }
    })
    await button(wrapper, 'profile.balanceNotify.removeEmail').trigger('click')
    await flushPromises()
    expect(wrapper.text()).not.toContain(entry.email)

    await wrapper.setProps({ extraEmails: [] })
    await wrapper.setProps({ extraEmails: [{ ...entry }] })
    expect(wrapper.text()).not.toContain(entry.email)
    expect(button(wrapper, 'profile.balanceNotify.verify')).toBeUndefined()
    expect(vi.getTimerCount()).toBe(0)

    wrapper.unmount()
    const remounted = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '', extraEmails: [{ ...entry }] }
    })
    expect(remounted.text()).toContain(entry.email)
  })

  it('shows an externally re-added address only after a fresh profile refresh', async () => {
    const entry = { email: 'saved@example.com', disabled: false, verified: false }
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '', extraEmails: [entry] }
    })
    await button(wrapper, 'profile.balanceNotify.removeEmail').trigger('click')
    await flushPromises()
    expect(authStore.invalidateUserRefresh).toHaveBeenCalled()

    // An old parent snapshot cannot undo the confirmed deletion.
    await wrapper.setProps({ extraEmails: [{ ...entry }] })
    expect(wrapper.text()).not.toContain(entry.email)

    // A later successful auth refresh is current server state, so A can reappear.
    authStore.profileRefreshVersion++
    await flushPromises()
    expect(wrapper.text()).toContain(entry.email)
    expect(wrapper.text()).toContain('profile.balanceNotify.unverified')
  })

  it('does not hide a fresh external readdition with an older removal profile response', async () => {
    const entry = { email: 'saved@example.com', disabled: false, verified: false }
    let finishOldProfile!: (value: { balance_notify_extra_emails: typeof entry[] }) => void
    getProfile.mockReturnValueOnce(new Promise(resolve => { finishOldProfile = resolve }))
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '', extraEmails: [entry] }
    })
    await button(wrapper, 'profile.balanceNotify.removeEmail').trigger('click')
    await flushPromises()
    expect(wrapper.text()).not.toContain(entry.email)

    await wrapper.setProps({ extraEmails: [{ ...entry }] })
    authStore.profileRefreshVersion++
    await flushPromises()
    expect(wrapper.text()).toContain(entry.email)

    finishOldProfile({ balance_notify_extra_emails: [] })
    await flushPromises()
    expect(wrapper.text()).toContain(entry.email)
    expect(authStore.applyUserProfile).not.toHaveBeenCalled()
  })

  it('clears an active saved verification when parent props remove and re-add its email', async () => {
    const entry = { email: 'saved@example.com', disabled: false, verified: false }
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '', extraEmails: [entry] }
    })
    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    await flushPromises()
    await wrapper.get('input[maxlength="6"]').setValue('123456')
    expect(vi.getTimerCount()).toBe(1)

    await wrapper.setProps({ extraEmails: [] })
    expect(vi.getTimerCount()).toBe(0)
    await wrapper.setProps({ extraEmails: [{ ...entry }] })
    expect(wrapper.find('input[maxlength="6"]').exists()).toBe(false)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('clears the active saved verification timer and code on cancel', async () => {
    const entry = { email: 'saved@example.com', disabled: false, verified: false }
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '', extraEmails: [entry] }
    })
    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    await flushPromises()
    await wrapper.get('input[maxlength="6"]').setValue('123456')
    expect(vi.getTimerCount()).toBe(1)

    await button(wrapper, 'common.cancel').trigger('click')
    expect(wrapper.find('input[maxlength="6"]').exists()).toBe(false)
    expect(vi.getTimerCount()).toBe(0)

    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    await flushPromises()
    expect((wrapper.get('input[maxlength="6"]').element as HTMLInputElement).value).toBe('')
    expect(vi.getTimerCount()).toBe(1)
  })

  it.each(['success', 'failure'] as const)('does not reopen cancelled saved email verification after a late resend: %s', async (outcome) => {
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: {
        enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '',
        extraEmails: [{ email: 'saved@example.com', disabled: false, verified: false }]
      }
    })
    await button(wrapper, 'profile.balanceNotify.verify').trigger('click')
    await flushPromises()
    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()
    const request = deferred()
    sendNotifyEmailCode.mockReturnValueOnce(request.promise)
    showSuccess.mockClear()
    showError.mockClear()
    await button(wrapper, 'profile.balanceNotify.resend').trigger('click')
    await button(wrapper, 'common.cancel').trigger('click')

    if (outcome === 'success') request.resolve()
    else request.reject(new Error('obsolete resend'))
    await flushPromises()
    expect(wrapper.find('input[maxlength="6"]').exists()).toBe(false)
    expect(vi.getTimerCount()).toBe(0)
    expect(showSuccess).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
  })

  it.each([0, 1])('removes only verified emails when request %i finishes first', async (first) => {
    const emails = ['first@example.com', 'second@example.com', 'third@example.com']
    const requests = [deferred(), deferred()]
    verifyNotifyEmail.mockImplementation((email: string) => requests[emails.indexOf(email)]!.promise)
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, extraEmails: [], systemDefaultThreshold: 5, userEmail: '' }
    })

    for (const email of emails) {
      await wrapper.get('input[type="email"]').setValue(email)
      await button(wrapper, 'common.add').trigger('click')
    }
    for (const row of pendingRows(wrapper)) {
      await row.findAll('button').find(item => item.text() === 'profile.balanceNotify.sendCode')!.trigger('click')
    }
    await flushPromises()
    for (const row of pendingRows(wrapper)) {
      await row.get('input').setValue('123456')
    }
    for (const row of pendingRows(wrapper).slice(0, 2)) {
      await row.findAll('button').find(item => item.text() === 'profile.balanceNotify.verify')!.trigger('click')
    }
    expect(verifyNotifyEmail.mock.calls).toEqual(emails.slice(0, 2).map(email => [email, '123456']))

    requests[first]!.resolve()
    await flushPromises()
    expect(pendingEmails(wrapper)).toEqual(emails.filter((_, index) => index !== first))

    requests[1 - first]!.resolve()
    await flushPromises()
    expect(pendingEmails(wrapper)).toEqual([emails[2]])
    expect((pendingRows(wrapper)[0]!.get('input').element as HTMLInputElement).value).toBe('123456')
    expect(getProfile).toHaveBeenCalledTimes(2)
  })
})
