import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import type { AdminGroup } from '@/types'
import GroupRPMOverridesModal from '../GroupRPMOverridesModal.vue'

const mocks = vi.hoisted(() => ({ getGroupRPMOverrides: vi.fn(), batchSetGroupRPMOverrides: vi.fn(), clearGroupRPMOverrides: vi.fn(), list: vi.fn(), showError: vi.fn(), showSuccess: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { groups: mocks, users: { list: mocks.list } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: mocks.showError, showSuccess: mocks.showSuccess }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)
beforeEach(() => vi.resetAllMocks())
afterEach(() => { vi.restoreAllMocks(); vi.useRealTimers() })

function deferred() {
  let resolve!: (value: unknown) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

const group = (id: number) => ({ id, name: `Group ${id}`, platform: 'openai', rpm_limit: 100 } as AdminGroup)
const entries = (id: number) => [{ user_id: id, user_name: '', user_email: `user${id}@example.com`, user_status: 'active', rpm_override: 100 }]

async function openGroup() {
  const wrapper = mount(GroupRPMOverridesModal, {
    props: { show: false, group: group(1) },
    global: { stubs: {
      BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' },
      Icon: true, PlatformIcon: true, Pagination: true
    } }
  })
  await wrapper.setProps({ show: true })
  return wrapper
}

describe('group RPM override request ownership', () => {
  it('keeps the reopened group data when an older response finishes last', async () => {
    const old = deferred()
    mocks.getGroupRPMOverrides.mockReturnValueOnce(old.promise).mockResolvedValueOnce(entries(2))
    const wrapper = await openGroup()
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true, group: group(2) })
    await flushPromises()
    old.resolve(entries(1))
    await flushPromises()
    expect(wrapper.get('tbody').text()).toContain('user2@example.com')
    expect(wrapper.get('tbody').text()).not.toContain('user1@example.com')
    await wrapper.get('tbody input[type="number"]').setValue('200')
    await wrapper.findAll('button').find(button => button.text() === 'common.save')!.trigger('click')
    expect(mocks.batchSetGroupRPMOverrides).toHaveBeenCalledWith(2, [{ user_id: 2, rpm_override: 200 }])
  })

  it('does not end the new load or report an error when an old request fails', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const old = deferred()
    const current = deferred()
    mocks.getGroupRPMOverrides.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
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
    mocks.getGroupRPMOverrides.mockResolvedValueOnce(entries(1)).mockResolvedValueOnce(entries(2))
    const wrapper = await openGroup()
    await flushPromises()
    await wrapper.setProps({ group: group(2) })
    await flushPromises()
    expect(mocks.getGroupRPMOverrides).toHaveBeenLastCalledWith(2)
    expect(wrapper.get('tbody').text()).toContain('user2@example.com')
  })

  it('discards the previous group draft before loading the next group', async () => {
    mocks.getGroupRPMOverrides.mockResolvedValueOnce(entries(1)).mockReturnValueOnce(deferred().promise)
    const wrapper = await openGroup()
    await flushPromises()
    await wrapper.get('tbody input[type="number"]').setValue('200')
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true, group: group(2) })
    expect(wrapper.findAll('button').some(button => button.text() === 'common.save')).toBe(false)
  })

  it('ignores a request failure after unmounting', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const request = deferred()
    mocks.getGroupRPMOverrides.mockReturnValue(request.promise)
    const wrapper = await openGroup()
    wrapper.unmount()
    request.reject(new Error('offline'))
    await flushPromises()
    expect(mocks.showError).not.toHaveBeenCalled()
  })
})


const operations = ['save', 'clear'] as const

async function loadedEditor() {
  mocks.getGroupRPMOverrides.mockResolvedValueOnce(entries(1))
  const wrapper = await openGroup()
  await flushPromises()
  return wrapper
}

function action(wrapper: Awaited<ReturnType<typeof openGroup>>, key: string) {
  return wrapper.findAll('button').find(button => button.text() === key)!
}

async function startOperation(wrapper: Awaited<ReturnType<typeof openGroup>>, operation: typeof operations[number]) {
  if (operation === 'save') {
    await wrapper.get('tbody input[type="number"]').setValue('200')
    await action(wrapper, 'common.save').trigger('click')
  } else {
    await action(wrapper, 'admin.groups.clearAll').trigger('click')
  }
}

function operationMock(operation: typeof operations[number]) {
  return operation === 'save' ? mocks.batchSetGroupRPMOverrides : mocks.clearGroupRPMOverrides
}

describe('RPM save and clear session ownership', () => {
  it.each(operations)('ignores an old %s success without ending the current operation', async (operation) => {
    const old = deferred()
    const current = deferred()
    operationMock(operation).mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const wrapper = await loadedEditor()
    await startOperation(wrapper, operation)
    expect(operationMock(operation)).toHaveBeenCalledWith(...(operation === 'save'
      ? [1, [{ user_id: 1, rpm_override: 200 }]] : [1]))
    mocks.getGroupRPMOverrides.mockResolvedValueOnce(entries(2))
    await wrapper.setProps({ group: group(2) })
    await flushPromises()
    await startOperation(wrapper, operation)
    old.resolve(undefined)
    await flushPromises()
    expect(wrapper.get('tbody').text()).toContain('user2@example.com')
    expect(action(wrapper, operation === 'save' ? 'common.save' : 'admin.groups.clearAll').attributes('disabled')).toBeDefined()
    expect(mocks.showSuccess).not.toHaveBeenCalled()
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.emitted('success')).toBeUndefined()
    current.resolve(undefined)
    await flushPromises()
    expect(mocks.showSuccess).toHaveBeenCalledTimes(1)
    if (operation === 'save') {
      expect(operationMock(operation)).toHaveBeenLastCalledWith(2, [{ user_id: 2, rpm_override: 200 }])
      expect(wrapper.emitted('success')).toHaveLength(1)
      expect(wrapper.emitted('close')).toHaveLength(1)
    } else {
      expect(operationMock(operation)).toHaveBeenLastCalledWith(2)
      expect(wrapper.find('tbody').exists()).toBe(false)
      expect(wrapper.emitted('close')).toBeUndefined()
    }
  })

  it.each(operations)('ignores an old %s failure without clearing the new busy state', async (operation) => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const old = deferred()
    const current = deferred()
    operationMock(operation).mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const wrapper = await loadedEditor()
    await startOperation(wrapper, operation)
    mocks.getGroupRPMOverrides.mockResolvedValueOnce(entries(2))
    await wrapper.setProps({ group: group(2) })
    await flushPromises()
    await startOperation(wrapper, operation)
    old.reject(new Error('old failure'))
    await flushPromises()
    expect(mocks.showError).not.toHaveBeenCalled()
    expect(action(wrapper, operation === 'save' ? 'common.save' : 'admin.groups.clearAll').attributes('disabled')).toBeDefined()
    current.reject(new Error('current failure'))
    await flushPromises()
    expect(mocks.showError).toHaveBeenCalledTimes(1)
    expect(wrapper.get('tbody').text()).toContain('user2@example.com')
    expect(action(wrapper, operation === 'save' ? 'common.save' : 'admin.groups.clearAll').attributes('disabled')).toBeUndefined()
  })

  it.each(operations)('loads the reopened same group only after the earlier %s server write settles', async (operation) => {
    const old = deferred()
    let serverRows = entries(1)
    operationMock(operation).mockImplementationOnce(() => old.promise.then(() => {
      serverRows = operation === 'save' ? [{ ...entries(1)[0]!, rpm_override: 200 }] : []
    }))
    const wrapper = await loadedEditor()
    mocks.getGroupRPMOverrides.mockImplementation(async () => serverRows.map(row => ({ ...row })))
    await startOperation(wrapper, operation)
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(mocks.getGroupRPMOverrides).toHaveBeenCalledTimes(1)
    expect(wrapper.find('svg.animate-spin').exists()).toBe(true)
    expect(wrapper.find('tbody').exists()).toBe(false)
    old.resolve(undefined)
    await flushPromises()
    expect(mocks.getGroupRPMOverrides).toHaveBeenCalledTimes(2)
    if (operation === 'save') {
      expect((wrapper.get('tbody input[type="number"]').element as HTMLInputElement).value).toBe('200')
    } else {
      expect(wrapper.find('tbody').exists()).toBe(false)
      expect(wrapper.find('svg.animate-spin').exists()).toBe(false)
    }
    expect(mocks.showSuccess).not.toHaveBeenCalled()
    expect(wrapper.emitted('success')).toBeUndefined()
    expect(wrapper.emitted('close')).toBeUndefined()
  })

  it.each(operations)('releases a reopened same-group load after the earlier %s fails without a stale toast', async (operation) => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const old = deferred()
    operationMock(operation).mockReturnValueOnce(old.promise)
    const wrapper = await loadedEditor()
    await startOperation(wrapper, operation)
    await wrapper.setProps({ show: false })
    mocks.getGroupRPMOverrides.mockResolvedValueOnce(entries(1))
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(mocks.getGroupRPMOverrides).toHaveBeenCalledTimes(1)
    old.reject(new Error('old write failed'))
    await flushPromises()
    expect(mocks.getGroupRPMOverrides).toHaveBeenCalledTimes(2)
    expect(wrapper.get('tbody').text()).toContain('user1@example.com')
    expect(wrapper.find('svg.animate-spin').exists()).toBe(false)
    expect(mocks.showError).not.toHaveBeenCalled()
  })

  it.each(operations)('loads another group while waiting for an earlier same-group %s and discards the old reload', async (operation) => {
    const old = deferred()
    const server = new Map([[1, entries(1)], [2, entries(2)]])
    operationMock(operation).mockImplementationOnce(() => old.promise.then(() => {
      server.set(1, operation === 'save' ? [{ ...entries(1)[0]!, rpm_override: 200 }] : [])
    }))
    const wrapper = await loadedEditor()
    mocks.getGroupRPMOverrides.mockImplementation(async (id: number) => server.get(id)!.map(row => ({ ...row })))
    await startOperation(wrapper, operation)
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(mocks.getGroupRPMOverrides).toHaveBeenCalledTimes(1)
    await wrapper.setProps({ group: group(2) })
    await flushPromises()
    expect(mocks.getGroupRPMOverrides.mock.calls).toEqual([[1], [2]])
    expect(wrapper.get('tbody').text()).toContain('user2@example.com')
    expect(action(wrapper, 'admin.groups.clearAll').attributes('disabled')).toBeUndefined()
    old.resolve(undefined)
    await flushPromises()
    expect(mocks.getGroupRPMOverrides.mock.calls).toEqual([[1], [2]])
    expect(wrapper.get('tbody').text()).toContain('user2@example.com')
    expect(mocks.showSuccess).not.toHaveBeenCalled()
    expect(wrapper.emitted('close')).toBeUndefined()
  })

  it.each(operations)('ignores %s failures after unmounting', async (operation) => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const old = deferred()
    operationMock(operation).mockReturnValueOnce(old.promise)
    const wrapper = await loadedEditor()
    await startOperation(wrapper, operation)
    wrapper.unmount()
    old.reject(new Error('unmounted failure'))
    await flushPromises()
    expect(mocks.showError).not.toHaveBeenCalled()
    expect(mocks.showSuccess).not.toHaveBeenCalled()
    expect(wrapper.emitted('close')).toBeUndefined()
  })

  it.each(operations)('blocks the other write while %s is pending', async (operation) => {
    const current = deferred()
    operationMock(operation).mockReturnValueOnce(current.promise)
    const wrapper = await loadedEditor()
    await wrapper.get('tbody input[type="number"]').setValue('200')
    await startOperation(wrapper, operation)
    const other = operation === 'save' ? 'admin.groups.clearAll' : 'common.save'
    expect(action(wrapper, other).attributes('disabled')).toBeDefined()
    await action(wrapper, other).trigger('click')
    expect(operationMock(operation === 'save' ? 'clear' : 'save')).not.toHaveBeenCalled()
    expect(wrapper.get('tbody input[type="number"]').attributes('disabled')).toBeDefined()
    expect(action(wrapper, 'admin.groups.revertChanges').attributes('disabled')).toBeDefined()
    current.resolve(undefined)
    await flushPromises()
  })

  it.each(operations)('invalidates a pending %s immediately when the close button is clicked', async (operation) => {
    const old = deferred()
    operationMock(operation).mockReturnValueOnce(old.promise)
    const wrapper = await loadedEditor()
    await startOperation(wrapper, operation)
    await action(wrapper, 'common.close').trigger('click')
    old.resolve(undefined)
    await flushPromises()
    expect(mocks.showSuccess).not.toHaveBeenCalled()
    expect(wrapper.emitted('success')).toBeUndefined()
    expect(wrapper.emitted('close')).toHaveLength(1)
  })
})

describe('RPM load and search boundary controls', () => {
  it('loads a dialog that is initially open and preserves cancellation in the current group', async () => {
    mocks.getGroupRPMOverrides.mockResolvedValueOnce(entries(2))
    const wrapper = mount(GroupRPMOverridesModal, {
      props: { show: true, group: group(2) },
      global: { stubs: { BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' }, Icon: true, PlatformIcon: true, Pagination: true } }
    })
    await flushPromises()
    expect(mocks.getGroupRPMOverrides).toHaveBeenCalledTimes(1)
    expect(mocks.getGroupRPMOverrides).toHaveBeenCalledWith(2)
    await wrapper.get('tbody input[type="number"]').setValue('200')
    await action(wrapper, 'admin.groups.revertChanges').trigger('click')
    expect((wrapper.get('tbody input[type="number"]').element as HTMLInputElement).value).toBe('100')
    expect(action(wrapper, 'admin.groups.clearAll').attributes('disabled')).toBeUndefined()
    expect(mocks.batchSetGroupRPMOverrides).not.toHaveBeenCalled()
  })

  it('does not expose an old draft or enable writes while the new group is loading', async () => {
    const wrapper = await loadedEditor()
    await wrapper.get('tbody input[type="number"]').setValue('200')
    const current = deferred()
    mocks.getGroupRPMOverrides.mockReturnValueOnce(current.promise)
    await wrapper.setProps({ group: group(2) })
    expect(wrapper.find('tbody').exists()).toBe(false)
    expect(wrapper.findAll('button').some(button => button.text() === 'common.save')).toBe(false)
    expect(action(wrapper, 'common.add').attributes('disabled')).toBeDefined()
    expect(wrapper.get('input[type="text"]').attributes('disabled')).toBeDefined()
    current.resolve(entries(2))
    await flushPromises()
    expect(wrapper.get('tbody').text()).toContain('user2@example.com')
  })

  it('keeps a new same-group load when an earlier reopened load finishes last', async () => {
    const old = deferred()
    mocks.getGroupRPMOverrides.mockReturnValueOnce(old.promise).mockResolvedValueOnce(entries(2))
    const wrapper = await openGroup()
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    old.resolve(entries(1))
    await flushPromises()
    expect(wrapper.get('tbody').text()).toContain('user2@example.com')
    expect(wrapper.get('tbody').text()).not.toContain('user1@example.com')
  })

  it('does not resurrect rows after the selected group is removed', async () => {
    const old = deferred()
    mocks.getGroupRPMOverrides.mockReturnValueOnce(old.promise)
    const wrapper = await openGroup()
    await wrapper.setProps({ group: null })
    old.resolve(entries(1))
    await flushPromises()
    expect(wrapper.find('tbody').exists()).toBe(false)
    expect(mocks.showError).not.toHaveBeenCalled()
    expect(mocks.batchSetGroupRPMOverrides).not.toHaveBeenCalled()
  })

  it('reports a current load error and releases its loading state', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    mocks.getGroupRPMOverrides.mockRejectedValueOnce(new Error('current load failed'))
    const wrapper = await openGroup()
    await flushPromises()
    expect(mocks.showError).toHaveBeenCalledTimes(1)
    expect(wrapper.find('svg.animate-spin').exists()).toBe(false)
  })

  it('blocks adding and saving a partial replacement after the current load fails', async () => {
    vi.useFakeTimers()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    mocks.getGroupRPMOverrides.mockRejectedValueOnce(new Error('current load failed'))
    mocks.list.mockResolvedValue({ items: [{ id: 9, email: 'partial@example.com', status: 'active' }] })
    const wrapper = await openGroup()
    await flushPromises()
    expect(wrapper.find('svg.animate-spin').exists()).toBe(false)
    expect(wrapper.get('input[type="text"]').attributes('disabled')).toBeDefined()
    await wrapper.get('input[type="text"]').setValue('partial')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()
    expect(mocks.list).not.toHaveBeenCalled()
    await wrapper.get('input[placeholder="100"]').setValue('0')
    expect(action(wrapper, 'common.add').attributes('disabled')).toBeDefined()
    await action(wrapper, 'common.add').trigger('click')
    expect(wrapper.find('tbody').exists()).toBe(false)
    expect(wrapper.findAll('button').some(button => button.text() === 'common.save')).toBe(false)
    expect(wrapper.findAll('button').some(button => button.text() === 'admin.groups.clearAll')).toBe(false)
    expect(mocks.batchSetGroupRPMOverrides).not.toHaveBeenCalled()
    expect(mocks.clearGroupRPMOverrides).not.toHaveBeenCalled()
  })

  it('recovers after a successful reopened load and preserves existing rows when adding unlimited zero', async () => {
    vi.useFakeTimers()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    mocks.getGroupRPMOverrides.mockRejectedValueOnce(new Error('current load failed')).mockResolvedValueOnce(entries(1))
    const wrapper = await openGroup()
    await flushPromises()
    expect(wrapper.get('input[type="text"]').attributes('disabled')).toBeDefined()
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(wrapper.get('input[type="text"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('tbody').text()).toContain('user1@example.com')
    mocks.list.mockResolvedValueOnce({ items: [{ id: 9, email: 'new@example.com', status: 'active' }] })
    await wrapper.get('input[type="text"]').setValue('new')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text().includes('new@example.com'))!.trigger('click')
    await wrapper.get('input[placeholder="100"]').setValue('0')
    await action(wrapper, 'common.add').trigger('click')
    await action(wrapper, 'common.save').trigger('click')
    await flushPromises()
    expect(mocks.batchSetGroupRPMOverrides).toHaveBeenCalledWith(1, [
      { user_id: 1, rpm_override: 100 }, { user_id: 9, rpm_override: 0 }
    ])
    expect(mocks.showSuccess).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('success')).toHaveLength(1)
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('does not grant write readiness from an old successful load after the current load fails', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const old = deferred()
    mocks.getGroupRPMOverrides.mockReturnValueOnce(old.promise).mockRejectedValueOnce(new Error('current load failed'))
    const wrapper = await openGroup()
    await wrapper.setProps({ group: group(2) })
    await flushPromises()
    old.resolve(entries(1))
    await flushPromises()
    expect(wrapper.find('tbody').exists()).toBe(false)
    expect(wrapper.get('input[type="text"]').attributes('disabled')).toBeDefined()
    expect(action(wrapper, 'common.add').attributes('disabled')).toBeDefined()
    expect(mocks.showError).toHaveBeenCalledTimes(1)
    expect(mocks.batchSetGroupRPMOverrides).not.toHaveBeenCalled()
    expect(mocks.clearGroupRPMOverrides).not.toHaveBeenCalled()
  })

  it('discards a pending search when the group changes while open', async () => {
    vi.useFakeTimers()
    const old = deferred()
    const wrapper = await loadedEditor()
    mocks.list.mockReturnValueOnce(old.promise)
    await wrapper.get('input[type="text"]').setValue('old')
    await vi.advanceTimersByTimeAsync(300)
    expect(mocks.list).toHaveBeenCalledTimes(1)
    mocks.getGroupRPMOverrides.mockResolvedValueOnce(entries(2))
    await wrapper.setProps({ group: group(2) })
    await flushPromises()
    old.resolve({ items: [{ id: 9, email: 'old@example.com', status: 'active' }] })
    await flushPromises()
    expect(wrapper.text()).not.toContain('old@example.com')
    expect(action(wrapper, 'common.add').attributes('disabled')).toBeDefined()
    mocks.list.mockResolvedValueOnce({ items: [{ id: 10, email: 'new@example.com', status: 'active' }] })
    await wrapper.get('input[type="text"]').setValue('new')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text().includes('new@example.com'))!.trigger('click')
    await wrapper.get('input[placeholder="100"]').setValue('0')
    await action(wrapper, 'common.add').trigger('click')
    await action(wrapper, 'common.save').trigger('click')
    await flushPromises()
    expect(mocks.batchSetGroupRPMOverrides).toHaveBeenCalledWith(2, [
      { user_id: 2, rpm_override: 100 }, { user_id: 10, rpm_override: 0 }
    ])
  })
})
