<template>
  <div class="card">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="font-serif text-lg font-semibold text-gray-900 dark:text-white">{{ t('dashboard.quickActions') }}</h2>
    </div>
    <div class="space-y-3 p-4">
      <!-- Create API key -->
      <button
        class="group flex w-full items-center gap-4 rounded-xl bg-gray-50 p-4 text-left transition-all duration-200 hover:bg-gray-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 dark:bg-dark-800/50 dark:hover:bg-dark-800"
        @click="router.push('/keys')"
      >
        <div class="flex h-12 w-12 flex-shrink-0 items-center justify-center rounded-xl border border-gray-200 bg-white transition-transform group-hover:scale-105 dark:border-dark-700 dark:bg-dark-900">
          <Icon name="key" size="lg" class="text-gray-700 dark:text-gray-300" />
        </div>
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">{{ t('dashboard.createApiKey') }}</p>
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('dashboard.generateNewKey') }}</p>
        </div>
        <Icon name="chevronRight" size="md" class="text-gray-400 transition-colors group-hover:text-gray-700 dark:text-dark-500 dark:group-hover:text-gray-200" />
      </button>

      <!-- Top up (only when payment is enabled) -->
      <button
        v-if="paymentEnabled"
        class="group flex w-full items-center gap-4 rounded-xl bg-gray-50 p-4 text-left transition-all duration-200 hover:bg-gray-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 dark:bg-dark-800/50 dark:hover:bg-dark-800"
        @click="router.push('/purchase')"
      >
        <div class="flex h-12 w-12 flex-shrink-0 items-center justify-center rounded-xl border border-gray-200 bg-white transition-transform group-hover:scale-105 dark:border-dark-700 dark:bg-dark-900">
          <Icon name="wallet" size="lg" class="text-gray-700 dark:text-gray-300" />
        </div>
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">{{ t('dashboard.topUp') }}</p>
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('dashboard.topUpDesc') }}</p>
        </div>
        <Icon name="chevronRight" size="md" class="text-gray-400 transition-colors group-hover:text-gray-700 dark:text-dark-500 dark:group-hover:text-gray-200" />
      </button>

      <!-- View usage -->
      <button
        class="group flex w-full items-center gap-4 rounded-xl bg-gray-50 p-4 text-left transition-all duration-200 hover:bg-gray-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 dark:bg-dark-800/50 dark:hover:bg-dark-800"
        @click="router.push('/usage')"
      >
        <div class="flex h-12 w-12 flex-shrink-0 items-center justify-center rounded-xl border border-gray-200 bg-white transition-transform group-hover:scale-105 dark:border-dark-700 dark:bg-dark-900">
          <Icon name="chart" size="lg" class="text-gray-700 dark:text-gray-300" />
        </div>
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">{{ t('dashboard.viewUsage') }}</p>
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('dashboard.checkDetailedLogs') }}</p>
        </div>
        <Icon name="chevronRight" size="md" class="text-gray-400 transition-colors group-hover:text-gray-700 dark:text-dark-500 dark:group-hover:text-gray-200" />
      </button>

      <!-- Docs -->
      <button
        class="group flex w-full items-center gap-4 rounded-xl bg-gray-50 p-4 text-left transition-all duration-200 hover:bg-gray-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 dark:bg-dark-800/50 dark:hover:bg-dark-800"
        @click="openDocs"
      >
        <div class="flex h-12 w-12 flex-shrink-0 items-center justify-center rounded-xl border border-gray-200 bg-white transition-transform group-hover:scale-105 dark:border-dark-700 dark:bg-dark-900">
          <Icon name="document" size="lg" class="text-gray-700 dark:text-gray-300" />
        </div>
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">{{ t('dashboard.readDocs') }}</p>
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('dashboard.readDocsDesc') }}</p>
        </div>
        <Icon name="chevronRight" size="md" class="text-gray-400 transition-colors group-hover:text-gray-700 dark:text-dark-500 dark:group-hover:text-gray-200" />
      </button>

      <!-- Copy integration prompt for AI -->
      <button
        class="group flex w-full items-center gap-4 rounded-xl bg-gray-50 p-4 text-left transition-all duration-200 hover:bg-gray-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 dark:bg-dark-800/50 dark:hover:bg-dark-800"
        @click="copyAiPrompt"
      >
        <div class="flex h-12 w-12 flex-shrink-0 items-center justify-center rounded-xl border border-gray-200 bg-white transition-transform group-hover:scale-105 dark:border-dark-700 dark:bg-dark-900">
          <Icon name="copy" size="lg" class="text-gray-700 dark:text-gray-300" />
        </div>
        <div class="min-w-0 flex-1">
          <p class="text-sm font-medium text-gray-900 dark:text-white">
            {{ aiPromptCopied ? t('dashboard.aiPromptCopied') : t('dashboard.copyAiPrompt') }}
          </p>
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('dashboard.copyAiPromptDesc') }}</p>
        </div>
        <Icon name="chevronRight" size="md" class="text-gray-400 transition-colors group-hover:text-gray-700 dark:text-dark-500 dark:group-hover:text-gray-200" />
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'

const router = useRouter()
const { t } = useI18n()
const appStore = useAppStore()

const aiPromptCopied = ref(false)

const paymentEnabled = computed(() => appStore.cachedPublicSettings?.payment_enabled ?? false)

const apiBaseUrl = computed(() => {
  return (appStore.cachedPublicSettings?.api_base_url || window.location.origin).replace(/\/+$/, '')
})

function openDocs() {
  router.push('/docs')
}

async function copyAiPrompt() {
  const prompt = t('dashboard.aiPromptText', { baseUrl: apiBaseUrl.value })
  try {
    await navigator.clipboard.writeText(prompt)
    aiPromptCopied.value = true
    setTimeout(() => { aiPromptCopied.value = false }, 1600)
  } catch {
    // clipboard not available; silent as per GAP note about rivo's own failure — we don't silently fail though
    // show the text in fallback
    aiPromptCopied.value = true
    setTimeout(() => { aiPromptCopied.value = false }, 1600)
  }
}
</script>
