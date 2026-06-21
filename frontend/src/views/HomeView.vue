<template>
  <!-- Custom Home Content: Full Page Mode (admin override) -->
  <div v-if="homeContent" class="min-h-screen">
    <!-- iframe mode -->
    <iframe
      v-if="isHomeContentUrl"
      :src="homeContent.trim()"
      class="h-screen w-full border-0"
      allowfullscreen
    ></iframe>
    <!-- HTML mode - SECURITY: homeContent is admin-only setting, XSS risk is acceptable -->
    <div v-else v-html="homeContent"></div>
  </div>

  <!-- Default Home Page — Quiet Ledger: 编辑式、单张纸、发丝线、无光晕/无假终端 -->
  <div v-else class="landing flex min-h-screen flex-col">
    <!-- Header -->
    <header class="px-6 py-5 md:px-10">
      <nav class="mx-auto flex max-w-5xl items-center justify-between">
        <!-- Logo + Name -->
        <div class="flex items-center gap-2.5">
          <div class="flex h-8 w-8 items-center justify-center overflow-hidden">
            <img v-if="siteLogo" :src="siteLogo" alt="Logo" class="h-full w-full rounded-md object-contain" />
            <BrandMark v-else class="h-6 w-6" />
          </div>
          <span class="font-display text-[17px] font-semibold tracking-tight text-ink">{{ siteName }}</span>
        </div>

        <!-- Nav Actions -->
        <div class="flex items-center gap-1.5">
          <LocaleSwitcher />

          <a
            v-if="docUrl"
            :href="docUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="icon-btn"
            :title="t('home.viewDocs')"
          >
            <Icon name="book" size="md" />
          </a>

          <button
            @click="toggleTheme"
            class="icon-btn"
            :title="isDark ? t('home.switchToLight') : t('home.switchToDark')"
          >
            <Icon v-if="isDark" name="sun" size="md" />
            <Icon v-else name="moon" size="md" />
          </button>

          <router-link
            v-if="isAuthenticated"
            :to="dashboardPath"
            class="ml-1 inline-flex items-center gap-2 rounded-md bg-ink py-1 pl-1 pr-3 text-paper transition-opacity hover:opacity-90"
          >
            <span
              class="flex h-6 w-6 items-center justify-center rounded-md bg-clay text-[11px] font-semibold text-white"
            >
              {{ userInitial }}
            </span>
            <span class="text-[13px] font-medium">{{ t('home.dashboard') }}</span>
          </router-link>
          <router-link
            v-else
            to="/login"
            class="ml-1 inline-flex items-center rounded-md bg-ink px-4 py-1.5 text-[13px] font-medium text-paper transition-opacity hover:opacity-90"
          >
            {{ t('home.login') }}
          </router-link>
        </div>
      </nav>
    </header>

    <!-- Hero — left-set editorial thesis on a wide measure -->
    <main class="flex-1 px-6 md:px-10">
      <section class="mx-auto max-w-5xl pb-20 pt-20 md:pt-28">
        <p class="mb-6 text-[13px] font-medium text-clay">
          {{ t('home.providers.description') }}
        </p>
        <h1
          class="max-w-3xl font-display text-4xl font-semibold leading-[1.06] tracking-tight text-ink sm:text-5xl lg:text-[4rem]"
        >
          {{ heroTitle }}
        </h1>
        <p class="mt-7 max-w-xl text-lg leading-relaxed text-muted">
          {{ heroDesc }}
        </p>

        <div class="mt-9 flex flex-wrap items-center gap-x-7 gap-y-3">
          <router-link
            :to="isAuthenticated ? dashboardPath : '/login'"
            class="inline-flex items-center gap-2 rounded-md bg-ink px-6 py-3 text-[15px] font-medium text-paper transition-opacity hover:opacity-90"
          >
            {{ isAuthenticated ? t('home.goToDashboard') : t('home.getStarted') }}
            <Icon name="arrowRight" size="md" :stroke-width="2" />
          </router-link>
          <a
            v-if="docUrl"
            :href="docUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="link-clay text-[15px] font-medium"
          >
            {{ t('home.docs') }} →
          </a>
        </div>

        <!-- Signature: 一行 mono 端点（OpenAI 兼容），代替假终端卡 -->
        <div class="mt-12 max-w-xl border-t border-line pt-5">
          <p class="font-mono text-[13px] leading-relaxed text-faint">
            <span class="text-clay">POST</span> /v1/chat/completions
            <span class="ml-2 text-faint">{{ '// OpenAI 兼容 · 一个密钥即用' }}</span>
          </p>
          <div class="mt-4 flex flex-wrap items-center gap-2">
            <span
              v-for="model in models"
              :key="model"
              class="rounded-md border border-line px-2.5 py-1 font-mono text-[12px] text-ink/70"
            >
              {{ model }}
            </span>
          </div>
        </div>
      </section>

      <!-- Editorial numbered list — replaces the icon-tile triptych -->
      <section class="mx-auto max-w-5xl border-t border-line">
        <div
          v-for="(f, i) in featureList"
          :key="i"
          class="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1 border-b border-line py-8 md:grid-cols-[5rem_1fr] md:py-10"
        >
          <span class="font-mono text-sm text-clay">{{ String(i + 1).padStart(2, '0') }}</span>
          <div>
            <h3 class="font-display text-xl font-semibold text-ink md:text-2xl">{{ f.title }}</h3>
            <p class="mt-2 max-w-xl text-[15px] leading-relaxed text-muted">{{ f.desc }}</p>
          </div>
        </div>
      </section>

      <!-- Closing — plain ruled end-matter, no tinted band -->
      <section class="mx-auto max-w-5xl py-20 md:py-24">
        <h2 class="font-display text-2xl font-semibold tracking-tight text-ink md:text-3xl">
          {{ t('home.cta.title') }}
        </h2>
        <p class="mt-3 max-w-md text-base text-muted">{{ t('home.cta.description') }}</p>
        <router-link
          :to="isAuthenticated ? dashboardPath : '/register'"
          class="mt-6 inline-flex items-center gap-2 rounded-md bg-ink px-6 py-3 text-[15px] font-medium text-paper transition-opacity hover:opacity-90"
        >
          {{ isAuthenticated ? t('home.goToDashboard') : t('home.cta.button') }}
          <Icon name="arrowRight" size="md" :stroke-width="2" />
        </router-link>
      </section>
    </main>

    <!-- Footer -->
    <footer class="border-t border-line px-6 py-8 md:px-10">
      <div
        class="mx-auto flex max-w-5xl flex-col items-center justify-between gap-3 text-center sm:flex-row sm:text-left"
      >
        <p class="text-sm text-faint">&copy; {{ currentYear }} {{ siteName }}. {{ t('home.footer.allRightsReserved') }}</p>
        <a
          v-if="docUrl"
          :href="docUrl"
          target="_blank"
          rel="noopener noreferrer"
          class="text-sm text-faint transition-colors hover:text-ink"
        >
          {{ t('home.docs') }}
        </a>
      </div>
    </footer>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore, useAppStore } from '@/stores'
import LocaleSwitcher from '@/components/common/LocaleSwitcher.vue'
import Icon from '@/components/icons/Icon.vue'
import BrandMark from '@/components/common/BrandMark.vue'

const { t } = useI18n()

const authStore = useAuthStore()
const appStore = useAppStore()

// Site settings - directly from appStore (already initialized from injected config)
const siteName = computed(() => appStore.cachedPublicSettings?.site_name || appStore.siteName || 'Sub2API')
const siteLogo = computed(() => appStore.cachedPublicSettings?.site_logo || appStore.siteLogo || '')
const docUrl = computed(() => appStore.cachedPublicSettings?.doc_url || appStore.docUrl || '')
const homeContent = computed(() => appStore.cachedPublicSettings?.home_content || '')

// Hero copy: prefer admin-configured subtitle, else i18n default
const heroTitle = computed(() => t('home.heroTitle'))
const heroDesc = computed(() => appStore.cachedPublicSettings?.site_subtitle || t('home.heroDesc'))

// Supported models (OpenAI-focused)
const models = ['GPT-5.5', 'GPT-5.4', 'GPT-5.4-mini', '...']

// Feature cards
const featureList = computed(() => [
  { icon: 'server' as const, title: t('home.features.unifiedGateway'), desc: t('home.features.unifiedGatewayDesc') },
  { icon: 'swap' as const, title: t('home.features.multiAccount'), desc: t('home.features.multiAccountDesc') },
  { icon: 'chart' as const, title: t('home.features.balanceQuota'), desc: t('home.features.balanceQuotaDesc') }
])

// Check if homeContent is a URL (for iframe display)
const isHomeContentUrl = computed(() => {
  const content = homeContent.value.trim()
  return content.startsWith('http://') || content.startsWith('https://')
})

// Theme
const isDark = ref(document.documentElement.classList.contains('dark'))

// Auth state
const isAuthenticated = computed(() => authStore.isAuthenticated)
const isAdmin = computed(() => authStore.isAdmin)
const dashboardPath = computed(() => (isAdmin.value ? '/admin/dashboard' : '/dashboard'))
const userInitial = computed(() => {
  const user = authStore.user
  if (!user || !user.email) return ''
  return user.email.charAt(0).toUpperCase()
})

// Current year for footer
const currentYear = computed(() => new Date().getFullYear())

// Toggle theme
function toggleTheme() {
  isDark.value = !isDark.value
  document.documentElement.classList.toggle('dark', isDark.value)
  localStorage.setItem('theme', isDark.value ? 'dark' : 'light')
}

// Initialize theme
function initTheme() {
  const savedTheme = localStorage.getItem('theme')
  if (savedTheme === 'dark' || (!savedTheme && window.matchMedia('(prefers-color-scheme: dark)').matches)) {
    isDark.value = true
    document.documentElement.classList.add('dark')
  }
}

onMounted(() => {
  initTheme()
  authStore.checkAuth()
  if (!appStore.publicSettingsLoaded) {
    appStore.fetchPublicSettings()
  }
})
</script>

<style scoped>
/* ===== Quiet Ledger 暖中性调色板（落地页自带变量，与全站主题一致） ===== */
.landing {
  --paper: #faf9f5; /* page background (ivory) */
  --card: #ffffff;
  --ink: #1a1915; /* near-black text */
  --muted: #5f5b51; /* secondary text */
  --faint: #8c887d; /* tertiary text */
  --line: #e7e2d6; /* hairline borders */
  --clay: #cc785c; /* Anthropic book cloth accent */
  --clay-dark: #b5634a;

  background-color: var(--paper);
  color: var(--ink);
}

:global(.dark .landing) {
  --paper: #1c1b19;
  --card: #262420;
  --ink: #f3efe6;
  --muted: #b4ab9a;
  --faint: #8a8275;
  --line: #34312b;
  --clay: #db7c57;
  --clay-dark: #c96b48;
}

/* Display serif for headings — Fraunces (近 Tiempos 编辑感), 中文回退宋体 */
.font-display {
  font-family: 'Fraunces Variable', 'Fraunces', 'Noto Serif SC', 'Songti SC', 'STSong', Georgia, serif;
  font-optical-sizing: auto;
  letter-spacing: -0.02em;
}

/* Color utility bindings */
.text-ink {
  color: var(--ink);
}
.text-muted {
  color: var(--muted);
}
.text-faint {
  color: var(--faint);
}
.text-paper {
  color: var(--paper);
}
.bg-ink {
  background-color: var(--ink);
}
.bg-clay {
  background-color: var(--clay);
}
.text-clay {
  color: var(--clay);
}
.border-line {
  border-color: var(--line);
}

/* Clay text link with hover underline (rationed accent) */
.link-clay {
  color: var(--clay);
  transition: opacity 0.15s ease;
}
.link-clay:hover {
  text-decoration: underline;
  text-underline-offset: 3px;
}

/* Header icon buttons */
.icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 0.5rem;
  border-radius: 0.375rem;
  color: var(--faint);
  transition: all 0.15s ease;
}
.icon-btn:hover {
  color: var(--ink);
  background-color: color-mix(in srgb, var(--ink) 5%, transparent);
}
</style>
