<template>
  <Teleport to="body">
    <Transition name="side-drawer">
      <div
        v-if="show"
        class="fixed inset-0 z-50 flex justify-end bg-black/40 dark:bg-black/60"
        @click.self="emit('close')"
      >
        <aside
          class="drawer-panel flex h-full w-full flex-col bg-white shadow-overlay dark:bg-dark-800 sm:border-l sm:border-gray-200 sm:dark:border-dark-700"
          :class="widthClass"
          role="dialog"
          aria-modal="true"
          :aria-labelledby="titleId"
        >
          <header class="flex items-start justify-between gap-3 border-b border-gray-200 px-4 py-4 dark:border-dark-700 sm:px-6">
            <div class="min-w-0">
              <h2 :id="titleId" class="text-base font-semibold text-gray-900 dark:text-white">{{ title }}</h2>
              <p v-if="subtitle" class="mt-1 text-sm text-gray-500 dark:text-dark-300">{{ subtitle }}</p>
            </div>
            <button
              ref="closeBtn"
              type="button"
              class="-mr-2 rounded-md p-2 text-gray-400 transition-colors hover:bg-gray-100 hover:text-gray-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 dark:text-dark-400 dark:hover:bg-dark-700 dark:hover:text-dark-200"
              :aria-label="t('common.close')"
              @click="emit('close')"
            >
              <Icon name="x" size="md" />
            </button>
          </header>

          <div class="min-h-0 flex-1 overflow-y-auto px-4 py-5 sm:px-6">
            <slot />
          </div>

          <footer
            v-if="$slots.footer"
            class="flex items-center justify-end gap-3 border-t border-gray-200 bg-gray-50 px-4 py-3 dark:border-dark-700 dark:bg-dark-900/40 sm:px-6"
          >
            <slot name="footer" />
          </footer>
        </aside>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'

const props = withDefaults(defineProps<{ show: boolean; title: string; subtitle?: string; width?: 'normal' | 'wide' }>(), {
  width: 'normal'
})
const emit = defineEmits<{ (e: 'close'): void }>()

const { t } = useI18n()
const titleId = `side-drawer-${Math.random().toString(36).slice(2, 8)}`
const closeBtn = ref<HTMLButtonElement | null>(null)
let previousFocus: HTMLElement | null = null

const widthClass = computed(() => (props.width === 'wide' ? 'sm:max-w-[40rem]' : 'sm:max-w-[30rem]'))

function onKeydown(e: KeyboardEvent) {
  if (props.show && e.key === 'Escape') emit('close')
}

watch(
  () => props.show,
  async (open) => {
    if (open) {
      previousFocus = document.activeElement as HTMLElement | null
      await nextTick()
      closeBtn.value?.focus()
    } else {
      previousFocus?.focus?.()
      previousFocus = null
    }
  },
  { immediate: true }
)

onMounted(() => document.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => document.removeEventListener('keydown', onKeydown))
</script>

<style scoped>
.side-drawer-enter-active,
.side-drawer-leave-active {
  transition: opacity 0.2s ease;
}
.side-drawer-enter-active .drawer-panel,
.side-drawer-leave-active .drawer-panel {
  transition: transform 0.22s ease;
}
.side-drawer-enter-from,
.side-drawer-leave-to {
  opacity: 0;
}
.side-drawer-enter-from .drawer-panel,
.side-drawer-leave-to .drawer-panel {
  transform: translateX(24px);
}
</style>
