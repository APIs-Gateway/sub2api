import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({
  post: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    get: vi.fn(),
    post,
    put: vi.fn(),
    delete: vi.fn(),
  },
}))

import { create } from '@/api/keys'

describe('keys api – create 带 rateLimitData', () => {
  beforeEach(() => {
    post.mockReset()
    post.mockResolvedValue({ data: { id: 1, key: 'sk-test', name: 'test' } })
  })

  it('三个限速字段均大于 0 时全部写入请求体', async () => {
    await create('test', undefined, undefined, undefined, undefined, undefined, undefined, {
      rate_limit_5h: 100,
      rate_limit_1d: 500,
      rate_limit_7d: 2000,
    })

    expect(post).toHaveBeenCalledWith('/keys', expect.objectContaining({
      name: 'test',
      rate_limit_5h: 100,
      rate_limit_1d: 500,
      rate_limit_7d: 2000,
    }))
  })

  it('只传 rate_limit_5h 时只有该字段出现在请求体', async () => {
    await create('test', undefined, undefined, undefined, undefined, undefined, undefined, {
      rate_limit_5h: 50,
    })

    const payload = post.mock.calls[0][1]
    expect(payload.rate_limit_5h).toBe(50)
    expect(payload).not.toHaveProperty('rate_limit_1d')
    expect(payload).not.toHaveProperty('rate_limit_7d')
  })

  it('rateLimitData 为 undefined 时不带任何限速字段', async () => {
    await create('test')

    const payload = post.mock.calls[0][1]
    expect(payload).not.toHaveProperty('rate_limit_5h')
    expect(payload).not.toHaveProperty('rate_limit_1d')
    expect(payload).not.toHaveProperty('rate_limit_7d')
  })

  it('限速值为 0 时不写入请求体（0 视为不限速）', async () => {
    await create('test', undefined, undefined, undefined, undefined, undefined, undefined, {
      rate_limit_5h: 0,
      rate_limit_1d: 0,
      rate_limit_7d: 0,
    })

    const payload = post.mock.calls[0][1]
    expect(payload).not.toHaveProperty('rate_limit_5h')
    expect(payload).not.toHaveProperty('rate_limit_1d')
    expect(payload).not.toHaveProperty('rate_limit_7d')
  })
})
