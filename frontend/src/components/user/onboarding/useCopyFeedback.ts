import { onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'

/** 复制成功后「已复制」的显示时长（毫秒） */
const COPIED_RESET_MS = 1800

async function writeClipboard(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    /* 落到下面的兜底 */
  }
  try {
    const el = document.createElement('textarea')
    el.value = text
    el.setAttribute('readonly', '')
    el.style.position = 'fixed'
    el.style.opacity = '0'
    document.body.appendChild(el)
    el.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(el)
    return ok
  } catch {
    return false
  }
}

/**
 * 弹窗里所有「复制」共用的剪贴板写入和复制反馈，由弹窗外壳调用一次，页签通过 copy 事件把要复制的内容交上来。
 *
 * - copiedId：最近一次复制成功的按钮 id，到时间自动清空；页签用它决定自己的按钮显示「已复制」，
 *   外壳用它向读屏软件播报；
 * - copy(text, id)：写入剪贴板；失败时弹出错误提示，不改变 copiedId；
 * - reset()：立即清掉 copiedId（弹窗重新打开时用），不动计时器。
 *
 * 各页签的按钮 id 在同一个命名空间里，新增按钮时用带页签前缀的 id，避免重名。
 */
export function useCopyFeedback() {
  const { t } = useI18n()
  const appStore = useAppStore()
  const copiedId = ref<string>('')
  let timer: ReturnType<typeof setTimeout> | null = null

  async function copy(text: string, id: string) {
    if (!(await writeClipboard(text))) {
      appStore.showError(t('common.copyFailed'))
      return
    }
    copiedId.value = id
    if (timer) clearTimeout(timer)
    timer = setTimeout(() => (copiedId.value = ''), COPIED_RESET_MS)
  }

  function reset() {
    copiedId.value = ''
  }

  onBeforeUnmount(() => {
    if (timer) clearTimeout(timer)
  })

  return { copiedId, copy, reset }
}
