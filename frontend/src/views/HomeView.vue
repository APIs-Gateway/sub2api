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

  <!-- Default Home Page — Anthropic-inspired minimal -->
  <div v-else class="landing relative flex min-h-screen flex-col">
    <!-- Soft warm background accent -->
    <div class="pointer-events-none absolute inset-0 overflow-hidden">
      <div class="accent-glow absolute -top-32 right-[-8rem] h-[34rem] w-[34rem]"></div>
    </div>

    <!-- Header -->
    <header class="relative z-20 px-6 py-5 md:px-10">
      <nav class="mx-auto flex max-w-5xl items-center justify-between">
        <!-- Logo + Name -->
        <div class="flex items-center gap-2.5">
          <div class="h-8 w-8 overflow-hidden rounded-lg">
            <img :src="siteLogo || '/logo.png'" alt="Logo" class="h-full w-full object-contain" />
          </div>
          <span class="text-[15px] font-semibold tracking-tight text-ink">{{ siteName }}</span>
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
            class="ml-1 inline-flex items-center gap-2 rounded-lg bg-ink py-1 pl-1 pr-3 text-paper transition-opacity hover:opacity-90"
          >
            <span
              class="flex h-6 w-6 items-center justify-center rounded-full bg-clay text-[11px] font-semibold text-white"
            >
              {{ userInitial }}
            </span>
            <span class="text-[13px] font-medium">{{ t('home.dashboard') }}</span>
          </router-link>
          <router-link
            v-else
            to="/login"
            class="ml-1 inline-flex items-center rounded-lg bg-ink px-4 py-1.5 text-[13px] font-medium text-paper transition-opacity hover:opacity-90"
          >
            {{ t('home.login') }}
          </router-link>
        </div>
      </nav>
    </header>

    <!-- Hero — editorial, left-aligned, asymmetric -->
    <main class="relative z-10 flex-1 px-6 md:px-10">
      <section
        class="mx-auto grid max-w-5xl items-center gap-12 pb-16 pt-16 md:pt-24 lg:grid-cols-[1.05fr_0.95fr] lg:gap-16"
      >
        <!-- Left: copy -->
        <div class="text-left">
          <p class="mb-5 text-[13px] font-medium uppercase tracking-[0.14em] text-clay">
            {{ t('home.providers.description') }}
          </p>
          <h1
            class="font-display text-4xl font-semibold leading-[1.08] tracking-tight text-ink sm:text-5xl lg:text-[3.75rem]"
          >
            {{ heroTitle }}
          </h1>
          <p class="mt-6 max-w-lg text-lg leading-relaxed text-muted">
            {{ heroDesc }}
          </p>

          <div class="mt-9 flex flex-wrap items-center gap-3">
            <router-link
              :to="isAuthenticated ? dashboardPath : '/login'"
              class="inline-flex items-center gap-2 rounded-xl bg-ink px-7 py-3 text-base font-medium text-paper shadow-sm transition-all hover:-translate-y-0.5 hover:opacity-90"
            >
              {{ isAuthenticated ? t('home.goToDashboard') : t('home.getStarted') }}
              <Icon name="arrowRight" size="md" :stroke-width="2" />
            </router-link>
            <a
              v-if="docUrl"
              :href="docUrl"
              target="_blank"
              rel="noopener noreferrer"
              class="inline-flex items-center gap-2 rounded-xl border border-line px-7 py-3 text-base font-medium text-ink transition-colors hover:bg-ink/[0.04]"
            >
              {{ t('home.docs') }}
            </a>
          </div>

          <!-- Supported models, inline -->
          <div class="mt-9 flex flex-wrap items-center gap-2">
            <span
              v-for="model in models"
              :key="model"
              class="rounded-lg border border-line bg-card px-3 py-1 text-[13px] font-medium text-ink/75"
            >
              {{ model }}
            </span>
          </div>
        </div>

        <!-- Right: signature — a real API call -->
        <div class="code-card overflow-hidden rounded-xl border border-line">
          <div class="flex items-center gap-2 border-b border-line px-4 py-2.5">
            <span class="code-dot"></span><span class="code-dot"></span><span class="code-dot"></span>
            <span class="ml-2 font-mono text-xs text-faint">quickstart.py</span>
          </div>
          <pre class="code-body overflow-x-auto px-5 py-4 text-[13px] leading-[1.75]"><code><span class="tok-kw">from</span> openai <span class="tok-kw">import</span> OpenAI

client = <span class="tok-fn">OpenAI</span>(
  base_url=<span class="tok-str">"https://api.your-site.com/v1"</span>,
  api_key=<span class="tok-str">"sk-..."</span>,
)

resp = client.chat.completions.<span class="tok-fn">create</span>(
  model=<span class="tok-str">"gpt-5.5"</span>,
  messages=[{<span class="tok-str">"role"</span>: <span class="tok-str">"user"</span>,
             <span class="tok-str">"content"</span>: <span class="tok-str">"你好"</span>}],
)
<span class="tok-cmt"># 一个密钥，立即开始</span></code></pre>
        </div>
      </section>

      <!-- Feature row -->
      <section class="mx-auto max-w-5xl border-t border-line py-16 md:py-20">
        <div class="grid gap-10 md:grid-cols-3">
          <div v-for="(f, i) in featureList" :key="i" class="text-left">
            <div class="mb-4 inline-flex h-10 w-10 items-center justify-center rounded-xl bg-clay/10 text-clay">
              <Icon :name="f.icon" size="md" />
            </div>
            <h3 class="font-display text-xl font-semibold text-ink">{{ f.title }}</h3>
            <p class="mt-2 text-[15px] leading-relaxed text-muted">{{ f.desc }}</p>
          </div>
        </div>
      </section>

      <!-- Bottom CTA — editorial horizontal -->
      <section class="mx-auto max-w-5xl pb-24">
        <div
          class="cta-band flex flex-col items-start justify-between gap-6 rounded-2xl px-8 py-10 md:flex-row md:items-center md:px-12"
        >
          <div class="text-left">
            <h2 class="font-display text-2xl font-semibold tracking-tight text-ink md:text-3xl">
              {{ t('home.cta.title') }}
            </h2>
            <p class="mt-2 max-w-md text-base text-muted">{{ t('home.cta.description') }}</p>
          </div>
          <router-link
            :to="isAuthenticated ? dashboardPath : '/register'"
            class="inline-flex shrink-0 items-center gap-2 rounded-xl bg-ink px-7 py-3 text-base font-medium text-paper transition-all hover:-translate-y-0.5"
          >
            {{ isAuthenticated ? t('home.goToDashboard') : t('home.cta.button') }}
            <Icon name="arrowRight" size="md" :stroke-width="2" />
          </router-link>
        </div>
      </section>
    </main>

    <!-- Footer -->
    <footer class="relative z-10 border-t border-line px-6 py-8 md:px-10">
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
/* ===== Anthropic-inspired warm minimal palette ===== */
.landing {
  --paper: #faf9f5; /* page background (ivory) */
  --card: #ffffff;
  --ink: #1a1915; /* near-black text */
  --muted: #5f5b51; /* secondary text */
  --faint: #8c887d; /* tertiary text */
  --line: #e7e2d6; /* hairline borders */
  --clay: #cc785c; /* Anthropic book cloth accent */
  --clay-dark: #b5634a;
  --tok-str: #6f8159; /* code string token */

  background-color: var(--paper);
  color: var(--ink);
  font-feature-settings: 'ss01';
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
  --tok-str: #9bb07e;
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
.bg-paper {
  background-color: var(--paper);
}
.bg-card {
  background-color: var(--card);
}
.bg-clay {
  background-color: var(--clay);
}
.hover\:bg-clay-dark:hover {
  background-color: var(--clay-dark);
}
.text-clay {
  color: var(--clay);
}
.bg-clay\/10 {
  background-color: color-mix(in srgb, var(--clay) 12%, transparent);
}
.border-line {
  border-color: var(--line);
}

/* Soft warm glow behind hero */
.accent-glow {
  background: radial-gradient(
    circle at center,
    color-mix(in srgb, var(--clay) 18%, transparent) 0%,
    transparent 65%
  );
  filter: blur(40px);
  opacity: 0.7;
}

/* Bottom CTA band */
.cta-band {
  background: color-mix(in srgb, var(--clay) 7%, var(--card));
  border: 1px solid var(--line);
}

/* Signature code card */
.code-card {
  background-color: var(--card);
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.04);
}
.code-dot {
  width: 9px;
  height: 9px;
  border-radius: 9999px;
  background-color: var(--line);
}
.code-body {
  font-family: ui-monospace, 'SFMono-Regular', Menlo, Monaco, Consolas, monospace;
  color: color-mix(in srgb, var(--ink) 82%, transparent);
  font-feature-settings: normal;
}
.code-body .tok-kw {
  color: var(--clay);
}
.code-body .tok-str {
  color: var(--tok-str);
}
.code-body .tok-fn {
  color: var(--ink);
  font-weight: 600;
}
.code-body .tok-cmt {
  color: var(--faint);
  font-style: italic;
}

/* Header icon buttons */
.icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 0.5rem;
  border-radius: 0.625rem;
  color: var(--faint);
  transition: all 0.15s ease;
}
.icon-btn:hover {
  color: var(--ink);
  background-color: color-mix(in srgb, var(--ink) 5%, transparent);
}
</style>
