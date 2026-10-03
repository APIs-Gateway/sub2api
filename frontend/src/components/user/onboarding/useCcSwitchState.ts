import { computed, ref, watch, type Ref } from 'vue'
import type { CcSwitchClientType } from '@/utils/ccswitchImport'

export type CcsClient = CcSwitchClientType | 'codex'

/**
 * CC Switch 页签里用户填的内容，合成一个对象，外壳用一个 v-model:form 交给页签。
 * 以后页签要加字段（比如 Claude 的三档模型），只在这里和页签里加，外壳不用动。
 *
 * 更新时整个对象换成新的（页签 emit update:form），不在原对象上改字段。
 */
export interface CcSwitchForm {
  /** 选中的客户端 */
  client: CcsClient
  /** 自定义名称，留空时用默认名称 */
  name: string
  /** 选的模型，留空表示用默认 */
  model: string
}

/**
 * CC Switch 页签的状态：能选哪些客户端，以及用户填的表单。
 *
 * 它由弹窗外壳创建、通过 v-model:form 交给页签，而不是放在页签组件里：页签是按需渲染的，
 * 切到别的页签再切回来时用户填的东西要还在；关闭再打开弹窗时客户端选择保留，
 * 名称和模型清空（和换了分组时一样）。
 */
export function useCcSwitchState(opts: {
  platform: Ref<string | null>
  show: () => boolean
  groupId: () => number | undefined
  /** 分组是否开了 /v1/messages 调度。openai 分组开了之后也能导入成 Claude；现在还没有用到，留给后面的改动。 */
  allowMessagesDispatch?: () => boolean | undefined
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

  const form = ref<CcSwitchForm>({ client: 'claude', name: '', model: '' })
  watch(
    clients,
    (list) => {
      if (!list.includes(form.value.client)) form.value = { ...form.value, client: list[0] }
    },
    { immediate: true }
  )

  watch([opts.show, opts.groupId], () => {
    form.value = { ...form.value, name: '', model: '' }
  })

  return { clients, form }
}
