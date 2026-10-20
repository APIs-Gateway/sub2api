import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AdminGroup } from '@/types'
import GroupRPMOverridesModal from '../GroupRPMOverridesModal.vue'
import GroupRateMultipliersModal from '../GroupRateMultipliersModal.vue'

const mocks = vi.hoisted(() => ({
  list: vi.fn(),
  getGroupRPMOverrides: vi.fn(),
  getGroupRateMultipliers: vi.fn()
}))
vi.mock('@/api/admin', () => ({ adminAPI: { users: { list: mocks.list }, groups: mocks } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn() }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

beforeEach(() => {
  vi.resetAllMocks()
  vi.useFakeTimers()
  mocks.getGroupRPMOverrides.mockResolvedValue([])
  mocks.getGroupRateMultipliers.mockResolvedValue([])
  mocks.list.mockResolvedValue({ items: [{ id: 7, email: 'fresh@example.com' }] })
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe.each([GroupRPMOverridesModal, GroupRateMultipliersModal])('group modal lifecycle', (component) => {
  const open = (show: boolean) => mount(component, {
    props: { show, group: { id: 1, name: 'Group', platform: 'openai' } as AdminGroup },
    global: { stubs: {
      BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' },
      Icon: true, PlatformIcon: true, Pagination: true
    } }
  })

  it('registers one outside-click listener while shown and removes it on hide and unmount', async () => {
    const add = vi.spyOn(document, 'addEventListener')
    const remove = vi.spyOn(document, 'removeEventListener')
    const wrapper = open(false)
    expect(add.mock.calls.filter(([event]) => event === 'click')).toHaveLength(0)

    await wrapper.setProps({ show: true })
    const firstHandlers = add.mock.calls.filter(([event]) => event === 'click')
    expect(firstHandlers).toHaveLength(1)
    await wrapper.setProps({ show: false })
    expect(remove).toHaveBeenCalledWith('click', firstHandlers[0]![1])

    await wrapper.setProps({ show: true })
    const handlers = add.mock.calls.filter(([event]) => event === 'click')
    expect(handlers).toHaveLength(2)
    expect(handlers[1]![1]).toBe(firstHandlers[0]![1])
    wrapper.unmount()
    expect(remove).toHaveBeenCalledWith('click', handlers[1]![1])
    expect(remove.mock.calls.filter(([event]) => event === 'click')).toHaveLength(2)
  })

  it('cancels queued searches on hide and unmount, then searches normally after re-show', async () => {
    const wrapper = open(true)
    await wrapper.get('input[type="text"]').setValue('hidden')
    await wrapper.setProps({ show: false })
    await vi.advanceTimersByTimeAsync(300)
    expect(mocks.list).not.toHaveBeenCalled()

    await wrapper.setProps({ show: true })
    await wrapper.get('input[type="text"]').setValue('visible')
    await vi.advanceTimersByTimeAsync(300)
    await flushPromises()
    expect(mocks.list).toHaveBeenCalledTimes(1)
    expect(mocks.list).toHaveBeenCalledWith(1, 10, { search: 'visible' })
    expect(wrapper.text()).toContain('fresh@example.com')

    await wrapper.get('input[type="text"]').setValue('unmounted')
    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(300)
    expect(mocks.list).toHaveBeenCalledTimes(1)
  })

  it('ignores a search response that arrives after hide and re-show', async () => {
    let resolveSearch!: (result: { items: { id: number; email: string }[] }) => void
    mocks.list.mockImplementationOnce(() => new Promise(resolve => { resolveSearch = resolve }))
    const wrapper = open(true)
    await wrapper.get('input[type="text"]').setValue('stale')
    await vi.advanceTimersByTimeAsync(300)
    expect(mocks.list).toHaveBeenCalledTimes(1)

    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    resolveSearch({ items: [{ id: 8, email: 'stale@example.com' }] })
    await flushPromises()
    expect(wrapper.text()).not.toContain('stale@example.com')
    wrapper.unmount()
  })
})
