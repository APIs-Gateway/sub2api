<template>
  <BaseDialog :show="show" :title="t('keyOnboarding.title')" width="wide" @close="emit('close')">
    <!-- 复制成功后向读屏软件播报 -->
    <p class="sr-only" role="status" aria-live="polite" data-test="copy-status">{{ copiedId ? t('keyOnboarding.copied') : '' }}</p>
    <div v-if="apiKey" class="space-y-5">
      <!-- 副标题：弹窗标题在 BaseDialog 的头部，这里补上「接入哪个密钥」 -->
      <p class="flex flex-wrap items-baseline gap-x-3 gap-y-1" data-test="subtitle">
        <span class="text-base text-gray-600 dark:text-dark-300">{{ t('keyOnboarding.subtitle', { name: apiKey.name || t('keys.apiKey') }) }}</span>
        <code class="font-mono text-xs tabular-nums text-gray-500 dark:text-dark-400">{{ maskedKey }}</code>
      </p>

      <!-- 页签 -->
      <div
        ref="tablistRef"
        class="flex flex-wrap gap-1 border-b border-gray-200 pb-3 dark:border-dark-700"
        role="tablist"
        @keydown="onTabKeydown"
      >
        <button
          v-for="tab in tabs"
          :id="tabId(tab.id)"
          :key="tab.id"
          type="button"
          role="tab"
          class="onb-tab"
          :class="{ 'onb-tab-active': active === tab.id }"
          :aria-selected="active === tab.id"
          :aria-controls="panelId(tab.id)"
          :tabindex="active === tab.id ? 0 : -1"
          :data-test="`tab-${tab.id}`"
          @click="active = tab.id"
        >
          <Icon :name="tab.icon" size="sm" aria-hidden="true" />
          <span>{{ tab.label }}</span>
        </button>
      </div>

      <!-- 没有分组 -->
      <p
        v-if="!platform && active !== 'manual'"
        v-bind="panelAttrs"
        class="rounded-md border border-primary-200 bg-primary-50 px-3 py-2 text-sm text-primary-800 dark:border-primary-500/40 dark:bg-primary-500/10 dark:text-primary-300"
        data-test="no-group"
      >
        {{ t('keyOnboarding.noGroup') }}
      </p>

      <!-- ===== 一键安装 ===== -->
      <div v-else-if="active === 'install'" v-bind="panelAttrs" class="space-y-4" data-test="panel-install">
        <section v-for="card in cards" :key="card.client" class="onb-card" :data-test="`client-${card.client}`">
          <header class="flex items-center justify-between gap-3">
            <h4 class="font-serif text-lg text-gray-900 dark:text-white">{{ card.label }}</h4>
            <a
              v-if="card.tutorial"
              :href="card.tutorial"
              target="_blank"
              rel="noopener noreferrer"
              class="onb-link"
              :data-test="`tutorial-${card.client}`"
            >
              {{ t('keyOnboarding.install.tutorial') }}
              <Icon name="externalLink" size="sm" aria-hidden="true" />
            </a>
          </header>

          <div class="mt-4 space-y-2.5">
            <div v-for="(row, idx) in card.rows" :key="idx" class="grid gap-2.5 sm:grid-cols-2">
              <button
                v-for="tile in row"
                :key="tile.id"
                type="button"
                class="onb-tile"
                :data-copied="copiedId === tile.id ? 'true' : 'false'"
                :aria-label="t('keyOnboarding.install.copyTile', { client: card.label, label: tile.label })"
                :data-test="`copy-${tile.id}`"
                @click="copy(scripts[tile.id], tile.id)"
              >
                <span class="min-w-0 truncate">{{ tile.label }}</span>
                <Icon v-if="copiedId === tile.id" name="check" size="md" class="onb-tile-icon onb-pop" data-test="icon-check" aria-hidden="true" />
                <Icon v-else name="copy" size="md" class="onb-tile-icon" data-test="icon-copy" aria-hidden="true" />
              </button>
            </div>
          </div>

          <p v-if="card.client === 'codex'" class="mt-3 text-xs leading-relaxed text-gray-500 dark:text-dark-400" data-test="codex-modes">
            {{ t('keyOnboarding.install.codexModes', { min: CODEX_MIN_NODE_MAJOR }) }}
          </p>

          <!-- 想先看脚本再运行的人：折叠，默认不占地方 -->
          <details class="mt-3" :data-test="`script-${card.client}`">
            <summary class="inline-flex cursor-pointer select-none items-center gap-1 text-xs text-gray-500 hover:text-gray-900 dark:text-dark-400 dark:hover:text-white">
              {{ t('keyOnboarding.install.viewScript') }}
            </summary>
            <div class="mt-2 space-y-2">
              <ScriptBlock
                v-for="tile in card.rows.flat()"
                :key="tile.id"
                :label="`${tile.label} · ${scriptTargetPath(card.client, tile.os)}`"
                :code="scripts[tile.id]"
              />
            </div>
          </details>
        </section>

        <p class="text-sm leading-relaxed text-gray-500 dark:text-dark-400" data-test="install-footnote">
          {{ t('keyOnboarding.install.footnote', { site: siteName }) }}
        </p>
      </div>

      <!-- ===== 交给 AI ===== -->
      <div v-else-if="active === 'ai'" v-bind="panelAttrs" class="onb-card space-y-4" data-test="panel-ai">
        <p class="text-sm text-gray-600 dark:text-dark-400">{{ t('keyOnboarding.ai.intro') }}</p>
        <div class="flex flex-wrap gap-2" role="radiogroup" :aria-label="t('keyOnboarding.ai.clientLabel')">
          <button
            v-for="c in AI_CLIENTS"
            :key="c"
            type="button"
            role="radio"
            class="onb-chip"
            :class="{ 'onb-chip-active': aiClient === c }"
            :aria-checked="aiClient === c"
            :data-test="`ai-client-${c}`"
            @click="aiClient = c"
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
          <button type="button" class="btn btn-primary btn-sm" data-test="ai-copy" @click="copy(aiShort, 'ai-short')">
            {{ copiedId === 'ai-short' ? t('keyOnboarding.copied') : t('keyOnboarding.copy') }}
          </button>
          <button type="button" class="btn btn-secondary btn-sm" data-test="ai-copy-detail" @click="copy(aiDetailed, 'ai-detail')">
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

      <!-- ===== CC Switch ===== -->
      <div v-else-if="active === 'ccswitch'" v-bind="panelAttrs" class="onb-card space-y-4" data-test="panel-ccswitch">
        <div class="grid gap-4 sm:grid-cols-2">
          <div v-if="ccsClients.length > 1">
            <span :id="ccsClientLabelId" class="input-label">{{ t('keyOnboarding.ccs.client') }}</span>
            <div
              class="flex flex-wrap gap-2"
              role="radiogroup"
              :aria-label="t('keyOnboarding.ccs.client')"
              :aria-labelledby="ccsClientLabelId"
            >
              <button
                v-for="c in ccsClients"
                :key="c"
                type="button"
                role="radio"
                class="onb-chip"
                :class="{ 'onb-chip-active': ccsClient === c }"
                :aria-checked="ccsClient === c"
                :data-test="`ccs-client-${c}`"
                @click="ccsClient = c"
              >
                {{ CCS_LABELS[c] }}
              </button>
            </div>
          </div>
          <div :class="ccsClients.length > 1 ? '' : 'sm:col-span-2'">
            <label class="input-label" for="ccs-name">{{ t('keyOnboarding.ccs.name') }}</label>
            <input id="ccs-name" v-model="ccsCustomName" type="text" class="input" :placeholder="ccsDefaultName" data-test="ccs-name" />
          </div>
          <div v-if="models.length > 0" class="sm:col-span-2">
            <label class="input-label" for="ccs-model">{{ t('keyOnboarding.ccs.model') }}</label>
            <select id="ccs-model" v-model="ccsModel" class="input" data-test="ccs-model">
              <option value="">{{ t('keyOnboarding.ccs.modelDefault') }}</option>
              <option v-for="m in models" :key="m" :value="m">{{ m }}</option>
            </select>
          </div>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <button type="button" class="btn btn-primary" data-test="ccs-open" @click="openDeeplink">
            {{ t('keyOnboarding.ccs.open') }}
          </button>
          <button type="button" class="btn btn-secondary" data-test="ccs-copy-link" @click="copy(deeplink, 'deeplink')">
            {{ copiedId === 'deeplink' ? t('keyOnboarding.copied') : t('keyOnboarding.ccs.copyLink') }}
          </button>
        </div>
        <p class="text-sm text-gray-600 dark:text-dark-400">
          {{ t('keyOnboarding.ccs.notInstalled') }}
          <a
            :href="CC_SWITCH_RELEASES"
            target="_blank"
            rel="noopener noreferrer"
            class="font-medium text-primary-700 underline-offset-2 hover:underline dark:text-primary-400"
          >{{ t('keyOnboarding.ccs.download') }}</a>
        </p>
      </div>

      <!-- ===== 手动配置 ===== -->
      <div v-else-if="active === 'manual'" v-bind="panelAttrs" class="onb-card space-y-5" data-test="panel-manual">
        <div class="overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
          <table class="w-full text-sm">
            <tbody>
              <tr v-for="(row, idx) in manualRows" :key="row.id" :class="idx > 0 ? 'border-t border-gray-100 dark:border-dark-800' : ''">
                <td class="w-28 px-4 py-2.5 text-gray-600 dark:text-dark-400 sm:w-40">{{ row.label }}</td>
                <td class="px-4 py-2.5">
                  <div class="flex items-center gap-2">
                    <code class="min-w-0 flex-1 truncate font-mono text-sm text-gray-900 dark:text-gray-100">{{ row.shown }}</code>
                    <button type="button" class="copy-btn shrink-0" @click="copy(row.value, `m-${row.id}`)">
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
                @copy="copy(f.code, `${s.id}-${f.label}`)"
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
    </div>
  </BaseDialog>
</template>

<script lang="ts">
// 同一页上多个实例时，页签和面板的 id 不能撞
let onboardingUid = 0
</script>

<script setup lang="ts">
import { ref, computed, watch, h, defineComponent, nextTick, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import { userChannelsAPI } from '@/api/channels'
import type { UserAvailableChannel } from '@/api/channels'
import {
  CC_SWITCH_USAGE_SCRIPT,
  OPENAI_CC_SWITCH_CODEX_MODEL,
  buildCcSwitchImportDeeplink,
  type CcSwitchClientType
} from '@/utils/ccswitchImport'
import {
  AI_CLIENTS,
  CLIENT_LABELS,
  CODEX_MIN_NODE_MAJOR,
  SCRIPT_ERROR_TOKEN,
  SCRIPT_PATH_TOKEN,
  SCRIPT_VERSION_TOKEN,
  buildAiPrompt,
  buildInstallScript,
  chatgptUrl,
  claudeUrl,
  clientsForPlatform,
  codexProviderId,
  endpointFor,
  scriptTargetPath,
  tutorialHref,
  type AiClient,
  type CodexInstallMode,
  type OnboardingClient,
  type ScriptOs
} from '@/utils/keyOnboarding'

export type OnboardingTab = 'install' | 'ai' | 'ccswitch' | 'manual'

interface OnboardingKey {
  key: string
  name?: string
  group_id?: number | null
  group?: { id?: number; platform?: string | null; allow_messages_dispatch?: boolean } | null
}

const props = withDefaults(
  defineProps<{
    show: boolean
    apiKey: OnboardingKey | null
    baseUrl: string
    siteName?: string
    docUrl?: string
    initialTab?: OnboardingTab
  }>(),
  { initialTab: 'install' }
)

const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
const appStore = useAppStore()
const uid = `onboarding-${++onboardingUid}`

const CC_SWITCH_RELEASES = 'https://github.com/farion1231/cc-switch/releases'
const CCS_LABELS: Record<CcSwitchClientType | 'codex', string> = {
  claude: 'Claude',
  codex: 'Codex',
  gemini: 'Gemini'
}

const base = computed(() => (props.baseUrl || '').trim().replace(/\/+$/, ''))
const fullKey = computed(() => props.apiKey?.key || '')
const platform = computed(() => props.apiKey?.group?.platform || null)
const siteName = computed(() => (props.siteName || '').trim() || 'sub2api')

const maskedKey = computed(() => {
  const k = fullKey.value
  if (k.length <= 12) return k
  return `${k.slice(0, 6)}…${k.slice(-4)}`
})

// ===== 页签 =====
const tabs = computed<{ id: OnboardingTab; label: string; icon: 'bolt' | 'sparkles' | 'swap' | 'terminal' }[]>(() => [
  { id: 'install', label: t('keyOnboarding.tabs.install'), icon: 'bolt' },
  { id: 'ai', label: t('keyOnboarding.tabs.ai'), icon: 'sparkles' },
  { id: 'ccswitch', label: t('keyOnboarding.tabs.ccswitch'), icon: 'swap' },
  { id: 'manual', label: t('keyOnboarding.tabs.manual'), icon: 'terminal' }
])

const active = ref<OnboardingTab>(props.initialTab)
const copiedId = ref<string>('')

const tabId = (id: OnboardingTab) => `${uid}-tab-${id}`
const panelId = (id: OnboardingTab) => `${uid}-panel-${id}`
// 当前内容区：与对应页签互相关联
const panelAttrs = computed(() => ({
  role: 'tabpanel',
  id: panelId(active.value),
  'aria-labelledby': tabId(active.value)
}))

const tablistRef = ref<HTMLElement | null>(null)
// 方向键、Home、End 在页签间移动，焦点跟着走
function onTabKeydown(e: KeyboardEvent) {
  const list = tabs.value
  const current = list.findIndex((tab) => tab.id === active.value)
  let next: number
  switch (e.key) {
    case 'ArrowRight':
    case 'ArrowDown':
      next = (current + 1) % list.length
      break
    case 'ArrowLeft':
    case 'ArrowUp':
      next = (current - 1 + list.length) % list.length
      break
    case 'Home':
      next = 0
      break
    case 'End':
      next = list.length - 1
      break
    default:
      return
  }
  e.preventDefault()
  const target = list[next].id
  active.value = target
  void nextTick(() => tablistRef.value?.querySelector<HTMLElement>(`[id="${tabId(target)}"]`)?.focus())
}

// ===== 分组可用模型 =====
const channels = ref<UserAvailableChannel[] | null>(null)
let loadingModels = false
async function loadModels() {
  if (channels.value || loadingModels) return
  loadingModels = true
  try {
    channels.value = await userChannelsAPI.getAvailable()
  } catch {
    channels.value = []
  } finally {
    loadingModels = false
  }
}

const models = computed<string[]>(() => {
  const groupId = props.apiKey?.group?.id ?? props.apiKey?.group_id
  if (!groupId || !channels.value) return []
  const names = new Set<string>()
  for (const channel of channels.value) {
    for (const section of channel.platforms || []) {
      if (!(section.groups || []).some((g) => g.id === groupId)) continue
      for (const m of section.supported_models || []) {
        if (m?.name) names.add(m.name)
      }
    }
  }
  return [...names]
})

watch(
  () => props.show,
  (v) => {
    if (v) {
      active.value = props.initialTab
      copiedId.value = ''
      void loadModels()
    }
  },
  { immediate: true }
)
// 弹窗已打开时，行内的另一个入口再次点击也要能切换页签
watch(
  () => props.initialTab,
  (v) => {
    if (props.show) active.value = v
  }
)

// ===== 一键安装 =====
const clients = computed<OnboardingClient[]>(() =>
  clientsForPlatform(platform.value, { allowMessagesDispatch: props.apiKey?.group?.allow_messages_dispatch })
)

// 脚本在终端里打印的话，跟随当前界面语言；{path} / {error} / {version} 留给脚本运行时填
function scriptMessages() {
  return {
    pythonMissing: t('keyOnboarding.install.script.pythonMissing'),
    xcodeMissing: t('keyOnboarding.install.script.xcodeMissing'),
    backup: t('keyOnboarding.install.script.backup', { path: SCRIPT_PATH_TOKEN }),
    updated: t('keyOnboarding.install.script.updated', { path: SCRIPT_PATH_TOKEN }),
    failed: t('keyOnboarding.install.script.failed', { error: SCRIPT_ERROR_TOKEN }),
    nodeMissing: t('keyOnboarding.install.script.nodeMissing', { min: CODEX_MIN_NODE_MAJOR }),
    nodeTooOld: t('keyOnboarding.install.script.nodeTooOld', { version: SCRIPT_VERSION_TOKEN, min: CODEX_MIN_NODE_MAJOR }),
    npmMissing: t('keyOnboarding.install.script.npmMissing'),
    npmInstalling: t('keyOnboarding.install.script.npmInstalling'),
    npmPermission: t('keyOnboarding.install.script.npmPermission'),
    npmFailed: t('keyOnboarding.install.script.npmFailed')
  }
}

function installScript(client: OnboardingClient, os: ScriptOs, mode?: CodexInstallMode): string {
  return buildInstallScript(client, os, {
    baseUrl: base.value,
    apiKey: fullKey.value,
    platform: platform.value,
    siteName: siteName.value,
    mode,
    doneMessage: t('keyOnboarding.install.scriptDone', { client: CLIENT_LABELS[client] }),
    messages: scriptMessages()
  })
}

// 每张卡片：客户端名、教程链接，以及成行的复制瓦片（每行 macOS / Linux + Windows 两块）。
// Codex 有两行：完整安装、只刷新配置；其他客户端一行。
interface InstallTile {
  id: string
  os: ScriptOs
  mode?: CodexInstallMode
  label: string
}
interface InstallCard {
  client: OnboardingClient
  label: string
  tutorial: string | null
  rows: InstallTile[][]
}

const OS_LABELS: Record<ScriptOs, string> = { unix: 'macOS / Linux', windows: 'Windows' }

const cards = computed<InstallCard[]>(() =>
  clients.value.map((client) => {
    const modes: (CodexInstallMode | undefined)[] = client === 'codex' ? ['full', 'refresh'] : [undefined]
    const rows = modes.map((mode) =>
      (['unix', 'windows'] as const).map((os): InstallTile => {
        const modeLabel =
          mode === 'full' ? t('keyOnboarding.install.modeFull') : mode === 'refresh' ? t('keyOnboarding.install.modeRefresh') : ''
        return {
          id: mode ? `${client}-${mode}-${os}` : `${client}-${os}`,
          os,
          mode,
          label: modeLabel ? `${modeLabel} · ${OS_LABELS[os]}` : OS_LABELS[os]
        }
      })
    )
    return { client, label: CLIENT_LABELS[client], tutorial: tutorialHref(client, import.meta.env.BASE_URL), rows }
  })
)

// 所有瓦片对应的脚本：只在地址、密钥、语言等变化时重新生成，复制状态变化不会触发
const scripts = computed<Record<string, string>>(() => {
  const out: Record<string, string> = {}
  for (const card of cards.value) {
    for (const tile of card.rows.flat()) out[tile.id] = installScript(card.client, tile.os, tile.mode)
  }
  return out
})

// ===== 交给 AI =====
const aiClient = ref<AiClient>('claude')
// 注意：这里刻意不传密钥
function aiPrompt(detailed: boolean): string {
  return buildAiPrompt({
    t: (key, params) => t(key, params ?? {}),
    client: aiClient.value,
    clientLabel: t(`keyOnboarding.ai.clients.${aiClient.value}`),
    baseUrl: base.value,
    platform: platform.value,
    siteName: siteName.value,
    models: models.value,
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

// ===== CC Switch =====
const ccsClients = computed<(CcSwitchClientType | 'codex')[]>(() => {
  switch (platform.value) {
    case 'openai':
      return ['codex']
    case 'gemini':
      return ['gemini']
    case 'antigravity':
      return ['claude', 'gemini']
    default:
      return ['claude']
  }
})
const ccsClientLabelId = `${uid}-ccs-client`
const ccsClient = ref<CcSwitchClientType | 'codex'>('claude')
watch(
  ccsClients,
  (list) => {
    if (!list.includes(ccsClient.value)) ccsClient.value = list[0]
  },
  { immediate: true }
)
const ccsCustomName = ref('')
const ccsModel = ref('')
watch([() => props.show, () => props.apiKey?.group?.id], () => {
  ccsCustomName.value = ''
  ccsModel.value = ''
})
const ccsDefaultName = computed(() => `${siteName.value} - ${CCS_LABELS[ccsClient.value]}`)

const deeplink = computed(() =>
  buildCcSwitchImportDeeplink({
    baseUrl: base.value,
    platform: platform.value as never,
    clientType: ccsClient.value === 'gemini' ? 'gemini' : 'claude',
    providerName: ccsCustomName.value.trim() || ccsDefaultName.value,
    apiKey: fullKey.value,
    usageScript: CC_SWITCH_USAGE_SCRIPT,
    model: ccsModel.value || undefined
  })
)

function openDeeplink() {
  try {
    window.open(deeplink.value, '_self')
  } catch {
    /* 用户可手动复制链接 */
  }
}

// ===== 手动配置 =====
const manualRows = computed(() => [
  { id: 'base', label: t('keyOnboarding.manual.address'), shown: base.value, value: base.value },
  { id: 'v1', label: t('keyOnboarding.manual.openaiAddress'), shown: `${base.value}/v1`, value: `${base.value}/v1` },
  { id: 'key', label: t('keys.apiKey'), shown: maskedKey.value, value: fullKey.value }
])

function tomlQuote(v: string): string {
  return `"${v.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`
}

const snippets = computed(() => {
  // path：标签是文件路径（用等宽字体）；其余标签是普通文字
  const list: { id: string; title: string; files: { label: string; code: string; path?: boolean }[] }[] = []
  const key = fullKey.value
  for (const c of clients.value) {
    const url = endpointFor(c, platform.value, base.value)
    if (c === 'claude') {
      list.push({
        id: 'claude',
        title: CLIENT_LABELS.claude,
        files: [
          { label: t('keyOnboarding.manual.envVars'), code: `export ANTHROPIC_BASE_URL="${url}"\nexport ANTHROPIC_AUTH_TOKEN="${key}"` },
          {
            label: '~/.claude/settings.json',
            path: true,
            code: JSON.stringify({ env: { ANTHROPIC_BASE_URL: url, ANTHROPIC_AUTH_TOKEN: key } }, null, 2)
          }
        ]
      })
    } else if (c === 'codex') {
      const id = codexProviderId(siteName.value)
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
              `name = ${tomlQuote(siteName.value)}`,
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
          { label: t('keyOnboarding.manual.envVars'), code: `export GOOGLE_GEMINI_BASE_URL="${url}"\nexport GEMINI_API_KEY="${key}"` }
        ]
      })
    }
  }
  return list
})

// ===== 复制 =====
let copyTimer: ReturnType<typeof setTimeout> | null = null

async function writeClipboard(text: string): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    /* 落到下面的兜底 */
  }
  try {
    const el = document.createElement('textarea')
    el.value = text
    el.setAttribute('readonly', '')
    el.style.position = 'fixed'
    el.style.opacity = '0'
    document.body.appendChild(el)
    el.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(el)
    return ok
  } catch {
    return false
  }
}

async function copy(text: string, id: string) {
  if (!(await writeClipboard(text))) {
    appStore.showError(t('common.copyFailed'))
    return
  }
  copiedId.value = id
  if (copyTimer) clearTimeout(copyTimer)
  copyTimer = setTimeout(() => (copiedId.value = ''), 1800)
}

onBeforeUnmount(() => {
  if (copyTimer) clearTimeout(copyTimer)
})

// 脚本预览（只读，可选中）
const ScriptBlock = defineComponent({
  name: 'OnboardingScriptBlock',
  props: {
    label: { type: String, default: '' },
    code: { type: String, default: '' }
  },
  setup(p) {
    return () =>
      h('div', { class: 'overflow-hidden rounded-lg border border-gray-200 dark:border-dark-700' }, [
        h('div', { class: 'border-b border-gray-100 px-3 py-1.5 text-xs text-gray-500 dark:border-dark-800 dark:text-dark-400' }, p.label),
        h('pre', { class: 'max-h-64 overflow-auto bg-gray-900 px-4 py-3 text-[12px] leading-relaxed text-gray-100 dark:bg-dark-950' }, [
          h('code', { class: 'font-mono' }, p.code)
        ])
      ])
  }
})

// 带复制按钮的代码块
const CodeBlock = defineComponent({
  name: 'OnboardingCodeBlock',
  props: {
    label: { type: String, default: '' },
    /** 标签是文件路径时用等宽字体，普通文字用正常字体 */
    monoLabel: { type: Boolean, default: false },
    code: { type: String, default: '' },
    copied: { type: Boolean, default: false },
    copyLabel: { type: String, default: '' },
    copiedLabel: { type: String, default: '' }
  },
  emits: ['copy'],
  setup(p, { emit }) {
    return () =>
      h('div', { class: 'overflow-hidden rounded-lg border border-gray-200 dark:border-dark-700' }, [
        h('div', { class: 'flex items-center justify-between border-b border-gray-100 px-3 py-1.5 dark:border-dark-800' }, [
          h('span', { class: [p.monoLabel ? 'font-mono' : '', 'text-xs text-gray-500 dark:text-dark-400'] }, p.label),
          h('button', { type: 'button', class: 'copy-btn', onClick: () => emit('copy') }, [
            h('span', { class: 'text-xs' }, p.copied ? p.copiedLabel : p.copyLabel)
          ])
        ]),
        h('pre', { class: 'overflow-x-auto bg-gray-900 px-4 py-3 text-[13px] leading-relaxed text-gray-100 dark:bg-dark-950' }, [
          h('code', { class: 'font-mono' }, p.code)
        ])
      ])
  }
})
</script>

<style scoped>
/* 页签：选中是浅灰底，其余只有文字 */
.onb-tab {
  @apply inline-flex items-center gap-2 rounded-lg px-3.5 py-2 text-sm font-medium text-gray-500 transition-colors duration-150;
  @apply hover:bg-gray-50 hover:text-gray-900;
  @apply dark:text-dark-400 dark:hover:bg-dark-700/60 dark:hover:text-white;
}
.onb-tab-active,
.onb-tab-active:hover {
  @apply bg-gray-100 text-gray-900;
  @apply dark:bg-dark-700 dark:text-white dark:hover:bg-dark-700;
}
.onb-tab:focus-visible,
.onb-chip:focus-visible,
.onb-tile:focus-visible,
.onb-link:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 2px;
}

/* 面板与卡片：浅底圆角，里面的控件是白底 */
.onb-card {
  @apply rounded-xl border border-gray-200 bg-gray-50 p-4 sm:p-5;
  @apply dark:border-dark-700 dark:bg-dark-900/40;
}

/* 复制瓦片：整块可点，右侧图标复制后变勾 */
.onb-tile {
  @apply flex w-full items-center justify-between gap-3 rounded-lg border border-gray-200 bg-white px-4 py-3.5 text-left text-sm text-gray-700 transition-colors duration-150;
  @apply hover:border-gray-300 hover:bg-gray-50 hover:text-gray-900;
  @apply dark:border-dark-600 dark:bg-dark-800 dark:text-gray-300 dark:hover:border-dark-500 dark:hover:bg-dark-700 dark:hover:text-white;
}
.onb-tile-icon {
  @apply shrink-0 text-gray-400 dark:text-dark-400;
}
.onb-tile:hover .onb-tile-icon {
  @apply text-gray-600 dark:text-dark-200;
}
.onb-tile[data-copied='true'],
.onb-tile[data-copied='true']:hover {
  border-color: theme('colors.primary.300');
}
:global(.dark) .onb-tile[data-copied='true'],
:global(.dark) .onb-tile[data-copied='true']:hover {
  border-color: theme('colors.primary.600');
}
.onb-tile[data-copied='true'] .onb-tile-icon {
  color: theme('colors.primary.600');
}
:global(.dark) .onb-tile[data-copied='true'] .onb-tile-icon {
  color: theme('colors.primary.400');
}
@media (prefers-reduced-motion: no-preference) {
  .onb-pop {
    animation: onb-pop 180ms ease-out;
  }
}
@keyframes onb-pop {
  from {
    transform: scale(0.6);
    opacity: 0;
  }
  to {
    transform: scale(1);
    opacity: 1;
  }
}

.onb-link {
  @apply inline-flex shrink-0 items-center gap-1 rounded text-sm text-gray-500 transition-colors duration-150 hover:text-gray-900;
  @apply dark:text-dark-400 dark:hover:text-white;
}

/* 单选小胶囊（交给 AI、CC Switch 里选客户端） */
.onb-chip {
  @apply rounded-lg border border-gray-200 bg-white px-3.5 py-2 text-sm text-gray-600 transition-colors duration-150;
  @apply hover:bg-gray-50 hover:text-gray-900;
  @apply dark:border-dark-600 dark:bg-dark-800 dark:text-gray-300 dark:hover:bg-dark-700 dark:hover:text-white;
}
.onb-chip-active,
.onb-chip-active:hover {
  @apply border-gray-400 bg-gray-100 font-medium text-gray-900;
  @apply dark:border-dark-400 dark:bg-dark-700 dark:text-white dark:hover:bg-dark-700;
}

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
