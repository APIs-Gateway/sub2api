/**
 * User-facing Channel Monitor API endpoints
 * Read-only views for end users to inspect channel availability/status.
 */

import { apiClient } from './client'
import type { Provider, MonitorStatus } from './admin/channelMonitor'

export type { Provider, MonitorStatus } from './admin/channelMonitor'

export interface UserMonitorExtraModel {
  model: string
  status: MonitorStatus
  latency_ms: number | null
}

export interface MonitorTimelinePoint {
  status: MonitorStatus
  latency_ms: number | null
  /** 仅管理员的响应里有；普通用户的响应不含该字段。 */
  ping_latency_ms?: number | null
  checked_at: string
}

export interface UserMonitorView {
  id: number
  name: string
  group_name: string
  primary_status: MonitorStatus
  primary_latency_ms: number | null
  availability_7d: number
  timeline: MonitorTimelinePoint[]
  // 以下字段只在管理员的响应里出现。普通用户的响应由后端裁掉，
  // 页面必须按「有就显示、没有就不显示」来渲染，不能假定它们存在。
  provider?: Provider
  primary_model?: string
  primary_ping_latency_ms?: number | null
  extra_models?: UserMonitorExtraModel[]
}

export interface UserMonitorListResponse {
  items: UserMonitorView[]
}

export interface UserMonitorModelDetail {
  /** 仅管理员的响应里有模型名；普通用户拿到的 models 只有主模型一项，且没有这个字段。 */
  model?: string
  latest_status: MonitorStatus
  latest_latency_ms: number | null
  availability_7d: number
  availability_15d: number
  availability_30d: number
  avg_latency_7d_ms: number | null
}

export interface UserMonitorDetail {
  id: number
  name: string
  /** 仅管理员的响应里有。 */
  provider?: Provider
  group_name: string
  models: UserMonitorModelDetail[]
}

/**
 * List all monitor views available to the current user.
 */
export async function list(options?: { signal?: AbortSignal }): Promise<UserMonitorListResponse> {
  const { data } = await apiClient.get<UserMonitorListResponse>('/channel-monitors', {
    signal: options?.signal,
  })
  return data
}

/**
 * Get detailed status (multi-window availability + latency) for a single monitor.
 */
export async function status(id: number): Promise<UserMonitorDetail> {
  const { data } = await apiClient.get<UserMonitorDetail>(`/channel-monitors/${id}/status`)
  return data
}

export const channelMonitorUserAPI = {
  list,
  status,
}

export default channelMonitorUserAPI
