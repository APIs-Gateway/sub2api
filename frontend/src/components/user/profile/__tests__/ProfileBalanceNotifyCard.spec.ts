import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import ProfileBalanceNotifyCard from '../ProfileBalanceNotifyCard.vue'

const { sendNotifyEmailCode, verifyNotifyEmail, getProfile, removeNotifyEmail, showSuccess, showError } = vi.hoisted(() => ({
  sendNotifyEmailCode: vi.fn(),
  verifyNotifyEmail: vi.fn(),
  getProfile: vi.fn(),
  removeNotifyEmail: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api', () => ({
  userAPI: { sendNotifyEmailCode, verifyNotifyEmail, getProfile, removeNotifyEmail }
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: null }) }))
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
    sendNotifyEmailCode.mockResolvedValue({})
    getProfile.mockResolvedValue({ balance_notify_extra_emails: [] })
    removeNotifyEmail.mockResolvedValue({})
  })

  afterEach(() => {
    vi.clearAllTimers()
    vi.useRealTimers()
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

  it('clears an active saved verification after removal and same-address re-add', async () => {
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
    expect(button(wrapper, 'profile.balanceNotify.verify').exists()).toBe(true)
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

  it('accepts a later parent re-add after profile and parent confirmed the deletion', async () => {
    const entry = { email: 'saved@example.com', disabled: false, verified: false }
    const wrapper = mount(ProfileBalanceNotifyCard, {
      props: { enabled: true, threshold: null, systemDefaultThreshold: 5, userEmail: '', extraEmails: [entry] }
    })
    await button(wrapper, 'profile.balanceNotify.removeEmail').trigger('click')
    await flushPromises()
    expect(wrapper.text()).not.toContain(entry.email)

    await wrapper.setProps({ extraEmails: [] })
    await wrapper.setProps({ extraEmails: [{ ...entry }] })
    expect(wrapper.text()).toContain(entry.email)
    expect(button(wrapper, 'profile.balanceNotify.verify')).toBeDefined()
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
