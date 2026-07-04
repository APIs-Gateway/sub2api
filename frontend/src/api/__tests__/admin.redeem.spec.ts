import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({
  post: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    post,
  },
}))

import { generate } from '@/api/admin/redeem'

describe('admin redeem api', () => {
  beforeEach(() => {
    post.mockReset()
  })

  it('omits legacy group_id when generating degrouped subscription codes', async () => {
    post.mockResolvedValue({ data: [] })

    await generate(2, 'subscription', 30, null, 30)

    expect(post).toHaveBeenCalledWith('/admin/redeem-codes/generate', {
      count: 2,
      type: 'subscription',
      value: 30,
      validity_days: 30,
    })
  })

  it('keeps legacy group_id only when explicitly provided', async () => {
    post.mockResolvedValue({ data: [] })

    await generate(1, 'subscription', 30, 9, 30)

    expect(post).toHaveBeenCalledWith('/admin/redeem-codes/generate', {
      count: 1,
      type: 'subscription',
      value: 30,
      group_id: 9,
      validity_days: 30,
    })
  })
})
