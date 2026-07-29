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
