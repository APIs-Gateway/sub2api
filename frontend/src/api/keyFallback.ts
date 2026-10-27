/**
 * Key 级分组兜底链（用户端）
 *
 * 接口契约见回退链设计 5.1：三个接口都挂在 /keys 下，链只含兜底项，主分组由
 * PUT /keys/:id 的 group_id 管理。
 */

import { apiClient } from './client'
import type { KeyFallbackChain, KeyFallbackSummary } from '@/types'

/** 读单个 Key 的链（含每项参考价与可添加的分组） */
export async function getChain(
  keyId: number,
  options?: { model?: string; signal?: AbortSignal }
): Promise<KeyFallbackChain> {
  const { data } = await apiClient.get<KeyFallbackChain>(`/keys/${keyId}/fallback-chain`, {
    params: options?.model ? { model: options.model } : undefined,
    signal: options?.signal
  })
  return data
}

/** 批量读：按平台分区列出所有已绑定分组的 Key（不含价格与可添加分组） */
export async function listChains(options?: { signal?: AbortSignal }): Promise<KeyFallbackSummary> {
  const { data } = await apiClient.get<KeyFallbackSummary>('/keys/fallback-chains', {
    signal: options?.signal
  })
  return data
}

/** 整条替换兜底项：只传兜底分组 ID，按顺序即位置；空数组表示清空 */
export async function replaceChain(
  keyId: number,
  groupIds: number[],
  options?: { model?: string }
): Promise<KeyFallbackChain> {
  const { data } = await apiClient.put<KeyFallbackChain>(
    `/keys/${keyId}/fallback-chain`,
    { group_ids: groupIds },
    { params: options?.model ? { model: options.model } : undefined }
  )
  return data
}

export const keyFallbackAPI = {
  getChain,
  listChains,
  replaceChain
}

export default keyFallbackAPI
