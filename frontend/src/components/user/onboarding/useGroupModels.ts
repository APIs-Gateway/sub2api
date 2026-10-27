import { computed, ref } from 'vue'
import { userChannelsAPI } from '@/api/channels'
import type { UserAvailableChannel } from '@/api/channels'

/**
 * 当前密钥所在分组的可用模型（「交给 AI」的详细版和 CC Switch 的模型下拉用）。
 *
 * - load()：第一次调用时请求一次可用渠道列表；失败后 loadFailed 为 true，可以再次调用 load() 重试；
 * - retry()：清除失败状态，重新请求（供「重试」按钮绑定）；
 * - loading：请求还在路上时为 true，页签据此显示「加载中」、禁用依赖模型的按钮；
 * - loadFailed：请求失败时为 true，页签据此显示重试入口；
 * - models：该分组下所有渠道里的模型名，去重，保持渠道里的顺序；没有分组或还没加载完时为空数组。
 */
export function useGroupModels(groupId: () => number | null | undefined) {
  const channels = ref<UserAvailableChannel[] | null>(null)
  const loading = ref(false)
  const loadFailed = ref(false)

  async function load() {
    if ((channels.value && !loadFailed.value) || loading.value) return
    loading.value = true
    loadFailed.value = false
    try {
      channels.value = await userChannelsAPI.getAvailable()
    } catch {
      channels.value = null
      loadFailed.value = true
    } finally {
      loading.value = false
    }
  }

  function retry() {
    channels.value = null
    loadFailed.value = false
    void load()
  }

  const models = computed<string[]>(() => {
    const id = groupId()
    if (!id || !channels.value) return []
    const names = new Set<string>()
    for (const channel of channels.value) {
      for (const section of channel.platforms || []) {
        if (!(section.groups || []).some((g) => g.id === id)) continue
        for (const m of section.supported_models || []) {
          if (m?.name) names.add(m.name)
        }
      }
    }
    return [...names]
  })

  return { models, loading, loadFailed, load, retry }
}
