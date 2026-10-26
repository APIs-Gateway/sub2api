import { ref, watch, type Ref } from 'vue'
import type { OnboardingClient } from '@/utils/keyOnboarding'
import { defaultManualCodeTab, type ManualCodeTab } from '@/utils/manualSamples'

/**
 * 「手动配置」页签里选中的代码页签。
 *
 * 它由弹窗外壳创建、通过 v-model:code-tab 交给页签，而不是放在页签组件里：页签是按需渲染的，
 * 切到别的页签再切回来、关闭再打开弹窗时，用户选的要还在。
 * 换了分组（平台或调度开关变了）就回到新分组的默认页签（defaultManualCodeTab）；
 * 同类分组之间换密钥，用户选的保留。和「交给 AI」页签选中的工具是同一套做法。
 */
export function useManualCodeTab(opts: {
  platform: Ref<string | null>
  /** 没有这个字段和 false 是一回事，不算换了分组 */
  allowMessagesDispatch: () => boolean | undefined
  clients: Ref<OnboardingClient[]>
}): Ref<ManualCodeTab> {
  const tab = ref<ManualCodeTab>(defaultManualCodeTab(opts.clients.value))
  watch([opts.platform, () => !!opts.allowMessagesDispatch()], () => {
    tab.value = defaultManualCodeTab(opts.clients.value)
  })
  return tab
}
