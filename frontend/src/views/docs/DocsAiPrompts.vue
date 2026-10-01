<template>
  <div class="docs-ai">
    <div class="docs-ai-tabs" role="tablist">
      <button
        v-for="tab in tabs"
        :key="tab.id"
        type="button"
        role="tab"
        :aria-selected="active === tab.id"
        class="docs-ai-tab"
        :class="{ 'is-active': active === tab.id }"
        @click="active = tab.id"
      >
        {{ tab.label }}
      </button>
    </div>

    <p class="docs-ai-prompt" data-testid="docs-ai-prompt">{{ currentPrompt }}</p>

    <div class="docs-ai-actions">
      <button type="button" class="docs-ai-primary" data-testid="docs-ai-copy" @click="copyPrompt">
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
/* 颜色变量由 DocsView 的 .docs-page 提供，和正文一起随明暗模式切换 */
.docs-ai-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 0.25rem 1.5rem;
  border-bottom: 1px solid var(--d-rule);
}
.docs-ai-tab {
  margin-bottom: -1px;
  border-bottom: 2px solid transparent;
  padding-block: 0.5rem;
  font-size: 0.875rem;
  font-weight: 500;
  color: var(--d-muted);
  transition: color 0.15s, border-color 0.15s;
}
.docs-ai-tab:hover {
  color: var(--d-ink);
}
.docs-ai-tab.is-active {
  border-bottom-color: var(--d-ink);
  color: var(--d-ink);
}
.docs-ai-prompt {
  margin-top: 1rem;
  white-space: pre-line;
  border: 1px solid var(--d-rule);
  border-radius: 0.25rem;
  background: var(--d-wash);
  padding: 1rem 1.125rem;
  font-size: 0.9375rem;
  line-height: 1.85;
  color: var(--d-text);
}
.docs-ai-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
  margin-top: 0.875rem;
}
.docs-ai-primary,
.docs-ai-link {
  display: inline-flex;
  height: 2.25rem;
  align-items: center;
  border-radius: 9999px;
  padding-inline: 1.125rem;
  font-size: 0.875rem;
  font-weight: 500;
  transition: opacity 0.15s, border-color 0.15s, color 0.15s;
}
.docs-ai-primary {
  background: var(--d-pill-bg);
  color: var(--d-pill-fg);
}
.docs-ai-primary:hover {
  opacity: 0.86;
}
.docs-ai-link {
  border: 1px solid var(--d-rule);
  color: var(--d-text);
}
.docs-ai-link:hover {
  border-color: var(--d-muted);
  color: var(--d-ink);
}
</style>
