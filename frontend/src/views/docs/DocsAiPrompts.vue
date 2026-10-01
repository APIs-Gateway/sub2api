<template>
  <div class="docs-ai">
    <div class="flex flex-wrap gap-x-5 gap-y-1 border-b border-gray-200 dark:border-dark-700" role="tablist">
      <button
        v-for="tab in tabs"
        :key="tab.id"
        type="button"
        role="tab"
        :aria-selected="active === tab.id"
        class="-mb-px border-b-2 py-2 text-sm font-medium transition-colors"
        :class="
          active === tab.id
            ? 'border-primary-600 text-primary-700 dark:border-primary-400 dark:text-primary-300'
            : 'border-transparent text-gray-600 hover:text-gray-900 dark:text-dark-300 dark:hover:text-white'
        "
        @click="active = tab.id"
      >
        {{ tab.label }}
      </button>
    </div>

    <p
      class="mt-4 whitespace-pre-line rounded-md bg-gray-100 px-4 py-3 text-[15px] leading-7 text-gray-800 dark:bg-dark-800 dark:text-dark-100"
      data-testid="docs-ai-prompt"
    >{{ currentPrompt }}</p>

    <div class="mt-3 flex flex-wrap items-center gap-2">
      <button
        type="button"
        class="inline-flex items-center rounded-md bg-primary-600 px-3.5 py-1.5 text-sm font-medium text-white transition hover:bg-primary-700"
        data-testid="docs-ai-copy"
        @click="copyPrompt"
      >
        {{ copied ? t('docs.copied') : t('docs.ai.copyPrompt') }}
      </button>
      <a
        :href="chatgptUrl"
        target="_blank"
        rel="noopener noreferrer"
        class="docs-ai-link"
        data-testid="docs-ai-chatgpt"
      >{{ t('docs.ai.openChatgpt') }}</a>
      <a
        :href="claudeUrl"
        target="_blank"
        rel="noopener noreferrer"
        class="docs-ai-link"
        data-testid="docs-ai-claude"
      >{{ t('docs.ai.openClaude') }}</a>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { copyText } from './docsRender'

const props = defineProps<{
  /** 提示词，键是 general / claude-code / codex / chat */
  prompts: Record<string, string>
}>()

const { t } = useI18n()

const active = ref('general')
const copied = ref(false)
let timer: ReturnType<typeof setTimeout> | null = null

const tabs = computed(() => [
  { id: 'general', label: t('docs.ai.tabs.general') },
  { id: 'claude-code', label: t('docs.ai.tabs.claudeCode') },
  { id: 'codex', label: t('docs.ai.tabs.codex') },
  { id: 'chat', label: t('docs.ai.tabs.chat') },
])

const currentPrompt = computed(() => props.prompts[active.value] ?? '')
const chatgptUrl = computed(() => `https://chatgpt.com/?q=${encodeURIComponent(currentPrompt.value)}`)
const claudeUrl = computed(() => `https://claude.ai/new?q=${encodeURIComponent(currentPrompt.value)}`)

async function copyPrompt() {
  if (!(await copyText(currentPrompt.value))) return
  copied.value = true
  if (timer) clearTimeout(timer)
  timer = setTimeout(() => (copied.value = false), 1800)
}

onBeforeUnmount(() => {
  if (timer) clearTimeout(timer)
})
</script>

<style scoped>
.docs-ai-link {
  @apply inline-flex items-center rounded-md border border-gray-300 px-3.5 py-1.5 text-sm font-medium text-gray-800 transition hover:bg-gray-100 dark:border-dark-600 dark:text-dark-100 dark:hover:bg-dark-800;
}
</style>
