import { describe, expect, it } from 'vitest'
import type { SnapshotDiffEntry } from '@/api/admin/pricingOps'
import { effectiveDirection, entryDirection, entryPercent, entryPrices, filterEntries, isUpward, otherChangedFieldCount, perMtok } from '../snapshotModel'

const entry = (over: Partial<SnapshotDiffEntry>): SnapshotDiffEntry => ({
  model_key: 'm',
  change_type: 'changed',
  changed_fields: ['input_cost_per_token'],
  decision: 'approve',
  ...over
})

describe('snapshotModel', () => {
  it('每 Token 价换成每百万 Token 时不带浮点误差', () => {
    expect(perMtok(1.25e-6)).toBe(1.25)
    expect(perMtok(3e-7)).toBe(0.3)
    expect(perMtok(undefined)).toBeNull()
  })

  it('涨跌方向：输入或输出任一涨价就算涨价（有涨有跌为 mixed）', () => {
    const up = entry({ old: { input_cost_per_token: 1e-6, output_cost_per_token: 4e-6 }, new: { input_cost_per_token: 1e-6, output_cost_per_token: 5e-6 } })
    const down = entry({ old: { input_cost_per_token: 2e-6 }, new: { input_cost_per_token: 1e-6 } })
    const mixed = entry({ old: { input_cost_per_token: 2e-6, output_cost_per_token: 1e-6 }, new: { input_cost_per_token: 1e-6, output_cost_per_token: 2e-6 } })
    expect(entryDirection(up)).toBe('up')
    expect(entryDirection(down)).toBe('down')
    expect(entryDirection(mixed)).toBe('mixed')
    expect(isUpward(entryDirection(mixed))).toBe(true)
    expect(entryDirection(entry({ change_type: 'added' }))).toBeNull()
    expect(entryPrices(up)).toEqual({ oldIn: 1, oldOut: 4, newIn: 1, newOut: 5 })
  })

  it('只涨了缓存读取价的模型必须识别为涨价，其余价格字段同理', () => {
    const base = { input_cost_per_token: 1e-6, output_cost_per_token: 4e-6 }
    for (const field of [
      'cache_read_input_token_cost',
      'cache_creation_input_token_cost',
      'input_cost_per_token_above_272k_tokens',
      'output_cost_per_token_priority',
      'output_cost_per_image_token',
      'input_cost_per_image_token',
      'output_cost_per_image',
      'long_context_input_cost_multiplier'
    ]) {
      const e = entry({ changed_fields: [field], old: { ...base, [field]: 1e-7 }, new: { ...base, [field]: 2e-7 } })
      expect(entryDirection(e), field).toBe('up')
      expect(filterEntries([e], 'up', '')).toHaveLength(1)
    }
    // 字段从无到有也是涨价；只有降价才是降价
    expect(entryDirection(entry({ changed_fields: ['cache_read_input_token_cost'], old: base, new: { ...base, cache_read_input_token_cost: 1e-7 } }))).toBe('up')
    expect(entryDirection(entry({ changed_fields: ['cache_read_input_token_cost'], old: { ...base, cache_read_input_token_cost: 2e-7 }, new: { ...base, cache_read_input_token_cost: 1e-7 } }))).toBe('down')
    expect(entryPercent(entry({ old: { ...base, cache_read_input_token_cost: 1e-7 }, new: { ...base, cache_read_input_token_cost: 2e-7 } }))).toBeNull()
  })

  it('有涨有跌显示 mixed 并按涨价处理；只有非价格字段变化才是 flat', () => {
    const mixed = entry({ changed_fields: ['input_cost_per_token', 'cache_read_input_token_cost'], old: { input_cost_per_token: 2e-6, cache_read_input_token_cost: 1e-7 }, new: { input_cost_per_token: 1e-6, cache_read_input_token_cost: 2e-7 } })
    expect(entryDirection(mixed)).toBe('mixed')
    expect(isUpward('mixed')).toBe(true)
    expect(isUpward('down')).toBe(false)
    expect(filterEntries([mixed], 'up', '')).toHaveLength(1)
    expect(entryDirection(entry({ changed_fields: ['mode'], old: { input_cost_per_token: 1e-6, mode: 'chat' }, new: { input_cost_per_token: 1e-6, mode: 'responses' } }))).toBe('flat')
  })

  it('实际变价表：输入输出没变但缓存价涨了，用差异条目的方向，不写没变', () => {
    const c = { model: 'm', old_missing: false, new_missing: false, old_input_per_mtok: 1, new_input_per_mtok: 1, old_output_per_mtok: 4, new_output_per_mtok: 4 }
    const e = entry({ model_key: 'm', changed_fields: ['cache_read_input_token_cost'], old: { cache_read_input_token_cost: 1e-7 }, new: { cache_read_input_token_cost: 2e-7 } })
    expect(effectiveDirection(c, [e])).toBe('up')
    expect(effectiveDirection(c, [])).toBe('flat')
  })

  it('涨跌幅看输入价，输入价为 0 时看输出价', () => {
    expect(entryPercent(entry({ old: { input_cost_per_token: 1e-6 }, new: { input_cost_per_token: 1.5e-6 } }))).toBeCloseTo(50)
    expect(entryPercent(entry({ old: { input_cost_per_token: 0, output_cost_per_token: 2e-6 }, new: { input_cost_per_token: 0, output_cost_per_token: 1e-6 } }))).toBeCloseTo(-50)
    expect(entryPercent(entry({ old: {}, new: {} }))).toBeNull()
  })

  it('统计输入输出之外的变化字段数', () => {
    expect(otherChangedFieldCount(entry({ changed_fields: ['input_cost_per_token', 'cache_read_input_token_cost', 'mode'] }))).toBe(2)
  })

  it('按类型、涨价和模型名筛选', () => {
    const list = [
      entry({ model_key: 'a', change_type: 'added' }),
      entry({ model_key: 'b', change_type: 'removed' }),
      entry({ model_key: 'GPT-up', old: { input_cost_per_token: 1e-6 }, new: { input_cost_per_token: 2e-6 } }),
      entry({ model_key: 'c-down', old: { input_cost_per_token: 2e-6 }, new: { input_cost_per_token: 1e-6 } })
    ]
    expect(filterEntries(list, 'added', '').map((e) => e.model_key)).toEqual(['a'])
    expect(filterEntries(list, 'up', '').map((e) => e.model_key)).toEqual(['GPT-up'])
    expect(filterEntries(list, 'all', 'gpt').map((e) => e.model_key)).toEqual(['GPT-up'])
    expect(filterEntries(list, 'changed', '')).toHaveLength(2)
  })
})
