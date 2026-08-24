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
            class="ml-1 inline-flex items-center gap-1.5 rounded-md bg-ink px-4 py-1.5 text-[13px] font-medium text-paper transition-opacity hover:opacity-90"
          >
            {{ t('home.dashboard') }}
            <Icon name="arrowRight" size="sm" :stroke-width="2" />
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
      <section class="relative mx-auto max-w-5xl overflow-hidden pb-20 pt-20 md:pt-28">
        <!-- 签名：大号几何星芒，低透明、缓慢呼吸（填补右侧留白、增加张力） -->
        <div class="hero-burst" aria-hidden="true">
          <svg viewBox="0 0 200 200" fill="none">
            <g stroke="currentColor" stroke-width="2" stroke-linecap="round">
              <path d="M100 66 L100 6" /><path d="M117 70.6 L147 18.6" /><path d="M129.4 83 L181.4 53" />
              <path d="M134 100 L194 100" /><path d="M129.4 117 L181.4 147" /><path d="M117 129.4 L147 181.4" />
              <path d="M100 134 L100 194" /><path d="M83 129.4 L53 181.4" /><path d="M70.6 117 L18.6 147" />
              <path d="M66 100 L6 100" /><path d="M70.6 83 L18.6 53" /><path d="M83 70.6 L53 18.6" />
            </g>
          </svg>
        </div>

        <div class="relative z-10">
          <p class="reveal mb-6 text-[13px] font-medium tracking-wide text-clay" style="--d: 0ms">
            {{ mk.eyebrow }}
          </p>
          <h1
            class="reveal max-w-3xl font-display text-4xl font-semibold leading-[1.06] tracking-tight text-ink sm:text-5xl lg:text-[4rem]"
            style="--d: 70ms"
          >
            {{ heroTitle }}
          </h1>
          <p class="reveal mt-7 max-w-xl text-lg leading-relaxed text-muted" style="--d: 140ms">
            {{ heroDesc }}
          </p>

          <div class="reveal mt-9 flex flex-wrap items-center gap-x-7 gap-y-3" style="--d: 210ms">
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

          <!-- Signature: mono 端点（OpenAI 兼容） -->
          <div class="reveal mt-12 max-w-xl border-t border-line pt-5" style="--d: 280ms">
            <p class="font-mono text-[13px] leading-relaxed text-faint">
              <span class="text-clay">POST</span> /v1/chat/completions<br>
              <span class="text-clay">POST</span> /v1/responses
              <span class="ml-2 text-faint">{{ mk.endpointNote }}</span>
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
        </div>
      </section>

      <!-- 订阅价值：深墨色对比带 + 大号 Fraunces 黏土数字（重要信息上移） -->
      <section class="reveal mx-auto mt-12 max-w-5xl md:mt-16">
        <div class="value-band rounded-xl px-7 py-11 md:px-12 md:py-14">
          <h2 class="font-display text-2xl font-semibold tracking-tight band-ink md:text-3xl">{{ mk.valueTitle }}</h2>
          <p class="mt-2.5 max-w-xl text-[15px] leading-relaxed band-muted">{{ mk.valueDesc }}</p>
          <div class="mt-10 grid grid-cols-1 gap-8 sm:grid-cols-3">
            <div v-for="(s, i) in mk.stats" :key="i">
              <div class="font-display text-4xl font-semibold tabular-nums band-clay md:text-5xl">{{ s.value }}</div>
              <div class="mt-2 text-sm band-muted">{{ s.label }}</div>
            </div>
          </div>
        </div>
      </section>

      <!-- 随处接入 -->
      <section class="mx-auto max-w-5xl border-t border-line py-14 md:py-16">
        <h2 class="font-display text-2xl font-semibold tracking-tight text-ink md:text-3xl">{{ mk.integrateTitle }}</h2>
        <p class="mt-2 max-w-xl text-[15px] leading-relaxed text-muted">{{ mk.integrateDesc }}</p>
        <div class="mt-6 flex flex-wrap gap-2.5">
          <span
            v-for="c in mk.clients"
            :key="c"
            class="rounded-md border border-line px-3 py-1.5 text-sm font-medium text-ink/80"
          >{{ c }}</span>
        </div>
      </section>

      <!-- 订阅制 vs 传统按量付费 -->
      <section class="mx-auto max-w-5xl border-t border-line py-14 md:py-16">
        <h2 class="font-display text-2xl font-semibold tracking-tight text-ink md:text-3xl">{{ mk.compareTitle }}</h2>
        <div class="mt-8 grid gap-px overflow-hidden rounded-md border border-line bg-[var(--line)] md:grid-cols-2">
          <div class="bg-[var(--card)] px-6 py-6">
            <p class="text-xs font-medium uppercase tracking-wide text-faint">{{ mk.paygTitle }}</p>
            <ul class="mt-3 space-y-2 text-[15px] leading-relaxed text-muted">
              <li v-for="(t, i) in mk.paygPoints" :key="i" class="flex gap-2"><span class="text-faint">—</span>{{ t }}</li>
            </ul>
          </div>
          <div class="bg-[var(--card)] px-6 py-6">
            <p class="text-xs font-medium uppercase tracking-wide text-clay">{{ mk.subTitle }}</p>
            <ul class="mt-3 space-y-2 text-[15px] leading-relaxed text-ink/85">
              <li v-for="(t, i) in mk.subPoints" :key="i" class="flex gap-2"><span class="text-clay">+</span>{{ t }}</li>
            </ul>
          </div>
        </div>
        <!-- 可透支 callout -->
        <div class="mt-6 rounded-md border border-line px-6 py-5">
          <h3 class="font-display text-lg font-semibold text-ink">{{ mk.overdraftTitle }}</h3>
          <p class="mt-1.5 max-w-2xl text-[15px] leading-relaxed text-muted">{{ mk.overdraftDesc }}</p>
        </div>
      </section>

      <!-- Editorial numbered list（次级亮点，下移到底部） -->
      <section class="mx-auto max-w-5xl border-t border-line">
        <div
          v-for="(f, i) in mk.features"
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
import { sanitizeUrl } from '@/utils/url'

const { t } = useI18n()

// 落地页营销文案（i18n: home.marketing.*；数字为占位示例，待确认后替换）
const mk = computed(() => ({
  eyebrow: t('home.marketing.eyebrow'),
  endpointNote: t('home.marketing.endpointNote'),
  valueTitle: t('home.marketing.valueTitle'),
  valueDesc: t('home.marketing.valueDesc'),
  stats: [
    { value: t('home.marketing.stat1Value'), label: t('home.marketing.stat1Label') },
    { value: t('home.marketing.stat2Value'), label: t('home.marketing.stat2Label') },
    { value: t('home.marketing.stat3Value'), label: t('home.marketing.stat3Label') },
  ],
  features: [
    { title: t('home.marketing.feature1Title'), desc: t('home.marketing.feature1Desc') },
    { title: t('home.marketing.feature2Title'), desc: t('home.marketing.feature2Desc') },
    { title: t('home.marketing.feature3Title'), desc: t('home.marketing.feature3Desc') },
  ],
  integrateTitle: t('home.marketing.integrateTitle'),
  integrateDesc: t('home.marketing.integrateDesc'),
  clients: ['Claude Code', 'Codex', 'OpenClaw', 'Hermes', 'Cherry Studio', t('home.marketing.clientAnySdk')],
  compareTitle: t('home.marketing.compareTitle'),
  paygTitle: t('home.marketing.paygTitle'),
  paygPoints: [
    t('home.marketing.paygPoint1'),
    t('home.marketing.paygPoint2'),
    t('home.marketing.paygPoint3'),
  ],
  subTitle: t('home.marketing.subTitle'),
  subPoints: [
    t('home.marketing.subPoint1'),
    t('home.marketing.subPoint2'),
    t('home.marketing.subPoint3'),
  ],
  overdraftTitle: t('home.marketing.overdraftTitle'),
  overdraftDesc: t('home.marketing.overdraftDesc'),
}))

const authStore = useAuthStore()
const appStore = useAppStore()

// Site settings - directly from appStore (already initialized from injected config)
const siteName = computed(() => appStore.cachedPublicSettings?.site_name || appStore.siteName || 'Sub2API')
const siteLogo = computed(() => sanitizeUrl(appStore.cachedPublicSettings?.site_logo || appStore.siteLogo || '', { allowRelative: true, allowDataUrl: true }))
const docUrl = computed(() => sanitizeUrl(appStore.cachedPublicSettings?.doc_url || appStore.docUrl || ''))
const homeContent = computed(() => appStore.cachedPublicSettings?.home_content || '')

// Hero copy: prefer admin-configured subtitle, else i18n default
const heroTitle = computed(() => t('home.heroTitle'))
const heroDesc = computed(() => appStore.cachedPublicSettings?.site_subtitle || t('home.heroDesc'))

// Supported models (OpenAI-focused)
const models = ['GPT-5.5', 'GPT-5.4', 'GPT-5.4-mini', '...']

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
/* 弱化 ink（ink 非 Tailwind 颜色，/NN 透明度修饰符不生效，这里显式补齐） */
.text-ink\/70 {
  color: color-mix(in srgb, var(--ink) 70%, transparent);
}
.text-ink\/80 {
  color: color-mix(in srgb, var(--ink) 80%, transparent);
}
.text-ink\/85 {
  color: color-mix(in srgb, var(--ink) 85%, transparent);
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

/* Hero 签名星芒：大号几何放射，低透明、缓慢呼吸 + 微旋 */
.hero-burst {
  position: absolute;
  top: -2.5rem;
  right: -3.5rem;
  width: 23rem;
  height: 23rem;
  color: var(--clay);
  opacity: 0.13;
  pointer-events: none;
  animation: burst-breathe 7s ease-in-out infinite;
}
.hero-burst svg {
  width: 100%;
  height: 100%;
}
@media (max-width: 768px) {
  .hero-burst {
    width: 13rem;
    height: 13rem;
    top: -1.5rem;
    right: -2.5rem;
    opacity: 0.1;
  }
}
@keyframes burst-breathe {
  0%,
  100% {
    opacity: 0.1;
    transform: rotate(0deg) scale(1);
  }
  50% {
    opacity: 0.17;
    transform: rotate(10deg) scale(1.05);
  }
}

/* 载入淡入上浮（带 --d 错峰；尊重 reduce-motion 由全局规则收敛） */
.reveal {
  animation: reveal 0.65s cubic-bezier(0.2, 0.6, 0.2, 1) both;
  animation-delay: var(--d, 0ms);
}
@keyframes reveal {
  from {
    opacity: 0;
    transform: translateY(14px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}

/* 深墨色对比带：打破全象牙、增加节奏 */
.value-band {
  background-color: #262420;
}
:global(.dark) .value-band {
  background-color: #2a2723;
  border: 1px solid var(--line);
}
.band-ink {
  color: #f5f2ea;
}
.band-muted {
  color: rgba(245, 242, 234, 0.62);
}
.band-clay {
  color: #e09372;
}
</style>
