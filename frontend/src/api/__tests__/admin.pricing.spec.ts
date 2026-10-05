import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get } = vi.hoisted(() => ({ get: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { get } }))

import pricingAPI, { getGroupDerive, listModelCatalog, quoteBatch } from '@/api/admin/pricing'

describe('admin pricing api（只读）', () => {
  beforeEach(() => {
    get.mockReset()
  })

  it('读模型目录，取 items', async () => {
    get.mockResolvedValue({ data: { items: [{ model_key: 'gpt-5.5' }] } })
    expect(await listModelCatalog()).toEqual([{ model_key: 'gpt-5.5' }])
    expect(get).toHaveBeenCalledWith('/admin/model-catalog')

    get.mockResolvedValue({ data: {} })
    expect(await listModelCatalog()).toEqual([])
  })

  it('读分组按渠道配置推出的结果', async () => {
    get.mockResolvedValue({ data: { group_id: 7 } })
    expect(await getGroupDerive(7)).toEqual({ group_id: 7 })
    expect(get).toHaveBeenCalledWith('/admin/pricing-matrix/groups/7/derive')
  })

  it('批量报价：分组和模型用逗号拼成查询参数；没有分组时只取官方参考价', async () => {
    get.mockResolvedValue({ data: { models: [{ model: 'a' }], cells: [{ model: 'a' }] } })
    expect(await quoteBatch([1, 2], ['a', 'b'])).toEqual({ models: [{ model: 'a' }], cells: [{ model: 'a' }] })
    expect(get).toHaveBeenCalledWith('/admin/pricing/quote-batch', { params: { group_ids: '1,2', models: 'a,b' } })

    get.mockResolvedValue({ data: {} })
    expect(await quoteBatch([], ['a'])).toEqual({ models: [], cells: [] })
    expect(get).toHaveBeenLastCalledWith('/admin/pricing/quote-batch', { params: { group_ids: '', models: 'a' } })
  })

  it('默认导出汇总三个接口', () => {
    expect(Object.keys(pricingAPI).sort()).toEqual(['getGroupDerive', 'listModelCatalog', 'quoteBatch'])
  })
})
