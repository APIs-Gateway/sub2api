<template>
  <div class="docs-page">
    <!-- 页头：左边站点标识，右边语言、主题、登录或控制台 -->
    <header class="docs-header">
      <div class="docs-header-inner">
        <RouterLink to="/home" class="docs-brand">
          <span class="docs-logo">
            <img :src="siteLogo || '/logo.png'" alt="" class="h-full w-full object-contain" />
          </span>
          <span class="docs-brand-name">{{ siteName }}</span>
        </RouterLink>
        <div class="flex flex-shrink-0 items-center gap-1">
          <LocaleSwitcher />
          <button
            type="button"
            class="docs-icon-btn"
            :title="isDark ? t('home.switchToLight') : t('home.switchToDark')"
            :aria-label="isDark ? t('home.switchToLight') : t('home.switchToDark')"
            @click="toggleTheme"
          >
            <Icon :name="isDark ? 'sun' : 'moon'" size="md" />
          </button>
          <RouterLink :to="isAuthenticated ? dashboardPath : '/login'" class="docs-pill ml-1.5">
            {{ isAuthenticated ? t('home.dashboard') : t('home.login') }}
          </RouterLink>
        </div>
      </div>
    </header>

    <!-- 通栏深色带：标题和按阅读顺序编号的目录 -->
    <div ref="heroRef" class="docs-hero">
      <h1 class="docs-hero-title">{{ siteName }} {{ t('docs.title') }}</h1>
      <nav class="docs-hero-toc" :aria-label="t('docs.menu')">
        <ol>
          <li v-for="(g, index) in groups" :key="g.id" :style="{ '--i': index }">
            <a :href="`#${g.items[0].id}`" @click.prevent="goTo(g.items[0].id)">
              <span class="docs-hero-num" aria-hidden="true">({{ index + 1 }})</span>
              <span class="docs-hero-rule" aria-hidden="true"></span>
              <span class="docs-hero-name">{{ g.label }}</span>
            </a>
          </li>
        </ol>
      </nav>
    </div>

    <!-- 窄屏：当前所在章节 + 展开目录 -->
    <div class="docs-bar lg:hidden">
      <button
        type="button"
        class="docs-bar-toggle"
        :aria-expanded="menuOpen"
        aria-controls="docs-menu"
        data-testid="docs-menu-toggle"
        @click="menuOpen = !menuOpen"
      >
        <span class="min-w-0 truncate">
          <span class="docs-bar-label">{{ t('docs.menu') }}</span>
          <span class="docs-bar-title">{{ activeTitle }}</span>
        </span>
        <Icon name="chevronDown" size="sm" class="flex-shrink-0 transition-transform" :class="menuOpen ? 'rotate-180' : ''" />
      </button>
      <nav v-if="menuOpen" id="docs-menu" class="docs-bar-menu" :aria-label="t('docs.menu')">
        <div v-for="g in groups" :key="g.id" class="docs-bar-group">
          <p class="docs-bar-group-label">{{ g.label }}</p>
          <ul>
            <li v-for="item in g.items" :key="item.id">
              <a
                :href="`#${item.id}`"
                :data-toc-id="item.id"
                :aria-current="activeSectionId === item.id ? 'true' : undefined"
                class="docs-bar-link"
                :class="{ 'is-active': activeSectionId === item.id }"
                @click.prevent="goTo(item.id, true)"
              >{{ item.title }}</a>
            </li>
          </ul>
        </div>
      </nav>
    </div>

    <!-- 宽屏：贴着左边缘的刻度目录，一节一条；悬停或键盘聚焦时展开节名 -->
    <nav
      class="docs-rail"
      :class="{ 'is-visible': railVisible }"
      :aria-label="t('docs.tocTitle')"
      data-testid="docs-toc"
    >
      <ul>
        <li v-for="(g, index) in groups" :key="g.id" class="docs-rail-group">
          <p class="docs-rail-group-label" aria-hidden="true">({{ index + 1 }}) {{ g.label }}</p>
          <ul>
            <li v-for="item in g.items" :key="item.id">
              <a
                :href="`#${item.id}`"
                :data-toc-id="item.id"
                :aria-current="activeSectionId === item.id ? 'true' : undefined"
                class="docs-rail-link"
                :class="{ 'is-active': activeSectionId === item.id }"
                @click.prevent="goTo(item.id)"
              >
                <span class="docs-rail-tick" aria-hidden="true"></span>
                <span class="docs-rail-label">{{ item.title }}</span>
              </a>
            </li>
          </ul>
        </li>
      </ul>
    </nav>

    <main class="docs-main" @click="onContentClick">
      <div class="docs-col">
        <p class="docs-lead">{{ t('docs.intro', { site: siteName }) }}</p>
        <p v-if="!isChineseUi" class="docs-note" data-testid="docs-lang-note">
          {{ t('docs.contentNotice') }}
        </p>

        <!-- 接入信息：地址随站点设置变化，点一下复制 -->
        <dl class="docs-connect" data-testid="docs-connect">
          <!-- 站点配了备用地址才出现。选哪个，下面的地址和全页示例就用哪个 -->
          <div v-if="endpointOptions.length > 1" class="docs-connect-row docs-connect-lines">
            <dt id="docs-endpoints-label">{{ t('keys.endpoints.title') }}</dt>
            <dd>
              <div class="docs-lines-body">
                <div
                  class="docs-lines"
                  role="radiogroup"
                  aria-labelledby="docs-endpoints-label"
                  aria-describedby="docs-endpoints-hint"
                  data-testid="docs-endpoints"
                >
                  <label v-for="opt in endpointOptions" :key="opt.id" class="docs-line">
                    <input v-model="endpointChoice" type="radio" name="docs-endpoint" class="docs-line-radio" :value="opt.id" />
                    <span class="docs-line-head">
                      <span class="docs-line-name">{{ opt.isDefault ? t('keys.endpoints.default') : opt.name }}</span>
                      <span v-if="opt.description" class="docs-line-desc">{{ opt.description }}</span>
                    </span>
                    <code class="docs-line-url">{{ opt.base }}</code>
                  </label>
                </div>
                <p id="docs-endpoints-hint" class="docs-lines-hint">{{ t('docs.connect.endpointHint') }}</p>
              </div>
            </dd>
          </div>
          <div v-for="row in connectRows" :key="row.id" class="docs-connect-row">
            <dt>{{ row.label }}</dt>
            <dd>
              <code :data-testid="`docs-connect-${row.id}`">{{ row.value }}</code>
              <button
                v-if="row.copyable"
                type="button"
                class="docs-copy-inline"
                :data-copied="copiedRow === row.id ? 'true' : undefined"
                @click="copyRow(row.id, row.value)"
              >
                {{ copiedRow === row.id ? t('docs.copied') : t('docs.copy') }}
              </button>
              <RouterLink v-else :to="row.to!" class="docs-link">
                {{ row.linkLabel }}
              </RouterLink>
            </dd>
          </div>
        </dl>
      </div>

      <article class="docs-article" data-testid="docs-article">
        <section
          v-for="s in sections"
          :id="s.id"
          :key="s.id"
          class="docs-section"
          :class="{ 'docs-section-part': partStarts.has(s.id) }"
        >
          <div class="docs-col">
            <!-- 每一部分的第一节顶上，重复一次首屏目录里的编号 -->
            <p v-if="partStarts.get(s.id)" class="docs-part" aria-hidden="true">
              <span class="docs-part-num">({{ partStarts.get(s.id)!.n }})</span>
              <span class="docs-part-rule"></span>
              <span class="docs-part-name">{{ partStarts.get(s.id)!.label }}</span>
            </p>

            <h2 class="docs-h2">{{ s.title }}</h2>
            <div class="docs-prose" v-html="s.html"></div>

            <DocsAiPrompts
              v-if="s.id === 'ai-assist'"
              class="mt-8"
              :vars="vars"
              :endpoint="activeEndpoint.isDefault ? undefined : activeEndpoint.base"
              :prompts-raw="aiPromptsRaw"
              :get-full-doc="getFullDoc"
            />

            <p v-if="s.id === 'models'" class="docs-models-link" data-testid="docs-models-link">
              <template v-if="isAuthenticated">
                <RouterLink to="/available-channels" class="docs-link">{{ t('docs.models.open') }}</RouterLink>
              </template>
              <template v-else>
                <span class="docs-muted">{{ t('docs.models.loginHint') }}</span>
                <RouterLink :to="{ path: '/login', query: { redirect: '/available-channels' } }" class="docs-link ml-1">{{ t('docs.models.login') }}</RouterLink>
              </template>
            </p>
          </div>
        </section>
      </article>
    </main>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import LocaleSwitcher from '@/components/common/LocaleSwitcher.vue'
import { useAppStore, useAuthStore } from '@/stores'
import { sanitizeUrl } from '@/utils/url'
import DocsAiPrompts from './DocsAiPrompts.vue'
import { DOC_GROUPS } from './sections'
import { fillMachineText, fullMarkdown } from './docsMachine'
import aiPromptsRaw from './ai-prompts.md?raw'
import {
  EXAMPLE_MODEL,
  copyText,
  loadSavedEndpointId,
  pickEndpoint,
  renderSection,
  resolveEndpointOptions,
  saveEndpointId,
  splitSection,
  type DocVars,
} from './docsRender'

const { t, locale } = useI18n()
const route = useRoute()
const router = useRouter()
const appStore = useAppStore()
const authStore = useAuthStore()

const settings = computed(() => appStore.cachedPublicSettings)
const siteName = computed(() => settings.value?.site_name || 'Sub2API')
const siteLogo = computed(() =>
  sanitizeUrl(settings.value?.site_logo || '', { allowRelative: true, allowDataUrl: true })
)
const isAuthenticated = computed(() => authStore.isAuthenticated)
const dashboardPath = computed(() => (authStore.isAdmin ? '/admin/dashboard' : '/dashboard'))
const isChineseUi = computed(() => locale.value === 'zh-CN')

// ---- 地址和占位符 ----
const origin = typeof window !== 'undefined' ? window.location.origin : ''

// 默认地址加上管理员配的备用地址（比如 CDN 加速域名）。读者选哪个，全页的示例和提示词就用哪个。
const endpointOptions = computed(() =>
  resolveEndpointOptions(settings.value?.api_base_url, settings.value?.custom_endpoints, origin)
)
const savedEndpointId = ref(loadSavedEndpointId())
/** 保存的那个地址已经不在站点设置里时，自动回到默认地址，但不改写已存的值：设置还没加载完时也是这个状态。 */
const activeEndpoint = computed(() => pickEndpoint(endpointOptions.value, savedEndpointId.value))
const endpointChoice = computed({
  get: () => activeEndpoint.value.id,
  set: (id: string) => {
    savedEndpointId.value = id
    saveEndpointId(id)
  },
})

const vars = computed<DocVars>(() => ({
  base: activeEndpoint.value.base,
  v1: activeEndpoint.value.v1,
  site: siteName.value,
  model: EXAMPLE_MODEL,
  llms: `${origin}/llms.txt`,
  origin,
}))

const sections = computed(() => {
  const labels = { copy: t('docs.copy'), copied: t('docs.copied') }
  return DOC_GROUPS.flatMap((g) => g.sections).map((s) => renderSection(s.id, s.raw, vars.value, labels))
})

/** 「复制整份文档」：和 /llms-full.txt 同一份内容，占位符换成当前选中的地址。 */
function getFullDoc(): string {
  return fillMachineText(fullMarkdown(), {
    site: siteName.value,
    apiBaseUrl: activeEndpoint.value.base,
    origin,
  })
}

// ---- 目录 ----
const groups = computed(() =>
  DOC_GROUPS.map((g) => ({
    id: g.id,
    label: t(`docs.groups.${g.id}`),
    items: g.sections.map((s) => ({ id: s.id, title: splitSection(s.raw).title })),
  }))
)

/** 每一部分的第一节：正文里在它的标题上方重复一次首屏目录的编号。 */
const partStarts = computed(
  () => new Map(groups.value.map((g, index) => [g.items[0].id, { n: index + 1, label: g.label }]))
)

const menuOpen = ref(false)
const activeSectionId = ref(DOC_GROUPS[0].sections[0].id)
const activeTitle = computed(() => sections.value.find((s) => s.id === activeSectionId.value)?.title ?? '')

const connectRows = computed(() => [
  { id: 'openai', label: t('docs.connect.openai'), value: vars.value.v1, copyable: true },
  { id: 'anthropic', label: t('docs.connect.anthropic'), value: vars.value.base, copyable: true },
  {
    id: 'key',
    label: t('docs.connect.key'),
    value: t('docs.connect.keyPlaceholder'),
    copyable: false,
    to: '/keys',
    linkLabel: t('docs.connect.createKey'),
  },
])

// ---- 滚动定位 ----
const SPY_OFFSET = 120

const heroRef = ref<HTMLElement | null>(null)
/** 刻度目录等首屏深色带滚过去之后再出现，不盖在标题上。 */
const railVisible = ref(false)

function prefersReducedMotion(): boolean {
  return typeof window.matchMedia === 'function' && window.matchMedia('(prefers-reduced-motion: reduce)').matches
}

function goTo(id: string, closeMenu = false) {
  const el = document.getElementById(id)
  if (!el) return
  if (closeMenu) menuOpen.value = false
  window.history.replaceState(window.history.state, '', `#${id}`)
  el.scrollIntoView({ behavior: prefersReducedMotion() ? 'instant' : 'smooth', block: 'start' })
}

function updateActive() {
  let section = sections.value[0]?.id ?? ''
  for (const s of sections.value) {
    const el = document.getElementById(s.id)
    if (el && el.getBoundingClientRect().top <= SPY_OFFSET) section = s.id
  }
  activeSectionId.value = section

  const hero = heroRef.value
  railVisible.value = hero ? hero.getBoundingClientRect().bottom < window.innerHeight * 0.6 : true
}

let frame = 0
function onScroll() {
  if (frame) return
  frame = window.requestAnimationFrame(() => {
    frame = 0
    updateActive()
  })
}

// ---- 复制 ----
const copiedRow = ref('')
let rowTimer: ReturnType<typeof setTimeout> | null = null
async function copyRow(id: string, value: string) {
  if (!(await copyText(value))) return
  copiedRow.value = id
  if (rowTimer) clearTimeout(rowTimer)
  rowTimer = setTimeout(() => (copiedRow.value = ''), 1800)
}

/** 正文是 v-html 渲染的，复制按钮和站内链接都在这里统一处理。 */
async function onContentClick(event: MouseEvent) {
  const target = event.target as HTMLElement | null
  if (!target) return

  const button = target.closest<HTMLElement>('[data-docs-copy]')
  if (button) {
    const code = button.closest('figure')?.querySelector('code')?.textContent ?? ''
    if (await copyText(code)) {
      button.setAttribute('data-copied', 'true')
      window.setTimeout(() => button.removeAttribute('data-copied'), 1800)
    }
    return
  }

  const anchor = target.closest<HTMLAnchorElement>('.docs-prose a')
  const href = anchor?.getAttribute('href') ?? ''
  if (anchor && href.startsWith('/') && !href.startsWith('//')) {
    event.preventDefault()
    void router.push(href)
  }
}

// ---- 主题 ----
const isDark = ref(typeof document !== 'undefined' && document.documentElement.classList.contains('dark'))
function toggleTheme() {
  isDark.value = !isDark.value
  document.documentElement.classList.toggle('dark', isDark.value)
  localStorage.setItem('theme', isDark.value ? 'dark' : 'light')
}

onMounted(async () => {
  window.addEventListener('scroll', onScroll, { passive: true })
  void appStore.fetchPublicSettings()
  await nextTick()
  if (route.hash) {
    document.getElementById(decodeURIComponent(route.hash.slice(1)))?.scrollIntoView()
  }
  updateActive()
})

onBeforeUnmount(() => {
  window.removeEventListener('scroll', onScroll)
  if (frame) window.cancelAnimationFrame(frame)
  if (rowTimer) clearTimeout(rowTimer)
})
</script>

<style scoped>
/*
 * 版式参考 Anthropic 的长文页：
 * 深色通栏 hero，暖白（深色模式下是暖炭）阅读面，单栏衬线标题加无衬线正文，
 * 发丝线表格，贴左缘的刻度目录。陶土色只用在链接、提示块的细线和表格重点列。
 * 颜色全部走下面这组变量，明暗各一套，不是简单反色。
 */
.docs-page {
  --d-paper: #faf9f5;
  --d-ink: #1a1814;
  --d-text: #3a352e;
  --d-muted: #6d6659;
  --d-faint: #918a7c;
  --d-rule: #d9d3c6;
  --d-hair: #e9e5db;
  --d-wash: #f3f1ea;
  --d-chip: rgba(58, 53, 46, 0.07);
  --d-accent: #964f3b;
  --d-accent-line: #dcab8e;
  --d-accent-wash: #fbf3ed;
  --d-callout: #d18d67;
  --d-pill-bg: #1a1814;
  --d-pill-fg: #faf9f5;
  --d-hero-bg: #141310;
  --d-hero-ink: #faf9f5;
  --d-hero-muted: #b4ab9a;
  --d-hero-faint: #8c8475;
  --d-hero-rule: #4f4940;
  --d-shadow: 0 8px 30px rgba(38, 36, 32, 0.12);
  --docs-col: 42rem;
  --docs-wide: min(56rem, 100vw - 2.5rem);

  min-height: 100vh;
  background: var(--d-paper);
  color: var(--d-text);
}
.dark .docs-page {
  --d-paper: #1c1b18;
  --d-ink: #f5f3ee;
  --d-text: #d3ccbe;
  --d-muted: #a39b8b;
  --d-faint: #8c8475;
  --d-rule: #3f3a33;
  --d-hair: #2f2c27;
  --d-wash: #232220;
  --d-chip: rgba(245, 243, 238, 0.09);
  --d-accent: #dcab8e;
  --d-accent-line: #7a4637;
  --d-accent-wash: rgba(204, 120, 92, 0.1);
  --d-callout: #b5634a;
  --d-pill-bg: #e9e5db;
  --d-pill-fg: #141310;
  --d-hero-bg: #0e0d0b;
  --d-hero-ink: #f5f3ee;
  --d-hero-muted: #a39b8b;
  --d-hero-faint: #6b6357;
  --d-hero-rule: #3a352e;
  --d-shadow: 0 8px 30px rgba(0, 0, 0, 0.5);
}

.docs-page :where(a, button, input):focus-visible {
  outline: 2px solid var(--d-accent);
  outline-offset: 2px;
}
.docs-hero a:focus-visible {
  outline-color: var(--d-hero-ink);
}

/* ---------- 页头 ---------- */
.docs-header {
  z-index: 30;
  border-bottom: 1px solid var(--d-hair);
  background: color-mix(in srgb, var(--d-paper) 97%, transparent);
  backdrop-filter: blur(8px);
}
@media (min-width: 1024px) {
  .docs-header {
    position: sticky;
    top: 0;
  }
}
.docs-header-inner {
  display: flex;
  height: 3.5rem;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  padding-inline: 1.25rem;
  margin-inline: auto;
  max-width: 78rem;
}
@media (min-width: 640px) {
  .docs-header-inner {
    padding-inline: 2rem;
  }
}
.docs-brand {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 0.625rem;
}
.docs-logo {
  display: flex;
  height: 1.75rem;
  width: 1.75rem;
  flex-shrink: 0;
  align-items: center;
  justify-content: center;
  overflow: hidden;
  border-radius: 0.375rem;
}
.docs-brand-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: theme('fontFamily.serif');
  font-size: 1.1875rem;
  font-weight: 600;
  color: var(--d-ink);
}
.docs-icon-btn {
  display: inline-flex;
  height: 2.25rem;
  width: 2.25rem;
  align-items: center;
  justify-content: center;
  border-radius: 9999px;
  color: var(--d-muted);
  transition: background-color 0.15s, color 0.15s;
}
.docs-icon-btn:hover {
  background: var(--d-chip);
  color: var(--d-ink);
}
.docs-pill {
  display: inline-flex;
  height: 2.25rem;
  align-items: center;
  border-radius: 9999px;
  background: var(--d-pill-bg);
  padding-inline: 1.125rem;
  font-size: 0.875rem;
  font-weight: 500;
  color: var(--d-pill-fg);
  transition: opacity 0.15s;
}
.docs-pill:hover {
  opacity: 0.86;
}

/* ---------- 首屏深色带 ---------- */
.docs-hero {
  display: flex;
  min-height: clamp(22rem, 52vh, 40rem);
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 5rem 1.25rem 5.5rem;
  background: var(--d-hero-bg);
  color: var(--d-hero-ink);
  text-align: center;
}
@media (min-width: 1024px) {
  .docs-hero {
    min-height: clamp(26rem, 64vh, 40rem);
  }
}
.docs-hero-title {
  max-width: 62rem;
  font-family: theme('fontFamily.serif');
  font-size: clamp(2.75rem, 8vw, 5.75rem);
  font-weight: 500;
  line-height: 1.08;
  letter-spacing: -0.015em;
  text-wrap: balance;
}
.docs-hero-toc {
  margin-top: 4.5rem;
  width: min(22.5rem, 100%);
}
.docs-hero-toc ol {
  list-style: none;
}
.docs-hero-toc a {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding-block: 0.4rem;
  font-size: 0.9375rem;
  line-height: 1.5;
  color: var(--d-hero-ink);
}
.docs-hero-num {
  flex-shrink: 0;
  font-variant-numeric: tabular-nums;
  color: var(--d-hero-faint);
}
.docs-hero-rule {
  height: 1px;
  flex: 1 1 auto;
  background: var(--d-hero-rule);
  transform-origin: left center;
  transition: background-color 0.2s;
}
.docs-hero-name {
  flex-shrink: 0;
  color: var(--d-hero-muted);
  transition: color 0.2s;
}
.docs-hero-toc a:hover .docs-hero-name,
.docs-hero-toc a:focus-visible .docs-hero-name {
  color: var(--d-hero-ink);
}
.docs-hero-toc a:hover .docs-hero-rule,
.docs-hero-toc a:focus-visible .docs-hero-rule {
  background: var(--d-hero-muted);
}

/* 页面载入时唯一的一段编排：标题升起，目录的引导线依次画出 */
@media (prefers-reduced-motion: no-preference) {
  .docs-hero-title {
    animation: docs-rise 0.9s cubic-bezier(0.2, 0.7, 0.2, 1) both;
  }
  .docs-hero-toc li {
    animation: docs-fade 0.6s ease both;
    animation-delay: calc(0.5s + var(--i) * 0.09s);
  }
  .docs-hero-rule {
    animation: docs-draw 0.9s cubic-bezier(0.2, 0.7, 0.2, 1) both;
    animation-delay: calc(0.5s + var(--i) * 0.09s);
  }
}
@keyframes docs-rise {
  from {
    opacity: 0;
    transform: translateY(14px);
  }
}
@keyframes docs-fade {
  from {
    opacity: 0;
  }
}
@keyframes docs-draw {
  from {
    transform: scaleX(0);
  }
}

/* ---------- 窄屏目录条 ---------- */
.docs-bar {
  position: sticky;
  top: 0;
  z-index: 20;
  border-bottom: 1px solid var(--d-hair);
  background: var(--d-paper);
}
.docs-bar-toggle {
  display: flex;
  width: 100%;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  padding: 0.75rem 1.25rem;
  text-align: left;
  font-size: 0.875rem;
  color: var(--d-ink);
}
.docs-bar-label {
  color: var(--d-muted);
}
.docs-bar-title {
  margin-left: 0.625rem;
  font-weight: 500;
}
.docs-bar-menu {
  max-height: 60vh;
  overflow-y: auto;
  border-top: 1px solid var(--d-hair);
  padding: 0.75rem 1.25rem 1.25rem;
}
.docs-bar-group + .docs-bar-group {
  margin-top: 1.25rem;
}
.docs-bar-group-label {
  margin-bottom: 0.25rem;
  font-size: 0.75rem;
  color: var(--d-muted);
}
.docs-bar-group ul {
  border-left: 1px solid var(--d-rule);
}
.docs-bar-link {
  display: block;
  margin-left: -1px;
  border-left: 2px solid transparent;
  padding: 0.35rem 0 0.35rem 0.875rem;
  font-size: 0.9375rem;
  color: var(--d-muted);
}
.docs-bar-link.is-active {
  border-left-color: var(--d-ink);
  font-weight: 500;
  color: var(--d-ink);
}

/* ---------- 宽屏刻度目录 ---------- */
.docs-rail {
  display: none;
}
@media (min-width: 1024px) {
  .docs-rail {
    position: fixed;
    top: 50%;
    left: 0;
    z-index: 25;
    display: block;
    width: 2.5rem;
    max-height: calc(100vh - 9rem);
    overflow: hidden auto;
    padding-block: 0.5rem;
    border-right: 1px solid transparent;
    border-radius: 0 0.375rem 0.375rem 0;
    background: transparent;
    opacity: 0;
    visibility: hidden;
    transform: translateY(-50%);
    transition: opacity 0.3s, visibility 0s linear 0.3s, width 0.22s ease, background-color 0.2s,
      border-color 0.2s, box-shadow 0.2s;
    scrollbar-width: none;
  }
  .docs-rail::-webkit-scrollbar {
    display: none;
  }
  .docs-rail.is-visible {
    opacity: 1;
    visibility: visible;
    transition: opacity 0.3s, visibility 0s, width 0.22s ease, background-color 0.2s, border-color 0.2s,
      box-shadow 0.2s;
  }
  .docs-rail.is-visible:hover,
  .docs-rail.is-visible:focus-within {
    width: 17rem;
    border-right-color: var(--d-hair);
    background: var(--d-paper);
    box-shadow: var(--d-shadow);
  }
}
.docs-rail ul {
  list-style: none;
}
.docs-rail-group-label {
  display: flex;
  height: 1.625rem;
  align-items: flex-end;
  padding-left: 2.5rem;
  padding-bottom: 0.125rem;
  white-space: nowrap;
  font-size: 0.75rem;
  color: var(--d-muted);
  opacity: 0;
  transition: opacity 0.15s;
}
.docs-rail:hover .docs-rail-group-label,
.docs-rail:focus-within .docs-rail-group-label {
  opacity: 1;
}
.docs-rail-link {
  display: flex;
  height: 1.25rem;
  align-items: center;
  white-space: nowrap;
}
.docs-rail-link:focus-visible {
  outline-offset: -2px;
}
.docs-rail-tick {
  position: relative;
  height: 100%;
  width: 2.5rem;
  flex-shrink: 0;
}
.docs-rail-tick::after {
  content: '';
  position: absolute;
  top: 50%;
  left: 0;
  width: 0.75rem;
  height: 1px;
  background: var(--d-faint);
  transition: width 0.2s ease, background-color 0.2s;
}
.docs-rail-link:hover .docs-rail-tick::after,
.docs-rail-link:focus-visible .docs-rail-tick::after {
  width: 1.375rem;
  background: var(--d-muted);
}
.docs-rail-link.is-active .docs-rail-tick::after {
  top: calc(50% - 0.5px);
  width: 1.375rem;
  height: 2px;
  background: var(--d-ink);
}
.docs-rail-label {
  font-size: 0.8125rem;
  color: var(--d-muted);
  opacity: 0;
  transition: opacity 0.15s, color 0.15s;
}
.docs-rail:hover .docs-rail-label,
.docs-rail:focus-within .docs-rail-label {
  opacity: 1;
}
.docs-rail-link:hover .docs-rail-label,
.docs-rail-link:focus-visible .docs-rail-label,
.docs-rail-link.is-active .docs-rail-label {
  color: var(--d-ink);
}
.docs-rail-link.is-active .docs-rail-label {
  font-weight: 500;
}
@media (prefers-reduced-motion: reduce) {
  .docs-rail,
  .docs-rail * {
    transition: none !important;
  }
}

/* ---------- 阅读栏 ---------- */
.docs-main {
  padding-block: 4.5rem 9rem;
}
.docs-col {
  width: min(var(--docs-col), 100% - 2.5rem);
  margin-inline: auto;
}
.docs-lead {
  font-family: theme('fontFamily.serif');
  font-size: clamp(1.25rem, 2.4vw, 1.375rem);
  font-weight: 500;
  line-height: 1.7;
  color: var(--d-ink);
  text-wrap: pretty;
}
.docs-note {
  margin-top: 0.875rem;
  font-size: 0.875rem;
  color: var(--d-muted);
}
.docs-muted {
  color: var(--d-muted);
}
.docs-link {
  color: var(--d-accent);
  text-decoration: underline;
  text-decoration-color: var(--d-accent-line);
  text-decoration-thickness: 1px;
  text-underline-offset: 0.25em;
  transition: text-decoration-color 0.15s;
}
.docs-link:hover {
  text-decoration-color: var(--d-accent);
}

/* 接入信息 */
.docs-connect {
  margin-top: 2.75rem;
  border-top: 1px solid var(--d-rule);
}
.docs-connect-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.25rem 1rem;
  border-bottom: 1px solid var(--d-rule);
  padding-block: 0.875rem;
}
.docs-connect-row dt {
  width: 9.5rem;
  flex-shrink: 0;
  font-size: 0.875rem;
  color: var(--d-muted);
}
.docs-connect-row dd {
  display: flex;
  min-width: 0;
  flex: 1 1 14rem;
  align-items: center;
  gap: 0.75rem;
  font-size: 0.875rem;
}
.docs-connect-row code {
  min-width: 0;
  flex: 1 1 auto;
  overflow-wrap: anywhere;
  font-family: theme('fontFamily.mono');
  font-size: 0.875rem;
  color: var(--d-ink);
}
.docs-copy-inline {
  flex-shrink: 0;
  border: 1px solid var(--d-rule);
  border-radius: 9999px;
  padding: 0.15rem 0.75rem;
  font-size: 0.75rem;
  color: var(--d-muted);
  transition: color 0.15s, border-color 0.15s, background-color 0.15s;
}
.docs-copy-inline:hover {
  border-color: var(--d-muted);
  color: var(--d-ink);
}
.docs-copy-inline[data-copied='true'] {
  border-color: var(--d-accent-line);
  color: var(--d-accent);
}

/* 接入信息里的地址选择：一组单选，每项写名称、说明和地址 */
.docs-connect-row.docs-connect-lines {
  align-items: flex-start;
}
.docs-connect-lines dt {
  padding-top: 0.3125rem;
}
.docs-lines-body {
  min-width: 0;
  flex: 1 1 auto;
}
.docs-lines {
  display: flex;
  flex-direction: column;
}
.docs-line {
  display: grid;
  grid-template-columns: 1rem minmax(0, 1fr);
  column-gap: 0.75rem;
  row-gap: 0.125rem;
  cursor: pointer;
  padding-block: 0.3125rem;
}
.docs-line-radio {
  appearance: none;
  box-sizing: border-box;
  height: 1rem;
  width: 1rem;
  margin: 0.1875rem 0 0;
  border: 1px solid var(--d-faint);
  border-radius: 9999px;
  background: transparent;
  cursor: pointer;
  transition: border-color 0.15s, background-color 0.15s;
}
.docs-line:hover .docs-line-radio {
  border-color: var(--d-muted);
}
.docs-line-radio:checked {
  border-color: var(--d-ink);
  background: var(--d-ink);
  box-shadow: inset 0 0 0 3px var(--d-paper);
}
.docs-line-head {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 0 0.75rem;
  font-size: 0.875rem;
  line-height: 1.5rem;
}
.docs-line-name {
  font-weight: 500;
  color: var(--d-muted);
  transition: color 0.15s;
}
.docs-line:hover .docs-line-name,
.docs-line-radio:checked ~ .docs-line-head .docs-line-name {
  color: var(--d-ink);
}
.docs-line-desc {
  font-size: 0.8125rem;
  color: var(--d-faint);
}
.docs-line-url {
  grid-column: 2;
  overflow-wrap: anywhere;
  font-family: theme('fontFamily.mono');
  font-size: 0.875rem;
  color: var(--d-muted);
  transition: color 0.15s;
}
.docs-line-radio:checked ~ .docs-line-url {
  color: var(--d-ink);
}
.docs-lines-hint {
  margin-top: 0.625rem;
  font-size: 0.8125rem;
  line-height: 1.6;
  color: var(--d-muted);
}

/* 章节 */
.docs-article {
  margin-top: 8rem;
}
.docs-section {
  margin-top: 6.5rem;
}
.docs-section,
.docs-prose :deep(h3) {
  scroll-margin-top: 5rem;
}
.docs-section-part {
  margin-top: 9.5rem;
}
.docs-article > .docs-section:first-child {
  margin-top: 0;
}
@media (min-width: 1024px) {
  .docs-section,
  .docs-prose :deep(h3) {
    scroll-margin-top: 5.5rem;
  }
}
.docs-part {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  margin-bottom: 3.5rem;
  font-size: 0.875rem;
  line-height: 1.5;
  color: var(--d-muted);
}
.docs-part-num {
  font-variant-numeric: tabular-nums;
  color: var(--d-faint);
}
.docs-part-rule {
  height: 1px;
  flex: 1 1 auto;
  background: var(--d-rule);
}
.docs-h2 {
  font-family: theme('fontFamily.serif');
  font-size: clamp(1.625rem, 4vw, 2rem);
  font-weight: 500;
  line-height: 1.3;
  color: var(--d-ink);
  text-wrap: balance;
}
.docs-models-link {
  margin-top: 1rem;
  line-height: 1.85;
}

/* ---------- 正文排版：无衬线 17px，行高 1.85 ---------- */
.docs-prose {
  margin-top: 1.75rem;
  font-size: 1.0625rem;
  line-height: 1.85;
  color: var(--d-text);
  overflow-wrap: anywhere;
}
.docs-prose :deep(p) {
  margin-bottom: 1.15em;
}
.docs-prose :deep(h3) {
  margin-top: 3.25rem;
  margin-bottom: 0.875rem;
  font-family: theme('fontFamily.serif');
  font-size: 1.375rem;
  font-weight: 500;
  line-height: 1.4;
  color: var(--d-ink);
  text-wrap: balance;
}
.docs-prose :deep(h3:first-child) {
  margin-top: 0;
}
.docs-prose :deep(h3 + .docs-table),
.docs-prose :deep(h3 + .docs-code) {
  margin-top: 1.25rem;
}
.docs-prose :deep(a) {
  color: var(--d-accent);
  text-decoration: underline;
  text-decoration-color: var(--d-accent-line);
  text-decoration-thickness: 1px;
  text-underline-offset: 0.25em;
  transition: text-decoration-color 0.15s;
}
.docs-prose :deep(a:hover) {
  text-decoration-color: var(--d-accent);
}
.docs-prose :deep(strong) {
  font-weight: 600;
  color: var(--d-ink);
}
.docs-prose :deep(ul),
.docs-prose :deep(ol) {
  margin-bottom: 1.15em;
  padding-left: 1.5em;
}
.docs-prose :deep(ul) {
  list-style: disc;
}
.docs-prose :deep(ol) {
  list-style: decimal;
}
.docs-prose :deep(li) {
  margin-bottom: 0.4em;
  padding-left: 0.25em;
}
.docs-prose :deep(li::marker) {
  color: var(--d-faint);
}
.docs-prose :deep(li > p) {
  margin-bottom: 0;
}

/* 提示块：只留一条细线，不上色块 */
.docs-prose :deep(blockquote) {
  margin-block: 1.75rem;
  border-left: 2px solid var(--d-callout);
  padding-left: 1.25rem;
  font-size: 0.96875rem;
  color: var(--d-text);
}
.docs-prose :deep(blockquote p) {
  margin-bottom: 0;
}

.docs-prose :deep(code) {
  border-radius: 0.25rem;
  background: var(--d-chip);
  padding: 0.1em 0.38em;
  font-family: theme('fontFamily.mono');
  font-size: 0.875em;
  color: var(--d-ink);
}

/* ---------- 表格：比阅读栏宽，发丝线网格；第二列是要照着填的那一列，用浅底和细边框标出 ---------- */
.docs-prose :deep(.docs-table) {
  width: var(--docs-wide);
  margin-block: 2.25rem;
  margin-inline: calc((100% - var(--docs-wide)) / 2);
  overflow-x: auto;
}
.docs-prose :deep(table) {
  width: 100%;
  min-width: 34rem;
  border-collapse: separate;
  border-spacing: 0;
  font-size: 0.9375rem;
  line-height: 1.65;
}
.docs-prose :deep(th),
.docs-prose :deep(td) {
  border-right: 1px solid var(--d-rule);
  border-bottom: 1px solid var(--d-rule);
  padding: 0.8rem 1rem;
  text-align: left;
  vertical-align: top;
  overflow-wrap: break-word;
}
.docs-prose :deep(th) {
  border-top: 1px solid var(--d-rule);
  font-size: 0.8125rem;
  font-weight: 600;
  line-height: 1.5;
  color: var(--d-ink);
  white-space: nowrap;
}
.docs-prose :deep(th:first-child),
.docs-prose :deep(td:first-child) {
  border-left: 1px solid var(--d-rule);
}
.docs-prose :deep(td:first-child) {
  font-weight: 500;
  color: var(--d-ink);
}
.docs-prose :deep(th:first-child),
.docs-prose :deep(td:first-child) {
  border-right-color: var(--d-accent-line);
}
.docs-prose :deep(th:nth-child(2)),
.docs-prose :deep(td:nth-child(2)) {
  border-right-color: var(--d-accent-line);
  background: var(--d-accent-wash);
}
.docs-prose :deep(th:nth-child(2)) {
  border-top-color: var(--d-accent-line);
}
.docs-prose :deep(tbody tr:last-child td:nth-child(2)) {
  border-bottom-color: var(--d-accent-line);
}
.docs-prose :deep(td code) {
  background: var(--d-chip);
}

/* ---------- 代码块：暖灰底、细边框，宽度同阅读栏 ---------- */
.docs-prose :deep(.docs-code) {
  position: relative;
  margin-block: 1.75rem;
  overflow: hidden;
  border: 1px solid var(--d-rule);
  border-radius: 0.25rem;
  background: var(--d-wash);
}
.docs-prose :deep(.docs-code figcaption) {
  display: flex;
  min-height: 2.375rem;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  border-bottom: 1px solid var(--d-hair);
  padding: 0.25rem 0.5rem 0.25rem 1rem;
  font-family: theme('fontFamily.mono');
  font-size: 0.75rem;
  line-height: 1.5;
  color: var(--d-muted);
}
/* 没有文件名的代码块不要空标题栏，复制按钮收到右上角 */
.docs-prose :deep(.docs-code-plain figcaption) {
  position: absolute;
  top: 0.375rem;
  right: 0.5rem;
  z-index: 1;
  min-height: 0;
  border-bottom: 0;
  padding: 0;
}
.docs-prose :deep(.docs-code-plain .docs-copy) {
  background: var(--d-wash);
}
.docs-prose :deep(.docs-code-plain pre) {
  padding-right: 4.75rem;
}
.docs-prose :deep(.docs-code-title) {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.docs-prose :deep(.docs-copy) {
  flex-shrink: 0;
  border: 1px solid transparent;
  border-radius: 9999px;
  padding: 0.15rem 0.7rem;
  font-family: theme('fontFamily.sans');
  font-size: 0.75rem;
  color: var(--d-muted);
  transition: color 0.15s, border-color 0.15s;
}
.docs-prose :deep(.docs-copy:hover) {
  border-color: var(--d-rule);
  color: var(--d-ink);
}
.docs-prose :deep(.docs-copy-done) {
  display: none;
}
.docs-prose :deep(.docs-copy[data-copied='true'] .docs-copy-done) {
  display: inline;
  color: var(--d-accent);
}
.docs-prose :deep(.docs-copy[data-copied='true'] .docs-copy-idle) {
  display: none;
}
.docs-prose :deep(.docs-code pre) {
  overflow-x: auto;
  padding: 1rem 1.125rem;
  font-size: 0.84375rem;
  line-height: 1.75;
  color: var(--d-text);
}
.docs-prose :deep(.docs-code code) {
  border-radius: 0;
  background: transparent;
  padding: 0;
  font-family: theme('fontFamily.mono');
  font-size: 1em;
  color: inherit;
  overflow-wrap: normal;
  white-space: pre;
}
</style>
