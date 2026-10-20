/**
 * Admin API Keys API endpoints
 * Handles API key management for administrators
 */

import { apiClient } from '../client'
import type { ApiKey, AdminKeyFallbackChain, AdminHiddenChainPayload } from '@/types'

export interface UpdateApiKeyGroupResult {
  api_key: ApiKey
  auto_granted_group_access: boolean
  granted_group_id?: number
  granted_group_name?: string
}

/**
 * Update an API key's group binding
 * @param id - API Key ID
 * @param groupId - Group ID (0 to unbind, positive to bind, null/undefined to skip)
 * @returns Updated API key with auto-grant info
 */
export async function updateApiKeyGroup(id: number, groupId: number | null): Promise<UpdateApiKeyGroupResult> {
  const { data } = await apiClient.put<UpdateApiKeyGroupResult>(`/admin/api-keys/${id}`, {
    group_id: groupId === null ? 0 : groupId
  })
  return data
}

/** 查看某 Key 的有效链（含隐藏链与各跳的跳过原因） */
export async function getFallbackChain(id: number): Promise<AdminKeyFallbackChain> {
  const { data } = await apiClient.get<AdminKeyFallbackChain>(`/admin/api-keys/${id}/fallback-chain`)
  return data
}

/** 整体替换隐藏链；note 必填（至少 4 个字符） */
export async function putHiddenFallbackChain(
  id: number,
  payload: AdminHiddenChainPayload
): Promise<AdminKeyFallbackChain> {
  const { data } = await apiClient.put<AdminKeyFallbackChain>(
    `/admin/api-keys/${id}/hidden-fallback-chain`,
    payload
  )
  return data
}

/** 清空隐藏链 */
export async function deleteHiddenFallbackChain(id: number): Promise<void> {
  await apiClient.delete(`/admin/api-keys/${id}/hidden-fallback-chain`)
}

export const apiKeysAPI = {
  updateApiKeyGroup,
  getFallbackChain,
  putHiddenFallbackChain,
  deleteHiddenFallbackChain
}

export default apiKeysAPI
