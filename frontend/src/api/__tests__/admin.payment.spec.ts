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

import { adminPaymentAPI } from '@/api/admin/payment'

describe('admin payment api', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
    put.mockReset()
    get.mockResolvedValue({ data: {} })
    post.mockResolvedValue({ data: {} })
    put.mockResolvedValue({ data: {} })
  })

  it('posts provider refund query requests to the admin order endpoint', async () => {
    await adminPaymentAPI.queryRefund(42, { refund_id: 'refund-123' })

    expect(post).toHaveBeenCalledWith('/admin/payment/orders/42/refund/query', {
      refund_id: 'refund-123',
    })
  })
})
