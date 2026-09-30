/**
 * User Announcements API endpoints
 */

import { apiClient } from './client'
import type { AxiosRequestConfig } from 'axios'
import type { UserAnnouncement } from '@/types'
import { getAnnouncementReadSessionVersion } from '@/utils/announcementReadSession'

type AnnouncementReadRequestConfig = AxiosRequestConfig & { _announcementReadSessionVersion: number }

export async function list(unreadOnly: boolean = false): Promise<UserAnnouncement[]> {
  const { data } = await apiClient.get<UserAnnouncement[]>('/announcements', {
    params: unreadOnly ? { unread_only: 1 } : {}
  })
  return data
}

export async function markRead(
  id: number,
  sessionVersion = getAnnouncementReadSessionVersion()
): Promise<{ message: string }> {
  const config: AnnouncementReadRequestConfig = { _announcementReadSessionVersion: sessionVersion }
  const { data } = await apiClient.post<{ message: string }>(`/announcements/${id}/read`, undefined, config)
  return data
}

const announcementsAPI = {
  list,
  markRead
}

export default announcementsAPI
