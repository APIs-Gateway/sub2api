import { afterEach, describe, expect, it, vi } from 'vitest'

describe('configured API base URL helpers', () => {
  afterEach(() => {
    vi.unstubAllEnvs()
    vi.resetModules()
  })

  it('builds API URLs from the configured API base', async () => {
    vi.stubEnv('VITE_API_BASE_URL', 'https://api.example.test/api/v1/')
    vi.resetModules()

    const { buildApiUrl } = await import('../client')

    expect(buildApiUrl('/admin/accounts/42/test')).toBe('https://api.example.test/api/v1/admin/accounts/42/test')
    expect(buildApiUrl('/api/v1/payment/orders/7')).toBe('https://api.example.test/api/v1/payment/orders/7')
  })

  it('builds gateway URLs from the configured API origin', async () => {
    vi.stubEnv('VITE_API_BASE_URL', 'https://gateway.example.test/api/v1')
    vi.resetModules()

    const { buildGatewayUrl } = await import('../client')

    expect(buildGatewayUrl('/v1/usage')).toBe('https://gateway.example.test/v1/usage')
  })

  it('keeps a configured non-api absolute gateway path', async () => {
    vi.stubEnv('VITE_API_BASE_URL', 'https://gateway.example.test/proxy-root/')
    vi.resetModules()

    const { buildGatewayUrl } = await import('../client')

    expect(buildGatewayUrl('/v1/chat/completions')).toBe('https://gateway.example.test/proxy-root/v1/chat/completions')
  })
})
