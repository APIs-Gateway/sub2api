import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({
  post: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: { post },
}))

import { performUpdate } from '@/api/admin/system'

describe('admin system api', () => {
  beforeEach(() => {
    post.mockReset()
  })

  it('allows in-place updates to wait for slow downloads', async () => {
    const result = { message: 'Update completed', need_restart: true }
    post.mockResolvedValue({ data: result })

    await expect(performUpdate()).resolves.toEqual(result)
    expect(post).toHaveBeenCalledWith('/admin/system/update', undefined, {
      timeout: 15 * 60 * 1000,
    })
  })
})
