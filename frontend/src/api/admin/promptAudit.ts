import { apiClient } from '../client'
import type { PaginatedResponse } from '@/types'

// Prompt Audit (issue #884) —— 后台只读事件查询 API 客户端。
// 契约来自 backend/internal/securityaudit/prompt_event_handler.go 与
// backend/internal/securityaudit/prompt_repository.go 中的 Event/PromptSnapshot：
// 这是一个刻意只读、且已脱敏的契约，不包含 full_prompt 字段（全文持久化在 #585 中单独跟踪）。

export type PromptAuditDecision = 'pass' | 'flag' | 'critical'
export type PromptAuditRiskLevel = 'low' | 'medium' | 'high' | 'critical'

export interface PromptAuditSnapshot {
  request_id: string
  user_id: number
  username: string
  user_email: string
  api_key_id: number
  api_key_name: string
  group_id?: number | null
  group_name: string
  provider: string
  endpoint: string
  protocol: string
  model: string
  prompt_hash: string
  redacted_preview: string
  prompt_length: number
  message_count: number
  stage: string
}

export interface PromptAuditIssueSummary {
  category: string
  scanner_id: string
  title: string
  description: string
  severity: string
  severity_label: string
  action: string
  action_label: string
  code: string
  score: number
  evidence: string
  evidence_hash: string
  start_rune?: number | null
  end_rune?: number | null
}

export interface PromptAuditEvent {
  id: number
  job_id: number
  snapshot: PromptAuditSnapshot
  decision: PromptAuditDecision
  risk_level: PromptAuditRiskLevel
  action: string
  categories: string[]
  matched_scanners: string[]
  scanner_scores: Record<string, number>
  scanner_evidence: Record<string, string>
  scanner_backend: string
  scanner_version: string
  guard_endpoint_id: string
  policy_id: string
  policy_version: number
  config_version: number
  chunk_total: number
  latency_ms: number
  issue_summaries: PromptAuditIssueSummary[]
  created_at: string
}

export interface ListPromptAuditEventsParams {
  decision?: string
  risk_level?: string
  endpoint?: string
  group_id?: number
  user_id?: number
  api_key_id?: number
  request_id?: string
  prompt_hash?: string
  keyword?: string
  start_at?: string
  end_at?: string
  page?: number
  page_size?: number
}

export async function listPromptAuditEvents(
  params: ListPromptAuditEventsParams = {},
): Promise<PaginatedResponse<PromptAuditEvent>> {
  const { data } = await apiClient.get<PaginatedResponse<PromptAuditEvent>>('/admin/prompt-audit/events', {
    params: {
      decision: params.decision || undefined,
      risk_level: params.risk_level || undefined,
      endpoint: params.endpoint || undefined,
      group_id: params.group_id || undefined,
      user_id: params.user_id || undefined,
      api_key_id: params.api_key_id || undefined,
      request_id: params.request_id || undefined,
      prompt_hash: params.prompt_hash || undefined,
      keyword: params.keyword || undefined,
      start_at: params.start_at || undefined,
      end_at: params.end_at || undefined,
      page: params.page ?? 1,
      page_size: params.page_size ?? 20,
    },
  })
  return data
}

export async function getPromptAuditEvent(id: number): Promise<PromptAuditEvent> {
  const { data } = await apiClient.get<PromptAuditEvent>(`/admin/prompt-audit/events/${id}`)
  return data
}

export default {
  listPromptAuditEvents,
  getPromptAuditEvent,
}
