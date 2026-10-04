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
            @click="selectClient(c)"
          >
            {{ CCS_LABELS[c] }}
          </button>
        </div>
      </div>
      <div :class="clients.length > 1 ? '' : 'sm:col-span-2'">
        <label class="input-label" for="ccs-name">{{ t('keyOnboarding.ccs.name') }}</label>
        <input id="ccs-name" v-model="customName" type="text" class="input" :placeholder="defaultName" data-test="ccs-name" />
      </div>
    </div>
    <!-- 模型：Claude 有主模型和 Haiku / Sonnet / Opus 四个，Codex、Gemini 只有主模型；选项都来自这把密钥所在分组 -->
    <div>
      <div class="grid gap-4 sm:grid-cols-2" data-test="ccs-models">
        <div v-for="f in modelFields" :key="f.key" :class="{ 'sm:col-span-2': modelFields.length === 1 }">
          <label class="input-label" :for="fieldId(f.key)">{{ t(`keyOnboarding.ccs.${f.label}`) }}</label>
          <select
            :id="fieldId(f.key)"
            class="input"
            :value="form[f.key]"
            :disabled="modelsLoading || options.length === 0"
            :aria-describedby="modelsHint ? hintId : undefined"
            :data-test="f.test"
            @change="setField(f.key, ($event.target as HTMLSelectElement).value)"
          >
            <option value="">{{ t('keyOnboarding.ccs.modelDefault') }}</option>
            <option v-for="m in optionsFor(f.key)" :key="m" :value="m">{{ m }}</option>
          </select>
        </div>
      </div>
      <p v-if="modelsHint" :id="hintId" class="input-hint mt-2 flex items-center gap-2" role="status" data-test="ccs-models-hint">
        <span
          v-if="modelsLoading"
          class="h-3 w-3 shrink-0 animate-spin rounded-full border-2 border-gray-300 border-t-gray-600 motion-reduce:animate-none dark:border-dark-600 dark:border-t-gray-300"
          aria-hidden="true"
        ></span>
        {{ modelsHint }}
      </p>
    </div>
    <div class="flex flex-wrap items-center gap-2">
      <button
        type="button"
        class="btn btn-primary"
        :disabled="modelsLoading"
        :aria-describedby="modelsLoading ? hintId : undefined"
        data-test="ccs-open"
        @click="openDeeplink"
      >
        {{ t('keyOnboarding.ccs.open') }}
      </button>
      <button
        type="button"
        class="btn btn-secondary"
        :disabled="modelsLoading"
        :aria-describedby="modelsLoading ? hintId : undefined"
        data-test="ccs-copy-link"
        @click="emit('copy', deeplink, 'deeplink')"
      >
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
 * 选中的客户端、自定义名称、各个模型合成一个表单对象，用 v-model:form 放在外壳里（见 useCcSwitchState），
 * 这样切到别的页签再回来时还在；链接本身由这里按当前状态生成。
 *
 * 模型：Claude 有主模型和 Haiku / Sonnet / Opus 四个下拉，Codex、Gemini 只有主模型；选项是这把密钥所在分组的模型，
 * 打开时按 pickCcSwitchModels 的规则预选（预选发生在外壳的 useCcSwitchState 里，换客户端时在这里）。
 * 模型还在加载时提示「加载中」，导入和复制都先禁用；分组没有可选的模型时给出提示，可以留空继续导入。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { EndpointOption } from '@/utils/apiEndpoints'
import {
  CC_SWITCH_USAGE_SCRIPT,
  buildCcSwitchImportDeeplink,
  ccSwitchModelOptions,
  pickCcSwitchModels
} from '@/utils/ccswitchImport'
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
  /** 密钥所在分组的可用模型；还没加载完或没有时是空数组 */
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

/** 表单：选中的客户端、自定义名称、各个模型。更新时换成新对象，交给外壳 */
const form = defineModel<CcSwitchForm>('form', { required: true })
const client = computed(() => form.value.client)
/** 自定义名称，留空时用默认名称 */
const customName = computed({
  get: () => form.value.name,
  set: (v: string) => {
    form.value = { ...form.value, name: v }
  }
})

type ModelField = 'model' | 'haikuModel' | 'sonnetModel' | 'opusModel'
/** 模型字段，顺序同 CC Switch：主模型在前，后面是 Claude 的三档 */
const MODEL_FIELDS: { key: ModelField; label: string; test: string }[] = [
  { key: 'model', label: 'modelMain', test: 'ccs-model' },
  { key: 'haikuModel', label: 'modelHaiku', test: 'ccs-model-haiku' },
  { key: 'sonnetModel', label: 'modelSonnet', test: 'ccs-model-sonnet' },
  { key: 'opusModel', label: 'modelOpus', test: 'ccs-model-opus' }
]
const modelFields = computed(() => (client.value === 'claude' ? MODEL_FIELDS : MODEL_FIELDS.slice(0, 1)))
const fieldId = (key: ModelField) => `${props.idPrefix}-ccs-${key}`
const hintId = computed(() => `${props.idPrefix}-ccs-models-hint`)

/** 当前客户端的模型选项：从分组的模型里按客户端筛选、排序 */
const options = computed(() => ccSwitchModelOptions(client.value, props.models))
/** 某个下拉的选项；当前值不在列表里时（比如外壳还没来得及重新预选）也列出来，免得下拉显示成空白 */
function optionsFor(key: ModelField): string[] {
  const v = form.value[key]
  return v && !options.value.includes(v) ? [...options.value, v] : options.value
}
function setField(key: ModelField, value: string) {
  form.value = { ...form.value, [key]: value }
}

/** 选客户端：名称回到默认，模型按新客户端重新预选。一次写入一个完整的新对象 */
function selectClient(next: CcsClient) {
  if (next === client.value) return
  form.value = { ...form.value, client: next, name: '', ...pickCcSwitchModels(next, props.models) }
}

const { t } = useI18n()

const CC_SWITCH_RELEASES = 'https://github.com/farion1231/cc-switch/releases'
const CCS_LABELS: Record<CcsClient, string> = {
  claude: 'Claude',
  codex: 'Codex',
  gemini: 'Gemini'
}

/** 模型下拉下面的提示：加载中，或这个分组没有可选的模型；有模型时不显示 */
const modelsHint = computed(() => {
  if (props.modelsLoading) return t('keyOnboarding.ccs.modelsLoading')
  if (options.value.length === 0) return t('keyOnboarding.ccs.noModels')
  return ''
})

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
    model: form.value.model,
    haikuModel: form.value.haikuModel,
    sonnetModel: form.value.sonnetModel,
    opusModel: form.value.opusModel
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
