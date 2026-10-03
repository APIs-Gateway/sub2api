<template>
  <div v-bind="$attrs" class="onb-card space-y-4" data-test="panel-ai">
    <p class="text-sm text-gray-600 dark:text-dark-400">{{ t('keyOnboarding.ai.intro') }}</p>
    <div class="flex flex-wrap gap-2" role="radiogroup" :aria-label="t('keyOnboarding.ai.clientLabel')">
      <button
        v-for="c in AI_CLIENTS"
        :key="c"
        type="button"
        role="radio"
        class="onb-chip"
        :class="{ 'onb-chip-active': client === c }"
        :aria-checked="client === c"
        :data-test="`ai-client-${c}`"
        @click="client = c"
      >
        {{ t(`keyOnboarding.ai.clients.${c}`) }}
      </button>
    </div>
    <textarea
      readonly
      rows="5"
      class="input w-full resize-none text-sm leading-relaxed"
      data-test="ai-prompt"
      :value="aiShort"
      @focus="($event.target as HTMLTextAreaElement).select()"
    />
    <div class="flex flex-wrap gap-2">
      <button type="button" class="btn btn-primary btn-sm" data-test="ai-copy" @click="emit('copy', aiShort, 'ai-short')">
        {{ copiedId === 'ai-short' ? t('keyOnboarding.copied') : t('keyOnboarding.copy') }}
      </button>
      <button type="button" class="btn btn-secondary btn-sm" data-test="ai-copy-detail" @click="emit('copy', aiDetailed, 'ai-detail')">
        {{ copiedId === 'ai-detail' ? t('keyOnboarding.copied') : t('keyOnboarding.ai.copyDetail') }}
      </button>
      <button type="button" class="btn btn-secondary btn-sm" data-test="ai-open-chatgpt" @click="openExternal(chatgptUrl(aiShort))">
        {{ t('keyOnboarding.ai.openChatgpt') }}
      </button>
      <button type="button" class="btn btn-secondary btn-sm" data-test="ai-open-claude" @click="openExternal(claudeUrl(aiShort))">
        {{ t('keyOnboarding.ai.openClaude') }}
      </button>
    </div>
    <p class="text-xs text-gray-500 dark:text-dark-400">{{ t('keyOnboarding.ai.keyNote') }}</p>
  </div>
</template>

<script setup lang="ts">
/**
 * 「交给 AI」页签：生成一段不含密钥的提示词，可以复制、或在 ChatGPT / Claude 里打开。
 * 选中的客户端用 v-model:client 放在外壳里，这样切到别的页签再回来、关闭再打开弹窗都还在。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { EndpointOption } from '@/utils/apiEndpoints'
import { AI_CLIENTS, buildAiPrompt, chatgptUrl, claudeUrl, type AiClient, type OnboardingClient } from '@/utils/keyOnboarding'

// 根元素要接住外壳传来的 tabpanel 属性（role / id / aria-labelledby），所以关掉自动继承、手动放在根上
defineOptions({ inheritAttrs: false })

const props = defineProps<{
  /** 当前选中的线路；提示词里写它的 API 根地址（base，不带结尾的 / 和 /v1） */
  endpoint: EndpointOption
  platform: string | null
  siteName: string
  /** 分组是否开了 /v1/messages 调度：openai 分组开了之后，Claude Code 也能接入 */
  allowMessagesDispatch?: boolean
  /** 这个分组能用的客户端，和一键安装、手动配置拿到的是同一份 */
  clients?: OnboardingClient[]
  /** 密钥所在分组的可用模型，详细版里列出 */
  models: string[]
  /** 模型列表还在加载 */
  modelsLoading?: boolean
  /** 站点配置的使用文档地址，没有时为空 */
  docUrl?: string
  /** 最近复制成功的按钮 id，对应的按钮显示「已复制」 */
  copiedId: string
}>()

const emit = defineEmits<{
  /** 要复制的文本和按钮 id */
  copy: [text: string, id: string]
}>()

/** 选中的客户端 */
const client = defineModel<AiClient>('client', { required: true })

const { t } = useI18n()

// 注意：这里刻意不传密钥
function aiPrompt(detailed: boolean): string {
  return buildAiPrompt({
    t: (key, params) => t(key, params ?? {}),
    client: client.value,
    clientLabel: t(`keyOnboarding.ai.clients.${client.value}`),
    baseUrl: props.endpoint.base,
    platform: props.platform,
    siteName: props.siteName,
    models: props.models,
    docUrl: props.docUrl,
    detailed
  })
}
const aiShort = computed(() => aiPrompt(false))
const aiDetailed = computed(() => aiPrompt(true))

function openExternal(url: string) {
  try {
    window.open(url, '_blank', 'noopener,noreferrer')
  } catch {
    /* 用户可手动复制 */
  }
}
</script>

<style scoped src="./shared.css"></style>
