import { beforeEach, describe, expect, it, vi } from 'vitest'

const { generateAuthUrl, exchangeCode, refreshGrokToken, showError } = vi.hoisted(() => ({
  generateAuthUrl: vi.fn(),
  exchangeCode: vi.fn(),
  refreshGrokToken: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    grok: { generateAuthUrl, exchangeCode, refreshGrokToken }
  }
}))

import { useGrokOAuth } from '@/composables/useGrokOAuth'

const tokenInfo = {
  access_token: 'access',
  refresh_token: 'refresh',
  token_type: 'Bearer',
  id_token: 'id',
  expires_at: 1_900_000_000,
  client_id: 'client',
  scope: 'scope',
  email: 'grok@example.com',
  subscription_tier: 'supergrok',
  entitlement_status: 'active'
}

describe('useGrokOAuth', () => {
  beforeEach(() => {
    generateAuthUrl.mockReset()
    exchangeCode.mockReset()
    refreshGrokToken.mockReset()
    showError.mockReset()
  })

  it('generates auth URL, exchanges code, and validates refresh token', async () => {
    generateAuthUrl.mockResolvedValue({ auth_url: 'https://auth.test', session_id: 'session', state: 'state' })
    exchangeCode.mockResolvedValue(tokenInfo)
    refreshGrokToken.mockResolvedValue(tokenInfo)
    const oauth = useGrokOAuth()

    await expect(oauth.generateAuthUrl(7)).resolves.toBe(true)
    expect(generateAuthUrl).toHaveBeenCalledWith({ proxy_id: 7 })
    expect(oauth.authUrl.value).toBe('https://auth.test')
    expect(oauth.sessionId.value).toBe('session')
    expect(oauth.state.value).toBe('state')
    expect(oauth.loading.value).toBe(false)

    await expect(oauth.exchangeAuthCode({ code: '  code  ', sessionId: 'session', state: 'state', proxyId: 7 }))
      .resolves.toEqual(tokenInfo)
    expect(exchangeCode).toHaveBeenCalledWith({ session_id: 'session', state: 'state', code: 'code', proxy_id: 7 })

    await expect(oauth.validateRefreshToken('  refresh  ', 7)).resolves.toEqual(tokenInfo)
    expect(refreshGrokToken).toHaveBeenCalledWith('refresh', 7)
  })

  it('rejects missing inputs and resets state', async () => {
    const oauth = useGrokOAuth()
    await expect(oauth.exchangeAuthCode({ code: ' ', sessionId: 'session', state: 'state' })).resolves.toBeNull()
    await expect(oauth.validateRefreshToken(' ')).resolves.toBeNull()
    expect(oauth.error.value).toBe('admin.accounts.oauth.grok.pleaseEnterRefreshToken')

    oauth.authUrl.value = 'url'
    oauth.sessionId.value = 'session'
    oauth.state.value = 'state'
    oauth.error.value = 'error'
    oauth.loading.value = true
    oauth.resetState()
    expect(oauth.authUrl.value).toBe('')
    expect(oauth.sessionId.value).toBe('')
    expect(oauth.state.value).toBe('')
    expect(oauth.error.value).toBe('')
    expect(oauth.loading.value).toBe(false)
  })

  it('surfaces API errors and builds filtered credentials and extra info', async () => {
    generateAuthUrl.mockRejectedValue({ response: { data: { detail: 'generate failed' } } })
    exchangeCode.mockRejectedValue({ response: { data: { detail: 'exchange failed' } } })
    refreshGrokToken.mockRejectedValue(new Error('refresh failed'))
    const oauth = useGrokOAuth()

    await expect(oauth.generateAuthUrl(null)).resolves.toBe(false)
    expect(showError).toHaveBeenCalledWith('generate failed')
    await expect(oauth.exchangeAuthCode({ code: 'code', sessionId: 'session', state: 'state' })).resolves.toBeNull()
    expect(showError).toHaveBeenCalledWith('exchange failed')
    await expect(oauth.validateRefreshToken('refresh')).resolves.toBeNull()

    const credentials = oauth.buildCredentials({ ...tokenInfo, refresh_token: '', id_token: '', scope: '' })
    expect(credentials).toEqual({
      access_token: 'access',
      token_type: 'Bearer',
      expires_at: 1_900_000_000,
      client_id: 'client',
      email: 'grok@example.com',
      subscription_tier: 'supergrok',
      entitlement_status: 'active'
    })
    expect(oauth.buildExtraInfo(tokenInfo)).toEqual({
      email: 'grok@example.com',
      subscription_tier: 'supergrok',
      entitlement_status: 'active'
    })
  })
})
