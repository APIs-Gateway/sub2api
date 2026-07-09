import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({
  post: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    post,
  },
}))

import { syncFromCrs } from '@/api/admin/accounts'

describe('admin accounts api', () => {
  beforeEach(() => {
    post.mockReset()
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
})
