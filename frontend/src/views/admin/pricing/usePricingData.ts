/**
 * 价格配置页的数据：分组、模型目录、分组的开放方式与阶段、官方参考价、「模型 × 分组」报价。
 * 模块级单例：模型页和矩阵页共用一份，切页不重复请求；点「刷新」才重新取。
 * 全部只读。
 */
import { computed, reactive } from 'vue'
import { adminAPI } from '@/api/admin'
import {
  QUOTE_BATCH_MAX_GROUPS,
  QUOTE_BATCH_MAX_MODELS,
  type GroupDeriveView,
  type ModelCatalogEntry,
  type OfficialReference,
  type QuoteBatchCell
} from '@/api/admin/pricing'
import {
  buildGroup,
  buildModelRows,
  cellKey,
  comparePlatforms,
  type ModelRow,
  type PricingGroup
} from './pricingModel'

const DERIVE_CONCURRENCY = 4
const QUOTE_CONCURRENCY = 3

interface PricingState {
  loading: boolean
  loaded: boolean
  error: string | null
  /** 没读到开放方式的分组数；这些分组按渠道配置、开放方式未知处理 */
  deriveFailed: number
  groups: PricingGroup[]
  catalog: ModelCatalogEntry[]
  derives: GroupDeriveView[]
  refs: Record<string, OfficialReference>
  cells: Record<string, QuoteBatchCell>
}

const state = reactive<PricingState>({
  loading: false,
  loaded: false,
  error: null,
  deriveFailed: 0,
  groups: [],
  catalog: [],
  derives: [],
  refs: {},
  cells: {}
})

let inflight: Promise<void> | null = null

function chunk<T>(items: T[], size: number): T[][] {
  const out: T[][] = []
  for (let i = 0; i < items.length; i += size) out.push(items.slice(i, i + size))
  return out
}

/** 最多 limit 个任务同时在跑，结果按输入顺序返回。 */
async function mapLimit<T, R>(items: T[], limit: number, fn: (item: T) => Promise<R>): Promise<R[]> {
  const results: R[] = new Array(items.length)
  let next = 0
  async function worker() {
    while (next < items.length) {
      const index = next++
      results[index] = await fn(items[index])
    }
  }
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker))
  return results
}

function errorMessage(err: unknown): string {
  if (err && typeof err === 'object' && 'message' in err && typeof (err as { message: unknown }).message === 'string') {
    return (err as { message: string }).message
  }
  return String(err)
}

async function fetchAll(): Promise<void> {
  const [groupList, catalog] = await Promise.all([adminAPI.groups.getAll(), adminAPI.pricing.listModelCatalog()])

  // 分组的阶段和开放方式要逐个读：读不到的分组不挡页面，按渠道配置处理并计数提示。
  let failed = 0
  const derives = await mapLimit(groupList, DERIVE_CONCURRENCY, async (g) => {
    try {
      return await adminAPI.pricing.getGroupDerive(g.id)
    } catch {
      failed++
      return null
    }
  })
  const groups = groupList
    .map((g, i) => buildGroup(g, derives[i]))
    .sort((a, b) => comparePlatforms(a.platform, b.platform) || a.name.localeCompare(b.name))
  const readDerives = derives.filter((d): d is GroupDeriveView => d !== null)
  const rows = buildModelRows(catalog, readDerives)

  // 官方参考价：不带分组，每次最多 60 个模型。
  const modelKeys = [...new Set(rows.map((r) => r.key))]
  const refs: Record<string, OfficialReference> = {}
  const refResults = await mapLimit(chunk(modelKeys, QUOTE_BATCH_MAX_MODELS), QUOTE_CONCURRENCY, (keys) =>
    adminAPI.pricing.quoteBatch([], keys)
  )
  for (const result of refResults) for (const ref of result.models) refs[ref.model] = ref

  // 报价：同平台的模型 × 同平台的分组，按上限分批。
  const jobs: { groupIds: number[]; models: string[] }[] = []
  for (const platform of new Set(groups.map((g) => g.platform))) {
    const groupIds = groups.filter((g) => g.platform === platform).map((g) => g.id)
    const models = rows.filter((r) => r.platform === platform).map((r) => r.key)
    for (const groupChunk of chunk(groupIds, QUOTE_BATCH_MAX_GROUPS)) {
      for (const modelChunk of chunk(models, QUOTE_BATCH_MAX_MODELS)) {
        jobs.push({ groupIds: groupChunk, models: modelChunk })
      }
    }
  }
  const cells: Record<string, QuoteBatchCell> = {}
  const quoteResults = await mapLimit(jobs, QUOTE_CONCURRENCY, (job) =>
    adminAPI.pricing.quoteBatch(job.groupIds, job.models)
  )
  for (const result of quoteResults) for (const cell of result.cells) cells[cellKey(cell.group_id, cell.model)] = cell

  state.groups = groups
  state.catalog = catalog
  state.derives = readDerives
  state.refs = refs
  state.cells = cells
  state.deriveFailed = failed
}

/** 取数据。已经取过且没有要求 force 时直接返回；同时多次调用共用同一次请求。 */
async function load(options: { force?: boolean } = {}): Promise<void> {
  if (inflight) return inflight
  if (state.loaded && !options.force) return
  state.loading = true
  state.error = null
  inflight = fetchAll()
    .then(() => {
      state.loaded = true
    })
    .catch((err) => {
      state.error = errorMessage(err)
    })
    .finally(() => {
      state.loading = false
      inflight = null
    })
  return inflight
}

/** 仅供测试复位。 */
export function resetPricingDataForTest() {
  state.loading = false
  state.loaded = false
  state.error = null
  state.deriveFailed = 0
  state.groups = []
  state.catalog = []
  state.derives = []
  state.refs = {}
  state.cells = {}
  inflight = null
}

export function usePricingData() {
  const rows = computed<ModelRow[]>(() => buildModelRows(state.catalog, state.derives))
  const platforms = computed(() => {
    const set = new Set<string>()
    state.groups.forEach((g) => set.add(g.platform))
    rows.value.forEach((r) => set.add(r.platform))
    return [...set].sort(comparePlatforms)
  })
  /** 是否有分组已经切到新配置（目前都还在用渠道配置）。 */
  const hasSwitchedGroups = computed(() => state.groups.some((g) => g.stage === 'v2'))
  const hasUnswitchedGroups = computed(() => state.groups.some((g) => g.stage !== 'v2'))

  return {
    state,
    rows,
    platforms,
    hasSwitchedGroups,
    hasUnswitchedGroups,
    load,
    refresh: () => load({ force: true })
  }
}
