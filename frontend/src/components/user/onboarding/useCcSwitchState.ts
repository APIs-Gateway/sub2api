import { computed, ref, watch, type Ref } from 'vue'
import type { CcSwitchClientType } from '@/utils/ccswitchImport'

export type CcsClient = CcSwitchClientType | 'codex'

/**
 * CC Switch 页签里用户填的内容：选哪个客户端、自定义名称、选的模型。
 *
 * 它由弹窗外壳创建、通过 v-model 交给页签，而不是放在页签组件里：页签是按需渲染的，
 * 切到别的页签再切回来时用户填的东西要还在；关闭再打开弹窗时客户端选择保留，
 * 名称和模型清空（和换了分组时一样）。
 */
export function useCcSwitchState(opts: {
  platform: Ref<string | null>
  show: () => boolean
  groupId: () => number | undefined
}) {
  // 该平台的分组在 CC Switch 里能导入成哪些客户端
  const clients = computed<CcsClient[]>(() => {
    switch (opts.platform.value) {
      case 'openai':
        return ['codex']
      case 'gemini':
        return ['gemini']
      case 'antigravity':
        return ['claude', 'gemini']
      default:
        return ['claude']
    }
  })

  const client = ref<CcsClient>('claude')
  watch(
    clients,
    (list) => {
      if (!list.includes(client.value)) client.value = list[0]
    },
    { immediate: true }
  )

  const customName = ref('')
  const model = ref('')
  watch([opts.show, opts.groupId], () => {
    customName.value = ''
    model.value = ''
  })

  return { clients, client, customName, model }
}
