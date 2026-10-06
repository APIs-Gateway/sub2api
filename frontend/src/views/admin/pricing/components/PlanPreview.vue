<template>
  <div class="space-y-4" data-test="plan-preview">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <p class="text-sm text-gray-700 dark:text-gray-300" data-test="plan-summary">{{ summary }}</p>
      <CurrencyModeSwitch />
    </div>

    <div v-if="ticket.price_delta === 'up' || ticket.price_delta === 'unknown'" class="note-signal" role="alert" data-test="plan-delta-warn">
      <p class="font-medium">{{ t(`admin.pricingConfig.write.delta.${ticket.price_delta}`) }}</p>
      <p class="mt-0.5 text-xs opacity-80">{{ t(`admin.pricingConfig.write.delta.${ticket.price_delta}Hint`) }}</p>
    </div>
    <p v-else-if="ticket.price_delta === 'down'" class="text-sm text-gray-700 dark:text-gray-300" data-test="plan-delta-down">
      {{ t('admin.pricingConfig.write.delta.down') }}
    </p>

    <div v-if="warnings.length" class="note-signal" data-test="plan-warnings">
      <p class="font-medium">{{ t('admin.pricingConfig.write.warnTitle', { n: warnings.length }) }}</p>
      <ul class="mt-1 space-y-0.5">
        <li v-for="(w, i) in warnings" :key="i">{{ groupName(w.group_id) }} · {{ w.model_key }}：{{ t(`admin.pricingConfig.write.issue.${w.reason}`, { target: w.target ?? '' }) }}</li>
      </ul>
    </div>

    <div class="overflow-x-auto rounded-md border border-gray-200 dark:border-dark-700">
      <table class="w-full min-w-[52rem] text-sm">
        <thead>
          <tr class="border-b border-gray-300 text-left text-xs font-medium text-gray-500 dark:border-dark-600 dark:text-dark-400">
            <th class="px-4 py-3">{{ t('admin.pricingConfig.models.columns.model') }}</th>
            <th class="px-4 py-3">{{ t('admin.pricingConfig.write.col.group') }}</th>
            <th class="px-4 py-3">{{ t('admin.pricingConfig.write.col.before') }}</th>
            <th class="px-4 py-3">{{ t('admin.pricingConfig.write.col.after') }}</th>
            <th class="px-4 py-3">{{ t('admin.pricingConfig.write.col.input') }}</th>
            <th class="px-4 py-3">{{ t('admin.pricingConfig.write.col.output') }}</th>
            <th class="px-4 py-3">{{ t('admin.pricingConfig.write.col.change') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="r in rows"
            :key="r.key"
            class="border-b border-gray-100 last:border-b-0 dark:border-dark-800"
            :class="{ 'opacity-50': r.change === 'same' }"
            data-test="plan-row"
          >
            <td class="px-4 py-3 align-top font-medium text-gray-900 dark:text-white">{{ r.model }}</td>
            <td class="px-4 py-3 align-top text-gray-700 dark:text-gray-300">
              {{ r.group.name }}
              <span class="num ml-1 text-xs text-gray-500 dark:text-dark-300">{{ t('admin.pricingConfig.group.rate', { rate: trimNum(r.group.rate) }) }}</span>
            </td>
            <td class="px-4 py-3 align-top"><CellFace v-if="r.before" :view="r.before" variant="chip" /><span v-else class="text-gray-400">—</span></td>
            <td class="px-4 py-3 align-top"><CellFace :view="r.after" variant="chip" /></td>
            <td class="px-4 py-3 align-top"><PriceChange :before="r.before?.usd?.input ?? null" :after="r.after.usd?.input ?? null" /></td>
            <td class="px-4 py-3 align-top"><PriceChange :before="r.before?.usd?.output ?? null" :after="r.after.usd?.output ?? null" /></td>
            <td class="px-4 py-3 align-top">
              <span class="badge" :class="r.change === 'up' ? 'badge-primary' : ''">{{ t(`admin.pricingConfig.write.change.${r.change}`) }}</span>
            </td>
          </tr>
          <tr v-if="!rows.length">
            <td colspan="7" class="px-4 py-10 text-center text-sm text-gray-500 dark:text-dark-300">{{ t('admin.pricingConfig.write.noChanges') }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <label v-if="warnings.length" class="flex items-start gap-2 text-sm text-gray-700 dark:text-gray-300" data-test="plan-ack">
      <input
        :checked="acked"
        type="checkbox"
        class="mt-0.5 h-4 w-4 rounded border-gray-300 accent-primary-600"
        @change="emit('update:acked', ($event.target as HTMLInputElement).checked)"
      />
      <span>{{ t('admin.pricingConfig.write.ack', { n: warnings.length }) }}</span>
    </label>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CellsTicket } from '@/api/admin/pricing'
import CurrencyModeSwitch from '@/components/common/CurrencyModeSwitch.vue'
import CellFace from './CellFace.vue'
import PriceChange from './PriceChange.vue'
import { usePricingData } from '../usePricingData'
import { cellKey, cellView, type CellView, type PricingGroup } from '../pricingModel'
import { describeChange, planWarnings, specToView, type ChangeKind } from '../pricingWrite'

const props = defineProps<{ ticket: CellsTicket; acked: boolean }>()
const emit = defineEmits<{ (e: 'update:acked', value: boolean): void }>()

const { t } = useI18n()
const { state } = usePricingData()

interface Row {
  key: string
  model: string
  group: PricingGroup
  before: CellView | null
  after: CellView
  change: ChangeKind
}

const warnings = computed(() => planWarnings(props.ticket))

const rows = computed<Row[]>(() => {
  const out: Row[] = []
  for (const p of props.ticket.planned) {
    const group = state.groups.find((g) => g.id === p.op.group_id)
    if (!group) continue
    const model = p.op.model_key
    const before = cellView(state.cells[cellKey(group.id, model)], group)
    const after = specToView(p.after ?? null, group, state.refs[model])
    out.push({ key: `${group.id}|${model}`, model, group, before, after, change: p.action === 'noop' ? 'same' : describeChange(before, after) })
  }
  return out
})

const summary = computed(() => {
  const count: Partial<Record<ChangeKind, number>> = {}
  rows.value.forEach((r) => {
    count[r.change] = (count[r.change] ?? 0) + 1
  })
  const parts = (Object.keys(count) as ChangeKind[]).map((k) => `${t(`admin.pricingConfig.write.change.${k}`)} ${count[k]}`)
  return t('admin.pricingConfig.write.summary', { n: rows.value.length, parts: parts.join('，') })
})

const groupName = (id: number) => state.groups.find((g) => g.id === id)?.name ?? `#${id}`

function trimNum(n: number): string {
  return String(Number(n.toFixed(4)))
}
</script>

<style scoped>
.note-signal {
  @apply rounded-md border-l-2 border-primary-600 bg-primary-50 px-3 py-2.5 text-sm text-primary-900;
  @apply dark:border-primary-500 dark:bg-primary-950/40 dark:text-primary-100;
}
</style>
