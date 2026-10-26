<template>
  <Teleport to="body">
    <Transition name="fallback-drawer">
      <div
        v-if="show"
        class="fixed inset-0 z-50 flex justify-end bg-black/40"
        data-test="drawer-overlay"
        @click.self="emit('close')"
      >
        <aside
          ref="panel"
          class="drawer-panel flex h-full w-full flex-col bg-white shadow-overlay dark:bg-dark-800 sm:max-w-[30rem] sm:border-l sm:border-gray-200 sm:dark:border-dark-700"
          role="dialog"
          aria-modal="true"
          :aria-labelledby="titleId"
          tabindex="-1"
          data-test="drawer"
          @keydown.tab="onTab"
        >
          <header class="flex items-start justify-between gap-3 border-b border-gray-200 px-4 py-4 dark:border-dark-700 sm:px-6">
            <div class="min-w-0">
              <h2 :id="titleId" class="text-base font-semibold text-gray-900 dark:text-white">
                {{ t('keyFallback.drawer.title') }}
              </h2>
              <p class="mt-1 text-sm leading-relaxed text-gray-600 dark:text-gray-400" data-test="intro">
                {{ t('keyFallback.drawer.intro') }}
              </p>
            </div>
            <button
              ref="closeBtn"
              type="button"
              class="-mr-2 rounded-lg p-2 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 dark:text-gray-400 dark:hover:bg-dark-700 dark:hover:text-gray-100"
              :aria-label="t('keyFallback.drawer.close')"
              data-test="drawer-close"
              @click="emit('close')"
            >
              <Icon name="x" size="md" />
            </button>
          </header>

          <div class="min-h-0 flex-1 overflow-y-auto px-4 py-4 sm:px-6">
            <div v-if="loading" class="space-y-3" data-test="drawer-loading">
              <div class="h-12 animate-pulse rounded-lg bg-gray-100 dark:bg-dark-700" />
              <div class="h-12 animate-pulse rounded-lg bg-gray-100 dark:bg-dark-700" />
            </div>

            <div
              v-else-if="loadError"
              class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900/50 dark:bg-red-900/20 dark:text-red-300"
              role="alert"
              data-test="drawer-error"
            >
              <p>{{ loadError }}</p>
              <button type="button" class="mt-2 text-sm font-medium underline underline-offset-2" @click="load">
                {{ t('keyFallback.drawer.retry') }}
              </button>
            </div>

            <div v-else-if="sections.length === 0" class="py-10 text-center" data-test="drawer-empty">
              <p class="text-sm font-medium text-gray-900 dark:text-white">
                {{ t('keyFallback.drawer.emptyTitle') }}
              </p>
              <p class="mt-1 text-sm text-gray-600 dark:text-gray-400">
                {{ t('keyFallback.drawer.emptyHint') }}
              </p>
            </div>

            <div v-else class="space-y-6">
              <section v-for="sec in sections" :key="sec.platform" :data-test="`platform-${sec.platform}`">
                <h3 class="mb-2 flex items-center gap-2 text-sm font-semibold text-gray-700 dark:text-gray-200">
                  <PlatformIcon :platform="sec.platform" size="sm" />
                  {{ t(`keyFallback.platforms.${sec.platform}`) }}
                </h3>
                <ul class="divide-y divide-gray-100 overflow-hidden rounded-lg border border-gray-200 dark:divide-dark-700 dark:border-dark-600">
                  <li v-for="k in sec.keys" :key="k.key_id" :data-test="`key-${k.key_id}`">
                    <button
                      type="button"
                      class="flex w-full items-center justify-between gap-3 px-3 py-3 text-left transition-colors hover:bg-gray-50 focus-visible:bg-gray-50 focus-visible:outline-none dark:hover:bg-dark-700 dark:focus-visible:bg-dark-700"
                      :aria-expanded="expandedKeyId === k.key_id"
                      :aria-label="
                        t(expandedKeyId === k.key_id ? 'keyFallback.drawer.collapse' : 'keyFallback.drawer.expand', {
                          name: k.name
                        })
                      "
                      data-test="key-toggle"
                      @click="toggle(k.key_id)"
                    >
                      <span class="min-w-0">
                        <span class="block truncate text-sm font-medium text-gray-900 dark:text-white">{{ k.name }}</span>
                        <span class="mt-0.5 block truncate text-xs text-gray-600 dark:text-gray-400" data-test="key-summary">
                          {{ summary(k) }}
                        </span>
                      </span>
                      <Icon
                        :name="expandedKeyId === k.key_id ? 'chevronUp' : 'chevronDown'"
                        size="sm"
                        class="shrink-0 text-gray-500 dark:text-gray-400"
                      />
                    </button>
                    <div v-if="expandedKeyId === k.key_id" class="border-t border-gray-100 bg-gray-50/60 px-3 py-4 dark:border-dark-700 dark:bg-dark-900/40">
                      <KeyFallbackChainEditor :key-id="k.key_id" @changed="onChanged(k.key_id, $event)" />
                    </div>
                  </li>
                </ul>
              </section>
            </div>
          </div>
        </aside>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import KeyFallbackChainEditor from '@/components/keys/KeyFallbackChainEditor.vue'
import { keyFallbackAPI } from '@/api/keyFallback'
import { fallbackErrorMessage } from '@/utils/keyFallbackError'
import type { KeyFallbackChain, KeyFallbackSummary, KeyFallbackSummaryKey } from '@/types'

const props = defineProps<{
  show: boolean
  /** 从哪把 Key 的行内入口打开；打开后直接展开它 */
  initialKeyId?: number | null
  /** 关闭后把焦点还给谁；不传就还给打开时拿着焦点的元素 */
  returnFocus?: HTMLElement | null
}>()
const emit = defineEmits<{ (e: 'close'): void }>()

const { t } = useI18n()
const titleId = 'key-fallback-drawer-title'

const summary_ = ref<KeyFallbackSummary | null>(null)
const loading = ref(false)
const loadError = ref('')
const expandedKeyId = ref<number | null>(null)
const closeBtn = ref<HTMLButtonElement | null>(null)
const panel = ref<HTMLElement | null>(null)
let loadVersion = 0
let previousFocus: HTMLElement | null = null

const sections = computed(() => (summary_.value?.platforms ?? []).filter((p) => p.keys.length > 0))

function summary(k: KeyFallbackSummaryKey): string {
  const n = k.items.filter((i) => i.role === 'fallback').length
  return n === 0 ? t('keyFallback.drawer.noFallback') : t('keyFallback.drawer.fallbackCount', { n })
}

async function load() {
  const version = ++loadVersion
  loading.value = true
  loadError.value = ''
  try {
    const res = await keyFallbackAPI.listChains()
    if (version !== loadVersion) return
    summary_.value = res
  } catch (err) {
    if (version !== loadVersion) return
    summary_.value = null
    loadError.value = fallbackErrorMessage(err, t, 'keyFallback.drawer.loadFailed')
  } finally {
    if (version === loadVersion) loading.value = false
  }
}

function toggle(keyId: number) {
  expandedKeyId.value = expandedKeyId.value === keyId ? null : keyId
}

/** 编辑器保存成功后，把这把 Key 的摘要同步成最新的链 */
function onChanged(keyId: number, chain: KeyFallbackChain) {
  for (const p of summary_.value?.platforms ?? []) {
    const k = p.keys.find((x) => x.key_id === keyId)
    if (k) k.items = chain.items.map(({ group_id, name, role, position, status, usable }) => ({
      group_id, name, role, position, status, usable
    }))
  }
}

/** 抽屉里当前能用 Tab 走到的元素（排除禁用的和被样式隐藏的，比如窄屏下的拖动手柄） */
function focusableElements(): HTMLElement[] {
  const root = panel.value
  if (!root) return []
  return Array.from(
    root.querySelectorAll<HTMLElement>(
      'a[href], button, input, select, textarea, [tabindex]:not([tabindex="-1"])'
    )
  ).filter(
    (el) =>
      !(el as HTMLButtonElement).disabled &&
      el.getAttribute('aria-hidden') !== 'true' &&
      getComputedStyle(el).display !== 'none'
  )
}

/** 焦点陷阱：Tab / Shift+Tab 只在抽屉里循环 */
function onTab(e: KeyboardEvent) {
  const items = focusableElements()
  if (items.length === 0) {
    e.preventDefault()
    panel.value?.focus()
    return
  }
  const first = items[0]
  const last = items[items.length - 1]
  const active = document.activeElement as HTMLElement | null
  if (!active || !panel.value?.contains(active)) {
    e.preventDefault()
    ;(e.shiftKey ? last : first).focus()
  } else if (e.shiftKey && (active === first || active === panel.value)) {
    e.preventDefault()
    last.focus()
  } else if (!e.shiftKey && active === last) {
    e.preventDefault()
    first.focus()
  }
}

// 焦点跑到抽屉外面时（比如点了遮罩后再按 Tab），把它拉回来
function onKeydown(e: KeyboardEvent) {
  if (!props.show) return
  // 编辑器里的选择列表展开时，Esc 由它先处理（只收起列表）
  if (e.key === 'Escape' && !e.defaultPrevented) emit('close')
  else if (e.key === 'Tab' && panel.value && !panel.value.contains(e.target as Node)) onTab(e)
}

watch(
  () => props.show,
  async (open) => {
    if (open) {
      previousFocus = document.activeElement as HTMLElement | null
      expandedKeyId.value = props.initialKeyId ?? null
      load()
      await nextTick()
      closeBtn.value?.focus()
    } else {
      loadVersion++
      const target = props.returnFocus && props.returnFocus.isConnected ? props.returnFocus : previousFocus
      target?.focus?.()
      previousFocus = null
    }
  },
  { immediate: true }
)

onMounted(() => document.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => document.removeEventListener('keydown', onKeydown))
</script>

<style scoped>
.fallback-drawer-enter-active,
.fallback-drawer-leave-active {
  transition: background-color 200ms ease;
}
.fallback-drawer-enter-active .drawer-panel,
.fallback-drawer-leave-active .drawer-panel {
  transition: transform 220ms cubic-bezier(0.22, 1, 0.36, 1);
}
.fallback-drawer-enter-from,
.fallback-drawer-leave-to {
  background-color: transparent;
}
.fallback-drawer-enter-from .drawer-panel,
.fallback-drawer-leave-to .drawer-panel {
  transform: translateX(100%);
}
@media (prefers-reduced-motion: reduce) {
  .fallback-drawer-enter-active,
  .fallback-drawer-leave-active,
  .fallback-drawer-enter-active .drawer-panel,
  .fallback-drawer-leave-active .drawer-panel {
    transition: none;
  }
}
</style>
