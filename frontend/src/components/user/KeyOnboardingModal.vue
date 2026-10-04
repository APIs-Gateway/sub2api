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

      <!-- 线路：站点配了备用地址才出现。选哪个，下面所有页签生成的内容就用哪个；和使用文档页共用同一个选择。
           手动配置页签自己用地址卡片选线路，这里不重复显示 -->
      <div v-if="endpointOptions.length > 1 && active !== 'manual'" class="onb-lines" data-test="endpoints">
        <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
          <span :id="`${uid}-endpoints-label`" class="onb-lines-label">{{ t('keys.endpoints.title') }}</span>
          <div
            class="flex flex-wrap gap-2"
            role="radiogroup"
            :aria-labelledby="`${uid}-endpoints-label`"
            :aria-describedby="`${uid}-endpoints-hint`"
          >
            <label v-for="opt in endpointOptions" :key="opt.id" class="onb-chip onb-line" :class="{ 'onb-chip-active': activeEndpoint.id === opt.id }">
              <input
                v-model="endpointChoice"
                type="radio"
                class="sr-only"
                :name="`${uid}-endpoint`"
                :value="opt.id"
                :data-test="`endpoint-${opt.isDefault ? 'default' : opt.id}`"
              />
              {{ opt.isDefault ? t('keys.endpoints.default') : opt.name }}
            </label>
          </div>
        </div>
        <p class="onb-lines-detail" data-test="endpoint-detail">
          <span v-if="activeEndpoint.description" class="text-gray-600 dark:text-dark-300">{{ activeEndpoint.description }}</span>
          <code class="font-mono tabular-nums">{{ activeEndpoint.base }}</code>
        </p>
        <p :id="`${uid}-endpoints-hint`" class="onb-lines-hint">{{ t('keyOnboarding.endpointHint') }}</p>
      </div>

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
      <InstallTab
        v-else-if="active === 'install'"
        v-bind="panelAttrs"
        :endpoint="activeEndpoint"
        :full-key="fullKey"
        :platform="platform"
        :site-name="siteName"
        :clients="clients"
        :copied-id="copiedId"
        @copy="copy"
      />

      <!-- ===== 交给 AI ===== -->
      <AiTab
        v-else-if="active === 'ai'"
        v-bind="panelAttrs"
        v-model:client="aiClient"
        :endpoint="activeEndpoint"
        :platform="platform"
        :site-name="siteName"
        :origin="origin"
        :allow-messages-dispatch="allowMessagesDispatch"
        :clients="clients"
        :models="models"
        :models-loading="modelsLoading"
        :doc-url="docUrl"
        :copied-id="copiedId"
        @copy="copy"
      />

      <!-- ===== CC Switch ===== -->
      <CcSwitchTab
        v-else-if="active === 'ccswitch'"
        v-bind="panelAttrs"
        v-model:form="ccsForm"
        :endpoint="activeEndpoint"
        :full-key="fullKey"
        :platform="platform"
        :site-name="siteName"
        :models="models"
        :models-loading="modelsLoading"
        :clients="ccsClients"
        :id-prefix="uid"
        :copied-id="copiedId"
        @copy="copy"
      />

      <!-- ===== 手动配置 ===== -->
      <ManualTab
        v-else-if="active === 'manual'"
        v-bind="panelAttrs"
        v-model:endpoint-id="endpointChoice"
        v-model:code-tab="manualCodeTab"
        :endpoint="activeEndpoint"
        :endpoint-options="endpointOptions"
        :full-key="fullKey"
        :masked-key="maskedKey"
        :platform="platform"
        :site-name="siteName"
        :clients="clients"
        :models="models"
        :doc-url="docUrl"
        :copied-id="copiedId"
        @copy="copy"
      />
    </div>
  </BaseDialog>
</template>

<script lang="ts">
// 同一页上多个实例时，页签和面板的 id 不能撞
let onboardingUid = 0
</script>

<script setup lang="ts">
/**
 * 接入弹窗的外壳：页签栏（切换、键盘导航、aria）、线路选择、复制反馈（含读屏播报）、
 * 以及要跨页签保留的状态。每个页签的内容在 ./onboarding/ 下各自一个组件，只拿自己需要的 props，
 * 通过 copy 事件把要复制的内容交回外壳：
 *
 *   InstallTab   一键安装   瓦片 / 脚本预览
 *   AiTab        交给 AI    提示词 / ChatGPT、Claude 链接
 *   CcSwitchTab  CC Switch  导入链接
 *   ManualTab    手动配置   地址卡片（同时是线路选择）、代码示例、配置片段
 *
 * 页签面板的 role / id / aria-labelledby 由外壳通过 v-bind="panelAttrs" 传给页签，页签放在自己的根元素上。
 */
import { ref, computed, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import type { CustomEndpoint } from '@/types'
import {
  loadSavedEndpointId,
  pickEndpoint,
  resolveEndpointOptions,
  saveEndpointId
} from '@/utils/apiEndpoints'
import { aiClientsForPlatform, clientsForPlatform, type AiClient, type OnboardingClient } from '@/utils/keyOnboarding'
import AiTab from './onboarding/AiTab.vue'
import CcSwitchTab from './onboarding/CcSwitchTab.vue'
import InstallTab from './onboarding/InstallTab.vue'
import ManualTab from './onboarding/ManualTab.vue'
import { useCcSwitchState } from './onboarding/useCcSwitchState'
import { useCopyFeedback } from './onboarding/useCopyFeedback'
import { useGroupModels } from './onboarding/useGroupModels'
import { useManualCodeTab } from './onboarding/useManualCodeTab'

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
    /** 站点配置的备用线路（公开设置的 custom_endpoints）；为空时不显示线路选择 */
    customEndpoints?: CustomEndpoint[]
    siteName?: string
    docUrl?: string
    initialTab?: OnboardingTab
  }>(),
  { initialTab: 'install', customEndpoints: () => [] }
)

const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
const uid = `onboarding-${++onboardingUid}`

// ===== 线路 =====
// 站点自己的来源：线路没填默认地址时的退路，也是「交给 AI」里文档链接的域名
const origin = window.location.origin
const endpointOptions = computed(() => resolveEndpointOptions(props.baseUrl, props.customEndpoints, origin))
const savedEndpointId = ref(loadSavedEndpointId())
// 已选线路被站点删掉时 pickEndpoint 回落到默认地址
const activeEndpoint = computed(() => pickEndpoint(endpointOptions.value, savedEndpointId.value))
const endpointChoice = computed({
  get: () => activeEndpoint.value.id,
  set: (id: string) => {
    savedEndpointId.value = id
    saveEndpointId(id)
  }
})
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

// ===== 复制：所有页签共用，反馈（已复制、读屏播报）也在这里 =====
const { copiedId, copy, reset: resetCopied } = useCopyFeedback()

// ===== 分组可用的客户端和模型 =====
const allowMessagesDispatch = computed(() => props.apiKey?.group?.allow_messages_dispatch)
const clients = computed<OnboardingClient[]>(() =>
  clientsForPlatform(platform.value, { allowMessagesDispatch: allowMessagesDispatch.value })
)
// 手动配置页签里选中的代码页签：跨页签保留，换了分组回到默认（原生客户端；openai 分组是 OpenAI SDK）
const manualCodeTab = useManualCodeTab({ platform, allowMessagesDispatch: () => allowMessagesDispatch.value, clients })
const {
  models,
  loading: modelsLoading,
  load: loadModels
} = useGroupModels(() => props.apiKey?.group?.id ?? props.apiKey?.group_id)

// ===== 要跨页签保留的页签状态：页签是按需渲染的，状态放在页签组件里切走就丢了 =====
const aiClient = ref<AiClient>('claude')
// 换了分组（平台或调度开关变了）就回到这个分组的默认工具；同类分组之间换密钥，用户选的保留。
// 不靠初始值：否则默认取决于上一个打开的密钥，而不是当前分组。页签里的回退只是兜底。
watch(
  // 没有这个字段和 false 是一回事，不算换了分组
  [platform, () => !!allowMessagesDispatch.value],
  ([p, dispatch]) => {
    aiClient.value = aiClientsForPlatform(p, { allowMessagesDispatch: dispatch })[0] ?? 'claude'
  },
  { immediate: true }
)
const { clients: ccsClients, form: ccsForm } = useCcSwitchState({
  platform,
  show: () => props.show,
  groupId: () => props.apiKey?.group?.id,
  models: () => models.value,
  allowMessagesDispatch: () => allowMessagesDispatch.value
})

watch(
  () => props.show,
  (v) => {
    if (v) {
      // 文档页可能在这期间改过选择
      savedEndpointId.value = loadSavedEndpointId()
      active.value = props.initialTab
      resetCopied()
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
</script>

<style scoped src="./onboarding/shared.css"></style>

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
.onb-tab:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 2px;
}

/* 线路选择：小胶囊 + 当前线路的说明与地址 */
.onb-lines {
  @apply space-y-1.5;
}
.onb-lines-label {
  @apply text-sm text-gray-600 dark:text-dark-300;
}
.onb-line {
  @apply cursor-pointer;
}
.onb-line:has(input:focus-visible) {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 2px;
}
.onb-lines-detail {
  @apply flex flex-wrap items-baseline gap-x-3 gap-y-0.5 text-xs text-gray-500 dark:text-dark-400;
}
.onb-lines-hint {
  @apply text-xs text-gray-500 dark:text-dark-400;
}
</style>
