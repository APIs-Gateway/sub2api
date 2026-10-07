import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import type { AdminGroup } from '@/types'
import GroupRateMultipliersModal from '../GroupRateMultipliersModal.vue'

const mocks = vi.hoisted(() => ({ getGroupRateMultipliers: vi.fn(), batchSetGroupRateMultipliers: vi.fn(), showError: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { groups: mocks } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: mocks.showError, showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)
beforeEach(() => vi.resetAllMocks())
afterEach(() => vi.restoreAllMocks())

function deferred() {
  let resolve!: (value: unknown) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}
const group = (id: number) => ({ id, name: `Group ${id}`, platform: 'openai', rate_multiplier: 1 } as AdminGroup)
const entries = (id: number) => [{ user_id: id, user_name: '', user_email: `user${id}@example.com`, user_status: 'active', rate_multiplier: 2, rpm_override: null }]

async function openGroup(initiallyVisible = false) {
  const wrapper = mount(GroupRateMultipliersModal, {
    props: { show: initiallyVisible, group: group(1) },
    global: { stubs: {
      BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' },
      Icon: true, PlatformIcon: true, Pagination: true
    } }
  })
  if (!initiallyVisible) await wrapper.setProps({ show: true })
  return wrapper
}

describe('group rate multiplier request ownership', () => {
  it('keeps the reopened group data when an older response finishes last', async () => {
    const old = deferred()
    mocks.getGroupRateMultipliers.mockReturnValueOnce(old.promise).mockResolvedValueOnce(entries(2))
    const wrapper = await openGroup()
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true, group: group(2) })
    await flushPromises()
    old.resolve(entries(1))
    await flushPromises()
    expect(wrapper.get('tbody').text()).toContain('user2@example.com')
    expect(wrapper.get('tbody').text()).not.toContain('user1@example.com')
    await wrapper.get('tbody input[type="number"]').setValue('3')
    await wrapper.findAll('button').find(button => button.text() === 'common.save')!.trigger('click')
    expect(mocks.batchSetGroupRateMultipliers).toHaveBeenCalledWith(2, [{ user_id: 2, rate_multiplier: 3 }])
  })

  it('does not end the new load or report an error when an old request fails', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const old = deferred()
    const current = deferred()
    mocks.getGroupRateMultipliers.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const wrapper = await openGroup()
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true, group: group(2) })
    old.reject(new Error('old request failed'))
    await flushPromises()
    expect(mocks.showError).not.toHaveBeenCalled()
    expect(wrapper.find('svg.animate-spin').exists()).toBe(true)
    current.resolve(entries(2))
    await flushPromises()
    expect(wrapper.get('tbody').text()).toContain('user2@example.com')
  })

  it('loads a different group while the dialog remains open', async () => {
    mocks.getGroupRateMultipliers.mockResolvedValueOnce(entries(1)).mockResolvedValueOnce(entries(2))
    const wrapper = await openGroup()
    await flushPromises()
    await wrapper.setProps({ group: group(2) })
    await flushPromises()
    expect(mocks.getGroupRateMultipliers).toHaveBeenLastCalledWith(2)
    expect(wrapper.get('tbody').text()).toContain('user2@example.com')
  })

  it('discards the previous group draft before loading the next group', async () => {
    mocks.getGroupRateMultipliers.mockResolvedValueOnce(entries(1)).mockReturnValueOnce(deferred().promise)
    const wrapper = await openGroup()
    await flushPromises()
    await wrapper.get('tbody input[type="number"]').setValue('3')
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true, group: group(2) })
    expect(wrapper.findAll('button').some(button => button.text() === 'common.save')).toBe(false)
  })

  it('ignores a request failure after unmounting', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const request = deferred()
    mocks.getGroupRateMultipliers.mockReturnValue(request.promise)
    const wrapper = await openGroup()
    wrapper.unmount()
    request.reject(new Error('offline'))
    await flushPromises()
    expect(mocks.showError).not.toHaveBeenCalled()
  })

  it('loads the requested group when initially mounted visible', async () => {
    mocks.getGroupRateMultipliers.mockResolvedValue(entries(1))
    const wrapper = await openGroup(true)
    await flushPromises()
    expect(mocks.getGroupRateMultipliers).toHaveBeenCalledWith(1)
    expect(wrapper.get('tbody').text()).toContain('user1@example.com')
  })

  it('clears the old rows when the selected group becomes unavailable', async () => {
    mocks.getGroupRateMultipliers.mockResolvedValue(entries(1))
    const wrapper = await openGroup()
    await flushPromises()
    await wrapper.get('tbody input[type="number"]').setValue('3')
    await wrapper.setProps({ group: null })
    expect(wrapper.find('tbody input[type="number"]').exists()).toBe(false)
    expect(wrapper.findAll('button').some(button => button.text() === 'common.save')).toBe(false)
    expect(mocks.batchSetGroupRateMultipliers).not.toHaveBeenCalled()
  })

  it.each([false, true])('does not repopulate a closed dialog (reject=%s)', async (fail) => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const old = deferred()
    mocks.getGroupRateMultipliers.mockReturnValue(old.promise)
    const wrapper = await openGroup()
    await wrapper.setProps({ show: false })
    if (fail) old.reject(new Error('closed request failed'))
    else old.resolve(entries(1))
    await flushPromises()
    await wrapper.setProps({ show: true, group: null })
    expect(wrapper.find('tbody input[type="number"]').exists()).toBe(false)
    expect(mocks.showError).not.toHaveBeenCalled()
  })

  it.each([false, true])('does not close or notify a new group after an old save completes (reject=%s)', async (fail) => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const oldSave = deferred()
    mocks.batchSetGroupRateMultipliers.mockReturnValue(oldSave.promise)
    mocks.getGroupRateMultipliers.mockResolvedValueOnce(entries(1)).mockResolvedValueOnce(entries(2))
    const wrapper = await openGroup()
    await flushPromises()
    await wrapper.get('tbody input[type="number"]').setValue('3')
    await wrapper.findAll('button').find(button => button.text() === 'common.save')!.trigger('click')
    expect(mocks.batchSetGroupRateMultipliers).toHaveBeenCalledWith(1, [{ user_id: 1, rate_multiplier: 3 }])
    await wrapper.setProps({ group: group(2) })
    await flushPromises()
    expect(wrapper.get('tbody').text()).toContain('user2@example.com')
    if (fail) oldSave.reject(new Error('old save failed'))
    else oldSave.resolve(undefined)
    await flushPromises()
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.emitted('success')).toBeUndefined()
    expect(mocks.showError).not.toHaveBeenCalled()
    expect(wrapper.get('tbody').text()).toContain('user2@example.com')
  })
})
