import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({ post: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { post } }))

import { runNow } from '@/api/admin/channelMonitor'

describe('admin channel monitor runNow API', () => {
  beforeEach(() => {
    post.mockReset()
    post.mockResolvedValue({ data: { results: [] } })
  })

  it('waits longer than the backend check budget instead of the 30s default', async () => {
    const res = await runNow(5)

    expect(res).toEqual({ results: [] })
    expect(post).toHaveBeenCalledTimes(1)
    const [url, body, options] = post.mock.calls[0]
    expect(url).toBe('/admin/channel-monitors/5/run')
    expect(body).toBeUndefined()
    // 后端单次检测最长 90 秒（外加 8 秒 ping），浏览器侧不能先超时。
    expect(options.timeout).toBeGreaterThan(98_000)
  })
})
