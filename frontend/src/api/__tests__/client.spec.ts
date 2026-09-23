import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import axios from 'axios'
import type { AxiosInstance } from 'axios'

// 需要在导入 client 之前设置 mock
vi.mock('@/i18n', () => ({
  getLocale: () => 'zh-CN',
}))

describe('API Client', () => {
  let apiClient: AxiosInstance
  let getAPIBaseURL: () => string

  beforeEach(async () => {
    localStorage.clear()
    sessionStorage.clear()
    window.history.replaceState({}, '', '/')
    // Simulate a browser with the Web Locks API (jsdom has none) so refresh failures only use the
    // short peer-reconciliation window instead of the uncoordinated-tab recovery window.
    Object.defineProperty(navigator, 'locks', {
      configurable: true,
      value: { request: (_name: string, callback: () => Promise<unknown>) => callback() },
    })
    // 每次测试重新导入以获取干净的模块状态
    vi.resetModules()
    const mod = await import('@/api/client')
    apiClient = mod.apiClient
    getAPIBaseURL = mod.getAPIBaseURL
  })

  afterEach(() => {
    vi.restoreAllMocks()
    Object.defineProperty(navigator, 'locks', { configurable: true, value: undefined })
  })

  // --- 请求拦截器 ---

  it('exposes the same configured base URL used by the Axios client', () => {
    expect(getAPIBaseURL()).toBe(apiClient.defaults.baseURL)
  })

  describe('请求拦截器', () => {
    it('自动附加 Authorization 头', async () => {
      localStorage.setItem('auth_token', 'my-jwt-token')

      // 拦截实际请求
      const adapter = vi.fn().mockResolvedValue({
        status: 200,
        data: { code: 0, data: {} },
        headers: {},
        config: {},
        statusText: 'OK',
      })
      apiClient.defaults.adapter = adapter

      await apiClient.get('/test')

      const config = adapter.mock.calls[0][0]
      expect(config.headers.get('Authorization')).toBe('Bearer my-jwt-token')
    })

    it('无 token 时不附加 Authorization 头', async () => {
      const adapter = vi.fn().mockResolvedValue({
        status: 200,
        data: { code: 0, data: {} },
        headers: {},
        config: {},
        statusText: 'OK',
      })
      apiClient.defaults.adapter = adapter

      await apiClient.get('/test')

      const config = adapter.mock.calls[0][0]
      expect(config.headers.get('Authorization')).toBeFalsy()
    })

    it('GET 请求自动附加 timezone 参数', async () => {
      const adapter = vi.fn().mockResolvedValue({
        status: 200,
        data: { code: 0, data: {} },
        headers: {},
        config: {},
        statusText: 'OK',
      })
      apiClient.defaults.adapter = adapter

      await apiClient.get('/test')

      const config = adapter.mock.calls[0][0]
      expect(config.params).toHaveProperty('timezone')
    })

    it('POST 请求不附加 timezone 参数', async () => {
      const adapter = vi.fn().mockResolvedValue({
        status: 200,
        data: { code: 0, data: {} },
        headers: {},
        config: {},
        statusText: 'OK',
      })
      apiClient.defaults.adapter = adapter

      await apiClient.post('/test', { foo: 'bar' })

      const config = adapter.mock.calls[0][0]
      expect(config.params?.timezone).toBeUndefined()
    })

    it('请求默认带 withCredentials 以支持跨域 cookie', async () => {
      const adapter = vi.fn().mockResolvedValue({
        status: 200,
        data: { code: 0, data: {} },
        headers: {},
        config: {},
        statusText: 'OK',
      })
      apiClient.defaults.adapter = adapter

      await apiClient.post('/auth/oauth/bind-token')

      const config = adapter.mock.calls[0][0]
      expect(config.withCredentials).toBe(true)
    })
  })

  // --- 响应拦截器 ---

  describe('响应拦截器', () => {
    it('code=0 时解包 data 字段', async () => {
      const adapter = vi.fn().mockResolvedValue({
        status: 200,
        data: { code: 0, data: { name: 'test' }, message: 'ok' },
        headers: {},
        config: {},
        statusText: 'OK',
      })
      apiClient.defaults.adapter = adapter

      const response = await apiClient.get('/test')
      expect(response.data).toEqual({ name: 'test' })
    })

    it('code!=0 时拒绝并返回结构化错误', async () => {
      const adapter = vi.fn().mockResolvedValue({
        status: 200,
        data: { code: 1001, message: '参数错误', data: null },
        headers: {},
        config: {},
        statusText: 'OK',
      })
      apiClient.defaults.adapter = adapter

      await expect(apiClient.get('/test')).rejects.toEqual(
        expect.objectContaining({
          code: 1001,
          message: '参数错误',
        })
      )
    })

    it('部署与运营合规未确认时广播事件且保留登录态', async () => {
      localStorage.setItem('auth_token', 'admin-token')
      const listener = vi.fn()
      window.addEventListener('admin-compliance-required', listener)

      const adapter = vi.fn().mockRejectedValue({
        response: {
          status: 423,
          data: {
            code: 'ADMIN_COMPLIANCE_ACK_REQUIRED',
            message: 'administrator compliance acknowledgement is required',
            metadata: {
              version: 'v2026.06.10',
              document_path_zh: 'docs/legal/admin-compliance.zh.md',
              document_path_en: 'docs/legal/admin-compliance.en.md',
            },
          },
        },
        config: {
          url: '/admin/users',
          headers: { Authorization: 'Bearer admin-token' },
        },
        code: 'ERR_BAD_REQUEST',
      })
      apiClient.defaults.adapter = adapter

      await expect(apiClient.get('/admin/users')).rejects.toEqual(
        expect.objectContaining({
          status: 423,
          code: 'ADMIN_COMPLIANCE_ACK_REQUIRED',
          metadata: expect.objectContaining({
            version: 'v2026.06.10',
          }),
        })
      )

      expect(listener).toHaveBeenCalledTimes(1)
      expect((listener.mock.calls[0][0] as CustomEvent).detail).toEqual(
        expect.objectContaining({
          version: 'v2026.06.10',
        })
      )
      expect(localStorage.getItem('auth_token')).toBe('admin-token')

      window.removeEventListener('admin-compliance-required', listener)
    })
  })

  // --- 401 Token 刷新 ---

  describe('401 Token 刷新', () => {
    it('refresh 请求显式使用 30 秒超时', async () => {
      localStorage.setItem('auth_token', 'expired-token')
      localStorage.setItem('refresh_token', 'refresh-token')

      const refresh = vi.spyOn(axios, 'post').mockResolvedValue({
        data: {
          code: 0,
          data: {
            access_token: 'fresh-token',
            refresh_token: 'fresh-refresh-token',
            expires_in: 3600,
          },
        },
      })
      const adapter = vi
        .fn()
        .mockRejectedValueOnce({
          response: {
            status: 401,
            data: { code: 'TOKEN_EXPIRED', message: 'Token expired' },
          },
          config: { url: '/test', headers: {} },
          code: 'ERR_BAD_REQUEST',
        })
        .mockResolvedValueOnce({
          status: 200,
          data: { code: 0, data: { ok: true } },
          headers: {},
          config: {},
          statusText: 'OK',
        })
      apiClient.defaults.adapter = adapter

      await expect(apiClient.get('/test')).resolves.toMatchObject({ data: { ok: true } })

      expect(refresh).toHaveBeenCalledWith(
        '/api/v1/auth/refresh',
        { refresh_token: 'refresh-token' },
        expect.objectContaining({ timeout: 30000 }),
      )
    })

    it('无 refresh_token 时 401 清除 localStorage', async () => {
      localStorage.setItem('auth_token', 'expired-token')
      // 不设置 refresh_token

      // Mock window.location
      const originalLocation = window.location
      Object.defineProperty(window, 'location', {
        value: { ...originalLocation, pathname: '/dashboard', href: '/dashboard' },
        writable: true,
      })

      const adapter = vi.fn().mockRejectedValue({
        response: {
          status: 401,
          data: { code: 'TOKEN_EXPIRED', message: 'Token expired' },
        },
        config: {
          url: '/test',
          headers: { Authorization: 'Bearer expired-token' },
        },
        code: 'ERR_BAD_REQUEST',
      })
      apiClient.defaults.adapter = adapter

      await expect(apiClient.get('/test')).rejects.toBeDefined()

      expect(localStorage.getItem('auth_token')).toBeNull()

      // 恢复 location
      Object.defineProperty(window, 'location', {
        value: originalLocation,
        writable: true,
      })
    })

    it('有 refresh_token 时刷新并重试原请求', async () => {
      localStorage.setItem('auth_token', 'expired-token')
      localStorage.setItem('refresh_token', 'refresh-token')
      localStorage.setItem('token_expires_at', String(Date.now() - 1))
      localStorage.setItem('auth_user', JSON.stringify({ id: 7 }))

      const adapter = vi.fn()
        .mockRejectedValueOnce({
          response: {
            status: 401,
            data: { code: 'TOKEN_EXPIRED', message: 'Token expired' },
          },
          config: {
            url: '/test',
            headers: { Authorization: 'Bearer expired-token' },
          },
          code: 'ERR_BAD_REQUEST',
        })
        .mockResolvedValueOnce({
          status: 200,
          data: { code: 0, data: { ok: true } },
          headers: {},
          config: {},
          statusText: 'OK',
        })
      apiClient.defaults.adapter = adapter
      vi.spyOn(axios, 'post').mockResolvedValueOnce({
        data: {
          code: 0,
          message: 'ok',
          data: {
            access_token: 'new-token',
            refresh_token: 'new-refresh-token',
            expires_in: 3600,
            token_type: 'Bearer',
          },
        },
      })

      await expect(apiClient.get('/test')).resolves.toMatchObject({ data: { ok: true } })

      expect(adapter).toHaveBeenCalledTimes(2)
      expect(localStorage.getItem('auth_token')).toBe('new-token')
      expect(localStorage.getItem('refresh_token')).toBe('new-refresh-token')
      expect(adapter.mock.calls[1][0].headers.get('Authorization')).toBe('Bearer new-token')
    })

    it.each([429, 500, 503, 0])('刷新暂时失败（%s）时保留会话并返回实际状态', async (status) => {
      localStorage.setItem('auth_token', 'expired-token')
      localStorage.setItem('refresh_token', 'refresh-token')
      localStorage.setItem('auth_user', JSON.stringify({ id: 7 }))
      localStorage.setItem('token_expires_at', '123')
      const refreshError = new axios.AxiosError('Refresh unavailable', 'ERR_NETWORK')
      if (status) {
        refreshError.response = {
          status, data: { message: 'Please try again later' }, statusText: '',
          headers: {}, config: { headers: new axios.AxiosHeaders() },
        }
      }
      vi.spyOn(axios, 'post').mockRejectedValueOnce(refreshError)
      const adapter = vi.fn().mockRejectedValueOnce({
        response: { status: 401, data: { code: 'TOKEN_EXPIRED' } },
        config: { url: '/test', headers: { Authorization: 'Bearer expired-token' } },
      })
      apiClient.defaults.adapter = adapter

      await expect(apiClient.get('/test')).rejects.toMatchObject({
        status, code: 'TOKEN_REFRESH_UNAVAILABLE',
        message: status ? 'Please try again later' : 'Refresh unavailable',
      })
      expect(adapter).toHaveBeenCalledTimes(1)
      expect(localStorage.getItem('auth_token')).toBe('expired-token')
      expect(localStorage.getItem('refresh_token')).toBe('refresh-token')
      expect(localStorage.getItem('auth_user')).toBe(JSON.stringify({ id: 7 }))
      expect(localStorage.getItem('token_expires_at')).toBe('123')
      expect(sessionStorage.getItem('auth_expired')).toBeNull()
      expect(window.location.pathname).toBe('/')
    })

    it('刷新暂时失败时所有并发等待请求均保留会话和真实上游错误', async () => {
      localStorage.setItem('auth_token', 'expired-token')
      localStorage.setItem('refresh_token', 'refresh-token')
      localStorage.setItem('auth_user', JSON.stringify({ id: 7 }))
      localStorage.setItem('token_expires_at', '123')

      let rejectRefresh!: (reason: unknown) => void
      vi.spyOn(axios, 'post').mockImplementationOnce(
        () => new Promise((_resolve, reject) => {
          rejectRefresh = reject
        })
      )
      apiClient.defaults.adapter = vi.fn()
        .mockRejectedValueOnce({
          response: { status: 401, data: { code: 'TOKEN_EXPIRED' } },
          config: { url: '/first', headers: { Authorization: 'Bearer expired-token' } },
        })
        .mockRejectedValueOnce({
          response: { status: 401, data: { code: 'TOKEN_EXPIRED' } },
          config: { url: '/second', headers: { Authorization: 'Bearer expired-token' } },
        })

      const requests = [
        apiClient.get('/first').catch((error) => error),
        apiClient.get('/second').catch((error) => error),
      ]
      await vi.waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1))
      rejectRefresh(Object.assign(new axios.AxiosError('Please try again later'), {
        response: { status: 503, data: { message: 'Please try again later' } },
      }))

      await expect(Promise.all(requests)).resolves.toEqual([
        expect.objectContaining({
          status: 503,
          code: 'TOKEN_REFRESH_UNAVAILABLE',
          message: 'Please try again later',
        }),
        expect.objectContaining({
          status: 503,
          code: 'TOKEN_REFRESH_UNAVAILABLE',
          message: 'Please try again later',
        }),
      ])
      expect(localStorage.getItem('auth_token')).toBe('expired-token')
      expect(localStorage.getItem('refresh_token')).toBe('refresh-token')
      expect(localStorage.getItem('auth_user')).toBe(JSON.stringify({ id: 7 }))
      expect(localStorage.getItem('token_expires_at')).toBe('123')
      expect(sessionStorage.getItem('auth_expired')).toBeNull()
    })

    it('刷新被拒绝时并发等待请求仍返回自己的原始 401 错误', async () => {
      window.history.replaceState({}, '', '/login')
      localStorage.setItem('auth_token', 'expired-token')
      localStorage.setItem('refresh_token', 'refresh-token')
      localStorage.setItem('auth_user', JSON.stringify({ id: 7 }))

      let rejectRefresh!: (reason: unknown) => void
      vi.spyOn(axios, 'post').mockImplementationOnce(
        () => new Promise((_resolve, reject) => {
          rejectRefresh = reject
        })
      )
      apiClient.defaults.adapter = vi.fn()
        .mockRejectedValueOnce({
          response: { status: 401, data: { code: 'TOKEN_EXPIRED', message: 'Token expired' } },
          config: { url: '/first', headers: { Authorization: 'Bearer expired-token' } },
        })
        .mockRejectedValueOnce({
          response: { status: 401, data: { code: 'TOKEN_EXPIRED', message: 'Token expired' } },
          config: { url: '/second', headers: { Authorization: 'Bearer expired-token' } },
        })

      const requests = [
        apiClient.get('/first').catch((error) => error),
        apiClient.get('/second').catch((error) => error),
      ]
      await vi.waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1))
      rejectRefresh(Object.assign(new axios.AxiosError('Refresh rejected'), { response: { status: 401 } }))

      await expect(Promise.all(requests)).resolves.toEqual([
        expect.objectContaining({ status: 401, code: 'TOKEN_REFRESH_FAILED' }),
        expect.objectContaining({ status: 401, code: 'TOKEN_EXPIRED', message: 'Token expired' }),
      ])
      expect(localStorage.getItem('auth_token')).toBeNull()
      expect(localStorage.getItem('refresh_token')).toBeNull()
      expect(sessionStorage.getItem('auth_expired')).toBe('1')
    })

    it.each([401, 403, null])('刷新被拒绝（%s）时仍清除失效会话', async (status) => {
      window.history.replaceState({}, '', '/login')
      localStorage.setItem('auth_token', 'expired-token')
      localStorage.setItem('refresh_token', 'refresh-token')
      localStorage.setItem('auth_user', JSON.stringify({ id: 7 }))
      localStorage.setItem('token_expires_at', '123')
      const refreshError = status
        ? Object.assign(new axios.AxiosError('Refresh rejected'), { response: { status } })
        : new Error('Invalid refresh response')
      vi.spyOn(axios, 'post').mockRejectedValueOnce(refreshError)
      apiClient.defaults.adapter = vi.fn().mockRejectedValueOnce({
        response: { status: 401, data: { code: 'TOKEN_EXPIRED' } },
        config: { url: '/test', headers: { Authorization: 'Bearer expired-token' } },
      })

      await expect(apiClient.get('/test')).rejects.toMatchObject({ status: 401, code: 'TOKEN_REFRESH_FAILED' })
      for (const key of ['auth_token', 'refresh_token', 'auth_user', 'token_expires_at']) {
        expect(localStorage.getItem(key)).toBeNull()
      }
      expect(sessionStorage.getItem('auth_expired')).toBe('1')
    })

    it('主动刷新与 401 刷新并发时只提交一次 refresh_token', async () => {
      localStorage.setItem('auth_token', 'expired-token')
      localStorage.setItem('refresh_token', 'refresh-token')
      localStorage.setItem('token_expires_at', String(Date.now() + 60_000))
      localStorage.setItem('auth_user', JSON.stringify({ id: 7 }))

      let resolveRefresh!: (value: unknown) => void
      const refresh = vi.spyOn(axios, 'post').mockImplementation(
        () => new Promise((resolve) => {
          resolveRefresh = resolve
        })
      )
      const adapter = vi.fn()
        .mockRejectedValueOnce({
          response: { status: 401, data: { code: 'TOKEN_EXPIRED', message: 'Token expired' } },
          config: { url: '/test', headers: { Authorization: 'Bearer expired-token' } },
          code: 'ERR_BAD_REQUEST',
        })
        .mockResolvedValueOnce({
          status: 200,
          data: { code: 0, data: { ok: true } },
          headers: {},
          config: {},
          statusText: 'OK',
        })
      apiClient.defaults.adapter = adapter
      const { refreshToken: proactiveRefresh } = await import('@/api/auth')

      const scheduled = proactiveRefresh()
      const request = apiClient.get('/test')
      await vi.waitFor(() => expect(adapter).toHaveBeenCalledTimes(1))
      resolveRefresh({
        data: {
          code: 0,
          message: 'ok',
          data: {
            access_token: 'new-token',
            refresh_token: 'new-refresh-token',
            expires_in: 3600,
            token_type: 'Bearer',
          },
        },
      })

      await expect(scheduled).resolves.toMatchObject({ access_token: 'new-token' })
      await expect(request).resolves.toMatchObject({ data: { ok: true } })
      expect(refresh).toHaveBeenCalledTimes(1)
      expect(localStorage.getItem('refresh_token')).toBe('new-refresh-token')
      expect(adapter.mock.calls[1][0].headers.get('Authorization')).toBe('Bearer new-token')
    })

    it('刷新期间换号时旧请求不会清除新会话', async () => {
      localStorage.setItem('auth_token', 'user-a-access')
      localStorage.setItem('refresh_token', 'user-a-refresh')
      localStorage.setItem('token_expires_at', String(Date.now() - 1))
      localStorage.setItem('auth_user', JSON.stringify({ id: 7 }))

      apiClient.defaults.adapter = vi.fn().mockRejectedValueOnce({
        response: {
          status: 401,
          data: { code: 'TOKEN_EXPIRED', message: 'Token expired' },
        },
        config: {
          url: '/test',
          headers: { Authorization: 'Bearer user-a-access' },
        },
        code: 'ERR_BAD_REQUEST',
      })

      let rejectRefresh!: (reason: Error) => void
      vi.spyOn(axios, 'post').mockImplementationOnce(
        () => new Promise((_resolve, reject) => {
          rejectRefresh = reject
        })
      )

      const staleRequest = apiClient.get('/test')
      await vi.waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1))

      localStorage.setItem('auth_token', 'user-b-access')
      localStorage.setItem('refresh_token', 'user-b-refresh')
      localStorage.setItem('token_expires_at', String(Date.now() + 3600_000))
      localStorage.setItem('auth_user', JSON.stringify({ id: 8 }))
      rejectRefresh(new Error('stale refresh failed'))

      await expect(staleRequest).rejects.toMatchObject({ code: 'AUTH_SESSION_CHANGED' })
      expect(localStorage.getItem('auth_token')).toBe('user-b-access')
      expect(localStorage.getItem('refresh_token')).toBe('user-b-refresh')
      expect(localStorage.getItem('auth_user')).toBe(JSON.stringify({ id: 8 }))
      expect(window.location.pathname).toBe('/')
    })
  })

  // --- 网络错误 ---

  describe('网络错误', () => {
    it('网络错误返回 status 0 的错误', async () => {
      const adapter = vi.fn().mockRejectedValue({
        code: 'ERR_NETWORK',
        message: 'Network Error',
        config: { url: '/test' },
        // 没有 response
      })
      apiClient.defaults.adapter = adapter

      await expect(apiClient.get('/test')).rejects.toEqual(
        expect.objectContaining({
          status: 0,
          message: 'Network error. Please check your connection.',
        })
      )
    })
  })

  // --- 请求取消 ---

  describe('请求取消', () => {
    it('取消的请求保持原始取消错误', async () => {
      const source = axios.CancelToken.source()

      const adapter = vi.fn().mockRejectedValue(
        new axios.Cancel('Operation canceled')
      )
      apiClient.defaults.adapter = adapter

      await expect(
        apiClient.get('/test', { cancelToken: source.token })
      ).rejects.toBeDefined()
    })
  })
})
