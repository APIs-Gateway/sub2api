import { defineComponent, h } from 'vue'

/** 脚本预览（只读，可选中）：一键安装页签里「查看脚本」折叠区用。 */
export const ScriptBlock = defineComponent({
  name: 'OnboardingScriptBlock',
  props: {
    label: { type: String, default: '' },
    code: { type: String, default: '' }
  },
  setup(p) {
    return () =>
      h('div', { class: 'overflow-hidden rounded-lg border border-gray-200 dark:border-dark-700' }, [
        h('div', { class: 'border-b border-gray-100 px-3 py-1.5 text-xs text-gray-500 dark:border-dark-800 dark:text-dark-400' }, p.label),
        h('pre', { class: 'max-h-64 overflow-auto bg-gray-900 px-4 py-3 text-[12px] leading-relaxed text-gray-100 dark:bg-dark-950' }, [
          h('code', { class: 'font-mono' }, p.code)
        ])
      ])
  }
})
