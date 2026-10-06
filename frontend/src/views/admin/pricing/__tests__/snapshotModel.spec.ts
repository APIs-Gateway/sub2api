import { describe, expect, it } from 'vitest'
import type { SnapshotDiffEntry } from '@/api/admin/pricingOps'
import { entryDirection, entryPercent, entryPrices, filterEntries, otherChangedFieldCount, perMtok } from '../snapshotModel'

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

  it('涨跌方向：输入或输出任一涨价就算涨价', () => {
    const up = entry({ old: { input_cost_per_token: 1e-6, output_cost_per_token: 4e-6 }, new: { input_cost_per_token: 1e-6, output_cost_per_token: 5e-6 } })
    const down = entry({ old: { input_cost_per_token: 2e-6 }, new: { input_cost_per_token: 1e-6 } })
    const mixed = entry({ old: { input_cost_per_token: 2e-6, output_cost_per_token: 1e-6 }, new: { input_cost_per_token: 1e-6, output_cost_per_token: 2e-6 } })
    expect(entryDirection(up)).toBe('up')
    expect(entryDirection(down)).toBe('down')
    expect(entryDirection(mixed)).toBe('up')
    expect(entryDirection(entry({ change_type: 'added' }))).toBeNull()
    expect(entryPrices(up)).toEqual({ oldIn: 1, oldOut: 4, newIn: 1, newOut: 5 })
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
