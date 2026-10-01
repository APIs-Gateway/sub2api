<template>
  <div class="min-h-screen bg-gray-50 text-gray-900 dark:bg-dark-950 dark:text-white">
    <header class="border-b border-gray-200 bg-white/95 dark:border-dark-800 dark:bg-dark-900/95">
      <div class="mx-auto flex max-w-[78rem] items-center justify-between gap-4 px-4 py-3 sm:px-6">
        <RouterLink to="/home" class="flex min-w-0 items-center gap-3">
          <span class="flex h-9 w-9 flex-shrink-0 items-center justify-center overflow-hidden rounded-lg bg-white ring-1 ring-gray-200 dark:bg-dark-800 dark:ring-dark-700">
            <img :src="siteLogo || '/logo.png'" alt="" class="h-full w-full object-contain" />
          </span>
          <span class="truncate font-serif text-lg font-semibold text-gray-950 dark:text-white">{{ siteName }}</span>
        </RouterLink>
        <div class="flex flex-shrink-0 items-center gap-1.5">
          <LocaleSwitcher />
          <button
            type="button"
            class="inline-flex h-9 w-9 items-center justify-center rounded-md text-gray-600 transition hover:bg-gray-100 dark:text-dark-300 dark:hover:bg-dark-800"
            :title="isDark ? t('home.switchToLight') : t('home.switchToDark')"
            :aria-label="isDark ? t('home.switchToLight') : t('home.switchToDark')"
            @click="toggleTheme"
          >
            <Icon :name="isDark ? 'sun' : 'moon'" size="md" />
          </button>
          <RouterLink
            :to="isAuthenticated ? dashboardPath : '/login'"
            class="ml-1 inline-flex items-center rounded-md bg-primary-600 px-3.5 py-1.5 text-sm font-medium text-white transition hover:bg-primary-700"
          >
            {{ isAuthenticated ? t('home.dashboard') : t('home.login') }}
          </RouterLink>
        </div>
      </div>
    </header>

    <!-- 窄屏：当前所在章节 + 展开目录 -->
    <div class="sticky top-0 z-20 border-b border-gray-200 bg-gray-50 lg:hidden dark:border-dark-800 dark:bg-dark-950">
      <button
        type="button"
        class="flex w-full items-center justify-between gap-3 px-4 py-2.5 text-left text-sm sm:px-6"
        :aria-expanded="menuOpen"
        aria-controls="docs-menu"
        data-testid="docs-menu-toggle"
        @click="menuOpen = !menuOpen"
      >
        <span class="min-w-0 truncate">
          <span class="text-gray-500 dark:text-dark-400">{{ t('docs.menu') }}</span>
          <span class="ml-2 font-medium text-gray-900 dark:text-white">{{ activeTitle }}</span>
        </span>
        <Icon name="chevronDown" size="sm" class="flex-shrink-0 transition-transform" :class="menuOpen ? 'rotate-180' : ''" />
      </button>
      <nav
        v-if="menuOpen"
        id="docs-menu"
        class="max-h-[60vh] overflow-y-auto border-t border-gray-200 px-4 pb-4 pt-2 sm:px-6 dark:border-dark-800"
        :aria-label="t('docs.menu')"
      >
        <DocsNavList :groups="groups" :active-id="activeSectionId" @go="goTo($event, true)" />
      </nav>
    </div>

    <div class="mx-auto grid max-w-[78rem] gap-x-10 px-4 sm:px-6 lg:grid-cols-[13.5rem_minmax(0,1fr)] xl:grid-cols-[13.5rem_minmax(0,1fr)_12.5rem]">
      <!-- 左侧：章节目录 -->
      <aside class="hidden lg:block">
        <nav
          class="sticky top-0 max-h-screen overflow-y-auto py-10 pr-2"
          :aria-label="t('docs.tocTitle')"
          data-testid="docs-toc"
        >
          <DocsNavList :groups="groups" :active-id="activeSectionId" @go="goTo($event, true)" />
        </nav>
      </aside>

      <main class="min-w-0 pb-24 pt-10" @click="onContentClick">
        <div class="docs-intro">
          <h1 class="font-serif text-3xl font-medium tracking-tight text-gray-950 sm:text-4xl dark:text-white">
            {{ t('docs.title') }}
          </h1>
          <p class="mt-3 max-w-[70ch] text-[16px] leading-8 text-gray-600 dark:text-dark-300">
            {{ t('docs.intro', { site: siteName }) }}
          </p>
          <p v-if="!isChineseUi" class="mt-2 max-w-[70ch] text-sm text-gray-500 dark:text-dark-400" data-testid="docs-lang-note">
            {{ t('docs.contentNotice') }}
          </p>

          <!-- 接入信息：地址随站点设置变化，点一下复制 -->
          <dl class="mt-7 divide-y divide-gray-200 border-y border-gray-200 dark:divide-dark-700 dark:border-dark-700" data-testid="docs-connect">
            <div v-for="row in connectRows" :key="row.id" class="flex flex-wrap items-center gap-x-4 gap-y-1 py-3">
              <dt class="w-36 flex-shrink-0 text-sm text-gray-600 dark:text-dark-300">{{ row.label }}</dt>
              <dd class="flex min-w-0 flex-1 items-center gap-3">
                <code class="min-w-0 break-all font-mono text-sm text-gray-900 dark:text-dark-50" :data-testid="`docs-connect-${row.id}`">{{ row.value }}</code>
                <button
                  v-if="row.copyable"
                  type="button"
                  class="docs-copy-inline"
                  :data-copied="copiedRow === row.id ? 'true' : undefined"
                  @click="copyRow(row.id, row.value)"
                >
                  {{ copiedRow === row.id ? t('docs.copied') : t('docs.copy') }}
                </button>
                <RouterLink v-else :to="row.to!" class="text-sm text-primary-700 hover:underline dark:text-primary-300">
                  {{ row.linkLabel }}
                </RouterLink>
              </dd>
            </div>
          </dl>
        </div>

        <article class="mt-14" data-testid="docs-article">
          <section
            v-for="(s, index) in sections"
            :id="s.id"
            :key="s.id"
            class="docs-section scroll-mt-20"
            :class="index > 0 ? 'mt-14 border-t border-gray-200 pt-12 dark:border-dark-700' : ''"
          >
            <h2 class="font-serif text-2xl font-medium tracking-tight text-gray-950 sm:text-[1.75rem] dark:text-white">{{ s.title }}</h2>
            <div class="docs-prose mt-5" v-html="s.html"></div>

            <DocsAiPrompts v-if="s.id === 'ai-assist'" class="mt-6" :prompts="aiPrompts" />

            <p v-if="s.id === 'models'" class="mt-4 max-w-[70ch] text-[16px] leading-8" data-testid="docs-models-link">
              <template v-if="isAuthenticated">
                <RouterLink to="/available-channels" class="docs-link">{{ t('docs.models.open') }}</RouterLink>
              </template>
              <template v-else>
                <span class="text-gray-600 dark:text-dark-300">{{ t('docs.models.loginHint') }}</span>
                <RouterLink :to="{ path: '/login', query: { redirect: '/available-channels' } }" class="docs-link ml-1">{{ t('docs.models.login') }}</RouterLink>
              </template>
            </p>
          </section>
        </article>
      </main>

      <!-- 右侧：本节小标题，宽屏才显示 -->
      <aside class="hidden xl:block">
        <nav
          v-if="activeHeadings.length"
          class="sticky top-0 max-h-screen overflow-y-auto py-10"
          :aria-label="t('docs.onThisPage')"
          data-testid="docs-page-toc"
        >
          <p class="mb-3 text-sm font-medium text-gray-900 dark:text-white">{{ t('docs.onThisPage') }}</p>
          <ul class="space-y-1.5 border-l border-gray-200 dark:border-dark-700">
            <li v-for="heading in activeHeadings" :key="heading.id">
              <a
                :href="`#${heading.id}`"
                class="-ml-px block border-l-2 py-0.5 pl-3 text-[13px] leading-5 transition-colors"
                :class="
                  activeHeadingId === heading.id
                    ? 'border-primary-600 text-primary-700 dark:border-primary-400 dark:text-primary-300'
                    : 'border-transparent text-gray-600 hover:text-gray-900 dark:text-dark-300 dark:hover:text-white'
                "
                @click.prevent="goTo(heading.id)"
              >{{ heading.text }}</a>
            </li>
          </ul>
        </nav>
      </aside>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, nextTick, onBeforeUnmount, onMounted, ref, type PropType } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import LocaleSwitcher from '@/components/common/LocaleSwitcher.vue'
import { useAppStore, useAuthStore } from '@/stores'
import { sanitizeUrl } from '@/utils/url'
import DocsAiPrompts from './DocsAiPrompts.vue'
import { DOC_GROUPS } from './sections'
import aiPromptsRaw from './ai-prompts.md?raw'
import {
  EXAMPLE_MODEL,
  copyText,
  parseAiPrompts,
  renderSection,
  resolveApiBases,
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
const vars = computed<DocVars>(() => ({
  ...resolveApiBases(settings.value?.api_base_url, origin),
  site: siteName.value,
  model: EXAMPLE_MODEL,
  llms: `${origin}/llms.txt`,
}))

const sections = computed(() => {
  const labels = { copy: t('docs.copy'), copied: t('docs.copied') }
  return DOC_GROUPS.flatMap((g) => g.sections).map((s) => renderSection(s.id, s.raw, vars.value, labels))
})
const aiPrompts = computed(() => parseAiPrompts(aiPromptsRaw, vars.value))

// ---- 目录 ----
const groups = computed(() =>
  DOC_GROUPS.map((g) => ({
    id: g.id,
    label: t(`docs.groups.${g.id}`),
    items: g.sections.map((s) => ({ id: s.id, title: splitSection(s.raw).title })),
  }))
)

const DocsNavList = defineComponent({
  name: 'DocsNavList',
  props: {
    groups: { type: Array as PropType<Array<{ id: string; label: string; items: Array<{ id: string; title: string }> }>>, required: true },
    activeId: { type: String, default: '' },
  },
  emits: ['go'],
  setup(props, { emit }) {
    return () =>
      h(
        'div',
        { class: 'space-y-6' },
        props.groups.map((g) =>
          h('div', { key: g.id }, [
            h('p', { class: 'mb-2 text-xs text-gray-500 dark:text-dark-400' }, g.label),
            h(
              'ul',
              { class: 'border-l border-gray-200 dark:border-dark-700' },
              g.items.map((item) =>
                h('li', { key: item.id }, [
                  h(
                    'a',
                    {
                      href: `#${item.id}`,
                      'data-toc-id': item.id,
                      'aria-current': props.activeId === item.id ? 'true' : undefined,
                      class: [
                        '-ml-px block border-l-2 py-1 pl-3 text-sm transition-colors',
                        props.activeId === item.id
                          ? 'border-primary-600 font-medium text-primary-700 dark:border-primary-400 dark:text-primary-300'
                          : 'border-transparent text-gray-700 hover:text-gray-950 dark:text-dark-200 dark:hover:text-white',
                      ],
                      onClick: (e: MouseEvent) => {
                        e.preventDefault()
                        emit('go', item.id)
                      },
                    },
                    item.title
                  ),
                ])
              )
            ),
          ])
        )
      )
  },
})

const menuOpen = ref(false)
const activeSectionId = ref(DOC_GROUPS[0].sections[0].id)
const activeHeadingId = ref('')
const activeTitle = computed(() => sections.value.find((s) => s.id === activeSectionId.value)?.title ?? '')
const activeHeadings = computed(() => sections.value.find((s) => s.id === activeSectionId.value)?.headings ?? [])

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

function prefersReducedMotion(): boolean {
  return typeof window.matchMedia === 'function' && window.matchMedia('(prefers-reduced-motion: reduce)').matches
}

function goTo(id: string, closeMenu = false) {
  const el = document.getElementById(id)
  if (!el) return
  if (closeMenu) menuOpen.value = false
  window.history.replaceState(window.history.state, '', `#${id}`)
  el.scrollIntoView({ behavior: prefersReducedMotion() ? 'auto' : 'smooth', block: 'start' })
}

function updateActive() {
  let section = sections.value[0]?.id ?? ''
  for (const s of sections.value) {
    const el = document.getElementById(s.id)
    if (el && el.getBoundingClientRect().top <= SPY_OFFSET) section = s.id
  }
  activeSectionId.value = section

  let heading = ''
  for (const item of activeHeadings.value) {
    const el = document.getElementById(item.id)
    if (el && el.getBoundingClientRect().top <= SPY_OFFSET) heading = item.id
  }
  activeHeadingId.value = heading
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
.docs-copy-inline {
  @apply flex-shrink-0 rounded-md border border-gray-300 px-2.5 py-0.5 text-xs text-gray-700 transition-colors hover:bg-gray-100 dark:border-dark-600 dark:text-dark-200 dark:hover:bg-dark-800;
}
.docs-copy-inline[data-copied='true'] {
  @apply border-primary-300 text-primary-700 dark:border-primary-700 dark:text-primary-300;
}
.docs-link {
  @apply text-primary-700 underline underline-offset-4 hover:text-primary-800 dark:text-primary-300 dark:hover:text-primary-200;
}

/* ---- 正文排版：阅读型，行宽约 70 个字符 ---- */
.docs-prose {
  @apply text-[16px] leading-[1.9] text-gray-700 dark:text-dark-200;
  overflow-wrap: anywhere;
}
.docs-prose > :deep(p),
.docs-prose > :deep(ul),
.docs-prose > :deep(ol),
.docs-prose > :deep(blockquote) {
  max-width: 70ch;
}
.docs-prose :deep(p) {
  @apply mb-4;
}
.docs-prose :deep(h3) {
  @apply mb-3 mt-10 scroll-mt-20 font-serif text-xl font-medium tracking-tight text-gray-950 dark:text-white;
}
.docs-prose :deep(a) {
  @apply text-primary-700 underline underline-offset-4 hover:text-primary-800 dark:text-primary-300 dark:hover:text-primary-200;
}
.docs-prose :deep(strong) {
  @apply font-semibold text-gray-950 dark:text-white;
}
.docs-prose :deep(ul) {
  @apply mb-4 list-disc pl-6;
}
.docs-prose :deep(ol) {
  @apply mb-4 list-decimal pl-6;
}
.docs-prose :deep(li) {
  @apply mb-1.5 pl-1;
}
.docs-prose :deep(li > p) {
  @apply mb-0;
}
.docs-prose :deep(blockquote) {
  @apply my-6 border-l-2 border-primary-400 pl-4 text-gray-700 dark:border-primary-500 dark:text-dark-200;
}
.docs-prose :deep(blockquote p) {
  @apply mb-0;
}
.docs-prose :deep(code) {
  @apply rounded bg-gray-200/70 px-1.5 py-0.5 font-mono text-[0.875em] text-gray-900 dark:bg-dark-800 dark:text-dark-50;
}
.docs-prose :deep(table) {
  @apply my-6 block w-full max-w-full overflow-x-auto border-collapse text-[15px] leading-7;
}
.docs-prose :deep(th) {
  @apply border-b border-gray-300 px-3 py-2 text-left font-medium text-gray-900 dark:border-dark-600 dark:text-white;
}
.docs-prose :deep(td) {
  @apply border-b border-gray-200 px-3 py-2 align-top dark:border-dark-700;
}

/* ---- 代码块：文件名在左，复制在右 ---- */
.docs-prose :deep(.docs-code) {
  @apply my-5 overflow-hidden rounded-lg bg-gray-900 dark:bg-dark-900 dark:ring-1 dark:ring-dark-700;
}
.docs-prose :deep(.docs-code figcaption) {
  @apply flex items-center justify-between gap-3 bg-gray-800 px-4 py-1.5 text-xs text-gray-300 dark:bg-dark-800;
}
.docs-prose :deep(.docs-code-title) {
  @apply min-w-0 truncate;
}
.docs-prose :deep(.docs-copy) {
  @apply flex-shrink-0 rounded px-2 py-0.5 text-xs text-gray-300 transition-colors hover:bg-gray-700 hover:text-white dark:hover:bg-dark-700;
}
.docs-prose :deep(.docs-copy-done) {
  display: none;
}
.docs-prose :deep(.docs-copy[data-copied='true'] .docs-copy-done) {
  display: inline;
  @apply text-primary-300;
}
.docs-prose :deep(.docs-copy[data-copied='true'] .docs-copy-idle) {
  display: none;
}
.docs-prose :deep(.docs-code pre) {
  @apply overflow-x-auto px-4 py-3.5 text-[13px] leading-6 text-gray-100;
}
.docs-prose :deep(.docs-code code) {
  @apply rounded-none bg-transparent p-0 font-mono text-[1em] text-inherit;
  overflow-wrap: normal;
  white-space: pre;
}
</style>
