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

      <!-- ===== Claude Code ===== -->
      <div v-if="active === 'claude'" class="space-y-4">
        <p class="text-sm text-gray-600 dark:text-dark-400">{{ tx.claudeIntro }}</p>
        <CodeBlock :label="tx.envVars" :code="claudeEnv" :copied="copiedId === 'claudeEnv'" :copy-label="tx.copy" :copied-label="tx.copied" @copy="copy(claudeEnv, 'claudeEnv')" />
        <CodeBlock :label="'~/.claude/settings.json'" :code="claudeJson" :copied="copiedId === 'claudeJson'" :copy-label="tx.copy" :copied-label="tx.copied" @copy="copy(claudeJson, 'claudeJson')" />
        <p class="text-xs text-gray-500 dark:text-dark-400">{{ tx.claudeHint }}</p>
      </div>

      <!-- ===== Codex / OpenAI SDK ===== -->
      <div v-else-if="active === 'openai'" class="space-y-4">
        <p class="text-sm text-gray-600 dark:text-dark-400">{{ tx.openaiIntro }}</p>
        <CodeBlock :label="tx.envVars" :code="openaiEnv" :copied="copiedId === 'openaiEnv'" :copy-label="tx.copy" :copied-label="tx.copied" @copy="copy(openaiEnv, 'openaiEnv')" />
        <CodeBlock label="Python" :code="openaiPy" :copied="copiedId === 'openaiPy'" :copy-label="tx.copy" :copied-label="tx.copied" @copy="copy(openaiPy, 'openaiPy')" />
        <CodeBlock label="curl" :code="openaiCurl" :copied="copiedId === 'openaiCurl'" :copy-label="tx.copy" :copied-label="tx.copied" @copy="copy(openaiCurl, 'openaiCurl')" />
      </div>

      <!-- ===== CC Switch 一键导入 ===== -->
      <div v-else-if="active === 'ccswitch'" class="space-y-4">
        <p class="text-sm text-gray-600 dark:text-dark-400">{{ tx.ccsIntro }}</p>
        <button class="btn btn-primary" @click="openDeeplink">
          {{ tx.ccsImport }}
        </button>
        <p class="text-xs text-gray-500 dark:text-dark-400">{{ tx.ccsHint }}</p>
        <CodeBlock :label="tx.ccsManualLink" :code="deeplink" :copied="copiedId === 'deeplink'" :copy-label="tx.copy" :copied-label="tx.copied" @copy="copy(deeplink, 'deeplink')" />
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

const { locale } = useI18n()
const isZh = computed(() => String(locale.value).toLowerCase().startsWith('zh'))

// 自包含中英文案（避免改动共享 i18n 文件）
const TXT = {
  zh: {
    title: '一键接入',
    currentKey: '当前密钥',
    endpoint: 'API 端点',
    copy: '复制',
    copied: '已复制',
    intro: '用这个密钥与端点生成本地配置或导入链接，选择你的客户端即可。',
    recommended: '推荐',
    envVars: '环境变量',
    claudeIntro: '将以下环境变量写入终端，或保存到 ~/.claude/settings.json，重启 Claude Code 后生效。',
    claudeHint: '提示：使用 settings.json 无需每次设置环境变量；修改后需重启客户端。',
    openaiIntro: '兼容主流 OpenAI SDK。设置 base_url 与 api_key 即可调用。',
    ccsIntro: '已安装 CC Switch 时，点击下方按钮自动导入该密钥与端点。',
    ccsImport: '导入到 CC Switch',
    ccsHint: '若未弹出 CC Switch，说明尚未安装或未关联协议；可复制下方链接手动导入。',
    ccsManualLink: '导入链接',
    scriptIntro: '复制下面这段脚本到终端执行，自动写入本地配置（脚本完全可见、不联网下载）。',
    scriptHint: '执行后重启客户端即可。脚本仅写入本地配置文件，可先通读再运行。',
    manualIntro: '以上方式都不行？用下面的原始值手动填写客户端配置，并对照排障清单。',
    troubleshootTitle: '排障清单',
    troubleshoot: [
      '确认 base_url 完整且无多余斜杠，按客户端要求决定是否带 /v1。',
      '确认密钥完整复制（以 sk- 开头），无空格或换行。',
      '修改环境变量或配置文件后，需重启客户端使其生效。',
      '检查本地网络 / 代理是否能访问该端点。',
      '确认客户端为较新版本，老版本可能不支持自定义端点。',
    ],
    viewDocs: '查看文档',
    model: '推荐模型',
    apiKeyFull: 'API 密钥',
  },
  en: {
    title: 'Quick connect',
    currentKey: 'Current key',
    endpoint: 'API endpoint',
    copy: 'Copy',
    copied: 'Copied',
    intro: 'Use this key and endpoint to generate local config or an import link — pick your client below.',
    recommended: 'Recommended',
    envVars: 'Environment variables',
    claudeIntro: 'Set the environment variables below, or save them to ~/.claude/settings.json, then restart Claude Code.',
    claudeHint: 'Tip: settings.json avoids exporting env vars each time; restart the client after editing.',
    openaiIntro: 'Compatible with standard OpenAI SDKs. Set base_url and api_key to start.',
    ccsIntro: 'With CC Switch installed, click below to import this key and endpoint automatically.',
    ccsImport: 'Import to CC Switch',
    ccsHint: 'If CC Switch did not open, it may not be installed or the protocol is unregistered — copy the link below to import manually.',
    ccsManualLink: 'Import link',
    scriptIntro: 'Copy this script into your terminal to write the local config (fully visible, no network download).',
    scriptHint: 'Restart your client afterwards. The script only writes a local config file — read it before running.',
    manualIntro: 'None of the above worked? Fill your client config manually with the raw values and follow the checklist.',
    troubleshootTitle: 'Troubleshooting',
    troubleshoot: [
      'Confirm base_url is complete with no trailing slash; include /v1 only if your client requires it.',
      'Confirm the key is copied in full (starts with sk-), no spaces or line breaks.',
      'Restart the client after changing env vars or config files.',
      'Check that your local network / proxy can reach the endpoint.',
      'Make sure the client is up to date; older versions may not support custom endpoints.',
    ],
    viewDocs: 'View docs',
    model: 'Suggested model',
    apiKeyFull: 'API key',
  },
}
const tx = computed(() => (isZh.value ? TXT.zh : TXT.en))

const base = computed(() => (props.baseUrl || '').replace(/\/+$/, ''))
const openaiBase = computed(() => `${base.value}/v1`)
const fullKey = computed(() => props.apiKey?.key || '')
const platform = computed(() => props.apiKey?.group?.platform || 'anthropic')
const isOpenAI = computed(() => platform.value === 'openai')
const recommended = computed(() => (isOpenAI.value ? 'openai' : 'claude'))

const maskedKey = computed(() => {
  const k = fullKey.value
  if (k.length <= 12) return k
  return `${k.slice(0, 6)}…${k.slice(-4)}`
})

const methods = computed(() => [
  { id: 'claude', label: 'Claude Code' },
  { id: 'openai', label: isZh.value ? 'Codex / OpenAI SDK' : 'Codex / OpenAI SDK' },
  { id: 'ccswitch', label: 'CC Switch' },
  { id: 'script', label: isZh.value ? '一键脚本' : 'Script' },
  { id: 'manual', label: isZh.value ? '手动 / 排障' : 'Manual' },
])

const active = ref<string>('claude')
watch(
  () => props.show,
  (v) => {
    if (v) active.value = recommended.value
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
  if (isOpenAI.value) {
    return `# 写入 shell 配置（按需改成 ~/.bashrc）\ncat >> ~/.zshrc <<'EOF'\nexport OPENAI_BASE_URL="${openaiBase.value}"\nexport OPENAI_API_KEY="${fullKey.value}"\nEOF\nsource ~/.zshrc\necho "✓ OpenAI 端点已配置"`
  }
  return `mkdir -p ~/.claude\ncat > ~/.claude/settings.json <<'EOF'\n{\n  "env": {\n    "ANTHROPIC_BASE_URL": "${base.value}",\n    "ANTHROPIC_AUTH_TOKEN": "${fullKey.value}"\n  }\n}\nEOF\necho "✓ Claude Code 已配置完成，请重启客户端"`
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
</style>
