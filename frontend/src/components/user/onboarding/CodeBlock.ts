import { defineComponent, h } from 'vue'

/**
 * 「复制」小按钮的样式。
 * CodeBlock 是渲染函数组件，里面的元素拿不到 SFC 的 scoped 标记，写在 <style scoped> 里的规则对它不生效
 * （之前代码块里的复制按钮就是没有样式的原因），所以这里直接用工具类；ManualTab 里的复制按钮也用同一份，外观保持一致。
 */
export const COPY_BTN_CLASS = [
  'inline-flex shrink-0 items-center rounded-md border border-gray-200 bg-white px-2 py-0.5 text-xs text-gray-600',
  'transition-colors duration-150 hover:bg-gray-100 hover:text-gray-900',
  'focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-500',
  'dark:border-dark-700 dark:bg-dark-800 dark:text-gray-400 dark:hover:bg-dark-700 dark:hover:text-white'
].join(' ')

/** 刚复制成功时按钮的文字颜色（和 COPY_BTN_CLASS 一起用，盖过它的灰色） */
export const COPY_BTN_DONE_CLASS = '!text-primary-700 dark:!text-primary-400'

/** 带复制按钮的代码块：手动配置页签里的代码示例和配置片段用。 */
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
      h('div', { class: 'overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800' }, [
        h('div', { class: 'flex items-center justify-between gap-3 border-b border-gray-100 px-3 py-1.5 dark:border-dark-700' }, [
          h('span', { class: [p.monoLabel ? 'font-mono' : '', 'min-w-0 truncate text-xs text-gray-500 dark:text-dark-400'] }, p.label),
          h('button', { type: 'button', class: [COPY_BTN_CLASS, p.copied ? COPY_BTN_DONE_CLASS : ''], onClick: () => emit('copy') }, [
            h('span', { class: 'text-xs' }, p.copied ? p.copiedLabel : p.copyLabel)
          ])
        ]),
        h('pre', { class: 'overflow-x-auto bg-gray-900 px-4 py-3 text-[13px] leading-relaxed text-gray-100 dark:bg-dark-950' }, [
          h('code', { class: 'font-mono' }, p.code)
        ])
      ])
  }
})
