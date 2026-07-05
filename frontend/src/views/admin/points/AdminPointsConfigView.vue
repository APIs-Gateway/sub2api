<template>
  <AppLayout>
    <div class="space-y-6">
      <div v-if="loading" class="flex justify-center py-12">
        <div class="h-8 w-8 animate-spin rounded-full border-2 border-gray-400 border-t-transparent dark:border-dark-500"></div>
      </div>

      <div v-else-if="settings" class="card p-6 max-w-4xl">
        <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('points.admin.config.title') }}</h3>

        <div class="mt-6 space-y-6">
          <div class="space-y-1">
            <label class="flex items-center justify-between gap-4">
              <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('points.admin.config.enabled') }}</span>
              <input v-model="settings.enabled" type="checkbox" class="h-4 w-4" />
            </label>
          </div>

          <section class="rounded-lg border border-gray-200 p-4 dark:border-dark-700">
            <div class="flex items-start justify-between gap-4">
              <div>
                <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('points.admin.config.earnSection') }}</h4>
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-500">{{ t('points.admin.config.earnHint') }}</p>
              </div>
            </div>
            <div class="mt-4 grid gap-4 md:grid-cols-2">
              <div class="space-y-1">
                <label class="input-label">{{ t('points.admin.config.cashbackRate') }}</label>
                <input v-model.number="settings.cashback_rate_percent" type="number" step="0.01" min="0" max="100" class="input" />
                <p class="text-xs text-gray-500 dark:text-gray-500">{{ t('points.admin.config.cashbackRateHint') }}</p>
              </div>

              <div class="space-y-1">
                <label class="input-label">{{ t('points.admin.config.freezeHours') }}</label>
                <input v-model.number="settings.freeze_hours" type="number" min="0" max="720" class="input" />
                <p class="text-xs text-gray-500 dark:text-gray-500">{{ t('points.admin.config.freezeHoursHint') }}</p>
              </div>
            </div>
          </section>

          <section class="rounded-lg border border-gray-200 p-4 dark:border-dark-700">
            <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('points.admin.config.valueSection') }}</h4>
            <div class="mt-4 space-y-1">
              <label class="input-label">{{ t('points.admin.config.peg') }}</label>
              <input v-model.number="settings.peg" type="number" step="0.0001" min="0" class="input" />
              <p class="text-xs text-gray-500 dark:text-gray-500">{{ t('points.admin.config.pegHint') }}</p>
            </div>
          </section>

          <section class="rounded-lg border border-gray-200 p-4 dark:border-dark-700">
            <label class="flex items-center justify-between gap-4">
              <span>
                <span class="block text-sm font-semibold text-gray-900 dark:text-white">{{ t('points.admin.config.withdrawSection') }}</span>
                <span class="mt-1 block text-xs text-gray-500 dark:text-gray-500">{{ t('points.admin.config.withdrawSectionHint') }}</span>
              </span>
              <input v-model="settings.withdraw_enabled" type="checkbox" class="h-4 w-4" />
            </label>
            <div class="mt-4 grid gap-4 md:grid-cols-2">
              <div class="space-y-1">
                <label class="input-label">{{ t('points.admin.config.withdrawMin') }}</label>
                <input v-model.number="settings.withdraw_min_points" type="number" min="0" class="input" />
              </div>

              <div class="space-y-1">
                <label class="input-label">{{ t('points.admin.config.withdrawFee') }}</label>
                <input v-model.number="settings.withdraw_fee_percent" type="number" step="0.01" min="0" max="100" class="input" />
                <p class="text-xs text-gray-500 dark:text-gray-500">{{ t('points.admin.config.withdrawFeeHint') }}</p>
              </div>

              <div class="space-y-1">
                <label class="input-label">{{ t('points.admin.config.withdrawUSDCNYRate') }}</label>
                <input v-model.number="settings.withdraw_usd_cny_rate" type="number" step="0.0001" min="0" class="input" />
                <p class="text-xs text-gray-500 dark:text-gray-500">{{ t('points.admin.config.withdrawUSDCNYRateHint') }}</p>
              </div>
            </div>
          </section>

          <section class="rounded-lg border border-gray-200 p-4 dark:border-dark-700">
            <label class="flex items-center justify-between gap-4">
              <span>
                <span class="block text-sm font-semibold text-gray-900 dark:text-white">{{ t('points.admin.config.redeemBalanceOn') }}</span>
                <span class="mt-1 block text-xs text-gray-500 dark:text-gray-500">{{ t('points.admin.config.redeemBalanceHint') }}</span>
              </span>
              <input v-model="settings.redeem_balance_on" type="checkbox" class="h-4 w-4" />
            </label>
          </section>

          <section class="rounded-lg border border-gray-200 p-4 dark:border-dark-700">
            <label class="flex items-center justify-between gap-4">
              <span>
                <span class="block text-sm font-semibold text-gray-900 dark:text-white">{{ t('points.admin.config.redeemPlanOn') }}</span>
                <span class="mt-1 block text-xs text-gray-500 dark:text-gray-500">{{ t('points.admin.config.redeemPlanHint') }}</span>
              </span>
              <input v-model="settings.redeem_plan_on" type="checkbox" class="h-4 w-4" />
            </label>
          </section>
        </div>

        <div class="mt-6">
          <button class="btn btn-primary" :disabled="saving" @click="save">{{ t('points.admin.config.save') }}</button>
        </div>
      </div>
    </div>

    <!-- peg 变更会重估全部存量积分价值 → BaseDialog 二次确认（spec §2.1/§7） -->
    <BaseDialog
      :show="pegConfirm.show"
      :title="t('points.admin.config.pegConfirmTitle')"
      width="narrow"
      :close-on-escape="!saving"
      @close="cancelPegChange"
    >
      <p class="text-sm text-gray-700 dark:text-gray-300">
        {{ t('points.admin.config.pegChangeConfirm', { from: pegConfirm.from, to: pegConfirm.to }) }}
      </p>
      <template #footer>
        <button class="btn btn-secondary" :disabled="saving" @click="cancelPegChange">{{ t('common.cancel') }}</button>
        <button class="btn btn-danger" :disabled="saving" @click="confirmPegChange">{{ t('common.confirm') }}</button>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { getPointsSettings, updatePointsSettings, type PointsSettings } from '@/api/admin/points'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t } = useI18n()
const appStore = useAppStore()
const loading = ref(true)
const saving = ref(false)
const settings = ref<PointsSettings | null>(null)
// peg 是积分↔余额的锚（money-safety）：记住载入值，保存时若被改动需二次确认。
const originalPeg = ref<number | null>(null)
const pegConfirm = reactive<{ show: boolean; from: number | null; to: number | null }>({
  show: false,
  from: null,
  to: null,
})

async function load(): Promise<void> {
  loading.value = true
  try {
    settings.value = await getPointsSettings()
    originalPeg.value = settings.value.peg
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('points.loadFailed')))
  } finally {
    loading.value = false
  }
}

async function persist(): Promise<void> {
  if (!settings.value) return
  saving.value = true
  try {
    settings.value = await updatePointsSettings(settings.value)
    originalPeg.value = settings.value.peg
    appStore.showSuccess(t('points.admin.config.saved'))
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('points.actionFailed')))
  } finally {
    saving.value = false
  }
}

async function save(): Promise<void> {
  if (!settings.value) return
  // peg 变更会重估全部存量积分价值 → 二次确认（spec §2.1/§7）。
  if (originalPeg.value !== null && settings.value.peg !== originalPeg.value) {
    pegConfirm.from = originalPeg.value
    pegConfirm.to = settings.value.peg
    pegConfirm.show = true
    return
  }
  await persist()
}

function cancelPegChange(): void {
  if (saving.value) return
  pegConfirm.show = false
}

async function confirmPegChange(): Promise<void> {
  pegConfirm.show = false
  await persist()
}

onMounted(() => {
  void load()
})
</script>
