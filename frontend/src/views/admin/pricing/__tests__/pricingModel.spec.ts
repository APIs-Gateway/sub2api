import { describe, expect, it } from 'vitest'
import type { QuoteBatchCell } from '@/api/admin/pricing'
import {
  buildGroup,
  buildModelRows,
  cellKey,
  cellView,
  comparePlatforms,
  isGroupUnswitched,
  isOpenCell,
  isUnpricedModel,
  modelStats,
  platformLabel,
  sourceKey,
  type PricingGroup
} from '../pricingModel'
import { catalog, derive, derives, groupList, quotes } from './fixtures'

const legacyGroup: PricingGroup = { id: 1, name: 'g', platform: 'openai', rate: 1.4, stage: 'legacy', accessMode: 'open' }
const v2Group: PricingGroup = { ...legacyGroup, id: 3, stage: 'v2' }

function quote(over: Partial<QuoteBatchCell>): QuoteBatchCell {
  return {
    group_id: 1,
    model: 'm',
    access: { ok: true },
    priced: true,
    source: 'litellm',
    billing_mode: 'token',
    group_multiplier: 1,
    effective_multiplier: 1,
    final_per_mtok: { input: 2, output: 8 },
    ...over
  }
}

describe('cellView', () => {
  it('没取到报价时返回 null', () => {
    expect(cellView(undefined, legacyGroup)).toBeNull()
  })

  it('开放：带用户实付单价', () => {
    const view = cellView(quote({}), legacyGroup)
    expect(view).toMatchObject({ kind: 'open', usd: { input: 2, output: 8 }, extra: null, unswitched: true })
    expect(isOpenCell(view)).toBe(true)
  })

  it('额外倍率：extra_multiplier 不等于 1 才算', () => {
    expect(cellView(quote({ extra_multiplier: 1.2 }), v2Group)).toMatchObject({ kind: 'extra', extra: 1.2, unswitched: false })
    expect(cellView(quote({ extra_multiplier: 1 }), v2Group)?.kind).toBe('open')
  })

  it('自定义价：价格来自渠道时优先于额外倍率', () => {
    expect(cellView(quote({ source: 'channel', extra_multiplier: 1.2 }), legacyGroup)?.kind).toBe('custom')
  })

  it('未定价：开放但没有任何价格，不带价格', () => {
    const view = cellView(quote({ priced: false, source: 'none', final_per_mtok: undefined }), legacyGroup)
    expect(view).toMatchObject({ kind: 'unpriced', usd: null, perRequestUsd: null })
    expect(isOpenCell(view)).toBe(true)
    // 标了有价但没有任何金额，同样按未定价
    expect(cellView(quote({ final_per_mtok: undefined }), legacyGroup)?.kind).toBe('unpriced')
  })

  it('按次计费：取每次的价格', () => {
    const view = cellView(quote({ final_per_mtok: undefined, per_request_price: 0.5 }), legacyGroup)
    expect(view).toMatchObject({ kind: 'open', usd: null, perRequestUsd: 0.5 })
  })

  it('按次计费只有区间价：取范围，不当成 0 元', () => {
    const view = cellView(quote({ final_per_mtok: undefined, per_request_min: 3.6, per_request_max: 5 }), legacyGroup)
    expect(view).toMatchObject({ kind: 'open', perRequestUsd: null, perRequestRange: { min: 3.6, max: 5 } })
  })

  it('关闭：保留原因，不算开放', () => {
    const view = cellView(quote({ access: { ok: false, reason: 'not_in_allowlist' } }), legacyGroup)
    expect(view).toMatchObject({ kind: 'closed', reason: 'not_in_allowlist', usd: null })
    expect(isOpenCell(view)).toBe(false)
    expect(cellView(quote({ access: { ok: false } }), legacyGroup)?.reason).toBeNull()
  })

  it('没报出价的格子是 error，且不算开放', () => {
    const view = cellView(quote({ error: 'GROUP_NOT_FOUND' }), legacyGroup)
    expect(view?.kind).toBe('error')
    expect(isOpenCell(view)).toBe(false)
    expect(isOpenCell(null)).toBe(false)
  })
})

describe('分组与平台', () => {
  it('buildGroup：阶段与开放方式来自派生结果，读不到时按 legacy', () => {
    expect(buildGroup(groupList[2], derives[3])).toMatchObject({ stage: 'v2', accessMode: 'open', rate: 2 })
    expect(buildGroup(groupList[1], derives[2])).toMatchObject({ stage: 'legacy', accessMode: 'allowlist' })
    expect(buildGroup(groupList[0], null)).toMatchObject({ stage: 'legacy', accessMode: null })
  })

  it('v2 分组的准入模式以库里为准，派生值不一致时不用；非 v2 分组仍用派生值', () => {
    const v2 = derive(3, 'anthropic', 'v2', 'open', ['m'], 'allowlist')
    expect(buildGroup(groupList[2], v2)).toMatchObject({ stage: 'v2', accessMode: 'allowlist' })
    // 库里没带准入模式（旧接口）时回落派生值
    const noStored = { ...v2, stored_config: { pricing_stage: 'v2' as const, revision: 7 } }
    expect(buildGroup(groupList[2], noStored)).toMatchObject({ accessMode: 'open' })
    // legacy 分组即使带了 stored_config.access_mode 也不采用
    const legacy = derive(1, 'openai', 'legacy', 'open', ['m'])
    const withStored = { ...legacy, stored_config: { pricing_stage: 'legacy' as const, access_mode: 'allowlist' as const } }
    expect(buildGroup(groupList[0], withStored)).toMatchObject({ accessMode: 'open' })
    expect(isGroupUnswitched(buildGroup(groupList[0], null))).toBe(true)
    expect(isGroupUnswitched(buildGroup(groupList[2], derives[3]))).toBe(false)
  })

  it('平台排序与名称', () => {
    expect(['grok', 'zzz', 'anthropic', 'openai', 'aaa'].sort(comparePlatforms)).toEqual(['openai', 'anthropic', 'grok', 'aaa', 'zzz'])
    expect(platformLabel('openai')).toBe('OpenAI')
    expect(platformLabel('custom')).toBe('custom')
  })

  it('价格来源的键', () => {
    expect(sourceKey('litellm')).toBe('litellm')
    expect(sourceKey('fallback')).toBe('fallback')
    expect(sourceKey('card_policy')).toBe('cardPolicy')
    expect(sourceKey('channel')).toBe('channel')
    expect(sourceKey('none')).toBe('none')
    expect(sourceKey(undefined)).toBe('none')
  })
})

describe('buildModelRows', () => {
  const rows = buildModelRows(catalog, Object.values(derives))

  it('目录里的模型在前，未登记的在后；通配符条目和别名不算未登记', () => {
    expect(rows.map((r) => `${r.platform}/${r.key}`)).toEqual([
      'openai/gpt-5.4',
      'openai/gpt-5.5',
      'openai/gpt-6.2-sol',
      'openai/minimax-m3',
      'openai/qwen3-max',
      'anthropic/claude-opus-5-5'
    ])
    const unregistered = rows.filter((r) => !r.registered)
    expect(unregistered.map((r) => r.key)).toEqual(['qwen3-max'])
    expect(unregistered[0]).toMatchObject({ status: null, referenceModel: null })
  })

  it('保留目录里的状态、显示名、别名与参考模型', () => {
    const sol = rows.find((r) => r.key === 'gpt-6.2-sol')
    expect(sol).toMatchObject({ status: 'draft', displayName: 'GPT 6.2 Sol', referenceModel: 'gpt-5.5', registered: true })
    expect(rows.find((r) => r.key === 'gpt-5.5')?.aliases).toEqual(['gpt5.5'])
  })

  it('已删除的分组不产生未登记模型', () => {
    const deleted = { ...derives[1], deleted: true }
    expect(buildModelRows([], [deleted])).toEqual([])
  })

  it('目录显示名为空时用模型名', () => {
    const [row] = buildModelRows([{ ...catalog[0], display_name: '' }], [])
    expect(row.displayName).toBe('gpt-5.5')
  })
})

describe('modelStats', () => {
  const groups = groupList.map((g) => buildGroup(g, derives[g.id]))
  const cells = Object.fromEntries(Object.entries(quotes).map(([k, v]) => [k, v]))
  const rows = buildModelRows(catalog, Object.values(derives))

  it('只统计同平台的分组', () => {
    const gpt55 = rows.find((r) => r.key === 'gpt-5.5')!
    expect(modelStats(gpt55, groups, cells)).toEqual({ groupCount: 2, openCount: 2, quotedCount: 2 })
    const draft = rows.find((r) => r.key === 'gpt-6.2-sol')!
    expect(modelStats(draft, groups, cells)).toEqual({ groupCount: 2, openCount: 0, quotedCount: 2 })
    const opus = rows.find((r) => r.key === 'claude-opus-5-5')!
    expect(modelStats(opus, groups, cells)).toEqual({ groupCount: 1, openCount: 1, quotedCount: 1 })
  })

  it('没取到报价的分组不计入 quotedCount', () => {
    const gpt55 = rows.find((r) => r.key === 'gpt-5.5')!
    expect(modelStats(gpt55, groups, { [cellKey(1, 'gpt-5.5')]: quotes['1|gpt-5.5'] })).toEqual({
      groupCount: 2,
      openCount: 1,
      quotedCount: 1
    })
  })

  it('isUnpricedModel', () => {
    expect(isUnpricedModel(undefined)).toBe(false)
    expect(isUnpricedModel({ model: 'x', priced: false, source: 'none' })).toBe(true)
    expect(isUnpricedModel({ model: 'x', priced: true, source: 'litellm' })).toBe(false)
  })
})
