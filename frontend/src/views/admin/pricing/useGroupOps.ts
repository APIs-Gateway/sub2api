/**
 * 阶段切换页与成本核算页共用的数据：分组列表，加上每个分组的阶段、基线 revision 与成本核算规则。
 * 每个调用方各自持有一份状态；写入成功后用 patchGroup 就地更新，不整页重读。
 */
import { reactive } from 'vue'
import { adminAPI } from '@/api/admin'
import { getGroupOpsView, type GroupOpsView } from '@/api/admin/pricingOps'
import { comparePlatforms } from './pricingModel'

const CONCURRENCY = 4

export interface OpsGroup {
  id: number
  name: string
  platform: string
  /** 读不到价格配置时为 null（已计入 failed） */
  view: GroupOpsView | null
}

export interface GroupOpsState {
  loading: boolean
  loaded: boolean
  error: string | null
  /** 没读到价格配置的分组数 */
  failed: number
  groups: OpsGroup[]
}

async function mapLimit<T, R>(items: T[], limit: number, fn: (item: T) => Promise<R>): Promise<R[]> {
  const out: R[] = new Array(items.length)
  let next = 0
  async function worker() {
    while (next < items.length) {
      const i = next++
      out[i] = await fn(items[i])
    }
  }
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker))
  return out
}

export function useGroupOps() {
  const state = reactive<GroupOpsState>({ loading: false, loaded: false, error: null, failed: 0, groups: [] })

  async function load() {
    state.loading = true
    state.error = null
    try {
      const list = await adminAPI.groups.getAll()
      let failed = 0
      const views = await mapLimit(list, CONCURRENCY, async (g) => {
        try {
          return await getGroupOpsView(g.id)
        } catch {
          failed++
          return null
        }
      })
      state.groups = list
        .map((g, i) => ({ id: g.id, name: g.name, platform: g.platform as string, view: views[i] }))
        .sort((a, b) => comparePlatforms(a.platform, b.platform) || a.name.localeCompare(b.name))
      state.failed = failed
      state.loaded = true
    } catch (err) {
      state.error = err && typeof err === 'object' && 'message' in err ? String((err as { message: unknown }).message) : String(err)
    } finally {
      state.loading = false
    }
  }

  /** 重读一个分组（写入之后以服务端为准）。 */
  async function reloadGroup(id: number) {
    const g = state.groups.find((x) => x.id === id)
    if (!g) return
    try {
      g.view = await getGroupOpsView(id)
    } catch {
      g.view = null
    }
  }

  return { state, load, reloadGroup }
}
