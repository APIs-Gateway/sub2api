import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { flushPromises } from '@vue/test-utils'
import { useAnnouncementStore } from '../announcements'
import type { UserAnnouncement } from '@/types'

const { markRead, list } = vi.hoisted(() => ({ markRead: vi.fn(), list: vi.fn() }))
vi.mock('@/api', () => ({ announcementsAPI: { markRead, list } }))

const announcement = (id: number): UserAnnouncement => ({
  id, title: `Notice ${id}`, content: 'Content', notify_mode: 'silent',
  created_at: '2026-09-15T00:00:00Z', updated_at: '2026-09-15T00:00:00Z'
})

beforeEach(() => {
  setActivePinia(createPinia())
  vi.resetAllMocks()
  vi.spyOn(console, 'error').mockImplementation(() => {})
})
afterEach(() => { vi.restoreAllMocks() })

describe('mark all announcements read', () => {
  it.each(['success', 'failure'])('ignores stale %s while a new session fetch is loading', async outcome => {
    const store = useAnnouncementStore()
    store.announcements = [announcement(1)]
    let finishRead!: () => void
    let failRead!: (error: Error) => void
    markRead.mockImplementation(() => new Promise<void>((resolve, reject) => {
      finishRead = resolve
      failRead = reject
    }))
    const oldMarkAll = store.markAllAsRead()
    await flushPromises()

    store.reset()
    let finishFetch!: (items: UserAnnouncement[]) => void
    list.mockImplementation(() => new Promise<UserAnnouncement[]>(resolve => { finishFetch = resolve }))
    const newFetch = store.fetchAnnouncements(true)
    expect(store.loading).toBe(true)

    if (outcome === 'success') finishRead()
    else failRead(new Error('old session offline'))
    await expect(oldMarkAll).resolves.toBe(false)
    expect(store.loading).toBe(true)
    expect(store.announcements).toEqual([])
    expect(console.error).not.toHaveBeenCalled()

    finishFetch([announcement(2)])
    await newFetch
    expect(store.loading).toBe(false)
    expect(store.announcements.map(item => item.id)).toEqual([2])
    expect(store.unreadCount).toBe(1)
  })

  it('shares an in-flight individual read with mark-all', async () => {
    const store = useAnnouncementStore()
    store.announcements = [announcement(1)]
    let finish!: () => void
    markRead.mockImplementation(() => new Promise<void>(resolve => { finish = resolve }))

    const individual = store.markAsRead(1)
    const all = store.markAllAsRead()
    await flushPromises()
    expect(markRead).toHaveBeenCalledTimes(1)

    finish()
    expect(await individual).toBe(true)
    await all
    expect(store.unreadCount).toBe(0)
  })

  it('keeps successful results and retries only failed announcements', async () => {
    const store = useAnnouncementStore()
    store.announcements = [announcement(1), announcement(2)]
    const error = new Error('offline')
    markRead.mockImplementation((id: number) => id === 1 ? Promise.resolve() : Promise.reject(error))
    await expect(store.markAllAsRead()).rejects.toBe(error)
    expect(store.announcements[0].read_at).toBeTruthy()
    expect(store.announcements[1].read_at).toBeFalsy()
    expect(store.unreadCount).toBe(1)
    markRead.mockClear().mockResolvedValue(undefined)
    await store.markAllAsRead()
    expect(markRead.mock.calls).toEqual([[2, store.sessionGeneration]])
    expect(store.unreadCount).toBe(0)
  })

  it('keeps loading until all submitted requests finish, even after a rejection', async () => {
    const store = useAnnouncementStore()
    store.announcements = [announcement(1), announcement(2)]
    let finish!: () => void
    const error = new Error('offline')
    markRead.mockImplementation((id: number) => id === 1
      ? Promise.reject(error) : new Promise<void>(resolve => { finish = resolve }))
    const result = store.markAllAsRead().catch(err => err)
    await flushPromises()
    expect(store.loading).toBe(true)
    finish()
    expect(await result).toBe(error)
    expect(store.loading).toBe(false)
    expect(store.announcements[1].read_at).toBeTruthy()
  })

  it('does not mark a newly arrived announcement that was not submitted', async () => {
    const store = useAnnouncementStore()
    store.announcements = [announcement(1)]
    let finish!: () => void
    markRead.mockImplementation(() => new Promise<void>(resolve => { finish = resolve }))
    const result = store.markAllAsRead()
    store.announcements.push(announcement(2))
    await flushPromises()
    finish()
    await result
    expect(markRead.mock.calls).toEqual([[1, store.sessionGeneration]])
    expect(store.announcements[0].read_at).toBeTruthy()
    expect(store.announcements[1].read_at).toBeFalsy()
  })
})
