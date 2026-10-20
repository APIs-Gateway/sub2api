<template>
  <div v-bind="$attrs" class="space-y-4" data-test="panel-install">
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
            @click="emit('copy', scripts[tile.id], tile.id)"
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
</template>

<script setup lang="ts">
/**
 * 「一键安装」页签：每个客户端一张卡片，卡片里是复制瓦片（每行 macOS / Linux 和 Windows 各一块）
 * 和折叠的脚本预览。点瓦片只是把脚本通过 copy 事件交给外壳，写剪贴板和「已复制」反馈都在外壳。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { EndpointOption } from '@/utils/apiEndpoints'
import {
  CLIENT_LABELS,
  CODEX_MIN_NODE_MAJOR,
  SCRIPT_ERROR_TOKEN,
  SCRIPT_PATH_TOKEN,
  SCRIPT_VERSION_TOKEN,
  buildInstallScript,
  scriptTargetPath,
  tutorialHref,
  type CodexInstallMode,
  type OnboardingClient,
  type ScriptOs
} from '@/utils/keyOnboarding'
import { ScriptBlock } from './ScriptBlock'

// 根元素要接住外壳传来的 tabpanel 属性（role / id / aria-labelledby），所以关掉自动继承、手动放在根上
defineOptions({ inheritAttrs: false })

const props = defineProps<{
  /** 当前选中的线路；脚本里写它的 API 根地址（base，不带结尾的 / 和 /v1） */
  endpoint: EndpointOption
  /** 完整密钥；脚本里写的是它，不是掩码 */
  fullKey: string
  /** 分组平台；没有分组时外壳不会渲染本页签 */
  platform: string | null
  siteName: string
  /** 这个分组可用的客户端，每个一张卡片 */
  clients: OnboardingClient[]
  /** 最近复制成功的按钮 id，对应的瓦片显示勾 */
  copiedId: string
}>()

const emit = defineEmits<{
  /** 要复制的脚本和按钮 id */
  copy: [text: string, id: string]
}>()

const { t } = useI18n()

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
    baseUrl: props.endpoint.base,
    apiKey: props.fullKey,
    platform: props.platform,
    siteName: props.siteName,
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
  props.clients.map((client) => {
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
</script>

<style scoped src="./shared.css"></style>

<style scoped>
.onb-link:focus-visible,
.onb-tile:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 2px;
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
.dark .onb-tile[data-copied='true'],
.dark .onb-tile[data-copied='true']:hover {
  border-color: theme('colors.primary.600');
}
.onb-tile[data-copied='true'] .onb-tile-icon {
  color: theme('colors.primary.600');
}
.dark .onb-tile[data-copied='true'] .onb-tile-icon {
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
</style>
