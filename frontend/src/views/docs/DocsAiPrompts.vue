<template>
  <div class="docs-ai">
    <p id="docs-ai-who" class="docs-ai-who">{{ t('docs.ai.iWant') }}</p>
    <div class="docs-ai-tools" role="radiogroup" aria-labelledby="docs-ai-who" data-testid="docs-ai-tools">
      <label v-for="tool in tools" :key="tool.id" class="docs-ai-tool">
        <input v-model="activeId" type="radio" name="docs-ai-tool" class="docs-ai-tool-input" :value="tool.id" />
        <span class="docs-ai-tool-face">{{ toolLabel(tool) }}</span>
      </label>
    </div>

    <p class="docs-ai-sentence" data-testid="docs-ai-sentence">{{ sentence }}</p>

    <div class="docs-ai-actions">
      <button type="button" class="docs-ai-primary" data-testid="docs-ai-copy" @click="copySentence">
        {{ copied === 'sentence' ? t('docs.copied') : t('docs.ai.copySentence') }}
      </button>
      <button type="button" class="docs-ai-secondary" data-testid="docs-ai-copy-full" @click="copyFull">
        {{ copied === 'full' ? t('docs.copied') : t('docs.ai.copyFull') }}
      </button>
      <button
        type="button"
        class="docs-ai-more-toggle"
        :aria-expanded="moreOpen"
        aria-controls="docs-ai-more"
        data-testid="docs-ai-more-toggle"
        @click="moreOpen = !moreOpen"
      >
        {{ t('docs.ai.more') }}
        <Icon name="chevronDown" size="sm" class="transition-transform" :class="moreOpen ? 'rotate-180' : ''" />
      </button>
    </div>

    <p class="docs-ai-hint">{{ t('docs.ai.hint', { url: llmsUrl }) }}</p>

    <!-- 更多方式：把地址和要求都写进去的完整提示词，AI 打不开链接时也能用 -->
    <div v-if="moreOpen" id="docs-ai-more" class="docs-ai-more" data-testid="docs-ai-more">
      <p class="docs-ai-more-title">{{ t('docs.ai.moreTitle') }}</p>
      <p class="docs-ai-more-desc">{{ t('docs.ai.moreDesc') }}</p>
      <p class="docs-ai-prompt" data-testid="docs-ai-prompt">{{ longPrompt }}</p>
      <div class="docs-ai-actions">
        <button type="button" class="docs-ai-secondary" data-testid="docs-ai-copy-prompt" @click="copyPrompt">
          {{ copied === 'prompt' ? t('docs.copied') : t('docs.ai.copyPrompt') }}
        </button>
        <a :href="chatgptUrl" target="_blank" rel="noopener noreferrer" class="docs-ai-link" data-testid="docs-ai-chatgpt">
          {{ t('docs.ai.openChatgpt') }}
        </a>
        <a :href="claudeUrl" target="_blank" rel="noopener noreferrer" class="docs-ai-link" data-testid="docs-ai-claude">
          {{ t('docs.ai.openClaude') }}
        </a>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { chatgptUrl as buildChatgptUrl, claudeUrl as buildClaudeUrl } from '@/utils/keyOnboarding'
import { AI_TOOLS, aiToolSentence, aiToolUrl, type AiTool } from './aiTools'
import { copyText, parseAiPrompts, type DocVars } from './docsRender'

const props = defineProps<{
  vars: DocVars
  /** 读者选了备用地址时传它的 API 根地址，生成的链接会带上，AI 读到的文档里就是这个地址 */
  endpoint?: string
  /** ai-prompts.md 的原文 */
  promptsRaw: string
  /** 点「复制整份文档」时才调用，返回已替换占位符的全文 */
  getFullDoc: () => string
}>()

const { t } = useI18n()

const tools = AI_TOOLS
const activeId = ref(AI_TOOLS[0].id)
const moreOpen = ref(false)
const copied = ref<'sentence' | 'full' | 'prompt' | ''>('')
let timer: ReturnType<typeof setTimeout> | null = null

const activeTool = computed<AiTool>(() => AI_TOOLS.find((x) => x.id === activeId.value) ?? AI_TOOLS[0])
const origin = computed(() => props.vars.origin ?? '')
const llmsUrl = computed(() => `${origin.value}/llms.txt`)
const docUrl = computed(() => aiToolUrl(activeTool.value, origin.value, props.endpoint))

function toolLabel(tool: AiTool): string {
  return tool.name ?? t(`docs.ai.tools.${tool.id}.label`)
}

const sentence = computed(() => aiToolSentence(activeTool.value, t, { site: props.vars.site, url: docUrl.value }))

const longPrompt = computed(() => {
  const prompts = parseAiPrompts(props.promptsRaw, { ...props.vars, llms: docUrl.value })
  return prompts[activeTool.value.id] ?? prompts[AI_TOOLS[0].id] ?? ''
})
// 和接入弹窗共用同一组链接写法（ChatGPT 带 hints=search，才会联网读文档链接）
const chatgptUrl = computed(() => buildChatgptUrl(longPrompt.value))
const claudeUrl = computed(() => buildClaudeUrl(longPrompt.value))

async function copyAs(kind: 'sentence' | 'full' | 'prompt', text: string) {
  if (!(await copyText(text))) return
  copied.value = kind
  if (timer) clearTimeout(timer)
  timer = setTimeout(() => (copied.value = ''), 1800)
}
const copySentence = () => copyAs('sentence', sentence.value)
const copyFull = () => copyAs('full', props.getFullDoc())
const copyPrompt = () => copyAs('prompt', longPrompt.value)

onBeforeUnmount(() => {
  if (timer) clearTimeout(timer)
})
</script>

<style scoped>
/* 颜色变量由 DocsView 的 .docs-page 提供，和正文一起随明暗模式切换 */
.docs-ai-who {
  font-size: 0.875rem;
  font-weight: 500;
  color: var(--d-muted);
}
.docs-ai-tools {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
  margin-top: 0.625rem;
}
.docs-ai-tool {
  position: relative;
  cursor: pointer;
}
/* 原生单选框藏在按钮后面，保留键盘方向键切换；焦点环画在按钮上 */
.docs-ai-tool-input {
  position: absolute;
  inset: 0;
  opacity: 0;
  margin: 0;
  cursor: pointer;
}
.docs-ai-tool-face {
  display: inline-flex;
  height: 2.125rem;
  align-items: center;
  border: 1px solid var(--d-rule);
  border-radius: 9999px;
  padding-inline: 1rem;
  font-size: 0.875rem;
  color: var(--d-text);
  transition: border-color 0.15s, background-color 0.15s, color 0.15s;
}
.docs-ai-tool:hover .docs-ai-tool-face {
  border-color: var(--d-muted);
  color: var(--d-ink);
}
.docs-ai-tool-input:checked + .docs-ai-tool-face {
  border-color: var(--d-ink);
  background: var(--d-ink);
  color: var(--d-paper);
}
.docs-ai-tool-input:focus-visible + .docs-ai-tool-face {
  outline: 2px solid var(--d-accent);
  outline-offset: 2px;
}
.docs-ai-sentence,
.docs-ai-prompt {
  margin-top: 1rem;
  border: 1px solid var(--d-rule);
  border-radius: 0.25rem;
  background: var(--d-wash);
  padding: 1rem 1.125rem;
  color: var(--d-text);
  overflow-wrap: anywhere;
}
.docs-ai-sentence {
  font-size: 1rem;
  line-height: 1.75;
  color: var(--d-ink);
}
.docs-ai-prompt {
  white-space: pre-line;
  font-size: 0.9375rem;
  line-height: 1.85;
}
.docs-ai-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
  margin-top: 0.875rem;
}
.docs-ai-primary,
.docs-ai-secondary,
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
.docs-ai-secondary,
.docs-ai-link {
  border: 1px solid var(--d-rule);
  color: var(--d-text);
}
.docs-ai-secondary:hover,
.docs-ai-link:hover {
  border-color: var(--d-muted);
  color: var(--d-ink);
}
.docs-ai-more-toggle {
  display: inline-flex;
  height: 2.25rem;
  align-items: center;
  gap: 0.25rem;
  padding-inline: 0.5rem;
  font-size: 0.875rem;
  color: var(--d-muted);
  transition: color 0.15s;
}
.docs-ai-more-toggle:hover {
  color: var(--d-ink);
}
.docs-ai-hint {
  margin-top: 0.75rem;
  font-size: 0.8125rem;
  line-height: 1.7;
  color: var(--d-muted);
  overflow-wrap: anywhere;
}
.docs-ai-more {
  margin-top: 1.25rem;
  border-top: 1px solid var(--d-hair);
  padding-top: 1.25rem;
}
.docs-ai-more-title {
  font-size: 0.9375rem;
  font-weight: 500;
  color: var(--d-ink);
}
.docs-ai-more-desc {
  margin-top: 0.25rem;
  font-size: 0.8125rem;
  line-height: 1.7;
  color: var(--d-muted);
}
.docs-ai-more .docs-ai-prompt {
  margin-top: 0.75rem;
}
</style>
