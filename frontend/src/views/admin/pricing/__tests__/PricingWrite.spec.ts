import { describe, expect, it } from 'vitest'
import type { StoredCell } from '@/api/admin/pricing'
import { errorInfo, parseIssues, toWriteError } from '../pricingErrors'
import { buildGroup } from '../pricingModel'
import {
  buildRequest,
  describeChange,
  opClear,
  opClose,
  opCustom,
  opExtra,
  opOpen,
  parseExtra,
  parsePrice,
  reconcileWithServer,
  specToView
} from '../pricingWrite'
import type { CellView } from '../pricingModel'
import { derive, derives } from './fixtures'

const openGroup = buildGroup({ id: 3, name: 'kiro cc', platform: 'anthropic', rate_multiplier: 2 }, derives[3])
const listGroup = { ...openGroup, id: 9, accessMode: 'allowlist' as const }
const stored = (over: Partial<StoredCell> = {}): StoredCell => ({
  group_id: 3,
  model_key: 'm',
  is_pattern: false,
  open: true,
  price_mode: 'inherit',
  revision: 4,
  ...over
})

describe('单元格操作', () => {
  it('关闭：开放分组写显式的 open=false 例外且不带价格设置；白名单分组删除单元格', () => {
    expect(opClose(openGroup, 'm', stored({ price_mode: 'extra', extra_multiplier: 1.5 }))).toEqual({
      group_id: 3,
      model_key: 'm',
      kind: 'upsert',
      open: false,
      price_mode: 'inherit',
      baseline_revision: 4
    })
    expect(opClose(listGroup, 'm', stored({ group_id: 9 }))).toEqual({ group_id: 9, model_key: 'm', kind: 'delete', baseline_revision: 4 })
    expect(opClose(listGroup, 'm', null)).toBeNull()
    expect(opClose(openGroup, 'm', stored({ open: false }))).toBeNull()
  })

  it('开放：已经开放的不动；开放分组里没有单元格就是默认开放，白名单里没有就是关闭', () => {
    expect(opOpen(openGroup, 'm', null)).toBeNull()
    expect(opOpen(listGroup, 'm', null)).toMatchObject({ kind: 'upsert', open: true, price_mode: 'inherit', baseline_revision: 0 })
    expect(opOpen(openGroup, 'm', stored({ open: false }))).toMatchObject({ open: true, price_mode: 'inherit', baseline_revision: 4 })
  })

  it('额外倍率：只改开放的格子；填 1 按继承写；关闭的格子不会带倍率', () => {
    expect(opExtra(openGroup, 'm', stored(), 1.5)).toMatchObject({ open: true, price_mode: 'extra', extra_multiplier: 1.5 })
    expect(opExtra(openGroup, 'm', stored(), 1)).toMatchObject({ open: true, price_mode: 'inherit' })
    expect(opExtra(openGroup, 'm', stored(), 1)).not.toHaveProperty('extra_multiplier')
    expect(opExtra(openGroup, 'm', stored({ open: false }), 1.5)).toBeNull()
    expect(opExtra(listGroup, 'm', null, 1.5)).toBeNull()
  })

  it('自定义价与清除覆盖', () => {
    const price = { billing_mode: 'token' as const, input_price: 1, output_price: null }
    // 界面按每百万 Token 填，接口按每 Token 存
    expect(opCustom(openGroup, 'm', stored(), { ...price, input_price: 1.25, output_price: 10 })).toMatchObject({
      price_mode: 'custom',
      custom_price: { billing_mode: 'token', input_price: 0.00000125, output_price: 0.00001, cache_write_price: null }
    })
    expect(opCustom(openGroup, 'm', stored(), price)).toMatchObject({ custom_price: { input_price: 0.000001, output_price: null } })
    expect(opClear(openGroup, 'm', null)).toBeNull()
    expect(opClear(openGroup, 'm', stored({ price_mode: 'extra', extra_multiplier: 2 }))).toMatchObject({ kind: 'delete' })
    expect(opClear(listGroup, 'm', stored({ price_mode: 'extra', extra_multiplier: 2 }))).toMatchObject({ kind: 'upsert', open: true, price_mode: 'inherit' })
  })

  it('请求里的 group_revisions 正好覆盖涉及的分组', () => {
    const ops = [opClose(openGroup, 'a', stored())!, opClose(openGroup, 'b', stored())!]
    expect(buildRequest(ops, [derives[3]])).toEqual({ ops, group_revisions: { '3': 7 } })
  })

  it('输入解析', () => {
    expect(parseExtra('')).toBe('none')
    expect(parseExtra('1.2')).toBe(1.2)
    expect(parseExtra('0')).toBeNull()
    expect(parseExtra('abc')).toBeNull()
    expect(parsePrice('')).toBeUndefined()
    expect(parsePrice('-1')).toBeNull()
    expect(parsePrice('0')).toBe(0)
  })
})

describe('预览里的格子', () => {
  const official = { model: 'm', priced: true, source: 'litellm' as const, per_mtok: { input: 5, output: 30 } }
  const base = { model_key: 'm', is_pattern: false, pattern_order: 0, source: 'manual' }

  it('官方价 × 分组倍率 × 额外倍率；关闭没有价格；没有官方价是未定价', () => {
    const extra = specToView({ ...base, open: true, price_mode: 'extra', extra_multiplier: 1.5 }, openGroup, official)
    expect(extra).toMatchObject({ kind: 'extra', extra: 1.5, usd: { input: 15, output: 90 } })
    expect(specToView({ ...base, open: false, price_mode: 'inherit' }, openGroup, official)).toMatchObject({ kind: 'closed', usd: null })
    expect(specToView({ ...base, open: true, price_mode: 'inherit' }, openGroup, undefined).kind).toBe('unpriced')
    // 自定义价没填的项回落官方价，仍然乘分组倍率
    const custom = specToView({ ...base, open: true, price_mode: 'custom', custom_price: { billing_mode: 'token', input_price: 0.000001 } }, openGroup, official)
    expect(custom).toMatchObject({ kind: 'custom', usd: { input: 2, output: 60 } })
    // 单元格不存在：开放分组是开放，白名单分组是关闭
    expect(specToView(null, openGroup, official).kind).toBe('open')
    expect(specToView(null, listGroup, official).kind).toBe('closed')
  })

  it('改动性质：涨价、降价、开放、关闭', () => {
    const open = specToView(null, openGroup, official)
    const up = specToView({ ...base, open: true, price_mode: 'extra', extra_multiplier: 2 }, openGroup, official)
    const down = specToView({ ...base, open: true, price_mode: 'extra', extra_multiplier: 0.5 }, openGroup, official)
    const closed = specToView({ ...base, open: false, price_mode: 'inherit' }, openGroup, official)
    expect(describeChange(open, up)).toBe('up')
    expect(describeChange(open, down)).toBe('down')
    expect(describeChange(open, closed)).toBe('close')
    expect(describeChange(closed, open)).toBe('open')
    expect(describeChange(open, open)).toBe('same')
  })
})

describe('准入模式以库里为准（派生值不一致）', () => {
  const g = { id: 3, name: 'kiro cc', platform: 'anthropic', rate_multiplier: 2 }
  // 派生按渠道推出「开放」，库里实际是白名单；反过来同理
  const listInDb = buildGroup(g, derive(3, 'anthropic', 'v2', 'open', ['m'], 'allowlist'))
  const openInDb = buildGroup(g, derive(3, 'anthropic', 'v2', 'allowlist', ['m'], 'open'))

  it('库里是白名单：清除覆盖保留开放、价格设回继承，不会变成删除（删除就是关闭）；没有单元格的格子是关闭', () => {
    expect(listInDb.accessMode).toBe('allowlist')
    const op = opClear(listInDb, 'm', stored({ price_mode: 'extra', extra_multiplier: 2 }))
    expect(op).toMatchObject({ kind: 'upsert', open: true, price_mode: 'inherit' })
    expect(specToView(null, listInDb, undefined).kind).toBe('closed')
    // 没有单元格时点额外倍率：格子本来是关闭的，不能被开放
    expect(opExtra(listInDb, 'm', null, 1.5)).toBeNull()
    // 白名单关闭 = 删除单元格
    expect(opClose(listInDb, 'm', stored())).toMatchObject({ kind: 'delete' })
  })

  it('库里是开放：关闭写显式 open=false；清除覆盖是删除单元格', () => {
    expect(openInDb.accessMode).toBe('open')
    expect(opClose(openInDb, 'm', stored())).toMatchObject({ kind: 'upsert', open: false, price_mode: 'inherit' })
    expect(opClose(openInDb, 'm', null)).toMatchObject({ kind: 'upsert', open: false })
    expect(opClear(openInDb, 'm', stored({ price_mode: 'extra', extra_multiplier: 2 }))).toMatchObject({ kind: 'delete' })
    expect(specToView(null, openInDb, { model: 'm', priced: true, source: 'litellm', per_mtok: { input: 1, output: 2 } }).kind).toBe('open')
  })
})

describe('预览里的涨跌标记', () => {
  const view = (over: Partial<CellView> = {}): CellView => ({
    kind: 'open',
    unswitched: false,
    usd: null,
    perRequestUsd: null,
    perRequestRange: null,
    extra: null,
    reason: null,
    ...over
  })
  const tok = (input: number, output: number, cache?: { cache_write?: number; cache_read?: number }, over: Partial<CellView> = {}) =>
    view({ usd: { input, output, ...cache }, ...over })

  it('输入涨、输出跌：有涨有跌，不会因为两项之和变小就标降价', () => {
    // 官方 $1.25 / $10 → 自定义 $5 / $5：和从 11.25 降到 10，但输入涨到 4 倍
    expect(describeChange(tok(1.25, 10), tok(5, 5, undefined, { kind: 'custom' }))).toBe('mixed')
  })

  it('全涨标涨价、全跌标降价、都没变是无变化；价格没变只是口径变了是调整', () => {
    expect(describeChange(tok(1, 2), tok(2, 4))).toBe('up')
    expect(describeChange(tok(1, 2), tok(1, 3))).toBe('up')
    expect(describeChange(tok(2, 4), tok(1, 2))).toBe('down')
    expect(describeChange(tok(1, 2), tok(1, 2))).toBe('same')
    expect(describeChange(tok(1, 2), tok(1, 2, undefined, { kind: 'extra', extra: 1.5 }))).toBe('adjust')
  })

  it('缓存读写参与比较：输入输出不变、缓存读涨了也是涨价；缓存一边有一边没有无法比较', () => {
    expect(describeChange(tok(1, 2, { cache_read: 0.1 }), tok(1, 2, { cache_read: 0.2 }))).toBe('up')
    expect(describeChange(tok(1, 2, { cache_read: 0.1, cache_write: 1 }), tok(0.5, 1, { cache_read: 0.2, cache_write: 0.5 }))).toBe('mixed')
    expect(describeChange(tok(1, 2, { cache_read: 0.1 }), tok(1, 2))).toBe('server')
  })

  it('按次价逐项比较；Token 与按次之间切换、区间价与单价之间切换以服务端为准', () => {
    expect(describeChange(view({ perRequestUsd: 1.8 }), view({ perRequestUsd: 3 }))).toBe('up')
    expect(describeChange(view({ perRequestUsd: 1.8 }), view({ perRequestUsd: 1 }))).toBe('down')
    expect(describeChange(view({ perRequestRange: { min: 1, max: 3 } }), view({ perRequestRange: { min: 2, max: 2 } }))).toBe('mixed')
    expect(describeChange(view({ perRequestUsd: 1.8, kind: 'custom' }), tok(5, 30))).toBe('server')
    expect(describeChange(view({ perRequestUsd: 1 }), view({ perRequestRange: { min: 1, max: 2 } }))).toBe('server')
    // 改前没有可比的价格
    expect(describeChange(view({ kind: 'unpriced' }), tok(5, 30))).toBe('server')
    expect(describeChange(null, tok(5, 30))).toBe('open')
  })

  it('服务端说涨价或无法确认时，行级的降价不能作为唯一提示', () => {
    expect(reconcileWithServer(['down', 'same'], 'up')).toEqual(['server', 'same'])
    expect(reconcileWithServer(['down'], 'unknown')).toEqual(['server'])
    // 已经有行标了涨价、有涨有跌或以服务端为准，其余行不动
    expect(reconcileWithServer(['down', 'up'], 'up')).toEqual(['down', 'up'])
    expect(reconcileWithServer(['down', 'mixed'], 'unknown')).toEqual(['down', 'mixed'])
    // 服务端说降价或没变化：不改行级结果
    expect(reconcileWithServer(['down'], 'down')).toEqual(['down'])
    expect(reconcileWithServer(['down'], 'none')).toEqual(['down'])
  })
})

describe('错误码', () => {
  it('按 reason 分支，带上 metadata；交互式会话错误给出明确提示键', () => {
    const e = toWriteError({ status: 409, reason: 'PRICE_BASELINE_CHANGED', metadata: { group_id: 7 }, message: 'english' })
    expect(e).toEqual({ status: 409, reason: 'PRICE_BASELINE_CHANGED', metadata: { group_id: '7' } })
    expect(errorInfo(e)).toMatchObject({ key: 'PRICE_BASELINE_CHANGED', action: 'refresh' })
    expect(errorInfo(toWriteError({ status: 403, reason: 'ADMIN_TOKEN_MANAGEMENT_JWT_ONLY' })).key).toBe('ADMIN_TOKEN_MANAGEMENT_JWT_ONLY')
    expect(errorInfo(toWriteError({ status: 409, reason: 'PRICE_WRITE_APPROVAL_EXPIRED' })).action).toBe('repreview')
    expect(errorInfo(toWriteError({ status: 500, reason: 'SOMETHING_NEW' })).key).toBe('UNKNOWN')
    // 非法自定义价返回的是渠道定价的 reason 码，兜底成通用的「内容不合法」
    expect(errorInfo(toWriteError({ status: 400, reason: 'CHANNEL_PRICING_INVALID' })).key).toBe('INVALID')
    expect(errorInfo(toWriteError({ status: 0, message: 'Network error' })).key).toBe('NETWORK')
  })

  it('解析「分组:模型:原因[->映射目标]」列表', () => {
    expect(parseIssues('7:gpt-5.5:unpriced;8:alias-b:mapping_target_unpriced->no-such')).toEqual([
      { groupId: 7, model: 'gpt-5.5', reason: 'unpriced', target: null },
      { groupId: 8, model: 'alias-b', reason: 'mapping_target_unpriced', target: 'no-such' }
    ])
    expect(parseIssues(undefined)).toEqual([])
  })
})
