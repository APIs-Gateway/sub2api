/**
 * 成本核算规则的表单模型：规则对象 <-> 表单文本，以及提交前的校验（限制与契约第 5 节一致）。
 * 校验失败返回 i18n key（admin.pricingOps.costRules.error.*），文案放在语言包里。
 *
 * 价格单位：界面按每百万 Token（美元）输入和显示；后端和渠道定价一样按每 Token 存，
 * 所以读回来用 perTokenToMTok、提交前用 mTokToPerToken 换算。按次价格本来就是每次，不换算。
 * 区间价格不在界面里编辑，保持后端存的每 Token 原值带回去。
 */
import { mTokToPerToken, perTokenToMTok } from '@/components/admin/channel/types'
import type { CostBillingMode, CostPrice, CostRuleBody, CostRulePriceRow, StoredCostRule } from '@/api/admin/pricingOps'

export const NAME_MAX = 100
export const IDS_MAX = 1000
export const ROWS_MAX = 200
export const MODELS_MAX = 200
/** 价格上限（与后端一致，超限后端返回 COST_RULE_INVALID / PRICE_TOO_HIGH）：按 Token 每百万 1 万美元，按次 1000 美元。 */
export const TOKEN_PRICE_MAX_PER_MTOK = 10000
export const REQUEST_PRICE_MAX = 1000

export interface PriceRowForm {
  platform: string
  modelsText: string
  mode: CostBillingMode
  input: string
  output: string
  cacheWrite: string
  cacheRead: string
  imageOutput: string
  perRequest: string
  /** 区间价格不在界面里编辑，原样带回去 */
  intervals: unknown[]
}

export interface RuleForm {
  name: string
  enabled: boolean
  sortOrder: string
  groupIdsText: string
  accountIdsText: string
  rows: PriceRowForm[]
}

export interface FormError {
  key: string
  params?: Record<string, unknown>
}

export function emptyRow(): PriceRowForm {
  return { platform: '', modelsText: '', mode: 'token', input: '', output: '', cacheWrite: '', cacheRead: '', imageOutput: '', perRequest: '', intervals: [] }
}

/** 新建规则：默认只命中当前这个分组，管理员可以改。 */
export function emptyForm(groupId: number): RuleForm {
  return { name: '', enabled: true, sortOrder: '0', groupIdsText: String(groupId), accountIdsText: '', rows: [emptyRow()] }
}

const num = (v: number | null | undefined) => (v === null || v === undefined ? '' : String(v))

export function fromRule(rule: StoredCostRule): RuleForm {
  return {
    name: rule.name,
    enabled: rule.enabled,
    sortOrder: String(rule.sort_order),
    groupIdsText: rule.group_ids.join(', '),
    accountIdsText: rule.account_ids.join(', '),
    rows: rule.prices.length
      ? rule.prices.map((p) => ({
          platform: p.platform,
          modelsText: p.models.join('\n'),
          mode: p.price.billing_mode,
          input: num(perTokenToMTok(p.price.input_price)),
          output: num(perTokenToMTok(p.price.output_price)),
          cacheWrite: num(perTokenToMTok(p.price.cache_write_price)),
          cacheRead: num(perTokenToMTok(p.price.cache_read_price)),
          imageOutput: num(perTokenToMTok(p.price.image_output_price)),
          perRequest: num(p.price.per_request_price),
          intervals: p.price.intervals ?? []
        }))
      : [emptyRow()]
  }
}

export function parseIdList(text: string): { ids: number[]; error?: boolean } {
  const parts = text.split(/[\s,，、]+/).filter(Boolean)
  const ids: number[] = []
  for (const p of parts) {
    if (!/^\d+$/.test(p) || Number(p) <= 0 || !Number.isSafeInteger(Number(p))) return { ids: [], error: true }
    const n = Number(p)
    if (!ids.includes(n)) ids.push(n)
  }
  return { ids }
}

export function parseModels(text: string): string[] {
  const out: string[] = []
  for (const m of text.split(/[\n,，]+/).map((s) => s.trim()).filter(Boolean)) if (!out.includes(m)) out.push(m)
  return out
}

/** 空串是「没填」（null）；填了就必须是不小于 0 的数。 */
function parsePrice(text: string | number | null | undefined): { value: number | null; bad: boolean } {
  // type="number" 的输入框经 v-model 拿到的是 number（空时是空串），统一转成文本再解析
  const s = String(text ?? '').trim()
  if (s === '') return { value: null, bad: false }
  const n = Number(s)
  if (!Number.isFinite(n) || n < 0) return { value: null, bad: true }
  return { value: n, bad: false }
}

export type BuildResult = { ok: true; body: CostRuleBody } | { ok: false; error: FormError }

export function buildRuleBody(form: RuleForm): BuildResult {
  const name = form.name.trim()
  if (!name || [...name].length > NAME_MAX) return { ok: false, error: { key: 'name', params: { max: NAME_MAX } } }

  const groups = parseIdList(form.groupIdsText)
  if (groups.error || groups.ids.length > IDS_MAX) return { ok: false, error: { key: 'groupIds', params: { max: IDS_MAX } } }
  const accounts = parseIdList(form.accountIdsText)
  if (accounts.error || accounts.ids.length > IDS_MAX) return { ok: false, error: { key: 'accountIds', params: { max: IDS_MAX } } }

  const sortText = String(form.sortOrder ?? '').trim()
  const sort = Number(sortText === '' ? 0 : sortText)
  if (!Number.isInteger(sort)) return { ok: false, error: { key: 'sortOrder' } }

  if (form.rows.length < 1 || form.rows.length > ROWS_MAX) return { ok: false, error: { key: 'rows', params: { max: ROWS_MAX } } }

  const prices: CostRulePriceRow[] = []
  for (let i = 0; i < form.rows.length; i++) {
    const r = form.rows[i]
    const n = i + 1
    const models = parseModels(r.modelsText)
    if (models.length < 1 || models.length > MODELS_MAX) return { ok: false, error: { key: 'models', params: { n, max: MODELS_MAX } } }

    const fields = {
      input: parsePrice(r.input),
      output: parsePrice(r.output),
      cacheWrite: parsePrice(r.cacheWrite),
      cacheRead: parsePrice(r.cacheRead),
      imageOutput: parsePrice(r.imageOutput),
      perRequest: parsePrice(r.perRequest)
    }
    if (Object.values(fields).some((f) => f.bad)) return { ok: false, error: { key: 'price', params: { n } } }
    const tokenPrices = [fields.input, fields.output, fields.cacheWrite, fields.cacheRead, fields.imageOutput]
    if (tokenPrices.some((f) => (f.value ?? 0) > TOKEN_PRICE_MAX_PER_MTOK)) {
      return { ok: false, error: { key: 'priceTooHigh', params: { n, max: TOKEN_PRICE_MAX_PER_MTOK } } }
    }
    if ((fields.perRequest.value ?? 0) > REQUEST_PRICE_MAX) {
      return { ok: false, error: { key: 'perRequestTooHigh', params: { n, max: REQUEST_PRICE_MAX } } }
    }
    if (r.mode !== 'token' && fields.perRequest.value === null && r.intervals.length === 0) {
      return { ok: false, error: { key: 'perRequest', params: { n } } }
    }

    const price: CostPrice = { billing_mode: r.mode }
    if (r.mode === 'token') {
      price.input_price = mTokToPerToken(fields.input.value)
      price.output_price = mTokToPerToken(fields.output.value)
      price.cache_write_price = mTokToPerToken(fields.cacheWrite.value)
      price.cache_read_price = mTokToPerToken(fields.cacheRead.value)
    } else {
      price.per_request_price = fields.perRequest.value
      if (r.mode === 'image') price.image_output_price = mTokToPerToken(fields.imageOutput.value)
    }
    if (r.intervals.length > 0) price.intervals = r.intervals
    prices.push({ platform: r.platform, models, price })
  }

  return {
    ok: true,
    body: { name, group_ids: groups.ids, account_ids: accounts.ids, sort_order: sort, enabled: form.enabled, prices }
  }
}

/** 列表里一条规则的价格摘要，换成界面单位：输入、输出是每百万 Token，按次价格是每次。 */
export function priceSummary(price: CostPrice): { in: number | null; out: number | null; perRequest: number | null } {
  return {
    in: perTokenToMTok(price.input_price),
    out: perTokenToMTok(price.output_price),
    perRequest: price.per_request_price ?? null
  }
}

/** 规则是否可由管理员改：渠道派生的只读。 */
export function isRuleEditable(rule: StoredCostRule): boolean {
  return rule.source === 'manual' || rule.source === 'legacy_frozen'
}
