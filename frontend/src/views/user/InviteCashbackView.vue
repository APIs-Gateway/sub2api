<template>
  <AppLayout>
    <div class="space-y-6">
      <div v-if="loading" class="flex justify-center py-12">
        <div class="h-8 w-8 animate-spin rounded-full border-2 border-primary-500 border-t-transparent"></div>
      </div>

      <template v-else-if="detail">
        <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
          <div class="card p-5">
            <p class="text-sm text-gray-500 dark:text-dark-400">{{ t('inviteCashback.stats.rate') }}</p>
            <p class="mt-2 text-2xl font-semibold text-primary-600 dark:text-primary-400">{{ formatPercent(detail.cashback_rate_percent) }}</p>
          </div>
          <div class="card p-5">
            <p class="text-sm text-gray-500 dark:text-dark-400">{{ t('inviteCashback.stats.invited') }}</p>
            <p class="mt-2 text-2xl font-semibold text-gray-900 dark:text-white">{{ detail.invited_count.toLocaleString() }}</p>
          </div>
          <div class="card p-5">
            <p class="text-sm text-gray-500 dark:text-dark-400">{{ t('inviteCashback.stats.total') }}</p>
            <p class="mt-2 text-2xl font-semibold text-emerald-600 dark:text-emerald-400">{{ formatCurrency(detail.total_cashback) }}</p>
          </div>
          <div class="card p-5">
            <p class="text-sm text-gray-500 dark:text-dark-400">{{ t('inviteCashback.stats.status') }}</p>
            <p class="mt-2 text-lg font-semibold" :class="detail.cashback_enabled ? 'text-emerald-600 dark:text-emerald-400' : 'text-gray-500 dark:text-dark-400'">
              {{ detail.cashback_enabled ? t('common.enabled') : t('common.disabled') }}
            </p>
          </div>
        </div>

        <div class="card p-6">
          <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('inviteCashback.share.title') }}</h3>
          <div class="mt-5 grid gap-4 md:grid-cols-2">
            <div class="space-y-2">
              <p class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('inviteCashback.share.code') }}</p>
              <div class="flex items-center gap-2 rounded-lg border border-gray-200 bg-gray-50 px-3 py-2 dark:border-dark-700 dark:bg-dark-900">
                <code class="flex-1 truncate text-sm font-semibold text-gray-900 dark:text-white">{{ detail.aff_code }}</code>
                <button class="btn btn-secondary btn-sm" @click="copyCode">
                  <Icon name="copy" size="sm" />
                  <span>{{ t('common.copy') }}</span>
                </button>
              </div>
            </div>
            <div class="space-y-2">
              <p class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('inviteCashback.share.link') }}</p>
              <div class="flex items-center gap-2 rounded-lg border border-gray-200 bg-gray-50 px-3 py-2 dark:border-dark-700 dark:bg-dark-900">
                <code class="flex-1 truncate text-sm text-gray-700 dark:text-gray-300">{{ inviteLink }}</code>
                <button class="btn btn-secondary btn-sm" @click="copyLink">
                  <Icon name="copy" size="sm" />
                  <span>{{ t('common.copy') }}</span>
                </button>
              </div>
            </div>
          </div>
        </div>

        <div class="card p-6">
          <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('inviteCashback.records.title') }}</h3>
          <div v-if="detail.records.length === 0" class="mt-4 rounded-lg border border-dashed border-gray-300 p-6 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-dark-400">
            {{ t('inviteCashback.records.empty') }}
          </div>
          <div v-else class="mt-4 overflow-x-auto">
            <table class="w-full min-w-[720px] text-left text-sm">
              <thead>
                <tr class="border-b border-gray-200 text-gray-500 dark:border-dark-700 dark:text-dark-400">
                  <th class="px-3 py-2 font-medium">{{ t('inviteCashback.records.invitee') }}</th>
                  <th class="px-3 py-2 font-medium">{{ t('inviteCashback.records.redeemValue') }}</th>
                  <th class="px-3 py-2 font-medium">{{ t('inviteCashback.records.baseAmount') }}</th>
                  <th class="px-3 py-2 font-medium">{{ t('inviteCashback.records.rate') }}</th>
                  <th class="px-3 py-2 font-medium text-right">{{ t('inviteCashback.records.amount') }}</th>
                  <th class="px-3 py-2 font-medium">{{ t('inviteCashback.records.time') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="item in detail.records" :key="item.ledger_id" class="border-b border-gray-100 last:border-b-0 dark:border-dark-800">
                  <td class="px-3 py-3 text-gray-900 dark:text-white">{{ item.invitee_email || item.invitee_username || '-' }}</td>
                  <td class="px-3 py-3 text-gray-700 dark:text-gray-300">{{ formatCurrency(item.redeem_value) }}</td>
                  <td class="px-3 py-3 text-gray-700 dark:text-gray-300">{{ formatCurrency(item.cashback_base_amount) }}</td>
                  <td class="px-3 py-3 text-gray-700 dark:text-gray-300">{{ formatPercent(item.cashback_rate_percent) }}</td>
                  <td class="px-3 py-3 text-right font-medium text-emerald-600 dark:text-emerald-400">{{ formatCurrency(item.cashback_amount) }}</td>
                  <td class="px-3 py-3 text-gray-700 dark:text-gray-300">{{ formatDateTime(item.created_at) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import { getInviteCashbackDetail, type InviteCashbackDetail } from '@/api/inviteCashback'
import { useAppStore } from '@/stores/app'
import { useClipboard } from '@/composables/useClipboard'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatCurrency, formatDateTime } from '@/utils/format'

const { t } = useI18n()
const appStore = useAppStore()
const { copyToClipboard } = useClipboard()
const loading = ref(true)
const detail = ref<InviteCashbackDetail | null>(null)

const inviteLink = computed(() => {
  const code = detail.value?.aff_code || ''
  if (!code) return ''
  if (typeof window === 'undefined') return `/register?aff=${encodeURIComponent(code)}`
  return `${window.location.origin}/register?aff=${encodeURIComponent(code)}`
})

function formatPercent(value: number): string {
  const rounded = Math.round(Number(value || 0) * 100) / 100
  return `${Number.isInteger(rounded) ? rounded : rounded.toString()}%`
}

async function loadDetail(): Promise<void> {
  loading.value = true
  try {
    detail.value = await getInviteCashbackDetail()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('inviteCashback.loadFailed')))
  } finally {
    loading.value = false
  }
}

async function copyCode(): Promise<void> {
  if (detail.value?.aff_code) {
    await copyToClipboard(detail.value.aff_code, t('inviteCashback.copied'))
  }
}

async function copyLink(): Promise<void> {
  if (inviteLink.value) {
    await copyToClipboard(inviteLink.value, t('inviteCashback.copied'))
  }
}

onMounted(() => {
  void loadDetail()
})
</script>
