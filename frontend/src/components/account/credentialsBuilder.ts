export function applyInterceptWarmup(
  credentials: Record<string, unknown>,
  enabled: boolean,
  mode: 'create' | 'edit'
): void {
  if (enabled) {
    credentials.intercept_warmup_requests = true
  } else if (mode === 'edit') {
    delete credentials.intercept_warmup_requests
  }
}

export const ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY = 'antigravity_project_id'

export function applyAntigravityProjectID(
  credentials: Record<string, unknown>,
  projectId: string,
  mode: 'create' | 'edit'
): void {
  const trimmed = projectId.trim()
  if (trimmed) {
    credentials[ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY] = trimmed
  } else if (mode === 'edit') {
    delete credentials[ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY]
  }
}

export const HEADER_OVERRIDE_ENABLED_CREDENTIAL_KEY = 'header_override_enabled'
export const HEADER_OVERRIDES_CREDENTIAL_KEY = 'header_overrides'

export interface HeaderOverrideRow {
  name: string
  value: string
}

export function isHeaderOverridePlatform(platform: string): boolean {
  return platform === 'anthropic' || platform === 'openai'
}

const HEADER_OVERRIDE_BLOCKED_NAMES = new Set([
  'host', 'content-length', 'content-type', 'transfer-encoding', 'connection', 'keep-alive',
  'proxy-authenticate', 'proxy-authorization', 'proxy-connection', 'te', 'trailer', 'upgrade',
  'authorization', 'x-api-key', 'x-goog-api-key', 'cookie', 'accept-encoding',
  'sec-websocket-key', 'sec-websocket-version', 'sec-websocket-extensions',
  'sec-websocket-protocol', 'sec-websocket-accept', 'session_id', 'conversation_id',
  'x-codex-turn-state', 'x-codex-turn-metadata', 'chatgpt-account-id',
  'x-claude-code-session-id', 'x-client-request-id'
])
const HEADER_NAME_PATTERN = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/
const HEADER_OVERRIDE_MAX_ENTRIES = 64
const HEADER_OVERRIDE_MAX_NAME_LENGTH = 200
const HEADER_OVERRIDE_MAX_VALUE_LENGTH = 8192
// eslint-disable-next-line no-control-regex
const HEADER_VALUE_INVALID_PATTERN = /[\x00-\x08\x0a-\x1f\x7f]/
const HEADER_TEXT_ENCODER = new TextEncoder()

export function validateHeaderOverrideRows(
  rows: HeaderOverrideRow[]
): 'invalidName' | 'blockedName' | 'duplicateName' | 'invalidValue' | 'tooManyEntries' | null {
  const seen = new Set<string>()
  for (const row of rows) {
    const name = row.name.trim()
    const value = row.value.trim()
    if (!name) {
      if (value) return 'invalidName'
      continue
    }
    if (!HEADER_NAME_PATTERN.test(name) || name.length > HEADER_OVERRIDE_MAX_NAME_LENGTH) return 'invalidName'
    const lower = name.toLowerCase()
    if (HEADER_OVERRIDE_BLOCKED_NAMES.has(lower)) return 'blockedName'
    if (seen.has(lower)) return 'duplicateName'
    if (HEADER_VALUE_INVALID_PATTERN.test(value) || HEADER_TEXT_ENCODER.encode(value).length > HEADER_OVERRIDE_MAX_VALUE_LENGTH) return 'invalidValue'
    seen.add(lower)
  }
  return seen.size > HEADER_OVERRIDE_MAX_ENTRIES ? 'tooManyEntries' : null
}

export function buildHeaderOverridesObject(rows: HeaderOverrideRow[]): Record<string, string> {
  const result: Record<string, string> = {}
  for (const row of rows) {
    const name = row.name.trim().toLowerCase()
    if (name) result[name] = row.value.trim()
  }
  return result
}

export function splitHeaderOverridesObject(value: unknown): HeaderOverrideRow[] {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return []
  return Object.entries(value as Record<string, unknown>)
    .filter(([, item]) => typeof item === 'string')
    .map(([name, item]) => ({ name, value: item as string }))
    .sort((a, b) => a.name.localeCompare(b.name))
}

export function applyHeaderOverride(
  credentials: Record<string, unknown>, enabled: boolean, rows: HeaderOverrideRow[], mode: 'create' | 'edit'
): void {
  if (enabled) {
    credentials[HEADER_OVERRIDE_ENABLED_CREDENTIAL_KEY] = true
    credentials[HEADER_OVERRIDES_CREDENTIAL_KEY] = buildHeaderOverridesObject(rows)
  } else if (mode === 'edit') {
    delete credentials[HEADER_OVERRIDE_ENABLED_CREDENTIAL_KEY]
    delete credentials[HEADER_OVERRIDES_CREDENTIAL_KEY]
  }
}

// ---------------------------------------------------------------------------
// 池模式（pool_mode*）与临时不可调度（temp_unschedulable_*）
// 池模式规则由创建、单账号编辑与批量编辑共用；临时不可调度的构造函数目前仅批量编辑使用。
// 键名与后端 Account.IsPoolMode / GetTempUnschedulableRules 对应。
// ---------------------------------------------------------------------------

export const DEFAULT_POOL_MODE_RETRY_COUNT = 3
export const MAX_POOL_MODE_RETRY_COUNT = 10
export const DEFAULT_POOL_MODE_RETRY_STATUS_CODES = [401, 403, 429]

/** 池模式同账号重试次数：非法值回退默认值，小于 0 按 0，超过上限截断。 */
export function normalizePoolModeRetryCount(value: number): number {
  if (!Number.isFinite(value)) return DEFAULT_POOL_MODE_RETRY_COUNT
  const normalized = Math.trunc(value)
  if (normalized < 0) return 0
  if (normalized > MAX_POOL_MODE_RETRY_COUNT) return MAX_POOL_MODE_RETRY_COUNT
  return normalized
}

/** 解析以逗号/空白分隔的状态码输入：只保留 100-599 的整数，去重并升序。 */
export function parsePoolModeRetryStatusCodes(input: string): number[] {
  if (!input || !input.trim()) return []
  const seen = new Set<number>()
  const out: number[] = []
  for (const token of input.split(/[,\s]+/)) {
    const trimmed = token.trim()
    if (!trimmed) continue
    const n = Number(trimmed)
    if (!Number.isFinite(n) || !Number.isInteger(n)) continue
    if (n < 100 || n > 599) continue
    if (seen.has(n)) continue
    seen.add(n)
    out.push(n)
  }
  return out.sort((a, b) => a - b)
}

export interface TempUnschedRuleForm {
  error_code: number | null
  keywords: string
  duration_minutes: number | null
  description: string
}

export interface TempUnschedRulePayload {
  error_code: number
  keywords: string[]
  duration_minutes: number
  description: string
}

export function splitTempUnschedKeywords(value: string): string[] {
  return value
    .split(/[,;]/)
    .map((item) => item.trim())
    .filter((item) => item.length > 0)
}

/** 把表单规则转成提交格式：错误码 100-599、时长大于 0、至少一个关键词，不合格的规则直接丢弃。 */
export function buildTempUnschedRules(rules: TempUnschedRuleForm[]): TempUnschedRulePayload[] {
  const out: TempUnschedRulePayload[] = []
  for (const rule of rules) {
    const errorCode = Number(rule.error_code)
    const duration = Number(rule.duration_minutes)
    const keywords = splitTempUnschedKeywords(rule.keywords)
    if (!Number.isFinite(errorCode) || errorCode < 100 || errorCode > 599) continue
    if (!Number.isFinite(duration) || duration <= 0) continue
    if (keywords.length === 0) continue
    out.push({
      error_code: Math.trunc(errorCode),
      keywords,
      duration_minutes: Math.trunc(duration),
      description: rule.description.trim()
    })
  }
  return out
}

/**
 * 批量编辑的池模式 credentials 补丁。批量更新对 credentials 做 JSONB 合并，没法删除键，
 * 所以关闭时只写 pool_mode=false，状态码留空时不发送（保持各账号原有设置）。
 */
export function buildBulkPoolModeCredentials(
  enabled: boolean,
  retryCount: number,
  retryStatusCodesInput: string
): Record<string, unknown> {
  if (!enabled) return { pool_mode: false }
  const credentials: Record<string, unknown> = {
    pool_mode: true,
    pool_mode_retry_count: normalizePoolModeRetryCount(retryCount)
  }
  const codes = parsePoolModeRetryStatusCodes(retryStatusCodesInput)
  if (codes.length > 0) {
    credentials.pool_mode_retry_status_codes = codes
  }
  return credentials
}

/**
 * 批量编辑的临时不可调度 credentials 补丁。启用时必须至少有一条合格规则（返回 null 表示规则不合格），
 * 关闭时只写 temp_unschedulable_enabled=false，已保存的规则保留。
 */
export function buildBulkTempUnschedCredentials(
  enabled: boolean,
  rules: TempUnschedRuleForm[]
): Record<string, unknown> | null {
  if (!enabled) return { temp_unschedulable_enabled: false }
  const built = buildTempUnschedRules(rules)
  if (built.length === 0) return null
  return { temp_unschedulable_enabled: true, temp_unschedulable_rules: built }
}
