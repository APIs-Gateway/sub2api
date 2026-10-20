import { describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({ post: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { post } }))

import { exchangeCode, generateAuthUrl, refreshGrokToken } from '@/api/admin/grok'

describe('admin Grok API', () => {
  it('posts auth URL and exchange payloads', async () => {
    post.mockResolvedValueOnce({ data: { auth_url: 'url', session_id: 'session', state: 'state' } })
    await expect(generateAuthUrl({ proxy_id: 3 })).resolves.toEqual({ auth_url: 'url', session_id: 'session', state: 'state' })
    expect(post).toHaveBeenLastCalledWith('/admin/grok/oauth/auth-url', { proxy_id: 3 })

    post.mockResolvedValueOnce({ data: { access_token: 'access' } })
    await expect(exchangeCode({ session_id: 'session', state: 'state', code: 'code' })).resolves.toEqual({ access_token: 'access' })
    expect(post).toHaveBeenLastCalledWith('/admin/grok/oauth/exchange-code', {
      session_id: 'session',
      state: 'state',
      code: 'code'
    })
  })

  it('posts refresh token with an optional proxy', async () => {
    post.mockResolvedValue({ data: { access_token: 'refreshed' } })
    await refreshGrokToken('refresh-token')
    expect(post).toHaveBeenLastCalledWith('/admin/grok/oauth/refresh-token', { refresh_token: 'refresh-token' })
    await refreshGrokToken('refresh-token', 9)
    expect(post).toHaveBeenLastCalledWith('/admin/grok/oauth/refresh-token', {
      refresh_token: 'refresh-token',
      proxy_id: 9
    })
  })
})
