import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { defineComponent, reactive } from 'vue'
import ProfileEditForm from '../ProfileEditForm.vue'

const mocks = vi.hoisted(() => ({ updateProfile: vi.fn(), showError: vi.fn(), showSuccess: vi.fn(), state: { user: { username: 'alice' } } }))
vi.mock('@/api', () => ({ userAPI: { updateProfile: mocks.updateProfile } }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => reactive(mocks.state) }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: mocks.showError, showSuccess: mocks.showSuccess }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.resetAllMocks()
  mocks.state.user = { username: 'alice' }
})

const Harness = defineComponent({
  components: { ProfileEditForm },
  setup: () => ({ auth: reactive(mocks.state) }),
  template: '<ProfileEditForm :initial-username="auth.user.username" embedded />'
})

describe('profile username draft', () => {
  it.each(['next-draft', 'alice'])('preserves %s typed while the previous username is being saved', async (draft) => {
    let finish!: (value: unknown) => void
    mocks.updateProfile.mockReturnValue(new Promise(resolve => { finish = resolve }))
    const wrapper = mount(Harness)
    await wrapper.get('#username').setValue('submitted')
    await wrapper.get('form').trigger('submit')
    expect(mocks.updateProfile).toHaveBeenCalledWith({ username: 'submitted' })
    await wrapper.get('#username').setValue(draft)
    finish({ username: 'submitted' })
    await flushPromises()
    expect(mocks.state.user.username).toBe('submitted')
    expect((wrapper.get('#username').element as HTMLInputElement).value).toBe(draft)
  })

  it('still synchronizes a pristine input with profile updates', async () => {
    const wrapper = mount(Harness)
    reactive(mocks.state).user = { username: 'updated' }
    await flushPromises()
    expect((wrapper.get('#username').element as HTMLInputElement).value).toBe('updated')
  })

  it('shows the server username after saving without further edits', async () => {
    mocks.updateProfile.mockResolvedValue({ username: 'saved-name' })
    const wrapper = mount(Harness)
    await wrapper.get('#username').setValue(' saved-name ')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect((wrapper.get('#username').element as HTMLInputElement).value).toBe('saved-name')
  })

  it('keeps a dirty draft on an external refresh, then synchronizes once pristine again', async () => {
    const wrapper = mount(Harness)
    await wrapper.get('#username').setValue('local-draft')
    reactive(mocks.state).user = { username: 'external-name' }
    await flushPromises()
    expect((wrapper.get('#username').element as HTMLInputElement).value).toBe('local-draft')
    await wrapper.get('#username').setValue('external-name')
    reactive(mocks.state).user = { username: 'next-external-name' }
    await flushPromises()
    expect((wrapper.get('#username').element as HTMLInputElement).value).toBe('next-external-name')
  })

  it('keeps an edited draft after failure and allows retrying that draft', async () => {
    let reject!: (reason: unknown) => void
    mocks.updateProfile.mockReturnValueOnce(new Promise((_resolve, fail) => { reject = fail }))
    const wrapper = mount(Harness)
    await wrapper.get('#username').setValue('submitted')
    await wrapper.get('form').trigger('submit')
    await wrapper.get('#username').setValue('retry-draft')
    reject({ response: { data: { detail: 'backend failure' } } })
    await flushPromises()
    expect((wrapper.get('#username').element as HTMLInputElement).value).toBe('retry-draft')
    expect(mocks.state.user.username).toBe('alice')
    expect(mocks.showError).toHaveBeenCalledWith('backend failure')
    expect(mocks.showSuccess).not.toHaveBeenCalled()
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
    mocks.updateProfile.mockResolvedValueOnce({ username: 'retry-draft' })
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocks.updateProfile).toHaveBeenLastCalledWith({ username: 'retry-draft' })
    expect(mocks.state.user.username).toBe('retry-draft')
    expect(mocks.showSuccess).toHaveBeenCalledTimes(1)
  })

  it('tracks the latest saved baseline across consecutive saves and server normalization', async () => {
    mocks.updateProfile.mockResolvedValueOnce({ username: 'first-saved' })
    const wrapper = mount(Harness)
    await wrapper.get('#username').setValue(' first-saved ')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect((wrapper.get('#username').element as HTMLInputElement).value).toBe('first-saved')
    let finish!: (value: unknown) => void
    mocks.updateProfile.mockReturnValueOnce(new Promise(resolve => { finish = resolve }))
    await wrapper.get('#username').setValue('second-submitted')
    await wrapper.get('form').trigger('submit')
    await wrapper.get('#username').setValue('first-saved')
    finish({ username: 'second-saved' })
    await flushPromises()
    expect(mocks.state.user.username).toBe('second-saved')
    expect((wrapper.get('#username').element as HTMLInputElement).value).toBe('first-saved')
    expect(mocks.updateProfile.mock.calls).toEqual([
      [{ username: ' first-saved ' }], [{ username: 'second-submitted' }]
    ])
    expect(mocks.showSuccess).toHaveBeenCalledTimes(2)
  })

  it('rejects a blank draft without sending an update or changing the saved profile', async () => {
    const wrapper = mount(Harness)
    await wrapper.get('#username').setValue('   ')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(mocks.updateProfile).not.toHaveBeenCalled()
    expect(mocks.showError).toHaveBeenCalledWith('profile.usernameRequired')
    expect(mocks.state.user.username).toBe('alice')
  })
})
