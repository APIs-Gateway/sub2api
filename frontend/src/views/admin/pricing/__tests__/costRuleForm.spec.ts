import { describe, expect, it } from 'vitest'
import type { StoredCostRule } from '@/api/admin/pricingOps'
import { buildRuleBody, emptyForm, fromRule, isRuleEditable, parseIdList, parseModels } from '../costRuleForm'

describe('costRuleForm', () => {
  it('新建时默认只命中当前分组', () => {
    expect(emptyForm(7).groupIdsText).toBe('7')
  })

  it('解析 ID 列表：去重，非正整数报错，空表示不限', () => {
    expect(parseIdList('1, 2，2 3')).toEqual({ ids: [1, 2, 3] })
    expect(parseIdList('')).toEqual({ ids: [] })
    expect(parseIdList('1, x').error).toBe(true)
    expect(parseIdList('0').error).toBe(true)
    expect(parseIdList('-3').error).toBe(true)
  })

  it('解析模型名：换行或逗号分隔，去空去重', () => {
    expect(parseModels('a\n b ,a,\n')).toEqual(['a', 'b'])
  })

  it('按 Token 的规则生成请求体：界面按每百万 Token 输入，提交按每 Token，空价格为 null', () => {
    const f = emptyForm(7)
    f.name = '  官方成本 '
    f.accountIdsText = '21 22'
    f.rows[0].modelsText = 'gpt-5.5\ngpt-5.5-mini'
    f.rows[0].input = '1.25'
    f.rows[0].output = '10'
    const r = buildRuleBody(f)
    expect(r.ok).toBe(true)
    if (!r.ok) return
    expect(r.body).toEqual({
      name: '官方成本',
      group_ids: [7],
      account_ids: [21, 22],
      sort_order: 0,
      enabled: true,
      prices: [
        { platform: '', models: ['gpt-5.5', 'gpt-5.5-mini'], price: { billing_mode: 'token', input_price: 1.25e-6, output_price: 1e-5, cache_write_price: null, cache_read_price: null } }
      ]
    })
  })

  it('读回来时把每 Token 换成每百万 Token，再提交得到同一个值', () => {
    const rule: StoredCostRule = {
      id: 1, scope_group_id: 7, source: 'manual', name: 'r', group_ids: [], account_ids: [], sort_order: 0, enabled: true,
      prices: [{ platform: '', models: ['m'], price: { billing_mode: 'token', input_price: 1.25e-6, output_price: 5e-8, cache_read_price: 1.25e-7 } }]
    }
    const form = fromRule(rule)
    expect(form.rows[0].input).toBe('1.25')
    expect(form.rows[0].output).toBe('0.05')
    expect(form.rows[0].cacheRead).toBe('0.125')
    const r = buildRuleBody(form)
    expect(r.ok && r.body.prices[0].price).toMatchObject({ input_price: 1.25e-6, output_price: 5e-8, cache_read_price: 1.25e-7 })
  })

  it('按次价格不换算；图片输出价按每百万 Token 换算', () => {
    const f = emptyForm(7)
    f.name = 'img'
    f.rows[0].modelsText = 'img-1'
    f.rows[0].mode = 'image'
    f.rows[0].perRequest = '0.04'
    f.rows[0].imageOutput = '40'
    const r = buildRuleBody(f)
    expect(r.ok && r.body.prices[0].price).toEqual({ billing_mode: 'image', per_request_price: 0.04, image_output_price: 4e-5 })
  })

  it('校验：名称、模型、负价格、按次价格', () => {
    const base = () => {
      const f = emptyForm(7)
      f.name = 'r'
      f.rows[0].modelsText = 'm'
      return f
    }
    const name = base()
    name.name = ' '
    expect(buildRuleBody(name)).toMatchObject({ ok: false, error: { key: 'name' } })

    const noModel = base()
    noModel.rows[0].modelsText = ''
    expect(buildRuleBody(noModel)).toMatchObject({ ok: false, error: { key: 'models' } })

    const neg = base()
    neg.rows[0].input = '-1'
    expect(buildRuleBody(neg)).toMatchObject({ ok: false, error: { key: 'price' } })

    const perReq = base()
    perReq.rows[0].mode = 'per_request'
    expect(buildRuleBody(perReq)).toMatchObject({ ok: false, error: { key: 'perRequest' } })
    perReq.rows[0].perRequest = '0.04'
    const ok = buildRuleBody(perReq)
    expect(ok.ok && ok.body.prices[0].price).toEqual({ billing_mode: 'per_request', per_request_price: 0.04 })
  })

  it('编辑时区间价格原样带回，不在界面里改', () => {
    const rule: StoredCostRule = {
      id: 41, scope_group_id: 7, source: 'manual', name: 'r', group_ids: [7], account_ids: [], sort_order: 2, enabled: false,
      prices: [{ platform: 'openai', models: ['m'], price: { billing_mode: 'per_request', intervals: [{ min_tokens: 0, per_request_price: 1 }] } }]
    }
    const form = fromRule(rule)
    const r = buildRuleBody(form)
    expect(r.ok && r.body.prices[0].price.intervals).toEqual([{ min_tokens: 0, per_request_price: 1 }])
    expect(r.ok && r.body.enabled).toBe(false)
  })

  it('渠道派生的规则只读', () => {
    const rule = { source: 'legacy_derived' } as StoredCostRule
    expect(isRuleEditable(rule)).toBe(false)
    expect(isRuleEditable({ source: 'manual' } as StoredCostRule)).toBe(true)
    expect(isRuleEditable({ source: 'legacy_frozen' } as StoredCostRule)).toBe(true)
  })
})

describe('number 输入框', () => {
  it('v-model 给出的 number 也能解析（type=number 的输入框）', () => {
    const f = emptyForm(7)
    f.name = 'n'
    f.rows[0].modelsText = 'm'
    ;(f.rows[0] as unknown as Record<string, unknown>).input = 1.25
    ;(f as unknown as Record<string, unknown>).sortOrder = 3
    const r = buildRuleBody(f)
    expect(r.ok && r.body.sort_order).toBe(3)
    expect(r.ok && r.body.prices[0].price.input_price).toBe(1.25e-6)
  })
})

describe('价格上限', () => {
  const base = () => {
    const f = emptyForm(7)
    f.name = 'r'
    f.rows[0].modelsText = 'm'
    return f
  }

  it('按 Token 的价格不能高于每百万 1 万美元', () => {
    const ok = base()
    ok.rows[0].input = '10000'
    expect(buildRuleBody(ok).ok).toBe(true)
    const bad = base()
    bad.rows[0].output = '10000.01'
    expect(buildRuleBody(bad)).toMatchObject({ ok: false, error: { key: 'priceTooHigh' } })
  })

  it('按次价格不能高于 1000 美元', () => {
    const bad = base()
    bad.rows[0].mode = 'per_request'
    bad.rows[0].perRequest = '1000.5'
    expect(buildRuleBody(bad)).toMatchObject({ ok: false, error: { key: 'perRequestTooHigh' } })
    bad.rows[0].perRequest = '1000'
    expect(buildRuleBody(bad).ok).toBe(true)
  })
})
