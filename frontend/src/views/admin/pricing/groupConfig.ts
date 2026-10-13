/**
 * 分组设置抽屉的纯逻辑：读出当前配置、和编辑中的草稿比较、生成写入请求。
 */
import type { GroupConfigRequest, GroupConfigState, GroupDeriveView, MappingEntry } from '@/api/admin/pricing'

export interface ConfigDraft {
  access: GroupConfigState['access_mode']
  billingSource: GroupConfigState['billing_model_source']
  cost: GroupConfigState['cost_mode']
  mapping: MappingEntry[]
  features: Record<string, unknown>
}

const DEFAULT_CONFIG: GroupConfigState = {
  access_mode: 'open',
  billing_model_source: null,
  model_mapping: [],
  features: {},
  cost_mode: 'account_rate'
}

/** 库里的分组配置；没有时用渠道推出的配置，再没有就是默认值。 */
export function currentConfig(view: GroupDeriveView | undefined): GroupConfigState {
  const stored = view?.stored_config
  const derived = view?.derived?.config as Partial<GroupConfigState> | undefined
  const src = { ...DEFAULT_CONFIG, ...(derived ?? {}), ...(stored ?? {}) }
  return {
    access_mode: src.access_mode,
    billing_model_source: src.billing_model_source ?? null,
    model_mapping: (src.model_mapping ?? []).map((m) => ({ src: m.src, dst: m.dst })),
    features: { ...(src.features ?? {}) },
    cost_mode: src.cost_mode
  }
}

export function toDraft(config: GroupConfigState): ConfigDraft {
  return {
    access: config.access_mode,
    billingSource: config.billing_model_source,
    cost: config.cost_mode,
    mapping: config.model_mapping.map((m) => ({ ...m })),
    features: { ...config.features }
  }
}

/** 映射里两列都填了的行才算数；半填的行不能保存。 */
export function cleanMapping(rows: MappingEntry[]): MappingEntry[] {
  return rows.map((m) => ({ src: m.src.trim(), dst: m.dst.trim() })).filter((m) => m.src !== '' || m.dst !== '')
}

export function mappingInvalid(rows: MappingEntry[]): boolean {
  return cleanMapping(rows).some((m) => m.src === '' || m.dst === '')
}

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)

/** 草稿相对当前配置改了哪些项；没改的项不进请求。 */
export function buildConfigRequest(current: GroupConfigState, draft: ConfigDraft, baseline: number): GroupConfigRequest {
  const req: GroupConfigRequest = { baseline_revision: baseline }
  if (draft.access !== current.access_mode) req.access_mode = draft.access
  if (draft.billingSource !== current.billing_model_source) {
    if (draft.billingSource === null) req.clear_billing_model_source = true
    else req.billing_model_source = draft.billingSource
  }
  const mapping = cleanMapping(draft.mapping)
  if (!same(mapping, current.model_mapping)) req.model_mapping = mapping
  if (!same(draft.features, current.features)) req.features = draft.features
  if (draft.cost !== current.cost_mode) req.cost_mode = draft.cost
  return req
}

export function hasChanges(req: GroupConfigRequest): boolean {
  return Object.keys(req).some((k) => k !== 'baseline_revision')
}

/** 功能开关的显示名：沿用渠道页的叫法；没见过的开关直接显示它的名字。 */
export const FEATURE_LABEL_KEY: Record<string, string> = {
  web_search_emulation: 'admin.channels.form.webSearchEmulation',
  codex_image_generation_bridge: 'admin.channels.form.codexImageGenerationBridge',
  bedrock_cc_compat: 'admin.channels.form.bedrockCCCompat'
}

/** 值是布尔的开关可以在这里改；按平台的表等其他形状只读，保持原样写回。 */
export function isToggleFeature(value: unknown): value is boolean {
  return typeof value === 'boolean'
}
