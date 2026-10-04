<template>
  <div v-bind="$attrs" class="onb-card space-y-5" data-test="panel-manual">
    <!-- 地址：每条线路一张卡片，卡片同时是线路选择；选中的线路决定下面代码里用哪个地址 -->
    <div
      class="grid gap-3"
      :class="multiple ? 'sm:grid-cols-2' : ''"
      :role="multiple ? 'radiogroup' : undefined"
      :aria-label="multiple ? t('keys.endpoints.title') : undefined"
      data-test="manual-lines"
    >
      <div
        v-for="(opt, i) in endpointOptions"
        :key="opt.id"
        class="line-card"
        :class="{ 'line-card-pick': multiple, 'line-card-active': multiple && opt.id === endpointId }"
        :data-test="`manual-line-${i}`"
        @click="pick(opt.id)"
      >
        <div class="flex items-center gap-2">
          <label v-if="multiple" class="line-pick">
            <input
              type="radio"
              class="sr-only"
              :name="`${uid}-line`"
              :value="opt.id"
              :checked="opt.id === endpointId"
              :aria-describedby="`${uid}-line-note-${i}`"
              :data-test="`manual-line-radio-${i}`"
              @change="pick(opt.id)"
            />
            <span class="line-dot" aria-hidden="true" />
            <span class="line-name">{{ lineName(opt) }}</span>
          </label>
          <span v-else class="line-name">{{ lineName(opt) }}</span>
          <span v-if="multiple && opt.id === endpointId" class="ml-auto text-xs text-gray-500 dark:text-dark-400">
            {{ t('keyOnboarding.manual.lineUsed') }}
          </span>
        </div>
        <div v-for="row in rowsFor(opt)" :key="row.id" class="space-y-1">
          <p class="text-xs text-gray-500 dark:text-dark-400">{{ row.label }}</p>
          <div class="flex items-center gap-2">
            <code class="min-w-0 flex-1 break-all font-mono text-sm text-gray-900 dark:text-gray-100">{{ row.value }}</code>
            <button
              type="button"
              :class="[COPY_BTN_CLASS, copiedId === `m-${i}-${row.id}` ? COPY_BTN_DONE_CLASS : '']"
              :data-test="`copy-line-${i}-${row.id}`"
              @click.stop="emit('copy', row.value, `m-${i}-${row.id}`)"
            >
              {{ copiedId === `m-${i}-${row.id}` ? t('keyOnboarding.copied') : t('keyOnboarding.copy') }}
            </button>
          </div>
        </div>
        <p :id="`${uid}-line-note-${i}`" class="text-xs text-gray-500 dark:text-dark-400" data-test="manual-line-note">{{ lineNote(opt) }}</p>
      </div>
    </div>

    <!-- 密钥：显示打码，复制的是完整密钥（图形界面的客户端要单独填密钥） -->
    <div v-if="fullKey" class="line-row" data-test="manual-key">
      <span class="shrink-0 text-sm text-gray-600 dark:text-dark-400">{{ t('keys.apiKey') }}</span>
      <code class="min-w-0 flex-1 truncate font-mono text-sm tabular-nums text-gray-900 dark:text-gray-100">{{ maskedKey }}</code>
      <button
        type="button"
        :class="[COPY_BTN_CLASS, copiedId === 'm-key' ? COPY_BTN_DONE_CLASS : '']"
        data-test="copy-key"
        @click="emit('copy', fullKey, 'm-key')"
      >
        {{ copiedId === 'm-key' ? t('keyOnboarding.copied') : t('keyOnboarding.copy') }}
      </button>
    </div>

    <!-- 代码示例：页签随分组能力（见 utils/manualSamples.ts 的 isManualCodeTabAvailable） -->
    <section v-if="visibleTabs.length > 0" class="space-y-3" data-test="manual-code">
      <div class="flex flex-wrap gap-2" role="radiogroup" :aria-label="t('keyOnboarding.manual.codeLabel')">
        <button
          v-for="id in visibleTabs"
          :key="id"
          type="button"
          role="radio"
          class="onb-chip"
          :class="{ 'onb-chip-active': activeTab === id }"
          :aria-checked="activeTab === id"
          :data-test="`manual-tab-${id}`"
          @click="codeTab = id"
        >
          {{ MANUAL_TAB_LABELS[id] }}
        </button>
      </div>
      <p class="text-xs text-gray-500 dark:text-dark-400" data-test="manual-key-state">{{ keyState }}</p>
      <div class="space-y-2">
        <CodeBlock
          v-for="f in codeFiles"
          :key="`${activeTab}-${f.id}`"
          :label="f.label"
          :code="f.code"
          :copied="copiedId === `code-${activeTab}-${f.id}`"
          :copy-label="t('keyOnboarding.copy')"
          :copied-label="t('keyOnboarding.copied')"
          :data-test="`manual-code-${activeTab}-${f.id}`"
          @copy="emit('copy', f.code, `code-${activeTab}-${f.id}`)"
        />
      </div>
    </section>
    <p v-else-if="!platform" class="text-sm text-gray-600 dark:text-dark-400" data-test="manual-no-group">{{ t('keyOnboarding.noGroup') }}</p>

    <!-- 写进配置文件：每个客户端一段 -->
    <div v-if="snippets.length > 0">
      <p class="mb-2 font-serif text-sm text-gray-900 dark:text-white">{{ t('keyOnboarding.manual.snippetsTitle') }}</p>
      <div class="space-y-2">
        <details v-for="s in snippets" :key="s.id" class="rounded-lg border border-gray-200 bg-white px-4 py-2.5 dark:border-dark-700 dark:bg-dark-800">
          <summary class="cursor-pointer select-none text-sm font-medium text-gray-900 dark:text-white">{{ s.title }}</summary>
          <div class="mt-3 space-y-2">
            <CodeBlock
              v-for="f in s.files"
              :key="f.label"
              :label="f.label"
              :mono-label="f.path"
              :code="f.code"
              :copied="copiedId === `${s.id}-${f.label}`"
              :copy-label="t('keyOnboarding.copy')"
              :copied-label="t('keyOnboarding.copied')"
              @copy="emit('copy', f.code, `${s.id}-${f.label}`)"
            />
          </div>
        </details>
      </div>
    </div>

    <div>
      <p class="mb-2 font-serif text-sm text-gray-900 dark:text-white">{{ t('keyOnboarding.manual.troubleshootTitle') }}</p>
      <ul class="list-disc space-y-1.5 pl-5 text-sm text-gray-600 marker:text-gray-300 dark:text-dark-400 dark:marker:text-dark-600">
        <li v-for="n in 4" :key="n">{{ t(`keyOnboarding.manual.troubleshoot${n}`) }}</li>
      </ul>
    </div>
    <a
      v-if="docUrl"
      :href="docUrl"
      target="_blank"
      rel="noopener noreferrer"
      class="inline-flex text-sm font-medium text-primary-700 hover:underline dark:text-primary-400"
    >
      {{ t('keyOnboarding.manual.viewDocs') }}
    </a>

    <p class="border-t border-gray-200 pt-3 text-xs text-gray-500 dark:border-dark-700 dark:text-dark-400" data-test="manual-footer">
      {{ t('keyOnboarding.manual.keepSafe') }}
    </p>
  </div>
</template>

<script lang="ts">
// 同一页上多个实例时，单选组的 name 和 id 不能撞
let manualUid = 0
</script>

<script setup lang="ts">
/**
 * 「手动配置」页签：
 * - 地址卡片：每条线路一张，卡片同时是线路选择（v-model:endpointId，和外壳、使用文档页共用同一个选择）；
 * - 代码示例：OpenAI SDK / curl / Codex / Claude Code / Gemini，拿到完整密钥时直接填好，示例模型取自分组；
 * - 按客户端的配置文件片段、排障提示、文档链接、页脚提醒。
 * 复制只是把内容通过 copy 事件交给外壳。
 */
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { EndpointOption } from '@/utils/apiEndpoints'
import { CLIENT_LABELS, endpointFor, type OnboardingClient } from '@/utils/keyOnboarding'
import {
  availableManualCodeTabs,
  buildConfigSnippets,
  buildManualCode,
  KEY_PLACEHOLDER,
  MANUAL_TAB_LABELS,
  type ManualCodeTab
} from '@/utils/manualSamples'
import { CodeBlock, COPY_BTN_CLASS, COPY_BTN_DONE_CLASS } from './CodeBlock'

// 根元素要接住外壳传来的 tabpanel 属性（role / id / aria-labelledby），所以关掉自动继承、手动放在根上
defineOptions({ inheritAttrs: false })

const props = defineProps<{
  /** 当前选中的线路；代码和配置片段里用它的 API 根地址（base，不带结尾的 / 和 /v1） */
  endpoint: EndpointOption
  /** 站点的全部线路：默认地址加管理员配置的备用线路，每条一张卡片 */
  endpointOptions: EndpointOption[]
  /** 完整密钥；复制和代码里用它。拿不到时为空 */
  fullKey: string
  /** 密钥的掩码，密钥行里显示它 */
  maskedKey: string
  platform: string | null
  siteName: string
  /** 这个分组可用的客户端（clientsForPlatform），决定显示哪些代码页签和配置片段 */
  clients: OnboardingClient[]
  /** 这个分组可用的模型，示例里的模型从这里挑；还没加载出来时为空 */
  models: string[]
  /** 站点配置的使用文档地址，有就显示「查看文档」链接 */
  docUrl?: string
  /** 最近复制成功的按钮 id，对应的按钮显示「已复制」 */
  copiedId: string
}>()

const emit = defineEmits<{
  /** 要复制的文本和按钮 id */
  copy: [text: string, id: string]
}>()

/** 选中线路的 id，和外壳的线路选择共用 */
const endpointId = defineModel<string>('endpointId', { required: true })

const { t } = useI18n()
const uid = `manual-${++manualUid}`

// ===== 地址卡片 =====
const multiple = computed(() => props.endpointOptions.length > 1)

function pick(id: string) {
  if (multiple.value) endpointId.value = id
}

/** 线路的名字：默认地址沿用「默认」，备用线路用管理员填的名称 */
function lineName(opt: EndpointOption): string {
  return opt.isDefault ? t('keys.endpoints.default') : opt.name
}

/** 一句说明：管理员给备用线路写了说明就原样用，否则用通用的一句 */
function lineNote(opt: EndpointOption): string {
  return opt.description || t(opt.isDefault ? 'keyOnboarding.manual.lineDefaultNote' : 'keyOnboarding.manual.lineAltNote')
}

// 卡片上列的地址就是下面代码里实际用到的：/v1 地址给 OpenAI 兼容的客户端（OpenAI SDK、curl、Codex、OpenCode），
// 接入地址给 Claude Code、Gemini CLI 这类原生接口的客户端（Antigravity 分组带 /antigravity）。没有分组时两个都给
const showV1 = computed(() => props.clients.length === 0 || props.clients.some((c) => c === 'codex' || c === 'opencode'))
const showRoot = computed(() => props.clients.length === 0 || props.clients.some((c) => c === 'claude' || c === 'gemini'))

function rowsFor(opt: EndpointOption): { id: string; label: string; value: string }[] {
  const rows: { id: string; label: string; value: string }[] = []
  if (showV1.value) rows.push({ id: 'v1', label: t('keyOnboarding.manual.openaiAddress'), value: opt.v1 })
  if (showRoot.value) rows.push({ id: 'base', label: t('keyOnboarding.manual.address'), value: endpointFor('claude', props.platform, opt.base) })
  return rows
}

// ===== 代码示例 =====
const visibleTabs = computed(() => availableManualCodeTabs(props.clients))
const codeTab = ref<ManualCodeTab>('openai')
// 选中的页签对新分组不可用时回到第一个可用的
const activeTab = computed<ManualCodeTab>(() => (visibleTabs.value.includes(codeTab.value) ? codeTab.value : visibleTabs.value[0]))

const sampleInput = computed(() => ({
  base: props.endpoint.base,
  platform: props.platform,
  apiKey: props.fullKey,
  models: props.models,
  siteName: props.siteName
}))

const codeFiles = computed(() => (visibleTabs.value.length > 0 ? buildManualCode(activeTab.value, sampleInput.value) : []))

const keyState = computed(() => {
  if (props.fullKey) return t('keyOnboarding.manual.keyFilled')
  const params = { placeholder: KEY_PLACEHOLDER, masked: props.maskedKey }
  return props.maskedKey ? t('keyOnboarding.manual.keyMasked', params) : t('keyOnboarding.manual.keyMissing', params)
})

// ===== 配置文件片段 =====
const snippets = computed(() =>
  buildConfigSnippets(props.clients, {
    ...sampleInput.value,
    envVarsLabel: t('keyOnboarding.manual.envVars'),
    clientLabels: CLIENT_LABELS
  })
)
</script>

<style scoped src="./shared.css"></style>

<style scoped>
/* 地址卡片：白底，多条线路时整张卡片可点，选中的描边加深、圆点填色 */
.line-card {
  @apply space-y-2.5 rounded-lg border border-gray-200 bg-white p-3.5 transition-colors duration-150;
  @apply dark:border-dark-700 dark:bg-dark-800;
}
.line-card-pick {
  @apply cursor-pointer hover:border-gray-300 dark:hover:border-dark-600;
}
.line-card-active,
.line-card-active:hover {
  @apply border-gray-400 dark:border-dark-400;
}
.line-card:has(input:focus-visible) {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 2px;
}
.line-pick {
  @apply flex cursor-pointer items-center gap-2;
}
.line-name {
  @apply text-sm font-medium text-gray-900 dark:text-white;
}
.line-dot {
  @apply inline-block h-3.5 w-3.5 shrink-0 rounded-full border border-gray-300 bg-white dark:border-dark-500 dark:bg-dark-800;
}
.line-card-active .line-dot {
  @apply border-primary-500 bg-primary-500;
  box-shadow: inset 0 0 0 2.5px theme('colors.white');
}
.dark .line-card-active .line-dot {
  box-shadow: inset 0 0 0 2.5px theme('colors.dark.800');
}

/* 密钥行：和地址卡片同样的白底 */
.line-row {
  @apply flex items-center gap-3 rounded-lg border border-gray-200 bg-white px-3.5 py-2.5;
  @apply dark:border-dark-700 dark:bg-dark-800;
}
</style>
