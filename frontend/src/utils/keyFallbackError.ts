import { extractApiErrorCode } from '@/utils/apiError'

type T = (key: string, params?: Record<string, unknown>) => string

/** 后端兜底链接口的错误码到文案的映射；只写用户需要知道的话 */
const ERROR_KEYS: Record<string, string> = {
  FALLBACK_GROUP_DUPLICATE: 'keyFallback.errors.FALLBACK_GROUP_DUPLICATE',
  FALLBACK_GROUP_IS_PRIMARY: 'keyFallback.errors.FALLBACK_GROUP_IS_PRIMARY',
  FALLBACK_CHAIN_TOO_LONG: 'keyFallback.errors.FALLBACK_CHAIN_TOO_LONG',
  FALLBACK_GROUP_PLATFORM_MISMATCH: 'keyFallback.errors.FALLBACK_GROUP_PLATFORM_MISMATCH',
  FALLBACK_GROUP_NOT_ALLOWED: 'keyFallback.errors.FALLBACK_GROUP_NOT_ALLOWED',
  FALLBACK_GROUP_NOT_FOUND: 'keyFallback.errors.FALLBACK_GROUP_NOT_FOUND',
  FALLBACK_GROUP_UNAVAILABLE: 'keyFallback.errors.FALLBACK_GROUP_UNAVAILABLE',
  FALLBACK_KEY_NOT_GROUPED: 'keyFallback.errors.FALLBACK_KEY_NOT_GROUPED',
  FALLBACK_KEY_CHANGED: 'keyFallback.errors.FALLBACK_KEY_CHANGED',
  API_KEY_NOT_FOUND: 'keyFallback.errors.API_KEY_NOT_FOUND',
  FALLBACK_INVALID_REQUEST: 'keyFallback.errors.FALLBACK_INVALID_REQUEST',
  FALLBACK_INVALID_MODEL: 'keyFallback.errors.FALLBACK_INVALID_REQUEST',
  FALLBACK_UNAVAILABLE: 'keyFallback.errors.FALLBACK_UNAVAILABLE'
}

/** 失败后编辑器会重新向服务器读取状态，这些错误码改用「已刷新，请重试」的文案 */
export const REFRESH_ON_ERROR = new Set([
  'FALLBACK_KEY_CHANGED',
  'API_KEY_NOT_FOUND'
])

/**
 * 把接口错误换成用户能看懂的提示：认识的错误码用对应文案，其余统一用 fallbackKey，
 * 不直接展示后端 message，避免把内部细节带给用户。
 */
export function fallbackErrorMessage(
  err: unknown,
  t: T,
  fallbackKey: string,
  variant?: 'refreshed'
): string {
  const code = extractApiErrorCode(err)
  const key = code ? ERROR_KEYS[code] : undefined
  if (key && variant === 'refreshed') return t(`${key}_REFRESHED`)
  return t(key ?? fallbackKey)
}
