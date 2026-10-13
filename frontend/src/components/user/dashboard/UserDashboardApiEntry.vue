<template>
  <div class="card">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="font-serif text-lg font-semibold text-gray-900 dark:text-white">{{ t('dashboard.apiEntry.title') }}</h2>
    </div>
    <div class="space-y-3 p-4">
      <!-- Primary endpoint -->
      <div class="rounded-lg border border-gray-200 bg-gray-50 dark:border-dark-700 dark:bg-dark-800/50">
        <div class="flex items-center gap-2 px-3 py-2">
          <span class="min-w-0 flex-1 truncate font-mono text-sm text-gray-700 dark:text-gray-300" :title="primaryUrl">{{ primaryUrl }}</span>
          <button
            type="button"
            class="flex-shrink-0 rounded px-2 py-1 text-xs font-medium transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500"
            :class="primaryCopied
              ? 'bg-primary-100 text-primary-700 dark:bg-primary-900/40 dark:text-primary-300'
              : 'bg-white text-gray-700 hover:bg-gray-100 dark:bg-dark-900 dark:text-gray-300 dark:hover:bg-dark-700'"
            @click="copy(primaryUrl, 'primary')"
          >
            {{ primaryCopied ? t('common.copied') : t('common.copy') }}
          </button>
        </div>
      </div>

      <!-- Alternate endpoints -->
      <template v-if="altEndpoints.length > 0">
        <p class="px-1 text-xs text-gray-500 dark:text-gray-400">{{ t('dashboard.apiEntry.altLabel') }}</p>
        <div
          v-for="ep in altEndpoints"
          :key="ep.endpoint"
          class="rounded-lg border border-gray-200 bg-gray-50 dark:border-dark-700 dark:bg-dark-800/50"
        >
          <div class="flex items-center gap-2 px-3 py-2">
            <div class="min-w-0 flex-1">
              <p v-if="ep.name" class="text-xs text-gray-500 dark:text-gray-400">{{ ep.name }}</p>
              <a
                :href="ep.endpoint"
                target="_blank"
                rel="noopener noreferrer"
                class="block truncate font-mono text-sm text-primary-600 underline-offset-2 hover:underline dark:text-primary-400"
                :title="ep.endpoint"
              >{{ ep.endpoint }}</a>
            </div>
            <button
              type="button"
              class="flex-shrink-0 rounded px-2 py-1 text-xs font-medium transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500"
              :class="altCopied === ep.endpoint
                ? 'bg-primary-100 text-primary-700 dark:bg-primary-900/40 dark:text-primary-300'
                : 'bg-white text-gray-700 hover:bg-gray-100 dark:bg-dark-900 dark:text-gray-300 dark:hover:bg-dark-700'"
              @click="copy(ep.endpoint, 'alt', ep.endpoint)"
            >
              {{ altCopied === ep.endpoint ? t('common.copied') : t('common.copy') }}
            </button>
          </div>
        </div>
      </template>

      <!-- Copy failed notice -->
      <p v-if="copyFailed" role="alert" class="px-1 text-xs text-red-600 dark:text-red-400">
        {{ t('dashboard.apiEntry.copyFailed') }}
      </p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'

const { t } = useI18n()
const appStore = useAppStore()

const primaryCopied = ref(false)
const altCopied = ref<string | null>(null)
const copyFailed = ref(false)

const primaryUrl = computed(() => {
  const base = appStore.cachedPublicSettings?.api_base_url || window.location.origin
  return base.replace(/\/+$/, '') + '/v1'
})

// CustomEndpoint from @/types has { name, endpoint, description }
const altEndpoints = computed(() => {
  const raw = appStore.cachedPublicSettings?.custom_endpoints
  if (!Array.isArray(raw)) return []
  return raw.filter((ep) => typeof ep === 'object' && typeof ep.endpoint === 'string' && ep.endpoint.length > 0)
})

async function copy(text: string, which: 'primary' | 'alt', altKey?: string) {
  copyFailed.value = false
  try {
    await navigator.clipboard.writeText(text)
    if (which === 'primary') {
      primaryCopied.value = true
      setTimeout(() => { primaryCopied.value = false }, 1600)
    } else {
      altCopied.value = altKey ?? null
      setTimeout(() => { altCopied.value = null }, 1600)
    }
  } catch {
    copyFailed.value = true
  }
}
</script>
