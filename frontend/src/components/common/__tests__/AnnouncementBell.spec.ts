import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import AnnouncementBell from '../AnnouncementBell.vue'
import { useAnnouncementStore } from '@/stores/announcements'

const { markRead, showError, showSuccess } = vi.hoisted(() => ({
  markRead: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api', () => ({ announcementsAPI: { markRead } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/utils/format', () => ({
  formatRelativeTime: () => 'now',
  formatRelativeWithDateTime: () => 'now'
}))

enableAutoUnmount(afterEach)

const announcement = (id: number) => ({
  id,
  title: `Notice ${id}`,
  content: `Details ${id}`,
  notify_mode: 'silent' as const,
  created_at: '2026-09-30',
  updated_at: '2026-09-30'
})

function deferred() {
  let resolve!: () => void
  let reject!: (error: Error) => void
  const promise = new Promise<void>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function confirmButton(wrapper: ReturnType<typeof mount>) {
  const button = wrapper.findAll('button').find(item => item.text() === 'announcements.markRead')
  if (!button) throw new Error('Mark-read button not found')
  return button
}

beforeEach(() => {
  setActivePinia(createPinia())
  vi.resetAllMocks()
  vi.spyOn(console, 'error').mockImplementation(() => {})
  useAnnouncementStore().announcements = [announcement(1)]
})

afterEach(() => {
  vi.restoreAllMocks()
  document.body.style.overflow = ''
})

describe('announcement read confirmation', () => {
  it('keeps a failed confirmation open and reports success only after a successful retry', async () => {
    markRead.mockRejectedValue(new Error('offline'))
    const wrapper = mount(AnnouncementBell, {
      global: { stubs: { Teleport: true, Transition: true, Icon: true } }
    })
    await wrapper.get('button').trigger('click')
    await wrapper.get('.group.relative').trigger('click')
    await flushPromises()
    expect(useAnnouncementStore().unreadCount).toBe(1)

    await confirmButton(wrapper).trigger('click')
    await flushPromises()
    expect(markRead).toHaveBeenCalledTimes(2)
    expect(showSuccess).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledTimes(2)
    expect(wrapper.get('.markdown-body').text()).toContain('Details 1')

    markRead.mockResolvedValue(undefined)
    await confirmButton(wrapper).trigger('click')
    await flushPromises()
    expect(showSuccess).toHaveBeenCalledWith('announcements.markedAsRead')
    expect(wrapper.find('.markdown-body').exists()).toBe(false)
    expect(useAnnouncementStore().unreadCount).toBe(0)
  })

  it('shares an in-flight automatic read and leaves a newer detail open', async () => {
    const firstRead = deferred()
    markRead.mockImplementation((id: number) => id === 1 ? firstRead.promise : Promise.resolve())
    useAnnouncementStore().announcements = [announcement(1), announcement(2)]
    const wrapper = mount(AnnouncementBell, {
      global: { stubs: { Teleport: true, Transition: true, Icon: true } }
    })
    await wrapper.get('button').trigger('click')
    await wrapper.findAll('.group.relative')[0].trigger('click')
    await confirmButton(wrapper).trigger('click')
    expect(markRead).toHaveBeenCalledTimes(1)

    const close = wrapper.findAll('button').find(item => item.text() === 'common.close')
    if (!close) throw new Error('Detail close button not found')
    await close.trigger('click')
    await wrapper.findAll('.group.relative')[1].trigger('click')
    await flushPromises()
    expect(wrapper.get('.markdown-body').text()).toContain('Details 2')

    firstRead.resolve()
    await flushPromises()
    expect(markRead).toHaveBeenCalledTimes(2)
    expect(wrapper.get('.markdown-body').text()).toContain('Details 2')
    expect(showSuccess).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
  })

  it.each(['success', 'failure'])('ignores an old %s after closing and reopening the same detail', async outcome => {
    const oldRead = deferred()
    const currentRead = deferred()
    markRead.mockReturnValueOnce(oldRead.promise).mockReturnValueOnce(currentRead.promise)
    const wrapper = mount(AnnouncementBell, {
      global: { stubs: { Teleport: true, Transition: true, Icon: true } }
    })
    await wrapper.get('button').trigger('click')
    await wrapper.get('.group.relative').trigger('click')
    await confirmButton(wrapper).trigger('click')
    expect(markRead).toHaveBeenCalledTimes(1)

    const close = wrapper.findAll('button').find(item => item.text() === 'common.close')
    if (!close) throw new Error('Detail close button not found')
    await close.trigger('click')
    await wrapper.get('.group.relative').trigger('click')
    expect(markRead).toHaveBeenCalledTimes(2)

    if (outcome === 'success') oldRead.resolve()
    else oldRead.reject(new Error('old read failed'))
    await flushPromises()
    expect(wrapper.get('.markdown-body').text()).toContain('Details 1')
    expect(showSuccess).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()

    currentRead.resolve()
    await flushPromises()
    expect(wrapper.get('.markdown-body').text()).toContain('Details 1')
    expect(useAnnouncementStore().unreadCount).toBe(0)
  })

  it('reconciles a failed new confirmation when an older request has already marked the item read', async () => {
    const oldRead = deferred()
    const newRead = deferred()
    markRead.mockReturnValueOnce(oldRead.promise).mockReturnValueOnce(newRead.promise)
    const wrapper = mount(AnnouncementBell, {
      global: { stubs: { Teleport: true, Transition: true, Icon: true } }
    })
    await wrapper.get('button').trigger('click')
    await wrapper.get('.group.relative').trigger('click')
    await confirmButton(wrapper).trigger('click')

    const close = wrapper.findAll('button').find(item => item.text() === 'common.close')
    if (!close) throw new Error('Detail close button not found')
    await close.trigger('click')
    await wrapper.get('.group.relative').trigger('click')
    await confirmButton(wrapper).trigger('click')
    expect(markRead).toHaveBeenCalledTimes(2)

    oldRead.resolve()
    await flushPromises()
    expect(useAnnouncementStore().unreadCount).toBe(0)
    expect(wrapper.get('.markdown-body').text()).toContain('Details 1')
    expect(showSuccess).not.toHaveBeenCalled()

    newRead.reject(new Error('new request failed'))
    await flushPromises()
    expect(showError).not.toHaveBeenCalled()
    expect(showSuccess).toHaveBeenCalledTimes(1)
    expect(showSuccess).toHaveBeenCalledWith('announcements.markedAsRead')
    expect(wrapper.find('.markdown-body').exists()).toBe(false)
  })

  it('does not show a read failure after unmount', async () => {
    const pending = deferred()
    markRead.mockReturnValueOnce(pending.promise)
    const wrapper = mount(AnnouncementBell, {
      global: { stubs: { Teleport: true, Transition: true, Icon: true } }
    })
    await wrapper.get('button').trigger('click')
    await wrapper.get('.group.relative').trigger('click')
    wrapper.unmount()

    pending.reject(new Error('offline'))
    await flushPromises()
    expect(showError).not.toHaveBeenCalled()
    expect(showSuccess).not.toHaveBeenCalled()
  })
})
