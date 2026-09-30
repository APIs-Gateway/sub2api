import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { announcementsAPI } from '@/api'
import type { UserAnnouncement } from '@/types'

const THROTTLE_MS = 20 * 60 * 1000 // 20 minutes

export const useAnnouncementStore = defineStore('announcements', () => {
  // State
  const announcements = ref<UserAnnouncement[]>([])
  const loading = ref(false)
  const lastFetchTime = ref(0)
  const popupQueue = ref<UserAnnouncement[]>([])
  const currentPopup = ref<UserAnnouncement | null>(null)

  // Session-scoped dedup set — not reactive, used as plain lookup only
  let shownPopupIds = new Set<number>()
  let fetchGeneration = 0
  let readGeneration = 0
  const pendingReadRequests = new Map<number, Promise<void>>()

  // Getters
  const unreadCount = computed(() =>
    announcements.value.filter((a) => !a.read_at).length
  )

  // Actions
  async function fetchAnnouncements(force = false) {
    const now = Date.now()
    if (!force && lastFetchTime.value > 0 && now - lastFetchTime.value < THROTTLE_MS) {
      return
    }

    // Set immediately to prevent concurrent duplicate requests
    lastFetchTime.value = now
    const generation = ++fetchGeneration

    try {
      loading.value = true
      const all = await announcementsAPI.list(false)
      if (generation !== fetchGeneration) return
      announcements.value = all.slice(0, 20)
      enqueueNewPopups()
    } catch (err: any) {
      if (generation !== fetchGeneration) return
      // Revert throttle timestamp on failure so retry is allowed
      lastFetchTime.value = 0
      console.error('Failed to fetch announcements:', err)
    } finally {
      if (generation === fetchGeneration) loading.value = false
    }
  }

  function enqueueNewPopups() {
    const newPopups = announcements.value.filter(
      (a) => a.notify_mode === 'popup' && !a.read_at && !shownPopupIds.has(a.id)
    )
    if (newPopups.length === 0) return

    for (const p of newPopups) {
      if (!popupQueue.value.some((q) => q.id === p.id)) {
        popupQueue.value.push(p)
      }
    }

    if (!currentPopup.value) {
      showNextPopup()
    }
  }

  function showNextPopup() {
    if (popupQueue.value.length === 0) {
      currentPopup.value = null
      return
    }
    currentPopup.value = popupQueue.value.shift()!
    shownPopupIds.add(currentPopup.value.id)
  }

  async function dismissPopup() {
    if (!currentPopup.value) return
    const id = currentPopup.value.id
    currentPopup.value = null

    // Mark as read (fire-and-forget, UI already updated)
    markAsRead(id)

    // Show next popup after a short delay
    if (popupQueue.value.length > 0) {
      setTimeout(() => showNextPopup(), 300)
    }
  }

  function markReadRequest(id: number): Promise<void> {
    const pending = pendingReadRequests.get(id)
    if (pending) return pending

    const generation = readGeneration
    const request = Promise.resolve()
      .then(() => announcementsAPI.markRead(id))
      .then(() => {
        if (generation !== readGeneration) return
        const ann = announcements.value.find((a) => a.id === id)
        if (ann) ann.read_at = new Date().toISOString()
      })
    pendingReadRequests.set(id, request)
    void request.then(
      () => { if (pendingReadRequests.get(id) === request) pendingReadRequests.delete(id) },
      () => { if (pendingReadRequests.get(id) === request) pendingReadRequests.delete(id) }
    )
    return request
  }

  async function markAsRead(id: number) {
    const generation = readGeneration
    try {
      await markReadRequest(id)
      return generation === readGeneration
    } catch (err: any) {
      if (generation === readGeneration) console.error('Failed to mark announcement as read:', err)
      return false
    }
  }

  async function markAllAsRead() {
    const generation = readGeneration
    const unread = announcements.value.filter((a) => !a.read_at)
    if (unread.length === 0) return true

    try {
      loading.value = true
      const results = await Promise.allSettled(unread.map((a) => markReadRequest(a.id)))
      if (generation !== readGeneration) return false
      const failure = results.find((result) => result.status === 'rejected')
      if (failure) throw failure.reason
      return true
    } catch (err: any) {
      if (generation !== readGeneration) return false
      console.error('Failed to mark all as read:', err)
      throw err
    } finally {
      if (generation === readGeneration) loading.value = false
    }
  }

  function reset() {
    fetchGeneration++
    readGeneration++
    pendingReadRequests.clear()
    announcements.value = []
    lastFetchTime.value = 0
    shownPopupIds = new Set()
    popupQueue.value = []
    currentPopup.value = null
    loading.value = false
  }

  return {
    // State
    announcements,
    loading,
    currentPopup,
    // Getters
    unreadCount,
    // Actions
    fetchAnnouncements,
    dismissPopup,
    markAsRead,
    markAllAsRead,
    reset,
  }
})
