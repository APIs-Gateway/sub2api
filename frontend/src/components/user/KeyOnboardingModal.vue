<template>
  <BaseDialog :show="show" :title="tx.title" width="wide" @close="emit('close')">
    <div v-if="apiKey" class="space-y-5">
      <!-- 当前密钥 + 端点 -->
      <div class="grid gap-3 sm:grid-cols-2">
        <div class="rounded-md border border-gray-200 px-4 py-3 dark:border-dark-700">
          <p class="text-xs font-medium text-gray-600 dark:text-dark-400">{{ tx.currentKey }}</p>
          <div class="mt-1 flex items-center gap-2">
            <code class="truncate font-mono text-sm tabular-nums text-gray-900 dark:text-gray-100">{{ maskedKey }}</code>
            <button class="copy-btn" :title="tx.copy" @click="copy(apiKey.key, 'key')">
              <span class="text-xs">{{ copiedId === 'key' ? tx.copied : tx.copy }}</span>
            </button>
          </div>
        </div>
        <div class="rounded-md border border-gray-200 px-4 py-3 dark:border-dark-700">
          <p class="text-xs font-medium text-gray-600 dark:text-dark-400">{{ tx.endpoint }}</p>
          <div class="mt-1 flex items-center gap-2">
            <code class="truncate font-mono text-sm text-gray-900 dark:text-gray-100">{{ base }}</code>
            <button class="copy-btn" :title="tx.copy" @click="copy(base, 'base')">
              <span class="text-xs">{{ copiedId === 'base' ? tx.copied : tx.copy }}</span>
            </button>
          </div>
        </div>
      </div>

      <p class="text-sm text-gray-600 dark:text-dark-400">{{ tx.intro }}</p>

      <!-- 方式选择（下划线 tab） -->
      <div class="tabs flex-wrap">
        <button
          v-for="m in methods"
          :key="m.id"
          class="tab"
          :class="{ 'tab-active': active === m.id }"
          @click="active = m.id"
        >
          {{ m.label }}
          <span
            v-if="m.id === recommended"
            class="ml-1.5 rounded-md border border-primary-300 px-1.5 py-0.5 text-[10px] font-medium text-primary-700 dark:border-primary-900/50 dark:text-primary-400"
          >{{ tx.recommended }}</span>
        </button>
      </div>

      <!-- ===== CC Switch（推荐） ===== -->
      <div v-if="active === 'ccswitch'" class="space-y-4">
        <p class="text-sm text-gray-600 dark:text-dark-400">{{ tx.ccsIntro }}</p>
        <!-- 支持导入的客户端 -->
        <div class="flex flex-wrap items-center gap-2">
          <span class="text-xs text-gray-500 dark:text-dark-400">{{ tx.ccsSupports }}</span>
          <span
            v-for="c in ccsClients"
            :key="c"
            class="rounded-md border border-gray-200 px-2 py-0.5 text-xs font-medium text-gray-700 dark:border-dark-700 dark:text-gray-300"
          >{{ c }}</span>
        </div>
        <button class="btn btn-primary" @click="openDeeplink">
          {{ tx.ccsImport }}
        </button>
        <p class="text-xs text-gray-500 dark:text-dark-400">{{ tx.ccsHint }}</p>
        <CodeBlock :label="tx.ccsManualLink" :code="deeplink" :copied="copiedId === 'deeplink'" :copy-label="tx.copy" :copied-label="tx.copied" @copy="copy(deeplink, 'deeplink')" />
      </div>

      <!-- ===== Claude Code ===== -->
      <div v-else-if="active === 'claude'" class="space-y-4">
        <p class="text-sm text-gray-600 dark:text-dark-400">{{ tx.claudeIntro }}</p>
        <ol class="space-y-1.5 text-sm text-gray-700 dark:text-gray-300">
          <li v-for="(s, i) in tx.claudeSteps" :key="i" class="flex gap-2">
            <span class="font-mono text-xs text-clay-num">{{ String(i + 1).padStart(2, '0') }}</span><span>{{ s }}</span>
          </li>
        </ol>
        <CodeBlock :label="tx.envVars" :code="claudeEnv" :copied="copiedId === 'claudeEnv'" :copy-label="tx.copy" :copied-label="tx.copied" @copy="copy(claudeEnv, 'claudeEnv')" />
        <CodeBlock :label="'~/.claude/settings.json'" :code="claudeJson" :copied="copiedId === 'claudeJson'" :copy-label="tx.copy" :copied-label="tx.copied" @copy="copy(claudeJson, 'claudeJson')" />
        <p class="text-xs text-gray-500 dark:text-dark-400">{{ tx.claudeHint }}</p>
      </div>

      <!-- ===== Codex / OpenAI SDK ===== -->
      <div v-else-if="active === 'openai'" class="space-y-4">
        <p class="text-sm text-gray-600 dark:text-dark-400">{{ tx.codexIntro }}</p>
        <ol class="space-y-1.5 text-sm text-gray-700 dark:text-gray-300">
          <li v-for="(s, i) in tx.codexSteps" :key="i" class="flex gap-2">
            <span class="font-mono text-xs text-clay-num">{{ String(i + 1).padStart(2, '0') }}</span><span>{{ s }}</span>
          </li>
        </ol>
        <CodeBlock label="~/.codex/config.toml" :code="codexToml" :copied="copiedId === 'codexToml'" :copy-label="tx.copy" :copied-label="tx.copied" @copy="copy(codexToml, 'codexToml')" />
        <CodeBlock :label="tx.envVars" :code="openaiEnv" :copied="copiedId === 'openaiEnv'" :copy-label="tx.copy" :copied-label="tx.copied" @copy="copy(openaiEnv, 'openaiEnv')" />
        <p class="pt-1 text-xs font-medium text-gray-600 dark:text-dark-400">{{ tx.genericSdk }}</p>
        <CodeBlock label="Python" :code="openaiPy" :copied="copiedId === 'openaiPy'" :copy-label="tx.copy" :copied-label="tx.copied" @copy="copy(openaiPy, 'openaiPy')" />
        <CodeBlock label="curl" :code="openaiCurl" :copied="copiedId === 'openaiCurl'" :copy-label="tx.copy" :copied-label="tx.copied" @copy="copy(openaiCurl, 'openaiCurl')" />
      </div>

      <!-- ===== 一键脚本 ===== -->
      <div v-else-if="active === 'script'" class="space-y-4">
        <p class="text-sm text-gray-600 dark:text-dark-400">{{ tx.scriptIntro }}</p>
        <CodeBlock label="macOS / Linux" :code="script" :copied="copiedId === 'script'" :copy-label="tx.copy" :copied-label="tx.copied" @copy="copy(script, 'script')" />
        <p class="text-xs text-gray-500 dark:text-dark-400">{{ tx.scriptHint }}</p>
      </div>

      <!-- ===== 手动配置 / 排障（兜底） ===== -->
      <div v-else-if="active === 'manual'" class="space-y-4">
        <p class="text-sm text-gray-600 dark:text-dark-400">{{ tx.manualIntro }}</p>
        <div class="overflow-hidden rounded-md border border-gray-200 dark:border-dark-700">
          <table class="w-full text-sm">
            <tbody>
              <tr v-for="(item, idx) in manualRows" :key="item.k" :class="idx > 0 ? 'border-t border-gray-100 dark:border-dark-800' : ''">
                <td class="w-40 px-4 py-2.5 text-gray-600 dark:text-dark-400">{{ item.k }}</td>
                <td class="px-4 py-2.5">
                  <div class="flex items-center gap-2">
                    <code class="truncate font-mono text-sm text-gray-900 dark:text-gray-100">{{ item.v }}</code>
                    <button class="copy-btn shrink-0" @click="copy(item.v, 'm' + idx)">
                      <span class="text-xs">{{ copiedId === 'm' + idx ? tx.copied : tx.copy }}</span>
                    </button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div>
          <p class="mb-2 font-serif text-sm text-gray-900 dark:text-white">{{ tx.troubleshootTitle }}</p>
          <ul class="space-y-1.5 text-sm text-gray-600 dark:text-dark-400">
            <li v-for="(t, i) in tx.troubleshoot" :key="i" class="flex gap-2">
              <span class="font-mono text-xs text-gray-400">{{ String(i + 1).padStart(2, '0') }}</span>
              <span>{{ t }}</span>
            </li>
          </ul>
        </div>
        <a
          v-if="docUrl"
          :href="docUrl"
          target="_blank"
          rel="noopener noreferrer"
          class="inline-flex text-sm font-medium text-primary-700 hover:underline dark:text-primary-400"
        >
          {{ tx.viewDocs }} →
        </a>
      </div>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, computed, watch, h, defineComponent } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { buildCcSwitchImportDeeplink, type CcSwitchClientType } from '@/utils/ccswitchImport'

interface OnboardingKey {
  key: string
  name?: string
  group?: { platform?: string | null } | null
}

const props = defineProps<{
  show: boolean
  apiKey: OnboardingKey | null
  baseUrl: string
  siteName?: string
  docUrl?: string
}>()

const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()

// 接入指引文案（i18n: keyOnboarding.*）
const tx = computed(() => ({
  title: t('keyOnboarding.title'),
  currentKey: t('keyOnboarding.currentKey'),
  endpoint: t('keyOnboarding.endpoint'),
  copy: t('keyOnboarding.copy'),
  copied: t('keyOnboarding.copied'),
  intro: t('keyOnboarding.intro'),
  recommended: t('keyOnboarding.recommended'),
  envVars: t('keyOnboarding.envVars'),
  genericSdk: t('keyOnboarding.genericSdk'),
  ccsIntro: t('keyOnboarding.ccsIntro'),
  ccsSupports: t('keyOnboarding.ccsSupports'),
  ccsImport: t('keyOnboarding.ccsImport'),
  ccsHint: t('keyOnboarding.ccsHint'),
  ccsManualLink: t('keyOnboarding.ccsManualLink'),
  claudeIntro: t('keyOnboarding.claudeIntro'),
  claudeSteps: [
    t('keyOnboarding.claudeStep1'),
    t('keyOnboarding.claudeStep2'),
    t('keyOnboarding.claudeStep3'),
  ],
  claudeHint: t('keyOnboarding.claudeHint'),
  codexIntro: t('keyOnboarding.codexIntro'),
  codexSteps: [
    t('keyOnboarding.codexStep1'),
    t('keyOnboarding.codexStep2'),
    t('keyOnboarding.codexStep3'),
  ],
  scriptIntro: t('keyOnboarding.scriptIntro'),
  scriptHint: t('keyOnboarding.scriptHint'),
  manualIntro: t('keyOnboarding.manualIntro'),
  troubleshootTitle: t('keyOnboarding.troubleshootTitle'),
  troubleshoot: [
    t('keyOnboarding.troubleshoot1'),
    t('keyOnboarding.troubleshoot2'),
    t('keyOnboarding.troubleshoot3'),
    t('keyOnboarding.troubleshoot4'),
    t('keyOnboarding.troubleshoot5'),
  ],
  viewDocs: t('keyOnboarding.viewDocs'),
  model: t('keyOnboarding.model'),
  apiKeyFull: t('keyOnboarding.apiKeyFull'),
}))

const base = computed(() => (props.baseUrl || '').replace(/\/+$/, ''))
const openaiBase = computed(() => `${base.value}/v1`)
const fullKey = computed(() => props.apiKey?.key || '')
const platform = computed(() => props.apiKey?.group?.platform || 'anthropic')

// CC Switch 为推荐方式
const recommended = 'ccswitch'
const ccsClients = ['Claude Code', 'Codex', 'OpenClaw', 'Hermes']

// 供应商 id（用于 config.toml 的表名，需为安全标识符）
const providerId = computed(() => {
  const raw = (props.siteName || 'sub2api').toLowerCase().replace(/[^a-z0-9_]/g, '')
  return raw || 'sub2api'
})

const maskedKey = computed(() => {
  const k = fullKey.value
  if (k.length <= 12) return k
  return `${k.slice(0, 6)}…${k.slice(-4)}`
})

const methods = computed(() => [
  { id: 'ccswitch', label: 'CC Switch' },
  { id: 'claude', label: 'Claude Code' },
  { id: 'openai', label: 'Codex / OpenAI SDK' },
  { id: 'script', label: t('keyOnboarding.methodScript') },
  { id: 'manual', label: t('keyOnboarding.methodManual') },
])

const active = ref<string>('ccswitch')
watch(
  () => props.show,
  (v) => {
    if (v) active.value = recommended
  },
  { immediate: true }
)

// ===== Snippets =====
const claudeEnv = computed(
  () => `export ANTHROPIC_BASE_URL="${base.value}"\nexport ANTHROPIC_AUTH_TOKEN="${fullKey.value}"`
)
const claudeJson = computed(
  () => `{\n  "env": {\n    "ANTHROPIC_BASE_URL": "${base.value}",\n    "ANTHROPIC_AUTH_TOKEN": "${fullKey.value}"\n  }\n}`
)
const codexToml = computed(
  () =>
    `model = "gpt-5.5"\nmodel_provider = "${providerId.value}"\n\n[model_providers.${providerId.value}]\nname = "${(props.siteName || 'sub2api').trim() || 'sub2api'}"\nbase_url = "${openaiBase.value}"\nwire_api = "chat"`
)
const openaiEnv = computed(
  () => `export OPENAI_BASE_URL="${openaiBase.value}"\nexport OPENAI_API_KEY="${fullKey.value}"`
)
const openaiPy = computed(
  () =>
    `from openai import OpenAI\n\nclient = OpenAI(\n    base_url="${openaiBase.value}",\n    api_key="${fullKey.value}",\n)\nresp = client.chat.completions.create(\n    model="gpt-5.5",\n    messages=[{"role": "user", "content": "你好"}],\n)\nprint(resp.choices[0].message.content)`
)
const openaiCurl = computed(
  () =>
    `curl ${openaiBase.value}/chat/completions \\\n  -H "Authorization: Bearer ${fullKey.value}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"gpt-5.5","messages":[{"role":"user","content":"你好"}]}'`
)

const script = computed(() => {
  if (platform.value === 'openai') {
    return `# ${t('keyOnboarding.scriptCommentCodex')}\nmkdir -p ~/.codex\ncat > ~/.codex/config.toml <<'EOF'\nmodel = "gpt-5.5"\nmodel_provider = "${providerId.value}"\n\n[model_providers.${providerId.value}]\nname = "${(props.siteName || 'sub2api').trim() || 'sub2api'}"\nbase_url = "${openaiBase.value}"\nwire_api = "chat"\nEOF\ncat >> ~/.zshrc <<'EOF'\nexport OPENAI_API_KEY="${fullKey.value}"\nEOF\nsource ~/.zshrc\necho "✓ ${t('keyOnboarding.scriptDoneCodex')}"`
  }
  return `mkdir -p ~/.claude\ncat > ~/.claude/settings.json <<'EOF'\n{\n  "env": {\n    "ANTHROPIC_BASE_URL": "${base.value}",\n    "ANTHROPIC_AUTH_TOKEN": "${fullKey.value}"\n  }\n}\nEOF\necho "✓ ${t('keyOnboarding.scriptDoneClaude')}"`
})

const usageScript = `({
    request: {
      url: "{{baseUrl}}/v1/usage",
      method: "GET",
      headers: { "Authorization": "Bearer {{apiKey}}" }
    },
    extractor: function(response) {
      const remaining = response?.remaining ?? response?.quota?.remaining ?? response?.balance;
      const unit = response?.unit ?? response?.quota?.unit ?? "USD";
      return {
        isValid: response?.is_active ?? response?.isValid ?? true,
        remaining,
        unit
      };
    }
  })`

const ccsClientType = computed<CcSwitchClientType>(() => (platform.value === 'gemini' ? 'gemini' : 'claude'))
const deeplink = computed(() =>
  buildCcSwitchImportDeeplink({
    baseUrl: base.value,
    platform: platform.value as never,
    clientType: ccsClientType.value,
    providerName: (props.siteName || 'sub2api').trim() || 'sub2api',
    apiKey: fullKey.value,
    usageScript,
  })
)

function openDeeplink() {
  try {
    window.open(deeplink.value, '_self')
  } catch {
    /* 用户可手动复制链接 */
  }
}

const manualRows = computed(() => [
  { k: 'base_url', v: base.value },
  { k: 'OpenAI base_url', v: openaiBase.value },
  { k: tx.value.apiKeyFull, v: fullKey.value },
  { k: tx.value.model, v: 'gpt-5.5' },
])

// ===== Copy =====
const copiedId = ref<string>('')
let copyTimer: ReturnType<typeof setTimeout> | null = null
function copy(text: string, id: string) {
  navigator.clipboard?.writeText(text).then(() => {
    copiedId.value = id
    if (copyTimer) clearTimeout(copyTimer)
    copyTimer = setTimeout(() => (copiedId.value = ''), 1500)
  })
}

// 轻量内联代码块组件（带复制按钮）
const CodeBlock = defineComponent({
  name: 'OnboardingCodeBlock',
  props: {
    label: { type: String, default: '' },
    code: { type: String, default: '' },
    copied: { type: Boolean, default: false },
    copyLabel: { type: String, default: 'Copy' },
    copiedLabel: { type: String, default: 'Copied' },
  },
  emits: ['copy'],
  setup(props, { emit }) {
    return () =>
      h('div', { class: 'rounded-md border border-gray-200 dark:border-dark-700 overflow-hidden' }, [
        h('div', { class: 'flex items-center justify-between border-b border-gray-100 px-3 py-1.5 dark:border-dark-800' }, [
          h('span', { class: 'font-mono text-xs text-gray-500 dark:text-dark-400' }, props.label),
          h('button', { class: 'copy-btn', onClick: () => emit('copy') }, [
            h('span', { class: 'text-xs' }, props.copied ? props.copiedLabel : props.copyLabel),
          ]),
        ]),
        h('pre', { class: 'overflow-x-auto bg-gray-900 px-4 py-3 text-[13px] leading-relaxed text-gray-100 dark:bg-dark-950' }, [
          h('code', { class: 'font-mono' }, props.code),
        ]),
      ])
  },
})
</script>

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
:global(.dark) .copy-btn {
  border-color: theme('colors.dark.700');
  color: theme('colors.gray.400');
}
:global(.dark) .copy-btn:hover {
  background-color: theme('colors.dark.800');
  color: #fff;
}
.text-clay-num {
  color: theme('colors.primary.500');
}
</style>
