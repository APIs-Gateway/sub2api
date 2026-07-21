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
  probeUpstreamBilling,
  probeUpstreamBillingBatch,
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
})
