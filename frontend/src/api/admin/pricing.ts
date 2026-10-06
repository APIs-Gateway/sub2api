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

export type PriceMode = 'inherit' | 'extra' | 'custom'

export interface CustomPrice {
  billing_mode: 'token' | 'per_request' | 'image'
  input_price?: number | null
  output_price?: number | null
  cache_write_price?: number | null
  cache_read_price?: number | null
  image_output_price?: number | null
  per_request_price?: number | null
  intervals?: unknown[]
}

/** 库里存着的单元格（只有分组已经切到新配置才会有）。 */
export interface StoredCell {
  group_id: number
  model_key: string
  is_pattern: boolean
  open: boolean
  price_mode: PriceMode
  extra_multiplier?: number | null
  custom_price?: CustomPrice | null
  source?: string
  /** 写入时的基线 */
  revision: number
}

export interface GroupDeriveView {
  group_id: number
  platform: string
  deleted: boolean
  stored_cells?: StoredCell[] | null
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
  stored_config: { pricing_stage: PricingStage; revision?: number } | null
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

// ---------------------------------------------------------------------------
// 写入（后端契约：W6 PR4b-2b-2）。预览不需要交互式会话，提交需要。

export interface CellOp {
  group_id: number
  model_key: string
  kind: 'upsert' | 'delete'
  open?: boolean
  price_mode?: PriceMode
  extra_multiplier?: number | null
  custom_price?: CustomPrice | null
  source?: 'manual' | 'copied'
  /** 界面读到的单元格 revision；0 表示界面认为单元格不存在 */
  baseline_revision: number
}

export interface CellsRequest {
  ops: CellOp[]
  /** 键是分组 id 的字符串，必须正好等于 ops 涉及的分组集合 */
  group_revisions: Record<string, number>
}

export type PriceDelta = 'up' | 'down' | 'none' | 'unknown'

export interface PlannedCellState {
  model_key: string
  is_pattern: boolean
  open: boolean
  price_mode: PriceMode
  extra_multiplier?: number | null
  custom_price?: CustomPrice | null
  source?: string
}

export interface PlannedCell {
  op: CellOp
  action: 'create' | 'update' | 'delete' | 'noop'
  before?: PlannedCellState
  after?: PlannedCellState
  touches_price: boolean
}

export interface PrecheckIssue {
  group_id: number
  model_key: string
  reason: string
  target?: string
}

export interface PrecheckReport {
  group_id: number
  stage: PricingStage
  access_mode: 'open' | 'allowlist'
  applicable: boolean
  blocking: PrecheckIssue[]
  warnings: PrecheckIssue[]
}

export interface CellsTicket {
  approval_id: number
  plan_hash: string
  touches_price: boolean
  price_delta: PriceDelta
  expires_at: string
  planned: PlannedCell[]
  precheck?: PrecheckReport[]
}

export interface CellsCommitResult {
  planned: PlannedCell[]
  changed_group_ids: number[]
  touches_price: boolean
}

export async function previewCells(request: CellsRequest): Promise<CellsTicket> {
  const { data } = await apiClient.post<CellsTicket>('/admin/pricing-matrix/cells/preview', request)
  return { ...data, planned: data.planned ?? [], precheck: data.precheck ?? [] }
}

/** approval_id 传预览返回的值；request 必须与预览时逐字段一致。 */
export async function commitCells(approvalId: number, request: CellsRequest): Promise<CellsCommitResult> {
  const { data } = await apiClient.post<CellsCommitResult>('/admin/pricing-matrix/cells/commit', {
    approval_id: approvalId,
    confirm: true,
    request
  })
  return data
}

export interface CatalogUsage {
  requests: number
  last_used_at: string | null
  window_days: number
}

export interface CatalogTransitionPreview {
  entry: ModelCatalogEntry
  from: CatalogStatus
  to: CatalogStatus
  usage: CatalogUsage
  confirm_required: boolean
}

export async function previewCatalogTransition(id: number, to: CatalogStatus): Promise<CatalogTransitionPreview> {
  const { data } = await apiClient.get<CatalogTransitionPreview>(`/admin/model-catalog/${id}/transition-preview`, {
    params: { to }
  })
  return data
}

export async function transitionCatalog(id: number, to: CatalogStatus, confirmUsage: boolean): Promise<ModelCatalogEntry> {
  const { data } = await apiClient.put<ModelCatalogEntry>(`/admin/model-catalog/${id}/status`, {
    to,
    confirm_usage: confirmUsage
  })
  return data
}

export interface CreateCatalogInput {
  model_key: string
  platform: string
  display_name: string
  aliases: string[]
  reference_model: string | null
  status: CatalogStatus
  note: string
  confirm_usage: boolean
}

export async function createCatalogEntry(input: CreateCatalogInput): Promise<ModelCatalogEntry> {
  const { data } = await apiClient.post<ModelCatalogEntry>('/admin/model-catalog', input)
  return data
}

export const pricingAPI = {
  listModelCatalog,
  getGroupDerive,
  quoteBatch,
  previewCells,
  commitCells,
  previewCatalogTransition,
  transitionCatalog,
  createCatalogEntry
}

export default pricingAPI
