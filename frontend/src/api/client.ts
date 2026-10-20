/**
 * Axios HTTP Client Configuration
 * Base client with interceptors for authentication, token refresh, and error handling
 */

import axios, { AxiosInstance, AxiosError, InternalAxiosRequestConfig, AxiosResponse } from 'axios'
import type { ApiResponse } from '@/types'
import { getLocale } from '@/i18n'
import { API_BASE_URL, refreshAuthTokens, type RefreshTokenResponse } from './tokenRefresh'
import { getAdminComplianceSessionVersion } from '@/utils/adminComplianceSession'
import { getAnnouncementReadSessionVersion } from '@/utils/announcementReadSession'
import { getAuthSessionVersion } from '@/utils/authSessionVersion'

type SessionRequestConfig = InternalAxiosRequestConfig & {
  _authSessionVersion?: number
  _complianceSessionVersion?: number
  _announcementReadSessionVersion?: number
}

function isAnnouncementReadRequest(url: string): boolean {
  return /^\/announcements\/\d+\/read(?:$|\?)/.test(url)
}

// ==================== Axios Instance Configuration ====================

export function getAPIBaseURL(): string {
  return API_BASE_URL
}

export const apiClient: AxiosInstance = axios.create({
  baseURL: API_BASE_URL,
  withCredentials: true,
  timeout: 30000,
  headers: {
    'Content-Type': 'application/json'
  }
})

// ==================== Token Refresh State ====================

type TokenRefreshUnavailableError = {
  status: number
  code: 'TOKEN_REFRESH_UNAVAILABLE'
  message: string
}

// Shared refresh promises that were rejected and already cleared the session. Concurrent 401s that
// awaited the same refresh keep rejecting with their own original error instead of AUTH_SESSION_CHANGED.
const expiredSessionRefreshes = new WeakSet<Promise<RefreshTokenResponse>>()

/**
 * Classify a refresh failure that means "refresh endpoint temporarily unavailable" rather than
 * "refresh token rejected": network errors, 429 and 5xx keep the session and surface the real status.
 */
function toTokenRefreshUnavailableError(refreshError: unknown): TokenRefreshUnavailableError | undefined {
  if (!axios.isAxiosError(refreshError)) {
    return undefined
  }
  const refreshStatus = refreshError.response?.status ?? 0
  if (refreshStatus === 0 || refreshStatus === 429 || refreshStatus >= 500) {
    const responseData = refreshError.response?.data as { message?: string } | undefined
    return {
      status: refreshStatus,
      code: 'TOKEN_REFRESH_UNAVAILABLE',
      message: responseData?.message || refreshError.message
    }
  }
  return undefined
}

// ==================== Request Interceptor ====================

// Get user's timezone
const getUserTimezone = (): string => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone
  } catch {
    return 'UTC'
  }
}

apiClient.interceptors.request.use(
  (config: InternalAxiosRequestConfig) => {
    const sessionRequest = config as SessionRequestConfig
    if (sessionRequest._authSessionVersion !== getAuthSessionVersion()) {
      return Promise.reject({
        status: 401,
        code: 'AUTH_SESSION_CHANGED',
        message: 'Authentication session changed before sending the request.'
      })
    }
    // Keep the original version on retries so an old acceptance cannot use a new session's token.
    if (sessionRequest._complianceSessionVersion === undefined) {
      sessionRequest._complianceSessionVersion = getAdminComplianceSessionVersion()
    }
    if (
      /^\/admin\/compliance(?:\/|$|\?)/.test(String(config.url || '')) &&
      sessionRequest._complianceSessionVersion !== getAdminComplianceSessionVersion()
    ) {
      return Promise.reject({
        status: 401,
        code: 'AUTH_SESSION_CHANGED',
        message: 'Authentication session changed before sending the compliance request.'
      })
    }
    if (
      isAnnouncementReadRequest(String(config.url || '')) &&
      sessionRequest._announcementReadSessionVersion !== getAnnouncementReadSessionVersion()
    ) {
      return Promise.reject({
        status: 401,
        code: 'AUTH_SESSION_CHANGED',
        message: 'Authentication session changed before sending the announcement read request.'
      })
    }
    // Attach token from localStorage
    const token = localStorage.getItem('auth_token')
    if (token && config.headers) {
      config.headers.Authorization = `Bearer ${token}`
    }

    // Attach locale for backend translations
    if (config.headers) {
      config.headers['Accept-Language'] = getLocale()
    }

    // Attach timezone for all GET requests (backend may use it for default date ranges)
    if (config.method === 'get') {
      if (!config.params) {
        config.params = {}
      }
      config.params.timezone = getUserTimezone()
    }

    return config
  },
  (error) => {
    return Promise.reject(error)
  },
  {
    // Axios runs runWhen synchronously while assembling its asynchronous interceptor chain.
    // Capture the caller's session now, so a session switch before the interceptor runs
    // rejects the old request without sending it with the new account's token. Retries
    // keep their original version.
    runWhen: (config) => {
      const sessionRequest = config as SessionRequestConfig
      if (sessionRequest._authSessionVersion === undefined) {
        sessionRequest._authSessionVersion = getAuthSessionVersion()
      }
      return true
    }
  }
)

// ==================== Response Interceptor ====================

apiClient.interceptors.response.use(
  (response: AxiosResponse) => {
    // Unwrap standard API response format { code, message, data }
    const apiResponse = response.data as ApiResponse<unknown>
    if (apiResponse && typeof apiResponse === 'object' && 'code' in apiResponse) {
      if (apiResponse.code === 0) {
        // Success - return the data portion
        response.data = apiResponse.data
      } else {
        // API error
        const resp = apiResponse as unknown as Record<string, unknown>
        return Promise.reject({
          status: response.status,
          code: apiResponse.code,
          message: apiResponse.message || 'Unknown error',
          reason: resp.reason,
          metadata: resp.metadata,
        })
      }
    }
    return response
  },
  async (error: AxiosError<ApiResponse<unknown>>) => {
    if (error.code === 'AUTH_SESSION_CHANGED') {
      return Promise.reject(error)
    }
    // Request cancellation: keep the original axios cancellation error so callers can ignore it.
    // Otherwise we'd misclassify it as a generic "network error".
    if (error.code === 'ERR_CANCELED' || axios.isCancel(error)) {
      return Promise.reject(error)
    }

    const originalRequest = error.config as SessionRequestConfig & { _retry?: boolean }
    const staleAuthRequest = () =>
      originalRequest?._authSessionVersion !== undefined &&
      originalRequest._authSessionVersion !== getAuthSessionVersion()
    if (staleAuthRequest()) {
      return Promise.reject({
        status: 401,
        code: 'AUTH_SESSION_CHANGED',
        message: 'Authentication session changed while the request was in flight.'
      })
    }

    // Handle common errors
    if (error.response) {
      const { status, data } = error.response
      const url = String(error.config?.url || '')
      const isComplianceRequest = /^\/admin\/compliance(?:\/|$|\?)/.test(url)
      const staleComplianceRequest = () =>
        isComplianceRequest &&
        originalRequest?._complianceSessionVersion !== getAdminComplianceSessionVersion()
      const staleAnnouncementReadRequest = () =>
        isAnnouncementReadRequest(url) &&
        originalRequest?._announcementReadSessionVersion !== getAnnouncementReadSessionVersion()
      const sessionChangedError = {
        status: 401,
        code: 'AUTH_SESSION_CHANGED',
        message: 'Authentication session changed while refreshing.'
      }

      if (staleAnnouncementReadRequest()) {
        return Promise.reject(sessionChangedError)
      }

      // Validate `data` shape to avoid HTML error pages breaking our error handling.
      const apiData = (typeof data === 'object' && data !== null ? data : {}) as Record<string, any>

      // Ops monitoring disabled: treat as feature-flagged 404, and proactively redirect away
      // from ops pages to avoid broken UI states.
      if (status === 404 && apiData.message === 'Ops monitoring is disabled') {
        try {
          localStorage.setItem('ops_monitoring_enabled_cached', 'false')
        } catch {
          // ignore localStorage failures
        }
        try {
          window.dispatchEvent(new CustomEvent('ops-monitoring-disabled'))
        } catch {
          // ignore event failures
        }

        if (window.location.pathname.startsWith('/admin/ops')) {
          window.location.href = '/admin/settings'
        }

        return Promise.reject({
          status,
          code: 'OPS_DISABLED',
          message: apiData.message || error.message,
          url
        })
      }

      if (status === 423 && apiData.code === 'ADMIN_COMPLIANCE_ACK_REQUIRED') {
        if (originalRequest?._complianceSessionVersion === getAdminComplianceSessionVersion()) {
          try {
            window.dispatchEvent(new CustomEvent('admin-compliance-required', {
              detail: apiData.metadata || {}
            }))
          } catch {
            // ignore event failures
          }
        }

        return Promise.reject({
          status,
          code: apiData.code,
          message: apiData.message || error.message,
          metadata: apiData.metadata,
        })
      }

      if (status === 401 && staleComplianceRequest()) {
        return Promise.reject(sessionChangedError)
      }

      // 401: Try to refresh the token if we have a refresh token
      // This handles TOKEN_EXPIRED, INVALID_TOKEN, TOKEN_REVOKED, etc.
      if (status === 401 && !originalRequest._retry) {
        const refreshToken = localStorage.getItem('refresh_token')
        const isAuthEndpoint =
          url.includes('/auth/login') || url.includes('/auth/register') || url.includes('/auth/refresh')

        // If we have a refresh token and this is not an auth endpoint, try to refresh
        if (refreshToken && !isAuthEndpoint) {
          const refreshSessionUser = localStorage.getItem('auth_user')
          originalRequest._retry = true
          let refreshPromise: Promise<RefreshTokenResponse> | undefined

          try {
            const headers = originalRequest.headers as Record<string, unknown> | undefined
            const authHeader = headers?.Authorization ?? headers?.authorization
            const failedAccessToken =
              typeof authHeader === 'string' && authHeader.startsWith('Bearer ')
                ? authHeader.slice('Bearer '.length)
                : null
            // The shared helper coordinates this 401 path with the auth store's proactive refresh
            // (and with other tabs) so a rotating refresh token is never submitted twice.
            refreshPromise = refreshAuthTokens({ failedAccessToken })
            const tokens = await refreshPromise

            if (staleAuthRequest() || staleComplianceRequest() || staleAnnouncementReadRequest()) {
              return Promise.reject(sessionChangedError)
            }

            // Retry the original request with the refreshed token
            if (originalRequest.headers) {
              originalRequest.headers.Authorization = `Bearer ${tokens.access_token}`
            }
            return apiClient(originalRequest)
          } catch (refreshError) {
            if (staleAuthRequest()) {
              return Promise.reject(sessionChangedError)
            }
            // Another request awaiting the same failed refresh already cleared the session and
            // redirected; keep rejecting with this request's own 401 error.
            if (refreshPromise && expiredSessionRefreshes.has(refreshPromise)) {
              return Promise.reject({
                status,
                code: apiData.code,
                message: apiData.message || apiData.detail || error.message
              })
            }

            // A stale request must never destroy a session that was logged out or replaced while
            // its refresh was in flight (for example, when another tab signs in as another user).
            const sessionChanged =
              localStorage.getItem('refresh_token') !== refreshToken ||
              localStorage.getItem('auth_user') !== refreshSessionUser
            if (sessionChanged) {
              return Promise.reject({
                status: 401,
                code: 'AUTH_SESSION_CHANGED',
                message: 'Authentication session changed while refreshing.'
              })
            }

            // A temporarily unavailable refresh endpoint must not log the user out; every request
            // sharing the refresh rejects with the same classified upstream failure.
            const unavailableError = toTokenRefreshUnavailableError(refreshError)
            if (unavailableError) {
              return Promise.reject(unavailableError)
            }

            if (refreshPromise) {
              expiredSessionRefreshes.add(refreshPromise)
            }

            // Clear tokens and redirect to login
            localStorage.removeItem('auth_token')
            localStorage.removeItem('refresh_token')
            localStorage.removeItem('auth_user')
            localStorage.removeItem('token_expires_at')
            sessionStorage.setItem('auth_expired', '1')

            if (!window.location.pathname.includes('/login')) {
              window.location.href = '/login'
            }

            return Promise.reject({
              status: 401,
              code: 'TOKEN_REFRESH_FAILED',
              message: 'Session expired. Please log in again.'
            })
          }
        }

        // No refresh token or is auth endpoint - clear auth and redirect
        const hasToken = !!localStorage.getItem('auth_token')
        const headers = error.config?.headers as Record<string, unknown> | undefined
        const authHeader = headers?.Authorization ?? headers?.authorization
        const sentAuth =
          typeof authHeader === 'string'
            ? authHeader.trim() !== ''
            : Array.isArray(authHeader)
              ? authHeader.length > 0
              : !!authHeader

        localStorage.removeItem('auth_token')
        localStorage.removeItem('refresh_token')
        localStorage.removeItem('auth_user')
        localStorage.removeItem('token_expires_at')
        if ((hasToken || sentAuth) && !isAuthEndpoint) {
          sessionStorage.setItem('auth_expired', '1')
        }
        // Only redirect if not already on login page
        if (!window.location.pathname.includes('/login')) {
          window.location.href = '/login'
        }
      }

      // Return structured error
      return Promise.reject({
        status,
        code: apiData.code,
        reason: apiData.reason,
        error: apiData.error,
        message: apiData.message || apiData.detail || error.message,
        metadata: apiData.metadata,
      })
    }

    // Network error
    return Promise.reject({
      status: 0,
      message: 'Network error. Please check your connection.'
    })
  }
)

export default apiClient
