<template>
  <BaseDialog :show="show" :title="dialogTitle" width="normal" @close="emit('close')">
    <div class="space-y-4">
      <p class="text-sm text-gray-600 dark:text-gray-400">
        {{ mode === 'renew' ? t('userSubscriptions.lifecycle.renewHint') : t('userSubscriptions.lifecycle.changeHint') }}
      </p>

      <!-- Loading -->
      <div v-if="loading" class="flex justify-center py-8">
        <div class="h-6 w-6 animate-spin rounded-full border-2 border-primary-500 border-t-transparent" />
      </div>

      <!-- Empty -->
      <p
        v-else-if="selectablePlans.length === 0"
        class="rounded-md border border-dashed border-gray-200 px-4 py-6 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-gray-400"
      >
        {{ t('userSubscriptions.lifecycle.noPlans') }}
      </p>

      <!-- Plan options -->
      <div v-else class="space-y-2">
        <button
          v-for="plan in selectablePlans"
          :key="plan.id"
          type="button"
          class="flex w-full items-center justify-between rounded-lg border px-4 py-3 text-left transition-colors"
          :class="
            selectedPlanId === plan.id
              ? 'border-primary-500 bg-primary-50 dark:border-primary-400 dark:bg-primary-500/10'
              : 'border-gray-200 hover:border-gray-300 dark:border-dark-700 dark:hover:border-dark-600'
          "
          @click="selectedPlanId = plan.id"
        >
          <div class="min-w-0">
            <div class="truncate text-sm font-semibold text-gray-900 dark:text-white">{{ plan.name }}</div>
            <div class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              ${{ plan.daily_amount_usd ?? 0 }} {{ t('userSubscriptions.lifecycle.perDay') }} ·
              {{ plan.validity_days }} {{ t('userSubscriptions.lifecycle.days') }}
            </div>
          </div>
          <div class="ml-3 shrink-0 text-sm font-bold text-gray-900 dark:text-white">${{ plan.price }}</div>
        </button>
      </div>

      <p v-if="mode === 'change' && selectedPlanId" class="text-xs text-gray-500 dark:text-gray-400">
        {{ t('userSubscriptions.lifecycle.changeDiffNote') }}
      </p>
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
        <button
          type="button"
          class="btn btn-primary"
          :disabled="submitting || !selectedPlanId"
          @click="handleConfirm"
        >
          {{ submitting ? t('common.saving') : t('userSubscriptions.lifecycle.confirm') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { paymentAPI } from '@/api/payment'
import subscriptionsAPI from '@/api/subscriptions'
import { extractApiErrorMessage, extractI18nErrorMessage } from '@/utils/apiError'
import type { SubscriptionPlan } from '@/types/payment'
import type { UserSubscription } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{
  show: boolean
  mode: 'renew' | 'change'
  subscription: UserSubscription
}>()

const emit = defineEmits<{
  close: []
  done: []
}>()

const { t } = useI18n()
const appStore = useAppStore()

const plans = ref<SubscriptionPlan[]>([])
const loading = ref(false)
const submitting = ref(false)
const selectedPlanId = ref<number | null>(null)

const dialogTitle = computed(() =>
  props.mode === 'renew'
    ? t('userSubscriptions.lifecycle.renewTitle')
    : t('userSubscriptions.lifecycle.changeTitle')
)

// 续费：仅相同每日额度 D 的在售套餐（同档续期）；转套餐：不同 D 的在售套餐（换档）。
const selectablePlans = computed(() => {
  const curD = props.subscription.daily_amount_usd ?? null
  return plans.value.filter((p) => {
    if (!p.for_sale || (p.daily_amount_usd ?? 0) <= 0) return false
    const sameD = curD != null && (p.daily_amount_usd ?? 0) === curD
    return props.mode === 'renew' ? sameD : !sameD
  })
})

watch(
  () => props.show,
  async (visible) => {
    if (!visible) return
    selectedPlanId.value = null
    if (plans.value.length === 0) {
      try {
        loading.value = true
        const res = await paymentAPI.getPlans()
        plans.value = res.data
      } catch (err: unknown) {
        appStore.showError(extractApiErrorMessage(err, t('common.error')))
      } finally {
        loading.value = false
      }
    }
  }
)

async function handleConfirm() {
  if (!selectedPlanId.value) return
  submitting.value = true
  try {
    if (props.mode === 'renew') {
      await subscriptionsAPI.renewSubscription(selectedPlanId.value)
      appStore.showSuccess(t('userSubscriptions.lifecycle.renewSuccess'))
    } else {
      const res = await subscriptionsAPI.changeSubscriptionPlan(selectedPlanId.value)
      const diff = res.diff
      if (diff > 0) {
        appStore.showSuccess(t('userSubscriptions.lifecycle.changeSuccessCharged', { amount: diff.toFixed(2) }))
      } else if (diff < 0) {
        appStore.showSuccess(t('userSubscriptions.lifecycle.changeSuccessRefunded', { amount: (-diff).toFixed(2) }))
      } else {
        appStore.showSuccess(t('userSubscriptions.lifecycle.changeSuccess'))
      }
    }
    emit('done')
    emit('close')
  } catch (err: unknown) {
    // 把后端语义错误码（NO_ACTIVE_SUBSCRIPTION/CHANGE_PLAN_DAILY_LIMIT/INSUFFICIENT_BALANCE_*/
    // RENEW_PLAN_MISMATCH 等）映射为友好本地化文案，无对应 key 时回退原始消息。
    appStore.showError(
      extractI18nErrorMessage(err, t, 'userSubscriptions.lifecycle.errors', t('common.error'))
    )
  } finally {
    submitting.value = false
  }
}
</script>
