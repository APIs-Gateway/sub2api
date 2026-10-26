import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get } = vi.hoisted(() => ({
  get: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    get,
  },
}))

import userChannelsAPI, { getPrices, getAvailable } from '@/api/channels'

describe('user channels api', () => {
  beforeEach(() => {
    get.mockReset()
  })

  it('getPrices 请求 /channels/prices 并原样返回数据', async () => {
    const catalog = { groups: [], models: [] }
    get.mockResolvedValue({ data: catalog })

    const result = await getPrices()

    expect(get).toHaveBeenCalledWith('/channels/prices', { signal: undefined })
    expect(result).toBe(catalog)
  })

  it('getPrices 透传 AbortSignal', async () => {
    get.mockResolvedValue({ data: { groups: [], models: [] } })
    const controller = new AbortController()

    await getPrices({ signal: controller.signal })

    expect(get).toHaveBeenCalledWith('/channels/prices', { signal: controller.signal })
  })

  it('默认导出包含 getAvailable 与 getPrices', () => {
    expect(userChannelsAPI.getPrices).toBe(getPrices)
    expect(userChannelsAPI.getAvailable).toBe(getAvailable)
  })
})
