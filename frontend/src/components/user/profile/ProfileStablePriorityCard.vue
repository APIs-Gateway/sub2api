<template>
  <div class="card">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-medium text-gray-900 dark:text-white">
        {{ t('profile.stablePriority.title') }}
      </h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
        {{ t('profile.stablePriority.description') }}
      </p>
    </div>
    <div class="px-6 py-6 space-y-4">
      <div class="flex items-center justify-between">
        <label class="input-label mb-0">{{ t('profile.stablePriority.enabled') }}</label>
        <label class="relative inline-flex items-center cursor-pointer">
          <input type="checkbox" v-model="enabled" @change="handleToggle" :disabled="saving" class="sr-only peer" />
          <div class="w-11 h-6 bg-gray-200 peer-focus:outline-none peer-focus:ring-4 peer-focus:ring-primary-300 dark:peer-focus:ring-primary-800 rounded-full peer dark:bg-gray-700 peer-checked:after:translate-x-full peer-checked:after:border-white after:content-[''] after:absolute after:top-[2px] after:left-[2px] after:bg-white after:border-gray-300 after:border after:rounded-full after:h-5 after:w-5 after:transition-all dark:after:border-gray-600 peer-checked:bg-primary-600"></div>
        </label>
      </div>
      <p class="text-xs text-yellow-600 dark:text-yellow-400">
        {{ t('profile.stablePriority.billingHint') }}
      </p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import { userAPI } from '@/api'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{
  enabled: boolean
}>()

const { t } = useI18n()
const authStore = useAuthStore()
const appStore = useAppStore()

const enabled = ref(props.enabled)
const saving = ref(false)

watch(() => props.enabled, (val) => { enabled.value = val })

const handleToggle = async () => {
  saving.value = true
  try {
    const updated = await userAPI.updateProfile({ stable_priority_enabled: enabled.value })
    authStore.user = updated
    appStore.showSuccess(t('common.saved'))
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
    enabled.value = !enabled.value
  } finally {
    saving.value = false
  }
}
</script>
