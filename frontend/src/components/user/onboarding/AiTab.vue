<template>
  <div v-bind="$attrs" class="onb-card space-y-4" data-test="panel-ai">
    <p class="text-sm text-gray-600 dark:text-dark-400">{{ t('keyOnboarding.ai.intro') }}</p>
    <div class="flex flex-wrap gap-2" role="radiogroup" :aria-label="t('keyOnboarding.ai.clientLabel')">
      <button
        v-for="c in available"
        :key="c"
        type="button"
        role="radio"
        class="onb-chip"
        :class="{ 'onb-chip-active': current === c }"
        :aria-checked="current === c"
        :data-test="`ai-client-${c}`"
        @click="client = c"
      >
        {{ t(`keyOnboarding.ai.clients.${c}`) }}
      </button>
    </div>
    <textarea
      readonly
      rows="3"
      class="input onb-sentence w-full resize-none text-sm leading-relaxed"
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
    <div class="space-y-1.5 text-xs text-gray-500 dark:text-dark-400">
      <p>{{ t('keyOnboarding.ai.keyNote') }}</p>
      <p data-test="ai-footnote">{{ t('keyOnboarding.ai.footnote') }}</p>
      <p>
        <a
          :href="catalogUrl"
          target="_blank"
          rel="noopener noreferrer"
          class="inline-flex items-center gap-1 font-medium text-primary-700 underline-offset-2 hover:underline dark:text-primary-400"
          data-test="ai-catalog"
        >
          {{ t('keyOnboarding.ai.docCatalog') }}
          <Icon name="externalLink" size="xs" aria-hidden="true" />
        </a>
      </p>
    </div>
  </div>
</template>

<script setup lang="ts">
/**
 * 「交给 AI」页签：一句不含密钥的话，指向站内给 AI 读的文档（/docs/<工具>.md，选了备用线路时带 ?endpoint=），
 * 可以复制、或在 ChatGPT / Claude 里打开；另有一份写好接入信息的详细版。
 * 工具和文档对应关系、句子模板都来自文档页的 views/docs/aiTools.ts，和文档页的「让 AI 帮你接入」是同一份。
 * 选中的客户端用 v-model:client 放在外壳里，这样切到别的页签再回来、关闭再打开弹窗都还在。
 * 能选哪些工具由分组决定（aiClientsForPlatform），选中的不在其中时改成第一个可用的。
 */
import { computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { EndpointOption } from '@/utils/apiEndpoints'
import { aiClientsForPlatform, buildAiPrompt, chatgptUrl, claudeUrl, type AiClient, type OnboardingClient } from '@/utils/keyOnboarding'
import { aiCatalogUrl, aiToolForClient, aiToolSentence, aiToolUrl } from '@/views/docs/aiTools'

// 根元素要接住外壳传来的 tabpanel 属性（role / id / aria-labelledby），所以关掉自动继承、手动放在根上
defineOptions({ inheritAttrs: false })

const props = defineProps<{
  /** 当前选中的线路；提示词里写它的 API 根地址（base，不带结尾的 / 和 /v1） */
  endpoint: EndpointOption
  platform: string | null
  siteName: string
  /** 站点自己的来源（页面所在的地址）：文档链接的域名。备用线路是给 API 用的，不保证能打开文档 */
  origin: string
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

/** 这个分组能选的工具，界面上的单选就是它 */
const available = computed(() => aiClientsForPlatform(props.platform, { allowMessagesDispatch: props.allowMessagesDispatch }))
/** 当前选中的工具：外壳里记着的那个不在可选范围内（第一次打开时的默认值、换了分组）时，用第一个可用的 */
const current = computed<AiClient>(() => (available.value.includes(client.value) ? client.value : (available.value[0] ?? client.value)))
// 把修正后的选择交回外壳，切走再回来时不会又变回去
watch(
  [current, client],
  () => {
    if (current.value !== client.value) client.value = current.value
  },
  { immediate: true }
)

// 注意：这里刻意不传密钥
function aiPrompt(detailed: boolean): string {
  return buildAiPrompt({
    t: (key, params) => t(key, params ?? {}),
    client: current.value,
    clientLabel: t(`keyOnboarding.ai.clients.${current.value}`),
    baseUrl: props.endpoint.base,
    platform: props.platform,
    siteName: props.siteName,
    models: props.models,
    docUrl: props.docUrl,
    detailed
  })
}
const aiDetailed = computed(() => aiPrompt(true))

// 一句话：只指向文档，接入地址、接口格式、要改的文件都在文档里。选了备用线路时链接带 ?endpoint=，AI 读到的文档里就是那个地址
const docEndpoint = computed(() => (props.endpoint.isDefault ? undefined : props.endpoint.base))
const site = computed(() => props.siteName.replace(/\s+/g, ' ').trim() || 'sub2api')
const aiShort = computed(() => {
  const tool = aiToolForClient(current.value)
  return aiToolSentence(tool, (key, params) => t(key, params ?? {}), {
    site: site.value,
    url: aiToolUrl(tool, props.origin, docEndpoint.value)
  })
})
const catalogUrl = computed(() => aiCatalogUrl(props.origin, docEndpoint.value))

function openExternal(url: string) {
  try {
    window.open(url, '_blank', 'noopener,noreferrer')
  } catch {
    /* 用户可手动复制 */
  }
}
</script>

<style scoped src="./shared.css"></style>

<style scoped>
/* 一句话的长度随站点名、线路变化：支持 field-sizing 的浏览器按内容撑开高度，其余用 rows 的三行 */
.onb-sentence {
  field-sizing: content;
  min-height: 3.5rem;
  max-height: 10rem;
}
</style>
