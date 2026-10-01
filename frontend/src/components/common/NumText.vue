<script lang="ts">
/**
 * 数字展示：把「符号 + 数字 + 单位」的展示串拆开，给货币符号和单位单独上样式。
 *
 *   <NumText :text="formatWallet(balance)" tier="primary" />   → ¥ 17,717.09
 *   <NumText :text="formatTokens(n)" tier="primary" />        → 227.8 M
 *   <NumText :text="formatDuration(ms)" tier="primary" />     → 25.70 s
 *
 * 字体、字号、字重、颜色由 style.css 的 .num-primary / .num-secondary / .num-aux 决定；
 * 符号和单位同族字体、字号略小、颜色略浅（.num-unit），与数字的间距各档一致。
 * 展示串不是「符号 + 数字 + 单位」的形状（如 `-`、`∞`、区间）时原样渲染。
 *
 * 只负责排印，不负责格式化：格式化规则在 utils/numberFormat 与 useCurrencyDisplay 里，
 * 保证 title / 复制出来的文字就是展示串本身。
 */
import { defineComponent, h, type PropType, type VNodeChild } from 'vue'
import { splitNumeric } from '@/utils/numberFormat'

export type NumTier = 'primary' | 'secondary' | 'aux'

export default defineComponent({
  name: 'NumText',
  props: {
    /** 已格式化好的展示串。 */
    text: { type: [String, Number] as PropType<string | number>, required: true },
    /** 层级；缺省时只套数字字体，字号和颜色继承父级。 */
    tier: { type: String as PropType<NumTier | undefined>, default: undefined }
  },
  setup(props) {
    /** 一段「符号 + 数字 + 单位」渲染成带单位样式的节点；不是这个形状就原样返回文字。 */
    function renderPart(raw: string): VNodeChild[] {
      const parts = splitNumeric(raw)
      if (!parts) return [raw]
      const nodes: VNodeChild[] = []
      if (parts.sign) nodes.push(parts.sign)
      if (parts.prefix) nodes.push(h('span', { class: 'num-unit num-unit-pre' }, parts.prefix))
      nodes.push(parts.digits)
      if (parts.suffix) nodes.push(h('span', { class: 'num-unit num-unit-post' }, parts.suffix))
      return nodes
    }

    return () => {
      const raw = String(props.text)
      const classes = ['num', props.tier ? `num-${props.tier}` : '']
      // 区间（¥0.03–¥0.07）和成对值（¥1.00 / ¥2.00）按分隔符拆成几段，每段各自上样式。
      const segments = raw.split(/(–| \/ )/)
      const children = segments.flatMap((segment, index) =>
        index % 2 === 1 ? [segment] : renderPart(segment)
      )
      return h('span', { class: classes }, children)
    }
  }
})
</script>
