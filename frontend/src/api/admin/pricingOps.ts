/**
 * Admin pricing ops API（W6 PR10b-2）：价格快照审批、阶段切换与影子比对。
 * 接口契约见 API_CONTRACT.md 第 6、8 节；快照接口见后端 pricing_snapshot_handler。
 * 价格单位：LiteLLM 快照里是美元 / Token，界面换成美元 / 百万 Token 后再显示。
 */

import { apiClient } from '../client'

// ==================== 通用错误 ====================

/** 拦截器抛出的结构化错误里界面用得到的部分。 */
export interface OpsApiError {
  status?: number
  code?: number
  message?: string
  reason?: string
  metadata?: Record<string, string>
}

export function asOpsError(err: unknown): OpsApiError {
  return err && typeof err === 'object' ? (err as OpsApiError) : { message: String(err) }
}

// ==================== 价格快照 ====================

export type SnapshotStatus = 'candidate' | 'active' | 'superseded' | 'rejected'
export type SnapshotMode = 'auto' | 'pinned'

export interface SnapshotMeta {
  id: number
  label: string
  source: string
  contentSha256: string
  modelCount: number
  status: SnapshotStatus
  fetchedAt: string
  approvedBy: number | null
  approvedAt: string | null
  note: string
}

export interface SnapshotOverview {
  mode: SnapshotMode
  active: SnapshotMeta | null
  pending: SnapshotMeta[]
  history: SnapshotMeta[]
}

/** LiteLLM 单个模型的价格记录（美元 / Token）；只列界面用到的字段。 */
export interface LiteLLMPricing {
  input_cost_per_token?: number
  output_cost_per_token?: number
  cache_creation_input_token_cost?: number
  cache_read_input_token_cost?: number
  output_cost_per_image?: number
  litellm_provider?: string
  mode?: string
}

export type DiffType = 'added' | 'removed' | 'changed'

export interface SnapshotDiffEntry {
  model_key: string
  change_type: DiffType
  changed_fields: string[]
  old?: LiteLLMPricing
  new?: LiteLLMPricing
  decision: 'approve' | 'hold'
}

export interface EffectiveChange {
  model: string
  old_missing: boolean
  new_missing: boolean
  old_input_per_mtok: number
  new_input_per_mtok: number
  old_output_per_mtok: number
  new_output_per_mtok: number
}

export interface SnapshotPlan {
  base: SnapshotMeta
  candidate: SnapshotMeta
  stats: { added: number; removed: number; changed: number; unchanged: number; approved: number; held: number }
  entries: SnapshotDiffEntry[]
  heldModels: string[]
  planHash: string
  effectiveChanges: EffectiveChange[]
  effectiveChangesError: string
  exposureError: string
}

export interface SnapshotFetchResult {
  unchanged: boolean
  created: boolean
  candidate: SnapshotMeta | null
}

type Raw = Record<string, unknown>

/** 后端的快照元数据目前没有 json 标签（字段名是 Go 的大写驼峰），这里两种写法都认。 */
function pick<T>(raw: Raw, ...keys: string[]): T | undefined {
  for (const k of keys) if (raw[k] !== undefined && raw[k] !== null) return raw[k] as T
  return undefined
}

export function normalizeSnapshotMeta(raw: unknown): SnapshotMeta {
  const r = (raw ?? {}) as Raw
  return {
    id: pick<number>(r, 'id', 'ID') ?? 0,
    label: pick<string>(r, 'label', 'Label') ?? '',
    source: pick<string>(r, 'source', 'Source') ?? '',
    contentSha256: pick<string>(r, 'content_sha256', 'ContentSHA256') ?? '',
    modelCount: pick<number>(r, 'model_count', 'ModelCount') ?? 0,
    status: (pick<string>(r, 'status', 'Status') ?? 'candidate') as SnapshotStatus,
    fetchedAt: pick<string>(r, 'fetched_at', 'FetchedAt') ?? '',
    approvedBy: pick<number>(r, 'approved_by', 'ApprovedBy') ?? null,
    approvedAt: pick<string>(r, 'approved_at', 'ApprovedAt') ?? null,
    note: pick<string>(r, 'note', 'Note') ?? ''
  }
}

function metaList(raw: unknown): SnapshotMeta[] {
  return Array.isArray(raw) ? raw.map(normalizeSnapshotMeta) : []
}

export async function getSnapshotOverview(): Promise<SnapshotOverview> {
  const { data } = await apiClient.get<Raw>('/admin/pricing/snapshots')
  return {
    mode: (data.mode === 'pinned' ? 'pinned' : 'auto') as SnapshotMode,
    active: data.active ? normalizeSnapshotMeta(data.active) : null,
    pending: metaList(data.pending_candidates),
    history: metaList(data.history)
  }
}

/** 固定当前价格：把线上正在用的价格存成第一个生效版本。需要登录会话。 */
export async function pinSnapshot(): Promise<void> {
  await apiClient.post('/admin/pricing/snapshots/pin', { confirm: true })
}

export async function fetchSnapshotCandidate(): Promise<SnapshotFetchResult> {
  const { data } = await apiClient.post<Raw>('/admin/pricing/snapshots/fetch')
  return {
    unchanged: data.unchanged === true,
    created: data.created === true,
    candidate: data.candidate ? normalizeSnapshotMeta(data.candidate) : null
  }
}

export async function previewSnapshot(id: number, holdModels: string[]): Promise<SnapshotPlan> {
  const { data } = await apiClient.post<Raw>(`/admin/pricing/snapshots/${id}/preview`, { hold_models: holdModels })
  const stats = (data.stats ?? {}) as SnapshotPlan['stats']
  return {
    base: normalizeSnapshotMeta(data.base_snapshot),
    candidate: normalizeSnapshotMeta(data.candidate),
    stats: {
      added: stats.added ?? 0,
      removed: stats.removed ?? 0,
      changed: stats.changed ?? 0,
      unchanged: stats.unchanged ?? 0,
      approved: stats.approved ?? 0,
      held: stats.held ?? 0
    },
    entries: (data.entries as SnapshotDiffEntry[] | null) ?? [],
    heldModels: (data.held_models as string[] | null) ?? [],
    planHash: (data.plan_hash as string) ?? '',
    effectiveChanges: (data.effective_changes as EffectiveChange[] | null) ?? [],
    effectiveChangesError: (data.effective_changes_error as string) ?? '',
    exposureError: (data.exposure_error as string) ?? ''
  }
}

/** 批准：必须带预览时拿到的 plan_hash 与同一份搁置名单。需要登录会话。 */
export async function approveSnapshot(id: number, holdModels: string[], planHash: string): Promise<void> {
  await apiClient.post(`/admin/pricing/snapshots/${id}/approve`, {
    hold_models: holdModels,
    plan_hash: planHash,
    confirm: true
  })
}

export async function rejectSnapshot(id: number): Promise<void> {
  await apiClient.post(`/admin/pricing/snapshots/${id}/reject`)
}

// ==================== 阶段切换与影子比对 ====================

export type OpsStage = 'legacy' | 'shadow' | 'v2'

export interface StageSwitchResult {
  group_id: number
  from: OpsStage
  to: OpsStage
  changed: boolean
  revision: number
  changed_at: string
}

/** 目前只允许 legacy 与 shadow；v2 等后端放开后再接，界面不会发 v2 请求。 */
export async function switchGroupStage(groupId: number, stage: 'legacy' | 'shadow'): Promise<StageSwitchResult> {
  const { data } = await apiClient.put<StageSwitchResult>(`/admin/pricing-matrix/groups/${groupId}/stage`, {
    stage,
    confirm: true
  })
  return data
}

export interface ShadowDiffCount {
  group_id: number
  kind: string
  class: string
  count: number
}

export interface ShadowStats {
  compared: { group_id: number; count: number }[]
  diffs: ShadowDiffCount[]
}

/** 进程内计数，重启后清零；多实例各算各的。 */
export async function getShadowStats(): Promise<ShadowStats> {
  const { data } = await apiClient.get<{ metrics?: Raw }>('/admin/pricing-matrix/shadow/stats')
  const m = (data.metrics ?? {}) as Raw
  return {
    compared: (m.pricing_shadow_compared_total as ShadowStats['compared'] | null) ?? [],
    diffs: (m.pricing_shadow_diff_total as ShadowDiffCount[] | null) ?? []
  }
}

export interface ShadowSample {
  created_at: string
  group_id: number
  model: string
  kind: string
  class: string
  legacy_view?: unknown
  v2_view?: unknown
}

export async function getShadowSamples(groupId: number, limit = 20): Promise<ShadowSample[]> {
  const { data } = await apiClient.get<{ items: ShadowSample[] | null }>('/admin/pricing-matrix/shadow/diffs', {
    params: { group_id: groupId, limit }
  })
  return data.items ?? []
}

// ==================== 分组价格配置现状 ====================

/** 分组的价格配置现状：阶段与基线 revision。读的是派生查看接口。 */
export interface GroupOpsView {
  groupId: number
  platform: string
  stage: OpsStage | null
  stageChangedAt: string | null
  /** 没有价格配置行（还没派生过）时为 null */
  revision: number | null
}

export async function getGroupOpsView(groupId: number): Promise<GroupOpsView> {
  const { data } = await apiClient.get<Raw>(`/admin/pricing-matrix/groups/${groupId}/derive`)
  const cfg = data.stored_config as { pricing_stage?: OpsStage; revision?: number; stage_changed_at?: string } | null
  return {
    groupId,
    platform: (data.platform as string) ?? '',
    stage: cfg?.pricing_stage ?? null,
    stageChangedAt: cfg?.stage_changed_at ?? null,
    revision: cfg ? (cfg.revision ?? 0) : null
  }
}

export const pricingOpsAPI = {
  getSnapshotOverview,
  pinSnapshot,
  fetchSnapshotCandidate,
  previewSnapshot,
  approveSnapshot,
  rejectSnapshot,
  switchGroupStage,
  getShadowStats,
  getShadowSamples,
  getGroupOpsView
}

export default pricingOpsAPI
