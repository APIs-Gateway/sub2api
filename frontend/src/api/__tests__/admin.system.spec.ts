import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({
  post: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    post,
  },
}))

import { performUpdate } from '@/api/admin/system'

describe('admin system api', () => {
  beforeEach(() => {
    post.mockReset()
    post.mockResolvedValue({
      data: {
        message: 'Update completed. Please restart the service.',
        need_restart: true,
      },
    })
  })

  it('uses the extended timeout for release downloads', async () => {
    await expect(performUpdate()).resolves.toEqual({
      message: 'Update completed. Please restart the service.',
      need_restart: true,
    })

    expect(post).toHaveBeenCalledWith('/admin/system/update', undefined, {
      timeout: 15 * 60 * 1000,
    })
  })
})
