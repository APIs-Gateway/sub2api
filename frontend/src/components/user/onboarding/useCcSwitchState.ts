import { computed, ref, watch, type Ref } from 'vue'
import { ccSwitchClientsForPlatform, pickCcSwitchModels, type CcSwitchApp } from '@/utils/ccswitchImport'

export type CcsClient = CcSwitchApp

/**
 * CC Switch 页签里用户填的内容，合成一个对象，外壳用一个 v-model:form 交给页签。
 * 以后页签要加字段，只在这里和页签里加，外壳不用动。
 *
 * 更新时整个对象换成新的（页签 emit update:form），不在原对象上改字段。
 * 一次要改好几个字段的地方（切换客户端时同时换掉名称和四个模型、换分组时同时清空名称和重新预选），
 * 必须一次写入一个完整的新对象：父组件接管 v-model 时，同一个 tick 里连着写两次，后一次会盖掉前一次。
 */
export interface CcSwitchForm {
  /** 选中的客户端 */
  client: CcsClient
  /** 自定义名称，留空时用默认名称 */
  name: string
  /** 主模型，留空表示用 CC Switch 的默认模型 */
  model: string
  /** Claude 的 Haiku / Sonnet / Opus 三档；其他客户端没有，一直是空串 */
  haikuModel: string
  sonnetModel: string
  opusModel: string
}

/**
 * CC Switch 页签的状态：能选哪些客户端，以及用户填的表单。
 *
 * 它由弹窗外壳创建、通过 v-model:form 交给页签，而不是放在页签组件里：页签是按需渲染的，
 * 切到别的页签再切回来时用户填的东西要还在；关闭再打开弹窗时客户端选择保留，
 * 名称清空、模型按分组重新预选（和换了分组时一样）。
 *
 * 模型预选：分组里的模型加载完、换了分组或换了客户端时，按 pickCcSwitchModels 的规则预选一遍；
 * 用户自己改过的选择，只会在这几种情况下被重新预选覆盖。
 */
export function useCcSwitchState(opts: {
  platform: Ref<string | null>
  show: () => boolean
  groupId: () => number | undefined
  /** 这把密钥所在分组的可用模型（还没加载完时是空数组） */
  models?: () => string[]
  /** 分组是否开了 /v1/messages 调度。openai 分组开了之后，除 Codex 外也能导入成 Claude。 */
  allowMessagesDispatch?: () => boolean | undefined
}) {
  const models = () => opts.models?.() ?? []

  // 该分组在 CC Switch 里能导入成哪些客户端，第一个是默认选中的（分组的主力客户端）
  const clients = computed<CcsClient[]>(() =>
    ccSwitchClientsForPlatform(opts.platform.value, { allowMessagesDispatch: !!opts.allowMessagesDispatch?.() })
  )

  const form = ref<CcSwitchForm>({ client: clients.value[0], name: '', ...pickCcSwitchModels(clients.value[0], models()) })
  // 换了平台、或 openai 分组的调度开关变了：能选的客户端变了，回到新分组的默认客户端（和「交给 AI」页签一致）。
  // 只看这两个条件，不看 clients 数组本身：同一个分组下重新取到的数据不该改掉用户选的客户端。
  watch(
    [opts.platform, () => !!opts.allowMessagesDispatch?.()],
    () => {
      const client = clients.value[0]
      if (client === form.value.client) return
      form.value = { ...form.value, client, ...pickCcSwitchModels(client, models()) }
    }
  )

  // 关闭再打开、换分组：名称清空，模型重新预选，客户端保留
  watch([opts.show, opts.groupId], () => {
    const client = form.value.client
    form.value = { client, name: '', ...pickCcSwitchModels(client, models()) }
  })

  // 分组里的模型加载完了（或变了）：重新预选，名称不动。
  // 比较内容而不是数组本身：密钥列表重新拉取时，同一个分组的模型会换成内容相同的新数组，不该冲掉用户的选择。
  watch(
    () => models().join('\n'),
    () => {
      form.value = { ...form.value, ...pickCcSwitchModels(form.value.client, models()) }
    }
  )

  return { clients, form }
}
