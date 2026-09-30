import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useAnnouncementStore } from '../announcements'
import type { UserAnnouncement } from '@/types'

const { list, markRead } = vi.hoisted(() => ({ list: vi.fn(), markRead: vi.fn() }))
vi.mock('@/api', () => ({ announcementsAPI: { list, markRead } }))

const notice = (id: number): UserAnnouncement => ({
  id, title: `Notice ${id}`, content: 'Content', notify_mode: 'popup',
  created_at: '2026-09-18T00:00:00Z', updated_at: '2026-09-18T00:00:00Z'
})

function pendingList() {
  let resolve!: (value: UserAnnouncement[]) => void
  let reject!: (reason: Error) => void
  const promise = new Promise<UserAnnouncement[]>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

beforeEach(() => {
  setActivePinia(createPinia())
  vi.resetAllMocks()
  markRead.mockResolvedValue({ message: 'ok' })
  vi.spyOn(console, 'error').mockImplementation(() => {})
})
afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

describe('announcement fetch ownership', () => {
  it('does not let an old dismissal timer replace a new session popup', async () => {
    vi.useFakeTimers()
    const store = useAnnouncementStore()
    list.mockResolvedValueOnce([notice(1), notice(2)]).mockResolvedValueOnce([notice(3), notice(4)])
    await store.fetchAnnouncements(true)
    expect(store.currentPopup?.id).toBe(1)
    await store.dismissPopup()
    expect(store.currentPopup).toBeNull()

    store.reset()
    await store.fetchAnnouncements(true)
    expect(store.currentPopup?.id).toBe(3)
    await vi.advanceTimersByTimeAsync(300)
    expect(store.currentPopup?.id).toBe(3)
  })

  it('does not let an old timer replace a popup opened by a same-session refresh', async () => {
    vi.useFakeTimers()
    const store = useAnnouncementStore()
    list.mockResolvedValueOnce([notice(1), notice(2)]).mockResolvedValueOnce([notice(2), notice(3)])
    await store.fetchAnnouncements(true)
    await store.dismissPopup()
    await store.fetchAnnouncements(true)
    expect(store.currentPopup?.id).toBe(2)

    await vi.advanceTimersByTimeAsync(300)
    expect(store.currentPopup?.id).toBe(2)
  })

  it('waits for the latest dismissal when two popup timers overlap', async () => {
    vi.useFakeTimers()
    const store = useAnnouncementStore()
    list.mockResolvedValueOnce([notice(1), notice(2), notice(3)])
      .mockResolvedValueOnce([notice(2), notice(3)])
    await store.fetchAnnouncements(true)
    await store.dismissPopup()
    await vi.advanceTimersByTimeAsync(100)
    await store.fetchAnnouncements(true)
    expect(store.currentPopup?.id).toBe(2)
    await store.dismissPopup()

    await vi.advanceTimersByTimeAsync(200)
    expect(store.currentPopup).toBeNull()
    await vi.advanceTimersByTimeAsync(100)
    expect(store.currentPopup?.id).toBe(3)
  })

  it('does not restore announcements or popups after logout resets the store', async () => {
    const store = useAnnouncementStore()
    const old = pendingList()
    list.mockReturnValueOnce(old.promise)
    const request = store.fetchAnnouncements()
    store.reset()
    old.resolve([notice(1)])
    await request
    expect(store.announcements).toEqual([])
    expect(store.currentPopup).toBeNull()
    expect(store.loading).toBe(false)
  })

  it('keeps the new session loading when an old request finishes', async () => {
    const store = useAnnouncementStore()
    const old = pendingList()
    const current = pendingList()
    list.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const oldRequest = store.fetchAnnouncements()
    store.reset()
    const currentRequest = store.fetchAnnouncements()
    old.resolve([])
    await oldRequest
    expect(store.loading).toBe(true)
    current.resolve([notice(2)])
    await currentRequest
    expect(store.announcements.map(a => a.id)).toEqual([2])
    expect(store.loading).toBe(false)
  })

  it('keeps the latest forced refresh when responses arrive out of order', async () => {
    const store = useAnnouncementStore()
    const old = pendingList()
    list.mockReturnValueOnce(old.promise).mockResolvedValueOnce([notice(2)])
    const oldRequest = store.fetchAnnouncements()
    await store.fetchAnnouncements(true)
    old.resolve([notice(1)])
    await oldRequest
    expect(store.announcements.map(a => a.id)).toEqual([2])
    expect(store.currentPopup?.id).toBe(2)
  })

  it('does not clear the new session throttle when an old request rejects', async () => {
    const store = useAnnouncementStore()
    const old = pendingList()
    list.mockReturnValueOnce(old.promise).mockResolvedValue([notice(2)])
    const oldRequest = store.fetchAnnouncements()
    store.reset()
    await store.fetchAnnouncements()
    old.reject(new Error('old request failed'))
    await oldRequest
    await store.fetchAnnouncements()
    expect(list).toHaveBeenCalledTimes(2)
    expect(store.announcements.map(a => a.id)).toEqual([2])
  })

  it('still retries a failed current request', async () => {
    const store = useAnnouncementStore()
    list.mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce([notice(1)])
    await store.fetchAnnouncements()
    await store.fetchAnnouncements()
    expect(list).toHaveBeenCalledTimes(2)
    expect(store.currentPopup?.id).toBe(1)
  })

  it('still throttles ordinary concurrent fetches', async () => {
    const store = useAnnouncementStore()
    const current = pendingList()
    list.mockReturnValueOnce(current.promise)
    const request = store.fetchAnnouncements()
    await store.fetchAnnouncements()
    expect(list).toHaveBeenCalledTimes(1)
    expect(store.loading).toBe(true)
    current.resolve([])
    await request
    expect(store.loading).toBe(false)
  })
})
