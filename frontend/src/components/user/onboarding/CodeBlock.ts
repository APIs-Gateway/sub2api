import { defineComponent, h } from 'vue'

/** 带复制按钮的代码块：手动配置页签里的配置片段用。 */
export const CodeBlock = defineComponent({
  name: 'OnboardingCodeBlock',
  props: {
    label: { type: String, default: '' },
    /** 标签是文件路径时用等宽字体，普通文字用正常字体 */
    monoLabel: { type: Boolean, default: false },
    code: { type: String, default: '' },
    copied: { type: Boolean, default: false },
    copyLabel: { type: String, default: '' },
    copiedLabel: { type: String, default: '' }
  },
  emits: ['copy'],
  setup(p, { emit }) {
    return () =>
      h('div', { class: 'overflow-hidden rounded-lg border border-gray-200 dark:border-dark-700' }, [
        h('div', { class: 'flex items-center justify-between border-b border-gray-100 px-3 py-1.5 dark:border-dark-800' }, [
          h('span', { class: [p.monoLabel ? 'font-mono' : '', 'text-xs text-gray-500 dark:text-dark-400'] }, p.label),
          h('button', { type: 'button', class: 'copy-btn', onClick: () => emit('copy') }, [
            h('span', { class: 'text-xs' }, p.copied ? p.copiedLabel : p.copyLabel)
          ])
        ]),
        h('pre', { class: 'overflow-x-auto bg-gray-900 px-4 py-3 text-[13px] leading-relaxed text-gray-100 dark:bg-dark-950' }, [
          h('code', { class: 'font-mono' }, p.code)
        ])
      ])
  }
})
