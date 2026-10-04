<template>
  <div v-bind="$attrs" class="onb-card space-y-4" data-test="panel-ccswitch">
    <div class="grid gap-4 sm:grid-cols-2">
      <div v-if="clients.length > 1">
        <span :id="clientLabelId" class="input-label">{{ t('keyOnboarding.ccs.client') }}</span>
        <div
          class="flex flex-wrap gap-2"
          role="radiogroup"
          :aria-label="t('keyOnboarding.ccs.client')"
          :aria-labelledby="clientLabelId"
        >
          <button
            v-for="c in clients"
            :key="c"
            type="button"
            role="radio"
            class="onb-chip"
            :class="{ 'onb-chip-active': client === c }"
            :aria-checked="client === c"
            :data-test="`ccs-client-${c}`"
            @click="client = c"
          >
            {{ CCS_LABELS[c] }}
          </button>
        </div>
      </div>
      <div :class="clients.length > 1 ? '' : 'sm:col-span-2'">
        <label class="input-label" for="ccs-name">{{ t('keyOnboarding.ccs.name') }}</label>
        <input id="ccs-name" v-model="customName" type="text" class="input" :placeholder="defaultName" data-test="ccs-name" />
      </div>
      <div v-if="models.length > 0" class="sm:col-span-2">
        <label class="input-label" for="ccs-model">{{ t('keyOnboarding.ccs.model') }}</label>
        <select id="ccs-model" v-model="selectedModel" class="input" data-test="ccs-model">
          <option value="">{{ t('keyOnboarding.ccs.modelDefault') }}</option>
          <option v-for="m in models" :key="m" :value="m">{{ m }}</option>
        </select>
      </div>
    </div>
    <div class="flex flex-wrap items-center gap-2">
      <button type="button" class="btn btn-primary" data-test="ccs-open" @click="openDeeplink">
        {{ t('keyOnboarding.ccs.open') }}
      </button>
      <button type="button" class="btn btn-secondary" data-test="ccs-copy-link" @click="emit('copy', deeplink, 'deeplink')">
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
</template>

<script setup lang="ts">
/**
 * 「CC Switch」页签：生成 ccswitch:// 导入链接，可以直接打开或复制。
 * 选中的客户端、自定义名称、选的模型合成一个表单对象，用 v-model:form 放在外壳里（见 useCcSwitchState），
 * 这样切到别的页签再回来时还在；链接本身由这里按当前状态生成。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { EndpointOption } from '@/utils/apiEndpoints'
import { CC_SWITCH_USAGE_SCRIPT, buildCcSwitchImportDeeplink } from '@/utils/ccswitchImport'
import type { CcSwitchForm, CcsClient } from './useCcSwitchState'

// 根元素要接住外壳传来的 tabpanel 属性（role / id / aria-labelledby），所以关掉自动继承、手动放在根上
defineOptions({ inheritAttrs: false })

const props = defineProps<{
  /** 当前选中的线路 */
  endpoint: EndpointOption
  /** 完整密钥；链接里带的是它，不是掩码 */
  fullKey: string
  platform: string | null
  siteName: string
  /** 密钥所在分组的可用模型；为空时不显示模型字段 */
  models: string[]
  /** 模型列表还在加载 */
  modelsLoading?: boolean
  /** 这个平台能导入成哪些客户端；多于一个时显示客户端选择 */
  clients: CcsClient[]
  /** 同一页上多个弹窗实例时，页签内 id 的前缀，避免 id 重复 */
  idPrefix: string
  /** 最近复制成功的按钮 id，对应的按钮显示「已复制」 */
  copiedId: string
}>()

const emit = defineEmits<{
  /** 要复制的文本和按钮 id */
  copy: [text: string, id: string]
}>()

/** 表单：选中的客户端、自定义名称、选的模型。更新时换成新对象，交给外壳 */
const form = defineModel<CcSwitchForm>('form', { required: true })
const client = computed({
  get: () => form.value.client,
  set: (v: CcsClient) => {
    form.value = { ...form.value, client: v }
  }
})
/** 自定义名称，留空时用默认名称 */
const customName = computed({
  get: () => form.value.name,
  set: (v: string) => {
    form.value = { ...form.value, name: v }
  }
})
/** 选的模型，留空表示用默认 */
const selectedModel = computed({
  get: () => form.value.model,
  set: (v: string) => {
    form.value = { ...form.value, model: v }
  }
})

const { t } = useI18n()

const CC_SWITCH_RELEASES = 'https://github.com/farion1231/cc-switch/releases'
const CCS_LABELS: Record<CcsClient, string> = {
  claude: 'Claude',
  codex: 'Codex',
  gemini: 'Gemini'
}

const clientLabelId = computed(() => `${props.idPrefix}-ccs-client`)
const defaultName = computed(() => `${props.siteName} - ${CCS_LABELS[client.value]}`)

// 导入链接要的地址：每个客户端各取它自己需要的那种，和改动前（1a4a797f6）一致。
// - Codex（openai 平台）：沿用管理员配置的地址（root 就是 root，带 /v1 就带 /v1），ccswitchImport.ts 的约定；
// - Claude / Gemini / antigravity：API 根地址。客户端自己会拼 /v1/messages，所以不能带结尾的 /v1。
const importBaseUrl = computed(() => (props.platform === 'openai' ? props.endpoint.configured : props.endpoint.base))

const deeplink = computed(() =>
  buildCcSwitchImportDeeplink({
    baseUrl: importBaseUrl.value,
    platform: props.platform as never,
    clientType: client.value === 'gemini' ? 'gemini' : 'claude',
    providerName: customName.value.trim() || defaultName.value,
    apiKey: props.fullKey,
    usageScript: CC_SWITCH_USAGE_SCRIPT,
    model: selectedModel.value || undefined
  })
)

function openDeeplink() {
  try {
    window.open(deeplink.value, '_self')
  } catch {
    /* 用户可手动复制链接 */
  }
}
</script>

<style scoped src="./shared.css"></style>
