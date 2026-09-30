import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { announcementsAPI } from '@/api'
import type { UserAnnouncement } from '@/types'
import { getAnnouncementReadSessionVersion, invalidateAnnouncementReadSession } from '@/utils/announcementReadSession'

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
  const sessionGeneration = ref(getAnnouncementReadSessionVersion())
  const pendingReadRequests = new Map<number, Promise<void>>()
  let nextPopupTimer: ReturnType<typeof setTimeout> | null = null
  let popupAdvanceVersion = 0

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
    popupAdvanceVersion++
    if (nextPopupTimer) clearTimeout(nextPopupTimer)
    nextPopupTimer = null
    if (popupQueue.value.length > 0) {
      const session = sessionGeneration.value
      const version = popupAdvanceVersion
      const timer = setTimeout(() => {
        if (nextPopupTimer === timer) nextPopupTimer = null
        if (session !== sessionGeneration.value || version !== popupAdvanceVersion || currentPopup.value) return
        showNextPopup()
      }, 300)
      nextPopupTimer = timer
    }
  }

  function markReadRequest(id: number): Promise<void> {
    const pending = pendingReadRequests.get(id)
    if (pending) return pending

    const generation = sessionGeneration.value
    const request = Promise.resolve()
      .then(() => {
        if (generation !== sessionGeneration.value) return
        return announcementsAPI.markRead(id, generation)
      })
      .then(() => {
        if (generation !== sessionGeneration.value) return
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
    const generation = sessionGeneration.value
    try {
      await markReadRequest(id)
      return generation === sessionGeneration.value
    } catch (err: any) {
      if (generation === sessionGeneration.value) console.error('Failed to mark announcement as read:', err)
      return false
    }
  }

  async function markAllAsRead() {
    const generation = sessionGeneration.value
    const unread = announcements.value.filter((a) => !a.read_at)
    if (unread.length === 0) return true

    try {
      loading.value = true
      const results = await Promise.allSettled(unread.map((a) => markReadRequest(a.id)))
      if (generation !== sessionGeneration.value) return false
      const failure = results.find((result) => result.status === 'rejected')
      if (failure) throw failure.reason
      return true
    } catch (err: any) {
      if (generation !== sessionGeneration.value) return false
      console.error('Failed to mark all as read:', err)
      throw err
    } finally {
      if (generation === sessionGeneration.value) loading.value = false
    }
  }

  function reset() {
    fetchGeneration++
    sessionGeneration.value = invalidateAnnouncementReadSession()
    popupAdvanceVersion++
    if (nextPopupTimer) clearTimeout(nextPopupTimer)
    nextPopupTimer = null
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
    sessionGeneration,
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
