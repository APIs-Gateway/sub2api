import { beforeEach, describe, expect, it, vi } from 'vitest'
import { QUOTE_BATCH_MAX_MODELS } from '@/api/admin/pricing'
import { catalog, derives, installApiMocks } from './fixtures'

const { api } = vi.hoisted(() => ({
  api: {
    groups: { getAll: vi.fn() },
    pricing: { listModelCatalog: vi.fn(), getGroupDerive: vi.fn(), quoteBatch: vi.fn() }
  }
}))

vi.mock('@/api/admin', () => ({ adminAPI: api }))

import { resetPricingDataForTest, usePricingData } from '../usePricingData'

describe('usePricingData', () => {
  beforeEach(() => {
    resetPricingDataForTest()
    installApiMocks(api)
  })

  it('取齐分组、目录、阶段、官方参考价和报价', async () => {
    const { state, rows, platforms, hasUnswitchedGroups, hasSwitchedGroups, load } = usePricingData()
    await load()

    expect(state.loaded).toBe(true)
    expect(state.error).toBeNull()
    expect(state.groups.map((g) => g.name)).toEqual(['codex pro', 'codex特惠分组', 'kiro cc'])
    expect(state.groups.find((g) => g.id === 3)?.stage).toBe('v2')
    expect(state.groups.find((g) => g.id === 2)).toMatchObject({ stage: 'legacy', accessMode: 'allowlist' })
    expect(rows.value).toHaveLength(6)
    expect(platforms.value).toEqual(['openai', 'anthropic'])
    expect(hasUnswitchedGroups.value).toBe(true)
    expect(hasSwitchedGroups.value).toBe(true)
    expect(state.refs['gpt-5.5']).toMatchObject({ priced: true, source: 'litellm' })
    expect(state.cells['1|gpt-5.5'].final_per_mtok).toEqual({ input: 7, output: 42 })
    expect(state.cells['3|claude-opus-5-5'].per_request_price).toBe(0.5)
  })

  it('同平台的模型 × 分组才报价，不跨平台', async () => {
    const { state, load } = usePricingData()
    await load()
    const calls = api.pricing.quoteBatch.mock.calls.filter(([groupIds]: [number[]]) => groupIds.length > 0)
    expect(calls).toHaveLength(2)
    const openai = calls.find(([, models]: [number[], string[]]) => models.includes('gpt-5.5'))!
    expect([...openai[0]].sort()).toEqual([1, 2])
    expect(openai[1]).not.toContain('claude-opus-5-5')
    expect(state.cells['3|gpt-5.5']).toBeUndefined()
  })

  it('已经取过就不再请求，refresh 才重新取', async () => {
    const { load, refresh } = usePricingData()
    await load()
    expect(api.groups.getAll).toHaveBeenCalledTimes(1)
    await load()
    expect(api.groups.getAll).toHaveBeenCalledTimes(1)
    await refresh()
    expect(api.groups.getAll).toHaveBeenCalledTimes(2)
  })

  it('同时多次调用共用同一次请求', async () => {
    const { load } = usePricingData()
    await Promise.all([load(), load(), load()])
    expect(api.groups.getAll).toHaveBeenCalledTimes(1)
  })

  it('读不到某个分组的开放方式时不挡页面，只计数', async () => {
    api.pricing.getGroupDerive.mockImplementation(async (id: number) => {
      if (id === 2) throw new Error('boom')
      return derives[id]
    })
    const { state, load } = usePricingData()
    await load()
    expect(state.loaded).toBe(true)
    expect(state.deriveFailed).toBe(1)
    expect(state.groups.find((g) => g.id === 2)).toMatchObject({ stage: 'legacy', accessMode: null })
  })

  it('分组或目录取失败时给出错误，允许重试', async () => {
    api.groups.getAll.mockRejectedValueOnce({ message: '没有权限' })
    const { state, load, refresh } = usePricingData()
    await load()
    expect(state.loaded).toBe(false)
    expect(state.error).toBe('没有权限')
    expect(state.loading).toBe(false)

    api.groups.getAll.mockRejectedValueOnce('plain failure')
    await refresh()
    expect(state.error).toBe('plain failure')

    await refresh()
    expect(state.loaded).toBe(true)
    expect(state.error).toBeNull()
  })

  it('模型多于单次上限时分批取官方参考价', async () => {
    const many = Array.from({ length: QUOTE_BATCH_MAX_MODELS + 5 }, (_, i) => ({
      ...catalog[0],
      id: 100 + i,
      model_key: `model-${i}`,
      aliases: []
    }))
    api.pricing.listModelCatalog.mockResolvedValue(many)
    const { state, load } = usePricingData()
    await load()
    const refCalls = api.pricing.quoteBatch.mock.calls.filter(([groupIds]: [number[]]) => groupIds.length === 0)
    const sizes = refCalls.map(([, models]: [number[], string[]]) => models.length)
    // 65 个目录模型，加上分组配置里出现过但目录没有的 4 个名字（gpt-5.5、qwen3-max、gpt5.5、claude-opus-5-5）
    expect(sizes[0]).toBe(QUOTE_BATCH_MAX_MODELS)
    expect(sizes.reduce((a: number, b: number) => a + b, 0)).toBe(69)
    expect(Object.keys(state.refs)).toHaveLength(69)
  })

  it('没有分组的平台只取官方参考价', async () => {
    api.groups.getAll.mockResolvedValue([])
    const { state, rows, load } = usePricingData()
    await load()
    expect(state.groups).toEqual([])
    expect(rows.value.length).toBe(catalog.length)
    expect(api.pricing.quoteBatch.mock.calls.every(([groupIds]: [number[]]) => groupIds.length === 0)).toBe(true)
    expect(state.refs['gpt-5.5']?.priced).toBe(true)
  })
})
