<template>
  <div
    v-if="visible"
    role="status"
    class="low-balance-banner border-b border-primary-200 bg-primary-50 dark:border-primary-900/60 dark:bg-primary-900/10"
    data-testid="low-balance-banner"
  >
    <div class="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-2.5 md:px-6 lg:px-8">
      <Icon name="exclamationCircle" size="sm" class="flex-shrink-0 text-primary-700 dark:text-primary-400" />
      <p class="min-w-0 flex-1 basis-48 text-sm text-gray-900 dark:text-gray-100" data-testid="low-balance-message">
        {{ depleted ? t('lowBalance.depleted') : t('lowBalance.low', { amount: balanceText }) }}
      </p>
      <router-link :to="topUpLocation" class="btn btn-primary btn-sm" data-testid="low-balance-topup">
        {{ t('nav.topUp') }}
      </router-link>
      <button
        type="button"
        class="btn-ghost btn-icon -mr-2 !p-1.5"
        :aria-label="t('common.close')"
        data-testid="low-balance-dismiss"
        @click="dismiss"
      >
        <Icon name="x" size="sm" />
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * 站内低余额横幅：挂在 AppLayout 里顶栏的下面、页面内容的上面，所有用户页都有。
 * 显示条件见 composables/useLowBalanceBanner。
 */
import { useI18n } from 'vue-i18n'

import Icon from '@/components/icons/Icon.vue'
import { useLowBalanceBanner } from '@/composables/useLowBalanceBanner'

const { t } = useI18n()
const { visible, depleted, balanceText, topUpLocation, dismiss } = useLowBalanceBanner()
</script>
