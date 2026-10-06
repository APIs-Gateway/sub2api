<template>
  <BaseDialog :show="!!ops" :title="title" width="extra-wide" @close="close">
    <div class="space-y-4" data-test="cell-plan">
      <div v-if="plan.phase.value === 'previewing' || (plan.phase.value === 'idle' && !plan.error.value)" class="flex min-h-[8rem] items-center justify-center" data-test="plan-loading">
        <LoadingSpinner />
      </div>
      <WriteErrorNote v-if="plan.error.value" :error="plan.error.value" @action="onAction" />
      <PlanPreview v-if="plan.ticket.value" v-model:acked="acked" :ticket="plan.ticket.value" />
    </div>
    <template #footer>
      <button type="button" class="btn btn-secondary" :disabled="plan.phase.value === 'committing'" @click="close">{{ t('admin.pricingConfig.write.back') }}</button>
      <button type="button" class="btn btn-primary" :disabled="!canConfirm" data-test="plan-confirm" @click="confirm">
        {{ plan.phase.value === 'committing' ? t('admin.pricingConfig.write.submitting') : t('admin.pricingConfig.write.submit') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import { useAppStore } from '@/stores/app'
import type { CellOp } from '@/api/admin/pricing'
import PlanPreview from './PlanPreview.vue'
import WriteErrorNote from './WriteErrorNote.vue'
import { useCellPlan } from '../useCellPlan'
import { usePricingData } from '../usePricingData'
import { planWarnings } from '../pricingWrite'
import type { ErrorAction } from '../pricingErrors'

const props = defineProps<{ ops: CellOp[] | null; title: string }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'done'): void }>()

const { t } = useI18n()
const app = useAppStore()
const { refresh } = usePricingData()
const plan = useCellPlan()
const acked = ref(false)

watch(
  () => props.ops,
  (ops) => {
    plan.reset()
    acked.value = false
    if (ops) void plan.preview(ops)
  },
  { immediate: true }
)

const canConfirm = computed(() => {
  const ticket = plan.ticket.value
  if (!ticket || plan.phase.value !== 'ready' || ticket.planned.length === 0) return false
  return planWarnings(ticket).length === 0 || acked.value
})

function close() {
  if (plan.phase.value === 'committing') return
  emit('close')
}

async function confirm() {
  if (!canConfirm.value) return
  if (await plan.commit()) {
    app.showSuccess(t('admin.pricingConfig.write.submitted'))
    void refresh()
    emit('done')
  }
}

async function onAction(action: ErrorAction) {
  if (action === 'refresh') {
    await refresh()
    app.showWarning(t('admin.pricingConfig.write.refreshed'))
    emit('close')
  } else if (action === 'repreview') {
    acked.value = false
    await plan.repreview()
  }
}
</script>
