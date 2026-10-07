import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import ErrorPassthroughRulesModal from '../ErrorPassthroughRulesModal.vue'

const mocks = vi.hoisted(() => ({ list: vi.fn(), toggleEnabled: vi.fn(), showError: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { errorPassthrough: mocks } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: mocks.showError }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)
afterEach(() => vi.restoreAllMocks())
beforeEach(() => vi.resetAllMocks())

function rule(enabled: boolean, id = 7) {
  return { id, name: 'Rule', enabled, priority: 1, error_codes: [429], keywords: [],
    platforms: [], match_mode: 'any', passthrough_code: true, passthrough_body: true, skip_monitoring: false }
}

async function openRules(enabled = true, rows = [rule(enabled)]) {
  mocks.list.mockResolvedValue(rows)
  const wrapper = mount(ErrorPassthroughRulesModal, {
    props: { show: false },
    global: { stubs: {
      BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' },
      ConfirmDialog: true, Icon: true
    } }
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

describe('error passthrough enabled state', () => {
  it.each([true, false])('keeps the server state after two pending clicks (enabled=%s)', async (enabled) => {
    const finishes: Array<(value: unknown) => void> = []
    mocks.toggleEnabled.mockImplementation(() => new Promise(resolve => { finishes.push(resolve) }))
    const wrapper = await openRules(enabled)
    const toggle = wrapper.get('tbody button.relative')
    await toggle.trigger('click')
    await toggle.trigger('click')
    expect(mocks.toggleEnabled.mock.calls).toEqual([[7, !enabled]])
    expect(toggle.attributes('disabled')).toBeDefined()
    for (const finish of finishes) {
      finish(rule(!enabled))
      await flushPromises()
      expect(toggle.classes().includes('bg-primary-600')).toBe(!enabled)
    }
    expect(toggle.attributes('disabled')).toBeUndefined()
  })

  it('allows sequential toggles in both directions', async () => {
    mocks.toggleEnabled.mockImplementation((_id: number, enabled: boolean) => Promise.resolve(rule(enabled)))
    const wrapper = await openRules()
    const toggle = wrapper.get('tbody button.relative')
    await toggle.trigger('click')
    await flushPromises()
    expect(toggle.classes()).not.toContain('bg-primary-600')
    await toggle.trigger('click')
    await flushPromises()
    expect(toggle.classes()).toContain('bg-primary-600')
    expect(mocks.toggleEnabled.mock.calls).toEqual([[7, false], [7, true]])
  })

  it('keeps the original state when saving fails', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    mocks.toggleEnabled.mockRejectedValue(new Error('offline'))
    const wrapper = await openRules()
    const toggle = wrapper.get('tbody button.relative')
    await toggle.trigger('click')
    await flushPromises()
    expect(toggle.classes()).toContain('bg-primary-600')
    expect(mocks.showError).toHaveBeenCalledWith('admin.errorPassthrough.failedToToggle')
  })

  it.each([true, false])('does not let an older response undo the next opposite saved state (initial=%s)', async (enabled) => {
    const pending: Array<{ target: boolean; finish: (value: unknown) => void }> = []
    mocks.toggleEnabled.mockImplementation((_id: number, target: boolean) => new Promise(resolve => {
      pending.push({ target, finish: resolve })
    }))
    const wrapper = await openRules(enabled)
    const toggle = wrapper.get('tbody button.relative')
    await toggle.trigger('click')
    await toggle.trigger('click')
    // Both older writes, if admitted, committed before their responses arrived.
    let serverEnabled = !enabled
    pending[0]!.finish(rule(serverEnabled))
    await flushPromises()
    await toggle.trigger('click')
    const next = pending[pending.length - 1]!
    expect(next.target).toBe(enabled)
    serverEnabled = enabled
    next.finish(rule(serverEnabled))
    await flushPromises()
    // An already-committed older response can arrive after the newer response.
    if (pending.length === 3) {
      pending[1]!.finish(rule(!enabled))
      await flushPromises()
    }
    expect(toggle.classes().includes('bg-primary-600')).toBe(serverEnabled)
    expect(mocks.toggleEnabled.mock.calls).toEqual([[7, !enabled], [7, enabled]])
  })

  it('lets different rules save concurrently and releases only the completed rule', async () => {
    const pending = new Map<number, (value: unknown) => void>()
    mocks.toggleEnabled.mockImplementation((id: number) => new Promise(resolve => { pending.set(id, resolve) }))
    const wrapper = await openRules(true, [rule(true, 7), rule(false, 8)])
    const [first, second] = wrapper.findAll('tbody button.relative')
    await first!.trigger('click')
    await second!.trigger('click')
    expect(mocks.toggleEnabled.mock.calls).toEqual([[7, false], [8, true]])
    expect(first!.attributes('disabled')).toBeDefined()
    expect(second!.attributes('disabled')).toBeDefined()
    pending.get(7)!(rule(false, 7))
    await flushPromises()
    expect(first!.attributes('disabled')).toBeUndefined()
    expect(second!.attributes('disabled')).toBeDefined()
    expect(first!.classes()).not.toContain('bg-primary-600')
    pending.get(8)!(rule(true, 8))
    await flushPromises()
    expect(second!.attributes('disabled')).toBeUndefined()
    expect(second!.classes()).toContain('bg-primary-600')
  })

  it('releases a failed toggle so the same rule can be retried', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    let fail!: (error: unknown) => void
    mocks.toggleEnabled.mockReturnValueOnce(new Promise((_resolve, reject) => { fail = reject }))
    const wrapper = await openRules()
    const toggle = wrapper.get('tbody button.relative')
    await toggle.trigger('click')
    expect(toggle.attributes('disabled')).toBeDefined()
    fail(new Error('offline'))
    await flushPromises()
    expect(toggle.attributes('disabled')).toBeUndefined()
    expect(toggle.classes()).toContain('bg-primary-600')
    expect(mocks.showError).toHaveBeenCalledTimes(1)
    mocks.toggleEnabled.mockResolvedValueOnce(rule(false))
    await toggle.trigger('click')
    await flushPromises()
    expect(toggle.classes()).not.toContain('bg-primary-600')
    expect(mocks.toggleEnabled.mock.calls).toEqual([[7, false], [7, false]])
  })

  it('applies the saved response state even when it differs from the requested target', async () => {
    mocks.toggleEnabled.mockResolvedValue(rule(true))
    const wrapper = await openRules()
    const toggle = wrapper.get('tbody button.relative')
    await toggle.trigger('click')
    await flushPromises()
    expect(mocks.toggleEnabled).toHaveBeenCalledWith(7, false)
    expect(toggle.classes()).toContain('bg-primary-600')
    expect(toggle.attributes('disabled')).toBeUndefined()
  })

  it('keeps the pending guard and applies the saved response to a reloaded row after reopen', async () => {
    let finish!: (value: unknown) => void
    mocks.toggleEnabled.mockReturnValue(new Promise(resolve => { finish = resolve }))
    const wrapper = await openRules()
    await wrapper.get('tbody button.relative').trigger('click')
    await wrapper.setProps({ show: false })
    mocks.list.mockResolvedValueOnce([rule(true)])
    await wrapper.setProps({ show: true })
    await flushPromises()
    const reloaded = wrapper.get('tbody button.relative')
    expect(reloaded.attributes('disabled')).toBeDefined()
    finish(rule(false))
    await flushPromises()
    expect(reloaded.classes()).not.toContain('bg-primary-600')
    expect(reloaded.attributes('disabled')).toBeUndefined()
    expect(mocks.toggleEnabled).toHaveBeenCalledTimes(1)
  })

  it.each([true, false])('preserves a confirmed save when the older reopen list arrives last (initial=%s)', async (enabled) => {
    let finishSave!: (value: unknown) => void
    let finishList!: (value: unknown) => void
    mocks.toggleEnabled.mockReturnValueOnce(new Promise(resolve => { finishSave = resolve }))
    const wrapper = await openRules(enabled)
    await wrapper.get('tbody button.relative').trigger('click')
    await wrapper.setProps({ show: false })
    mocks.list.mockReturnValueOnce(new Promise(resolve => { finishList = resolve }))
    await wrapper.setProps({ show: true })
    finishSave(rule(!enabled))
    await flushPromises()
    finishList([rule(enabled)])
    await flushPromises()
    const reloaded = wrapper.get('tbody button.relative')
    expect(reloaded.classes().includes('bg-primary-600')).toBe(!enabled)
    expect(reloaded.attributes('disabled')).toBeUndefined()
    mocks.toggleEnabled.mockResolvedValueOnce(rule(enabled))
    await reloaded.trigger('click')
    await flushPromises()
    expect(mocks.toggleEnabled.mock.calls).toEqual([[7, !enabled], [7, enabled]])
    expect(reloaded.classes().includes('bg-primary-600')).toBe(enabled)
  })

  it('accepts a new server state from a list started after a confirmed save', async () => {
    mocks.toggleEnabled.mockResolvedValueOnce(rule(false))
    const wrapper = await openRules()
    await wrapper.get('tbody button.relative').trigger('click')
    await flushPromises()
    await wrapper.setProps({ show: false })
    mocks.list.mockResolvedValueOnce([rule(true)])
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(wrapper.get('tbody button.relative').classes()).toContain('bg-primary-600')
  })

  it('only lets the latest list finish the loading state and replace rows', async () => {
    let finishOld!: (value: unknown) => void
    let finishNew!: (value: unknown) => void
    const wrapper = await openRules()
    await wrapper.setProps({ show: false })
    mocks.list.mockReturnValueOnce(new Promise(resolve => { finishOld = resolve }))
    await wrapper.setProps({ show: true })
    await wrapper.setProps({ show: false })
    mocks.list.mockReturnValueOnce(new Promise(resolve => { finishNew = resolve }))
    await wrapper.setProps({ show: true })
    finishOld([rule(true)])
    await flushPromises()
    expect(wrapper.find('tbody').exists()).toBe(false)
    finishNew([rule(false, 8)])
    await flushPromises()
    expect(wrapper.get('tbody button.relative').classes()).not.toContain('bg-primary-600')
    mocks.toggleEnabled.mockResolvedValueOnce(rule(true, 8))
    await wrapper.get('tbody button.relative').trigger('click')
    await flushPromises()
    expect(mocks.toggleEnabled).toHaveBeenCalledWith(8, true)
  })

  it('does not resurrect a deleted row when an in-flight toggle finishes', async () => {
    let finishSave!: (value: unknown) => void
    let finishList!: (value: unknown) => void
    mocks.toggleEnabled.mockReturnValueOnce(new Promise(resolve => { finishSave = resolve }))
    const wrapper = await openRules()
    await wrapper.get('tbody button.relative').trigger('click')
    await wrapper.setProps({ show: false })
    mocks.list.mockReturnValueOnce(new Promise(resolve => { finishList = resolve }))
    await wrapper.setProps({ show: true })
    finishSave(rule(false))
    await flushPromises()
    finishList([])
    await flushPromises()
    expect(wrapper.find('tbody').exists()).toBe(false)
    expect(wrapper.text()).toContain('admin.errorPassthrough.noRules')
  })


  it('ignores an obsolete list failure while the current list is still loading', async () => {
    let failOld!: (error: unknown) => void
    let finishNew!: (value: unknown) => void
    const wrapper = await openRules()
    await wrapper.setProps({ show: false })
    mocks.list.mockReturnValueOnce(new Promise((_resolve, reject) => { failOld = reject }))
    await wrapper.setProps({ show: true })
    await wrapper.setProps({ show: false })
    mocks.list.mockReturnValueOnce(new Promise(resolve => { finishNew = resolve }))
    await wrapper.setProps({ show: true })
    failOld(new Error('obsolete list failure'))
    await flushPromises()
    expect(mocks.showError).not.toHaveBeenCalled()
    expect(wrapper.find('tbody').exists()).toBe(false)
    finishNew([rule(false)])
    await flushPromises()
    expect(wrapper.get('tbody button.relative').classes()).not.toContain('bg-primary-600')
  })

  it('does not report a failed toggle after its component has been unmounted', async () => {
    let fail!: (error: unknown) => void
    mocks.toggleEnabled.mockReturnValueOnce(new Promise((_resolve, reject) => { fail = reject }))
    const wrapper = await openRules()
    await wrapper.get('tbody button.relative').trigger('click')
    wrapper.unmount()
    fail(new Error('disposed toggle failure'))
    await flushPromises()
    expect(mocks.showError).not.toHaveBeenCalled()
  })

})
