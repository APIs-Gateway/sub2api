import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post, put } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    get,
    post,
    put,
  },
}))

import {
  getUpstreamBillingProbeSettings,
  listIds,
  probeUpstreamBilling,
  probeUpstreamBillingBatch,
  refreshCredentials,
  reauthCodexSession,
  setUpstreamBillingProbeEnabled,
  syncFromCrs,
  updateUpstreamBillingProbeSettings,
} from '@/api/admin/accounts'

describe('admin accounts api', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
    put.mockReset()
    post.mockResolvedValue({
      data: {
        created: 0,
        updated: 0,
        skipped: 0,
        failed: 0,
        items: [],
      },
    })
  })

  it('uses an extended timeout for CRS account sync', async () => {
    const params = {
      base_url: 'https://crs.example.com',
      username: 'admin',
      password: 'secret',
      selected_account_ids: ['crs-1'],
    }

    await syncFromCrs(params)

    expect(post).toHaveBeenCalledWith('/admin/accounts/sync/crs', params, {
      timeout: 180000,
    })
  })

  it('maps upstream billing probe settings and account actions', async () => {
    const settings = { enabled: true, interval_minutes: 30 }
    get.mockResolvedValueOnce({ data: settings })
    expect(await getUpstreamBillingProbeSettings()).toEqual(settings)
    expect(get).toHaveBeenCalledWith('/admin/accounts/upstream-billing-probe/settings')

    put.mockResolvedValueOnce({ data: settings })
    expect(await updateUpstreamBillingProbeSettings(settings)).toEqual(settings)
    expect(put).toHaveBeenCalledWith('/admin/accounts/upstream-billing-probe/settings', settings)

    await setUpstreamBillingProbeEnabled(7, false)
    expect(put).toHaveBeenCalledWith('/admin/accounts/7/upstream-billing-probe', { enabled: false })

    const snapshot = {
      status: 'ok',
      last_attempt_at: '2026-07-13T00:00:00Z',
      next_probe_at: '2026-07-13T00:30:00Z'
    }
    post.mockResolvedValueOnce({ data: { account_id: 7, snapshot } })
    expect(await probeUpstreamBilling(7)).toEqual({ account_id: 7, snapshot })
    expect(post).toHaveBeenCalledWith('/admin/accounts/7/upstream-billing-probe')

    const results = [{ account_id: 7, snapshot }, { account_id: 8, error: 'unsupported' }]
    post.mockResolvedValueOnce({ data: { results } })
    expect(await probeUpstreamBillingBatch([7, 8])).toEqual(results)
    expect(post).toHaveBeenCalledWith('/admin/accounts/upstream-billing-probe/batch', {
      account_ids: [7, 8]
    })
  })

  it('normalizes refresh responses with and without a partial refresh warning', async () => {
    const account = { id: 42, name: 'antigravity' }
    post.mockResolvedValueOnce({ data: account })
    await expect(refreshCredentials(42)).resolves.toEqual({ account })
    expect(post).toHaveBeenLastCalledWith('/admin/accounts/42/refresh')

    const warned = {
      account,
      message: 'Token refreshed successfully, but project_id could not be retrieved (will retry automatically)',
      warning: 'missing_project_id_temporary',
    }
    post.mockResolvedValueOnce({ data: warned })
    await expect(refreshCredentials(42)).resolves.toEqual(warned)
  })

  it('sends Codex reauthorization only to the selected existing account', async () => {
    const result = { account: { id: 17, name: 'existing' }, warnings: ['old RT not verified'] }
    post.mockResolvedValueOnce({ data: result })

    await expect(reauthCodexSession(17, '{"tokens":{"access_token":"at"}}')).resolves.toEqual(result)
    expect(post).toHaveBeenLastCalledWith('/admin/accounts/17/reauth/codex-session', {
      content: '{"tokens":{"access_token":"at"}}'
    })
  })

  it('lists matching account ids with the same filters as the list endpoint', async () => {
    const result = { ids: [1, 2, 3], total: 3, platforms: ['openai'], types: ['apikey'] }
    get.mockResolvedValue({ data: result })
    const filters = { platform: 'openai', type: 'apikey', status: 'active', group: '12', search: 'relay', privacy_mode: '' }

    await expect(listIds(filters)).resolves.toEqual(result)

    expect(get).toHaveBeenCalledWith('/admin/accounts/ids', { params: filters, signal: undefined })
  })
})
