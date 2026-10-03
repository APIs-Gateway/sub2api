<template>
  <div v-bind="$attrs" class="onb-card space-y-5" data-test="panel-manual">
    <div class="overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
      <table class="w-full text-sm">
        <tbody>
          <tr v-for="(row, idx) in manualRows" :key="row.id" :class="idx > 0 ? 'border-t border-gray-100 dark:border-dark-800' : ''">
            <td class="w-28 px-4 py-2.5 text-gray-600 dark:text-dark-400 sm:w-40">{{ row.label }}</td>
            <td class="px-4 py-2.5">
              <div class="flex items-center gap-2">
                <code class="min-w-0 flex-1 truncate font-mono text-sm text-gray-900 dark:text-gray-100">{{ row.shown }}</code>
                <button type="button" class="copy-btn shrink-0" @click="emit('copy', row.value, `m-${row.id}`)">
                  <span class="text-xs">{{ copiedId === `m-${row.id}` ? t('keyOnboarding.copied') : t('keyOnboarding.copy') }}</span>
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="snippets.length > 0" class="space-y-2">
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
  </div>
</template>

<script setup lang="ts">
/**
 * 「手动配置」页签：接入地址、密钥（只显示掩码，复制的是完整密钥）和各客户端的配置片段，外加排障提示。
 * 复制只是把内容通过 copy 事件交给外壳。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { EndpointOption } from '@/utils/apiEndpoints'
import { OPENAI_CC_SWITCH_CODEX_MODEL } from '@/utils/ccswitchImport'
import { CLIENT_LABELS, codexProviderId, endpointFor, shQuote, type OnboardingClient } from '@/utils/keyOnboarding'
import { CodeBlock } from './CodeBlock'

// 根元素要接住外壳传来的 tabpanel 属性（role / id / aria-labelledby），所以关掉自动继承、手动放在根上
defineOptions({ inheritAttrs: false })

const props = defineProps<{
  /** 当前选中的线路；显示和写进片段的是它的 API 根地址（base，不带结尾的 / 和 /v1） */
  endpoint: EndpointOption
  /** 完整密钥；复制和代码片段里是它 */
  fullKey: string
  /** 密钥的掩码，表格里显示它 */
  maskedKey: string
  platform: string | null
  siteName: string
  /** 这个分组可用的客户端，每个一段配置片段 */
  clients: OnboardingClient[]
  /** 站点配置的使用文档地址，有就显示「查看文档」链接 */
  docUrl?: string
  /** 最近复制成功的按钮 id，对应的按钮显示「已复制」 */
  copiedId: string
}>()

const emit = defineEmits<{
  /** 要复制的文本和按钮 id */
  copy: [text: string, id: string]
}>()

const { t } = useI18n()

const base = computed(() => props.endpoint.base)

const manualRows = computed(() => [
  { id: 'base', label: t('keyOnboarding.manual.address'), shown: base.value, value: base.value },
  { id: 'v1', label: t('keyOnboarding.manual.openaiAddress'), shown: `${base.value}/v1`, value: `${base.value}/v1` },
  { id: 'key', label: t('keys.apiKey'), shown: props.maskedKey, value: props.fullKey }
])

function tomlQuote(v: string): string {
  return `"${v
    .replace(/\\/g, '\\\\')
    .replace(/"/g, '\\"')
    // eslint-disable-next-line no-control-regex
    .replace(/[\u0000-\u001f\u007f]/g, (c) => `\\u${c.charCodeAt(0).toString(16).padStart(4, '0')}`)}"`
}

const snippets = computed(() => {
  // path：标签是文件路径（用等宽字体）；其余标签是普通文字
  const list: { id: string; title: string; files: { label: string; code: string; path?: boolean }[] }[] = []
  const key = props.fullKey
  for (const c of props.clients) {
    const url = endpointFor(c, props.platform, base.value)
    if (c === 'claude') {
      list.push({
        id: 'claude',
        title: CLIENT_LABELS.claude,
        files: [
          { label: t('keyOnboarding.manual.envVars'), code: `export ANTHROPIC_BASE_URL=${shQuote(url)}\nexport ANTHROPIC_AUTH_TOKEN=${shQuote(key)}` },
          {
            label: '~/.claude/settings.json',
            path: true,
            code: JSON.stringify({ env: { ANTHROPIC_BASE_URL: url, ANTHROPIC_AUTH_TOKEN: key } }, null, 2)
          }
        ]
      })
    } else if (c === 'codex') {
      const id = codexProviderId(props.siteName)
      list.push({
        id: 'codex',
        title: CLIENT_LABELS.codex,
        files: [
          {
            label: '~/.codex/config.toml',
            path: true,
            code: [
              `model_provider = ${tomlQuote(id)}`,
              `model = ${tomlQuote(OPENAI_CC_SWITCH_CODEX_MODEL)}`,
              '',
              `[model_providers.${id}]`,
              `name = ${tomlQuote(props.siteName)}`,
              `base_url = ${tomlQuote(url)}`,
              'wire_api = "responses"',
              'requires_openai_auth = false',
              `experimental_bearer_token = ${tomlQuote(key)}`
            ].join('\n')
          }
        ]
      })
    } else if (c === 'gemini') {
      list.push({
        id: 'gemini',
        title: CLIENT_LABELS.gemini,
        files: [
          { label: t('keyOnboarding.manual.envVars'), code: `export GOOGLE_GEMINI_BASE_URL=${shQuote(url)}\nexport GEMINI_API_KEY=${shQuote(key)}` }
        ]
      })
    }
  }
  return list
})
</script>

<style scoped src="./shared.css"></style>

<style scoped>
.copy-btn {
  display: inline-flex;
  align-items: center;
  border-radius: 0.375rem;
  border: 1px solid theme('colors.gray.200');
  padding: 0.125rem 0.5rem;
  color: theme('colors.gray.600');
  transition: background-color 0.15s ease, color 0.15s ease;
}
.copy-btn:hover {
  background-color: theme('colors.gray.100');
  color: theme('colors.gray.900');
}
.copy-btn:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 2px;
}
:global(.dark) .copy-btn {
  border-color: theme('colors.dark.700');
  color: theme('colors.gray.400');
}
:global(.dark) .copy-btn:hover {
  background-color: theme('colors.dark.800');
  color: #fff;
}
</style>
