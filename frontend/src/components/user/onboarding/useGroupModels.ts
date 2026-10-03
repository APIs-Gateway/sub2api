import { computed, ref } from 'vue'
import { userChannelsAPI } from '@/api/channels'
import type { UserAvailableChannel } from '@/api/channels'

/**
 * 当前密钥所在分组的可用模型（「交给 AI」的详细版和 CC Switch 的模型下拉用）。
 *
 * - load()：第一次调用时请求一次可用渠道列表，之后不再请求（失败也不重试，模型列表只是锦上添花）；
 * - loading：请求还在路上时为 true，页签据此显示「加载中」、禁用依赖模型的按钮；
 * - models：该分组下所有渠道里的模型名，去重，保持渠道里的顺序；没有分组或还没加载完时为空数组。
 */
export function useGroupModels(groupId: () => number | null | undefined) {
  const channels = ref<UserAvailableChannel[] | null>(null)
  const loading = ref(false)

  async function load() {
    if (channels.value || loading.value) return
    loading.value = true
    try {
      channels.value = await userChannelsAPI.getAvailable()
    } catch {
      channels.value = []
    } finally {
      loading.value = false
    }
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

  return { models, loading, load }
}
