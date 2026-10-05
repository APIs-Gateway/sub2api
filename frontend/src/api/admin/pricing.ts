/**
 * Admin pricing config API（只读）
 * 模型目录、分组按渠道配置推出的结果、「模型 × 分组」报价。
 * 价格单位：*_per_mtok 是美元 / 百万 Token（官方价口径），按次计费是美元 / 次。
 */

import { apiClient } from '../client'

export type CatalogStatus = 'draft' | 'active' | 'retired'

export interface ModelCatalogEntry {
  id: number
  model_key: string
  platform: string
  display_name: string
  aliases: string[]
  reference_model: string | null
  status: CatalogStatus
  note: string
  created_at: string
  updated_at: string
}

export type QuoteSource = 'none' | 'channel' | 'litellm' | 'fallback' | 'card_policy'

export interface QuoteUnitPrices {
  input: number
  output: number
  cache_write?: number
  cache_read?: number
}

export interface QuoteAccess {
  ok: boolean
  /** closed_in_group / not_in_allowlist / catalog_draft / catalog_retired */
  reason?: string
}

export interface OfficialReference {
  model: string
  priced: boolean
  source: QuoteSource
  per_mtok?: QuoteUnitPrices
}

export interface QuoteBatchCell {
  group_id: number
  model: string
  /** 非空表示这个格子没报出价 */
  error?: string
  access: QuoteAccess
  priced: boolean
  source: QuoteSource
  billing_mode: string
  group_multiplier: number
  extra_multiplier?: number
  effective_multiplier: number
  /** 用户实付单价（美元 / 百万 Token，已乘倍率） */
  final_per_mtok?: QuoteUnitPrices
  /** 按次计费的用户实付单价（美元 / 次，已乘倍率） */
  per_request_price?: number
  /** 按次计费没有主价、只有区间价时，区间价（已乘倍率）的最小 / 最大值；此时没有 per_request_price */
  per_request_min?: number
  per_request_max?: number
}

export interface QuoteBatchResult {
  models: OfficialReference[]
  cells: QuoteBatchCell[]
}

export type PricingStage = 'legacy' | 'shadow' | 'v2'

export interface DerivedCell {
  model_key: string
  is_pattern: boolean
  open: boolean
  price_mode: 'inherit' | 'extra' | 'custom'
  extra_multiplier?: number
}

export interface GroupDeriveView {
  group_id: number
  platform: string
  deleted: boolean
  derived: {
    group_id: number
    channel_id: number
    config: {
      access_mode: 'open' | 'allowlist'
      billing_model_source: string | null
      cost_mode: string
    }
    cells: DerivedCell[]
  }
  stored_config: { pricing_stage: PricingStage } | null
}

/** 一次批量报价最多带的分组数与模型数，与后端上限一致。 */
export const QUOTE_BATCH_MAX_GROUPS = 40
export const QUOTE_BATCH_MAX_MODELS = 60

export async function listModelCatalog(): Promise<ModelCatalogEntry[]> {
  const { data } = await apiClient.get<{ items: ModelCatalogEntry[] }>('/admin/model-catalog')
  return data.items ?? []
}

export async function getGroupDerive(groupId: number): Promise<GroupDeriveView> {
  const { data } = await apiClient.get<GroupDeriveView>(`/admin/pricing-matrix/groups/${groupId}/derive`)
  return data
}

/** groupIds 可以为空，此时只返回模型的官方参考价。 */
export async function quoteBatch(groupIds: number[], models: string[]): Promise<QuoteBatchResult> {
  const { data } = await apiClient.get<QuoteBatchResult>('/admin/pricing/quote-batch', {
    params: { group_ids: groupIds.join(','), models: models.join(',') }
  })
  return { models: data.models ?? [], cells: data.cells ?? [] }
}

export const pricingAPI = { listModelCatalog, getGroupDerive, quoteBatch }

export default pricingAPI
