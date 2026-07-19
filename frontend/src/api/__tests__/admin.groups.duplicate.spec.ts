import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({
  post: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: { post },
}))

import { duplicate } from '@/api/admin/groups'

describe('admin group duplicate API', () => {
  beforeEach(() => {
    localStorage.clear()
    sessionStorage.clear()
    localStorage.setItem('auth_user', JSON.stringify({ id: 7 }))
    post.mockReset()
    post.mockResolvedValue({ data: { id: 43, name: 'primary (Copy)' } })
    vi.spyOn(globalThis.crypto, 'randomUUID').mockReturnValue(
      '11111111-1111-4111-8111-111111111111'
    )
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('sends a stable idempotency key with the duplicate request', async () => {
    const group = await duplicate(42)

    expect(post).toHaveBeenCalledWith('/admin/groups/42/duplicate', undefined, {
      headers: {
        'Idempotency-Key': 'group-duplicate-7-42-11111111-1111-4111-8111-111111111111',
      },
    })
    expect(group).toEqual({ id: 43, name: 'primary (Copy)' })
    expect(sessionStorage.length).toBe(0)
  })

  it('reuses the operation key after an ambiguous failed request', async () => {
    post.mockRejectedValueOnce(new Error('network timeout'))
    await expect(duplicate(99)).rejects.toThrow('network timeout')

    post.mockResolvedValueOnce({ data: { id: 100, name: 'retry (Copy)' } })
    await duplicate(99)

    expect(post).toHaveBeenCalledTimes(2)
    expect(post.mock.calls[1][2].headers).toEqual(post.mock.calls[0][2].headers)
    expect(sessionStorage.length).toBe(0)
  })
})
