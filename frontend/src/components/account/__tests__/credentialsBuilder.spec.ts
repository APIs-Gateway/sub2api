import { describe, it, expect } from 'vitest'
import {
  ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY,
  applyAntigravityProjectID,
  applyInterceptWarmup,
  applyHeaderOverride,
  buildBulkPoolModeCredentials,
  buildBulkTempUnschedCredentials,
  buildHeaderOverridesObject,
  buildTempUnschedRules,
  normalizePoolModeRetryCount,
  parsePoolModeRetryStatusCodes,
  validateHeaderOverrideRows
} from '../credentialsBuilder'

describe('applyInterceptWarmup', () => {
  it('create + enabled=true: should set intercept_warmup_requests to true', () => {
    const creds: Record<string, unknown> = { access_token: 'tok' }
    applyInterceptWarmup(creds, true, 'create')
    expect(creds.intercept_warmup_requests).toBe(true)
  })

  it('create + enabled=false: should not add the field', () => {
    const creds: Record<string, unknown> = { access_token: 'tok' }
    applyInterceptWarmup(creds, false, 'create')
    expect('intercept_warmup_requests' in creds).toBe(false)
  })

  it('edit + enabled=true: should set intercept_warmup_requests to true', () => {
    const creds: Record<string, unknown> = { api_key: 'sk' }
    applyInterceptWarmup(creds, true, 'edit')
    expect(creds.intercept_warmup_requests).toBe(true)
  })

  it('edit + enabled=false + field exists: should delete the field', () => {
    const creds: Record<string, unknown> = { api_key: 'sk', intercept_warmup_requests: true }
    applyInterceptWarmup(creds, false, 'edit')
    expect('intercept_warmup_requests' in creds).toBe(false)
  })

  it('edit + enabled=false + field absent: should not throw', () => {
    const creds: Record<string, unknown> = { api_key: 'sk' }
    applyInterceptWarmup(creds, false, 'edit')
    expect('intercept_warmup_requests' in creds).toBe(false)
  })

  it('should not affect other fields', () => {
    const creds: Record<string, unknown> = {
      api_key: 'sk',
      base_url: 'url',
      intercept_warmup_requests: true
    }
    applyInterceptWarmup(creds, false, 'edit')
    expect(creds.api_key).toBe('sk')
    expect(creds.base_url).toBe('url')
    expect('intercept_warmup_requests' in creds).toBe(false)
  })
})

describe('applyAntigravityProjectID', () => {
  it('create + project id: trims and stores configured project fallback', () => {
    const creds: Record<string, unknown> = { access_token: 'tok' }
    applyAntigravityProjectID(creds, '  configured-project  ', 'create')
    expect(creds[ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY]).toBe('configured-project')
  })

  it('create + empty project id: does not add the field', () => {
    const creds: Record<string, unknown> = { access_token: 'tok' }
    applyAntigravityProjectID(creds, '   ', 'create')
    expect(ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY in creds).toBe(false)
  })

  it('edit + empty project id: deletes existing fallback', () => {
    const creds: Record<string, unknown> = {
      access_token: 'tok',
      [ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY]: 'old-project'
    }
    applyAntigravityProjectID(creds, '', 'edit')
    expect(ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY in creds).toBe(false)
  })

  it('does not affect onboard project_id or other credentials', () => {
    const creds: Record<string, unknown> = {
      project_id: 'onboard-project',
      model_mapping: { 'gemini-*': 'gemini-2.5-flash' }
    }
    applyAntigravityProjectID(creds, 'configured-project', 'edit')
    expect(creds.project_id).toBe('onboard-project')
    expect(creds.model_mapping).toEqual({ 'gemini-*': 'gemini-2.5-flash' })
    expect(creds[ANTIGRAVITY_PROJECT_ID_CREDENTIAL_KEY]).toBe('configured-project')
  })
})

describe('header overrides', () => {
  it('normalizes names and preserves empty values as placeholders', () => {
    expect(buildHeaderOverridesObject([
      { name: ' X-Upstream-Mode ', value: ' enabled ' },
      { name: 'x-placeholder', value: '' }
    ])).toEqual({ 'x-upstream-mode': 'enabled', 'x-placeholder': '' })
  })

  it('matches backend blocked names and UTF-8 value length validation', () => {
    expect(validateHeaderOverrideRows([{ name: 'Content-Type', value: 'application/json' }])).toBe('blockedName')
    expect(validateHeaderOverrideRows([{ name: 'x-client-request-id', value: 'fixed' }])).toBe('blockedName')
    expect(validateHeaderOverrideRows([{ name: 'x-upstream', value: '测'.repeat(3000) }])).toBe('invalidValue')
  })

  it('removes override credentials while editing when disabled', () => {
    const credentials: Record<string, unknown> = {
      header_override_enabled: true,
      header_overrides: { 'x-old': 'value' }
    }
    applyHeaderOverride(credentials, false, [], 'edit')
    expect(credentials).not.toHaveProperty('header_override_enabled')
    expect(credentials).not.toHaveProperty('header_overrides')
  })
})

describe('pool mode helpers', () => {
  it('normalizes retry count into 0..10 and falls back to default for invalid values', () => {
    expect(normalizePoolModeRetryCount(5)).toBe(5)
    expect(normalizePoolModeRetryCount(-2)).toBe(0)
    expect(normalizePoolModeRetryCount(99)).toBe(10)
    expect(normalizePoolModeRetryCount(2.9)).toBe(2)
    expect(normalizePoolModeRetryCount(Number.NaN)).toBe(3)
  })

  it('parses status codes: keeps 100-599 integers, dedupes and sorts', () => {
    expect(parsePoolModeRetryStatusCodes('429, 401 403,429, 99, 600, 4x, 1.5')).toEqual([401, 403, 429])
    expect(parsePoolModeRetryStatusCodes('   ')).toEqual([])
  })

  it('bulk: disabled only writes pool_mode=false', () => {
    expect(buildBulkPoolModeCredentials(false, 7, '401')).toEqual({ pool_mode: false })
  })

  it('bulk: enabled writes retry count, and status codes only when provided', () => {
    expect(buildBulkPoolModeCredentials(true, 4, '')).toEqual({
      pool_mode: true,
      pool_mode_retry_count: 4
    })
    expect(buildBulkPoolModeCredentials(true, 4, '503, 429')).toEqual({
      pool_mode: true,
      pool_mode_retry_count: 4,
      pool_mode_retry_status_codes: [429, 503]
    })
  })
})

describe('temp unschedulable helpers', () => {
  const valid = {
    error_code: 529,
    keywords: 'overloaded; too many ,',
    duration_minutes: 60,
    description: '  busy  '
  }

  it('builds payload rules and drops invalid rows', () => {
    expect(
      buildTempUnschedRules([
        valid,
        { ...valid, error_code: 99 },
        { ...valid, duration_minutes: 0 },
        { ...valid, keywords: ' , ' },
        { ...valid, error_code: null }
      ])
    ).toEqual([
      {
        error_code: 529,
        keywords: ['overloaded', 'too many'],
        duration_minutes: 60,
        description: 'busy'
      }
    ])
  })

  it('bulk: disabled only writes temp_unschedulable_enabled=false', () => {
    expect(buildBulkTempUnschedCredentials(false, [valid])).toEqual({
      temp_unschedulable_enabled: false
    })
  })

  it('bulk: enabled without a valid rule returns null', () => {
    expect(buildBulkTempUnschedCredentials(true, [])).toBeNull()
    expect(buildBulkTempUnschedCredentials(true, [{ ...valid, keywords: '' }])).toBeNull()
  })

  it('bulk: enabled writes rules', () => {
    expect(buildBulkTempUnschedCredentials(true, [valid])).toEqual({
      temp_unschedulable_enabled: true,
      temp_unschedulable_rules: [
        { error_code: 529, keywords: ['overloaded', 'too many'], duration_minutes: 60, description: 'busy' }
      ]
    })
  })
})
