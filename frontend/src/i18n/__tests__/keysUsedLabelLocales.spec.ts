import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zhCN from '../locales/zh-CN'
import zhHK from '../locales/zh-HK'

// 密钥列表用量列里，「已用」是这把密钥创建以来的累计额度，旁边的「近 30 天」只是最近一个月的消费。
// 两个数字并排时，光写「已用」会被读成近 30 天的已用，所以标签要写明是累计。
describe('密钥列表用量列的「累计已用」标签', () => {
  it('三语都写明累计，且与「近 30 天」是两个不同的说法', () => {
    expect(zhCN.keys.usedLabel).toBe('累计已用')
    expect(zhHK.keys.usedLabel).toBe('累計已用')
    expect(en.keys.usedLabel).toBe('Total used')

    expect(zhCN.keys.total).toBe('近30天')
    expect(zhHK.keys.total).toBe('近30天')
    expect(en.keys.total).toBe('Last 30d')
  })

  it('沿用站内已有的「累计 / Total」说法，没有另造词', () => {
    // 用量查询页已经用「累计费用 / Total Cost」表示创建以来的总和。
    expect(zhCN.keyUsage.totalCost).toBe('累计费用')
    expect(zhHK.keyUsage.totalCost).toBe('累計費用')
    expect(en.keyUsage.totalCost).toBe('Total Cost')
    expect(zhCN.keys.usedLabel.startsWith('累计')).toBe(true)
    expect(zhHK.keys.usedLabel.startsWith('累計')).toBe(true)
    expect(en.keys.usedLabel.startsWith('Total ')).toBe(true)
  })
})
