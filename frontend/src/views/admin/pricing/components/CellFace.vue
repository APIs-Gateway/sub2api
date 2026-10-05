<template>
  <div v-if="variant === 'cell'" class="cell-face" :class="['kind-' + view.kind]" :title="tip" :data-kind="view.kind">
    <div class="cell-label">
      <span v-if="view.kind === 'open'" class="cell-dot" aria-hidden="true" />
      <Icon v-if="view.kind === 'unpriced'" name="exclamationTriangle" size="xs" class="flex-shrink-0" />
      <span class="truncate">{{ label }}</span>
    </div>
    <div v-if="priceLine" class="cell-price num">{{ priceLine }}</div>
  </div>
  <span v-else class="chip" :class="['kind-' + view.kind]" :title="tip" :data-kind="view.kind">
    <Icon v-if="view.kind === 'unpriced'" name="exclamationTriangle" size="xs" class="flex-shrink-0" />
    {{ label }}
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { CellView } from '../pricingModel'
import { usePricingFormat } from '../usePricingFormat'

const props = withDefaults(defineProps<{ view: CellView; variant?: 'cell' | 'chip' }>(), { variant: 'cell' })

const { t } = useI18n()
const { paid } = usePricingFormat()

/** 倍率去掉多余的 0：1.20 → 1.2 */
function trimNum(n: number): string {
  return String(Number(n.toFixed(4)))
}

const label = computed(() => {
  const v = props.view
  switch (v.kind) {
    case 'extra':
      return `×${trimNum(v.extra ?? 1)}`
    case 'open':
    case 'custom':
    case 'unpriced':
    case 'closed':
    case 'error':
      return t(`admin.pricingConfig.cell.${v.kind}`)
    default:
      return ''
  }
})

function priceText(v: CellView): { line: string; exact: string } | null {
  if (v.usd) {
    const input = paid(v.usd.input)
    const output = paid(v.usd.output)
    const exactIn = paid(v.usd.input, true)
    const exactOut = paid(v.usd.output, true)
    return {
      line: `${input.main} / ${output.main}`,
      exact: t('admin.pricingConfig.cell.tipPrice', {
        input: exactIn.sub ?? exactIn.main,
        output: exactOut.sub ?? exactOut.main
      })
    }
  }
  if (v.perRequestUsd !== null) {
    const price = paid(v.perRequestUsd)
    const exact = paid(v.perRequestUsd, true)
    return {
      line: t('admin.pricingConfig.cell.perRequest', { price: price.main }),
      exact: t('admin.pricingConfig.cell.tipPerRequest', { price: exact.sub ?? exact.main })
    }
  }
  return null
}

const priceLine = computed(() => {
  const v = props.view
  if (v.kind === 'unpriced') return t('admin.pricingConfig.cell.unpricedHint')
  if (v.kind === 'closed' || v.kind === 'error') return ''
  return priceText(v)?.line ?? ''
})

const tip = computed(() => {
  const v = props.view
  const lines: string[] = [label.value]
  if (v.kind === 'closed') {
    const reason = v.reason ? t(`admin.pricingConfig.closedReason.${v.reason}`) : ''
    if (reason) lines.push(reason)
  } else if (v.kind === 'unpriced') {
    lines.push(t('admin.pricingConfig.cell.unpricedTip'))
  } else if (v.kind === 'error') {
    lines.push(t('admin.pricingConfig.cell.errorTip'))
  } else {
    if (v.kind === 'custom') lines.push(t('admin.pricingConfig.cell.customTip'))
    const exact = priceText(v)
    if (exact) lines.push(exact.exact)
  }
  if (v.unswitched) lines.push(t('admin.pricingConfig.cell.unswitchedTip'))
  return lines.join('\n')
})
</script>

<style scoped>
.cell-face {
  @apply flex min-w-0 flex-col items-start gap-0.5 text-left;
}

.cell-label {
  @apply flex max-w-full items-center gap-1.5 text-[13px] font-medium leading-5;
}

.cell-price {
  @apply max-w-full truncate text-[11px] leading-4 text-gray-500 dark:text-dark-300;
}

.cell-dot {
  @apply inline-block h-1.5 w-1.5 flex-shrink-0 rounded-full bg-gray-800 dark:bg-gray-200;
}

.chip {
  @apply inline-flex items-center gap-1 whitespace-nowrap text-[13px] font-medium leading-5;
}

/* 关闭：安静的灰 */
.kind-closed .cell-label,
.chip.kind-closed {
  @apply font-normal text-gray-400 dark:text-dark-400;
}

.kind-error .cell-label,
.chip.kind-error {
  @apply font-normal text-gray-400 dark:text-dark-400;
}

.kind-open .cell-label,
.chip.kind-open {
  @apply text-gray-900 dark:text-gray-100;
}

/* 额外倍率：黏土色，整张表里只有它和「未定价」是彩色，一眼能找到加价的格子 */
.kind-extra .cell-label,
.chip.kind-extra {
  @apply font-semibold text-primary-700 dark:text-primary-300;
}

/* 自定义价：墨色加点线下划线 */
.kind-custom .cell-label span:last-child,
.chip.kind-custom {
  @apply text-gray-900 underline decoration-gray-400 decoration-dotted underline-offset-4 dark:text-white dark:decoration-dark-300;
}

.kind-unpriced .cell-label,
.chip.kind-unpriced {
  @apply font-semibold text-primary-700 dark:text-primary-300;
}

.kind-unpriced .cell-price {
  @apply text-primary-700/80 dark:text-primary-300/80;
}
</style>
