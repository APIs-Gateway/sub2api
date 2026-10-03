import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ProfileIdentityBindingsSection from '@/components/user/profile/ProfileIdentityBindingsSection.vue'
import { useAppStore, useAuthStore } from '@/stores'
import { authAPI } from '@/api'
import type { User } from '@/types'

const userApi = vi.hoisted(() => ({ send: vi.fn(), bind: vi.fn() }))
vi.mock('vue-router', () => ({ useRoute: () => ({ fullPath: '/profile' }) }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string, params?: { email?: string }) =>
    params?.email ? `${key}:${params.email}` : key }),
}))
vi.mock('@/api/user', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/api/user')>(),
  sendEmailBindingCode: (...args: unknown[]) => userApi.send(...args),
  bindEmailIdentity: (...args: unknown[]) => userApi.bind(...args),
}))

function createUser(overrides: Partial<User> = {}): User {
  return {
    id: 7, username: 'alice', email: 'alice@example.com', role: 'user', balance: 10,
    concurrency: 2, status: 'active', allowed_groups: null,
    created_at: '2026-04-20T00:00:00Z', updated_at: '2026-04-20T00:00:00Z', ...overrides,
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

let pinia: ReturnType<typeof createPinia>
function mountEmail(user: User | null = createUser()) {
  useAuthStore().user = user
  return mount(ProfileIdentityBindingsSection, { global: { plugins: [pinia] }, props: { user } })
}
type EmailWrapper = ReturnType<typeof mountEmail>
const input = (wrapper: EmailWrapper, field: 'email' | 'code' | 'password') =>
  wrapper.get<HTMLInputElement>(`[data-testid="profile-binding-email-${field === 'email' ? '' : `${field}-`}input"]`)
async function fillDraft(wrapper: EmailWrapper, email = 'draft@example.com') {
  await input(wrapper, 'email').setValue(email)
  await input(wrapper, 'code').setValue('123456')
  await input(wrapper, 'password').setValue('current-password')
}

describe('profile email draft and request ownership', () => {
  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
    vi.useFakeTimers()
    userApi.send.mockReset()
    userApi.bind.mockReset()
    vi.spyOn(authAPI, 'logout').mockResolvedValue(undefined)
  })
  afterEach(async () => {
    await useAuthStore().logout()
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it.each([true, false])('keeps a draft and its code recipient through polling (bound: %s)', async (bound) => {
    const wrapper = mountEmail(createUser({ email_bound: bound }))
    const send = deferred<void>()
    userApi.send.mockReturnValue(send.promise)
    userApi.bind.mockResolvedValue(createUser({ email: 'draft@example.com', email_bound: true }))
    await fillDraft(wrapper)
    await wrapper.get('[data-testid="profile-binding-email-send-code"]').trigger('click')
    await wrapper.setProps({ user: createUser({ email: 'server-refresh@example.com', email_bound: bound, balance: 20 }) })
    expect(input(wrapper, 'email').element.value).toBe('draft@example.com')
    send.resolve()
    await flushPromises()
    await wrapper.get('[data-testid="profile-binding-email-submit"]').trigger('click')
    await flushPromises()
    expect(userApi.send).toHaveBeenCalledWith('draft@example.com')
    expect(userApi.bind).toHaveBeenCalledWith({ email: 'draft@example.com', verify_code: '123456', password: 'current-password' })
    expect(useAuthStore().user?.email).toBe('draft@example.com')
    expect(input(wrapper, 'code').element.value).toBe('')
    expect(input(wrapper, 'password').element.value).toBe('')
    await wrapper.setProps({ user: createUser({ email: 'later-refresh@example.com' }) })
    expect(input(wrapper, 'email').element.value).toBe('later-refresh@example.com')
    wrapper.unmount()
  })

  it('syncs untouched forms and clears synthetic addresses', async () => {
    const wrapper = mountEmail()
    await wrapper.setProps({ user: createUser({ email: 'refreshed@example.com' }) })
    expect(input(wrapper, 'email').element.value).toBe('refreshed@example.com')
    await wrapper.setProps({ user: createUser({ email: 'oauth@linuxdo-connect.invalid' }) })
    expect(input(wrapper, 'email').element.value).toBe('')
    wrapper.unmount()
  })

  it.each(['code', 'password', 'email'] as const)('preserves a draft changed through %s alone', async (field) => {
    const wrapper = mountEmail()
    const value = field === 'email' ? '' : '123456'
    await input(wrapper, field).setValue(value)
    await wrapper.setProps({ user: createUser({ email: 'refreshed@example.com' }) })
    expect(input(wrapper, field).element.value).toBe(value)
    expect(input(wrapper, 'email').element.value).toBe(field === 'email' ? '' : 'alice@example.com')
    wrapper.unmount()
  })

  it('preserves an unedited recipient while sending and a failed binding draft', async () => {
    const wrapper = mountEmail()
    const send = deferred<void>()
    const success = vi.spyOn(useAppStore(), 'showSuccess')
    const error = vi.spyOn(useAppStore(), 'showError')
    userApi.send.mockReturnValue(send.promise)
    await wrapper.get('[data-testid="profile-binding-email-send-code"]').trigger('click')
    await wrapper.setProps({ user: createUser({ email: 'refreshed@example.com' }) })
    send.resolve()
    await flushPromises()
    expect(success).toHaveBeenCalledWith('profile.authBindings.codeSentTo:alice@example.com')
    await fillDraft(wrapper)
    userApi.bind.mockRejectedValue(new Error('Incorrect password'))
    await wrapper.get('[data-testid="profile-binding-email-submit"]').trigger('click')
    await flushPromises()
    expect(error).toHaveBeenCalledWith('Incorrect password')
    await wrapper.setProps({ user: createUser({ balance: 20 }) })
    expect(input(wrapper, 'email').element.value).toBe('draft@example.com')
    expect(input(wrapper, 'code').element.value).toBe('123456')
    expect(input(wrapper, 'password').element.value).toBe('current-password')
    wrapper.unmount()
  })

  async function transitionUser(wrapper: EmailWrapper, transition: 'switch' | 'logout' | 'same-user-session') {
    await useAuthStore().logout()
    if (transition === 'logout') {
      await wrapper.setProps({ user: null })
      return
    }
    const nextUser = createUser({ id: transition === 'switch' ? 8 : 7, email: 'new-session@example.com' })
    vi.spyOn(authAPI, 'login').mockResolvedValue({ access_token: 'new-token', token_type: 'Bearer', user: nextUser })
    await useAuthStore().login({ email: nextUser.email, password: 'password' })
    await wrapper.setProps({ user: useAuthStore().user })
  }

  it.each(['switch', 'logout', 'same-user-session'] as const)('resets sensitive fields on %s', async (transition) => {
    const wrapper = mountEmail()
    await fillDraft(wrapper)
    await transitionUser(wrapper, transition)
    expect(input(wrapper, 'email').element.value).toBe(transition === 'logout' ? '' : 'new-session@example.com')
    expect(input(wrapper, 'code').element.value).toBe('')
    expect(input(wrapper, 'password').element.value).toBe('')
    wrapper.unmount()
  })

  const obsoleteCases = (['send', 'bind'] as const).flatMap(operation =>
    (['switch', 'logout', 'same-user-session'] as const).flatMap(transition =>
      [true, false].map(succeeds => ({ operation, transition, succeeds }))))
  it.each(obsoleteCases)('ignores obsolete $operation (success: $succeeds) after $transition', async ({ operation, transition, succeeds }) => {
    const wrapper = mountEmail()
    const old = deferred<User | undefined>()
    const newer = deferred<User | undefined>()
    const api = operation === 'send' ? userApi.send : userApi.bind
    api.mockReturnValueOnce(old.promise).mockReturnValueOnce(newer.promise)
    const success = vi.spyOn(useAppStore(), 'showSuccess')
    const error = vi.spyOn(useAppStore(), 'showError')
    await fillDraft(wrapper)
    const button = `[data-testid="profile-binding-email-${operation === 'send' ? 'send-code' : 'submit'}"]`
    await wrapper.get(button).trigger('click')
    await transitionUser(wrapper, transition)
    if (transition !== 'logout') {
      await fillDraft(wrapper, 'new-draft@example.com')
      await wrapper.get(button).trigger('click')
    }
    if (succeeds) old.resolve(operation === 'bind' ? createUser({ email: 'obsolete-result@example.com' }) : undefined)
    else old.reject(new Error('obsolete request failed'))
    await flushPromises()
    expect(success).not.toHaveBeenCalled()
    expect(error).not.toHaveBeenCalled()
    expect(useAuthStore().user?.email ?? '').toBe(transition === 'logout' ? '' : 'new-session@example.com')
    expect(input(wrapper, 'email').element.value).toBe(transition === 'logout' ? '' : 'new-draft@example.com')
    expect(input(wrapper, 'code').element.value).toBe(transition === 'logout' ? '' : '123456')
    expect(input(wrapper, 'password').element.value).toBe(transition === 'logout' ? '' : 'current-password')
    expect((wrapper.get(button).element as HTMLButtonElement).disabled).toBe(transition !== 'logout')
    if (transition !== 'logout') {
      newer.reject(new Error('current request failed'))
      await flushPromises()
      expect(error).toHaveBeenCalledWith('current request failed')
      expect((wrapper.get(button).element as HTMLButtonElement).disabled).toBe(false)
    }
    wrapper.unmount()
  })

  it.each(['send', 'bind'] as const)('ignores late %s after unmount', async (operation) => {
    const wrapper = mountEmail()
    const pending = deferred<User | undefined>()
    const success = vi.spyOn(useAppStore(), 'showSuccess')
    const api = operation === 'send' ? userApi.send : userApi.bind
    api.mockReturnValue(pending.promise)
    await fillDraft(wrapper)
    await wrapper.get(`[data-testid="profile-binding-email-${operation === 'send' ? 'send-code' : 'submit'}"]`).trigger('click')
    wrapper.unmount()
    pending.resolve(operation === 'bind' ? createUser({ email: 'obsolete@example.com' }) : undefined)
    await flushPromises()
    expect(success).not.toHaveBeenCalled()
    expect(useAuthStore().user?.email).toBe('alice@example.com')
  })

  it('invalidates a request on a prop account switch even before auth state catches up', async () => {
    const wrapper = mountEmail()
    const pending = deferred<User>()
    const success = vi.spyOn(useAppStore(), 'showSuccess')
    userApi.bind.mockReturnValue(pending.promise)
    await fillDraft(wrapper)
    await wrapper.get('[data-testid="profile-binding-email-submit"]').trigger('click')
    await wrapper.setProps({ user: createUser({ id: 8, email: 'bob@example.com' }) })
    await wrapper.get('[data-testid="profile-binding-email-send-code"]').trigger('click')
    expect(userApi.send).not.toHaveBeenCalled()
    pending.resolve(createUser({ email: 'obsolete@example.com' }))
    await flushPromises()
    expect(success).not.toHaveBeenCalled()
    expect(useAuthStore().user?.email).toBe('alice@example.com')
    expect(input(wrapper, 'code').element.value).toBe('')
    expect(input(wrapper, 'password').element.value).toBe('')
    await wrapper.setProps({ user: null })
    expect(input(wrapper, 'email').element.value).toBe('')
    wrapper.unmount()
  })

  it('rejects a binding result belonging to a different user and releases the current request', async () => {
    const wrapper = mountEmail()
    const success = vi.spyOn(useAppStore(), 'showSuccess')
    userApi.bind.mockResolvedValue(createUser({ id: 8, email: 'bob@example.com' }))
    await fillDraft(wrapper)
    await wrapper.get('[data-testid="profile-binding-email-submit"]').trigger('click')
    await flushPromises()
    expect(useAuthStore().user?.email).toBe('alice@example.com')
    expect(input(wrapper, 'email').element.value).toBe('draft@example.com')
    expect(success).not.toHaveBeenCalled()
    expect((wrapper.get('[data-testid="profile-binding-email-submit"]').element as HTMLButtonElement).disabled).toBe(false)
    wrapper.unmount()
  })
})
