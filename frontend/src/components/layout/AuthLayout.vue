<template>
  <div class="relative flex min-h-screen items-center justify-center bg-gray-50 p-4 dark:bg-dark-950">
    <!-- Content Container -->
    <div class="relative z-10 w-full max-w-md">
      <!-- Logo/Brand -->
      <div class="mb-8 text-center">
        <!-- 品牌标记：自定义 logo 优先；否则黏土星芒 -->
        <template v-if="settingsLoaded">
          <div class="mb-4 inline-flex h-14 w-14 items-center justify-center">
            <img
              v-if="siteLogo"
              :src="siteLogo"
              alt="Logo"
              class="h-14 w-14 rounded-2xl border border-gray-200 object-contain dark:border-dark-700"
            />
            <BrandMark v-else animated class="h-11 w-11" />
          </div>
          <h1 class="mb-2 font-serif text-3xl font-semibold text-gray-900 dark:text-white">
            {{ siteName }}
          </h1>
          <p class="text-sm text-gray-500 dark:text-dark-400">
            {{ siteSubtitle }}
          </p>
        </template>
      </div>

      <!-- Card Container -->
      <div class="rounded-2xl border border-gray-200 bg-white p-8 shadow-sm dark:border-dark-700 dark:bg-dark-900">
        <slot />
      </div>

      <!-- Footer Links -->
      <div class="mt-6 text-center text-sm">
        <slot name="footer" />
      </div>

      <!-- Copyright -->
      <div class="mt-8 text-center text-xs text-gray-400 dark:text-dark-500">
        &copy; {{ currentYear }} {{ siteName }}. All rights reserved.
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useAppStore } from '@/stores'
import { sanitizeUrl } from '@/utils/url'
import BrandMark from '@/components/common/BrandMark.vue'

const appStore = useAppStore()

const siteName = computed(() => appStore.siteName || 'Sub2API')
const siteLogo = computed(() => sanitizeUrl(appStore.siteLogo || '', { allowRelative: true, allowDataUrl: true }))
const siteSubtitle = computed(() => appStore.cachedPublicSettings?.site_subtitle || '快速稳定的大模型 API 服务')
const settingsLoaded = computed(() => appStore.publicSettingsLoaded)

const currentYear = computed(() => new Date().getFullYear())

onMounted(() => {
  appStore.fetchPublicSettings()
})
</script>

<style scoped>
.text-gradient {
  @apply bg-gradient-to-r from-primary-600 to-primary-500 bg-clip-text text-transparent;
}
</style>
