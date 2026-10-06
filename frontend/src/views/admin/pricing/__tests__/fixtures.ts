import { vi } from 'vitest'
import type {
  GroupDeriveView,
  ModelCatalogEntry,
  OfficialReference,
  QuoteBatchCell,
  QuoteBatchResult
} from '@/api/admin/pricing'

/** 一份接近线上形状的数据：两个 openai 分组（一个白名单）、一个已切换的 anthropic 分组。 */
export const groupList = [
  { id: 1, name: 'codex特惠分组', platform: 'openai', rate_multiplier: 1.4 },
  { id: 2, name: 'codex pro', platform: 'openai', rate_multiplier: 5 },
  { id: 3, name: 'kiro cc', platform: 'anthropic', rate_multiplier: 2 }
]

function entry(id: number, key: string, platform: string, status: ModelCatalogEntry['status'], extra: Partial<ModelCatalogEntry> = {}): ModelCatalogEntry {
  return {
    id,
    model_key: key,
    platform,
    display_name: key,
    aliases: [],
    reference_model: null,
    status,
    note: '',
    created_at: '2026-10-01T00:00:00Z',
    updated_at: '2026-10-01T00:00:00Z',
    ...extra
  }
}

export const catalog: ModelCatalogEntry[] = [
  entry(1, 'gpt-5.5', 'openai', 'active', { aliases: ['gpt5.5'] }),
  entry(2, 'gpt-6.2-sol', 'openai', 'draft', { display_name: 'GPT 6.2 Sol', reference_model: 'gpt-5.5' }),
  entry(3, 'minimax-m3', 'openai', 'active'),
  entry(4, 'gpt-5.4', 'openai', 'retired'),
  entry(5, 'claude-opus-5-5', 'anthropic', 'active')
]

function derive(groupId: number, platform: string, stage: 'legacy' | 'v2' | null, access: 'open' | 'allowlist', models: string[]): GroupDeriveView {
  return {
    group_id: groupId,
    platform,
    deleted: false,
    derived: {
      group_id: groupId,
      channel_id: 7,
      config: { access_mode: access, billing_model_source: null, cost_mode: 'account_rate' },
      cells: [
        ...models.map((m) => ({ model_key: m, is_pattern: false, open: true, price_mode: 'inherit' as const })),
        { model_key: 'gpt-*', is_pattern: true, open: true, price_mode: 'inherit' as const }
      ]
    },
    stored_config: stage ? { pricing_stage: stage, ...(stage === 'v2' ? { revision: 7 } : {}) } : null,
    stored_cells: stage === 'v2' ? models.map((m) => ({ group_id: groupId, model_key: m, is_pattern: false, open: true, price_mode: 'inherit' as const, revision: 4 })) : []
  }
}

export const derives: Record<number, GroupDeriveView> = {
  1: derive(1, 'openai', 'legacy', 'open', ['gpt-5.5', 'qwen3-max', 'gpt5.5']),
  2: derive(2, 'openai', null, 'allowlist', ['gpt-5.5']),
  3: derive(3, 'anthropic', 'v2', 'open', ['claude-opus-5-5'])
}

export const refs: Record<string, OfficialReference> = {
  'gpt-5.5': { model: 'gpt-5.5', priced: true, source: 'litellm', per_mtok: { input: 5, output: 30 } },
  'gpt-6.2-sol': { model: 'gpt-6.2-sol', priced: true, source: 'fallback', per_mtok: { input: 2.4, output: 12 } },
  'minimax-m3': { model: 'minimax-m3', priced: false, source: 'none' },
  'gpt-5.4': { model: 'gpt-5.4', priced: true, source: 'card_policy', per_mtok: { input: 1, output: 4 } },
  'qwen3-max': { model: 'qwen3-max', priced: true, source: 'litellm', per_mtok: { input: 1.2, output: 6 } },
  'claude-opus-5-5': { model: 'claude-opus-5-5', priced: true, source: 'litellm', per_mtok: { input: 15, output: 75 } }
}

function cell(groupId: number, model: string, over: Partial<QuoteBatchCell>): QuoteBatchCell {
  return {
    group_id: groupId,
    model,
    access: { ok: true },
    priced: true,
    source: 'litellm',
    billing_mode: 'token',
    group_multiplier: 1,
    effective_multiplier: 1,
    ...over
  }
}

const closed = (reason: string) => ({ access: { ok: false, reason }, priced: false, source: 'none' as const })

export const quotes: Record<string, QuoteBatchCell> = {
  '1|gpt-5.5': cell(1, 'gpt-5.5', { effective_multiplier: 1.4, final_per_mtok: { input: 7, output: 42 } }),
  '2|gpt-5.5': cell(2, 'gpt-5.5', { extra_multiplier: 1.2, effective_multiplier: 6, final_per_mtok: { input: 30, output: 180 } }),
  '1|gpt-6.2-sol': cell(1, 'gpt-6.2-sol', closed('catalog_draft')),
  '2|gpt-6.2-sol': cell(2, 'gpt-6.2-sol', closed('not_in_allowlist')),
  '1|minimax-m3': cell(1, 'minimax-m3', { priced: false, source: 'none' }),
  '2|minimax-m3': cell(2, 'minimax-m3', { source: 'channel', final_per_mtok: { input: 1, output: 2 } }),
  '1|gpt-5.4': cell(1, 'gpt-5.4', closed('catalog_retired')),
  '2|gpt-5.4': cell(2, 'gpt-5.4', closed('closed_in_group')),
  '1|qwen3-max': cell(1, 'qwen3-max', { final_per_mtok: { input: 1.68, output: 8.4 } }),
  '2|qwen3-max': cell(2, 'qwen3-max', closed('not_in_allowlist')),
  '3|claude-opus-5-5': cell(3, 'claude-opus-5-5', { per_request_price: 0.5, final_per_mtok: undefined })
}

/** 按请求的分组与模型，从上面的表里挑出对应的格子。 */
export function fakeQuoteBatch(groupIds: number[], models: string[]): QuoteBatchResult {
  const cells: QuoteBatchCell[] = []
  for (const g of groupIds) {
    for (const m of models) {
      const hit = quotes[`${g}|${m}`]
      if (hit) cells.push(hit)
      else cells.push({ ...cell(g, m, {}), error: 'GROUP_NOT_FOUND' })
    }
  }
  return {
    models: models.map((m) => refs[m] ?? { model: m, priced: false, source: 'none' as const }),
    cells
  }
}

/** 装一套默认的接口替身，返回各个 mock 方便断言调用。 */
export function installApiMocks(api: {
  groups: { getAll: ReturnType<typeof vi.fn> }
  pricing: {
    listModelCatalog: ReturnType<typeof vi.fn>
    getGroupDerive: ReturnType<typeof vi.fn>
    quoteBatch: ReturnType<typeof vi.fn>
  }
}) {
  api.groups.getAll.mockReset().mockResolvedValue(groupList)
  api.pricing.listModelCatalog.mockReset().mockResolvedValue(catalog)
  api.pricing.getGroupDerive.mockReset().mockImplementation(async (id: number) => derives[id])
  api.pricing.quoteBatch.mockReset().mockImplementation(async (g: number[], m: string[]) => fakeQuoteBatch(g, m))
}
