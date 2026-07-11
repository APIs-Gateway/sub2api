<template>
  <div class="mt-4 border-t border-gray-200 pt-4 dark:border-dark-700">
    <label class="mb-2 block text-sm font-medium text-gray-700 dark:text-gray-300">
      {{ t('payment.cryptoNetwork') }}
    </label>
    <div class="grid grid-cols-1 gap-2 sm:grid-cols-3">
      <button
        v-for="network in networks"
        :key="network.value"
        type="button"
        class="rounded-lg border px-3 py-2 text-left transition-colors"
        :class="modelValue === network.value
          ? 'border-[#26A17B] bg-emerald-50 text-gray-900 dark:bg-emerald-950 dark:text-white'
          : 'border-gray-300 bg-white text-gray-700 hover:border-gray-400 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200'"
        @click="emit('update:modelValue', network.value)"
      >
        <span class="block text-sm font-semibold">{{ t(network.label) }}</span>
        <span class="mt-0.5 block text-xs text-gray-500 dark:text-gray-400">{{ network.value }}</span>
      </button>
    </div>
    <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">{{ t('payment.cryptoNetworkHint') }}</p>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'

defineProps<{
  modelValue: string
}>()

const emit = defineEmits<{
  'update:modelValue': [value: string]
}>()

const { t } = useI18n()
const networks = [
  { value: 'usdt.trc20', label: 'payment.cryptoNetworks.trc20' },
  { value: 'usdt.polygon', label: 'payment.cryptoNetworks.polygon' },
  { value: 'usdt.solana', label: 'payment.cryptoNetworks.solana' },
]
</script>
