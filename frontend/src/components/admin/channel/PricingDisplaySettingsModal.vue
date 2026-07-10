<template>
  <BaseDialog :show="show" :title="t('admin.channels.display.title')" width="wide" @close="close">
    <div class="space-y-5">
      <p class="rounded-lg bg-primary-50 px-3 py-2 text-xs leading-relaxed text-primary-700 dark:bg-primary-500/10 dark:text-primary-300">
        {{ t('admin.channels.display.intro') }}
      </p>

      <div v-if="loading" class="flex justify-center py-8">
        <div class="h-6 w-6 animate-spin rounded-full border-2 border-primary-500 border-t-transparent" />
      </div>

      <template v-else>
        <!-- 分组多选 -->
        <GroupSelector v-model="selectedGroupIds" :groups="groups" :searchable="'auto'" />

        <!-- 模型多选 -->
        <div>
          <label class="input-label">
            {{ t('admin.channels.display.modelsLabel') }}
            <span class="font-normal text-gray-400">{{ t('common.selectedCount', { count: selectedModels.length }) }}</span>
          </label>
          <div class="rounded-lg border border-gray-200 bg-gray-50 dark:border-dark-600 dark:bg-dark-800">
            <div class="flex items-center gap-2 border-b border-gray-200 px-3 py-2 dark:border-dark-600">
              <Icon name="search" size="sm" class="shrink-0 text-gray-400" />
              <input
                v-model="modelSearch"
                type="text"
                :placeholder="t('admin.channels.display.searchModels')"
                class="flex-1 bg-transparent text-sm text-gray-900 placeholder:text-gray-400 focus:outline-none dark:text-gray-100"
              />
              <button v-if="selectedModels.length > 0" type="button" class="shrink-0 text-xs text-gray-400 hover:text-gray-600 dark:hover:text-gray-200" @click="selectedModels = []">
                {{ t('admin.channels.display.clear') }}
              </button>
            </div>
            <div class="max-h-48 overflow-y-auto p-2">
              <label
                v-for="m in filteredModels"
                :key="m"
                class="flex cursor-pointer items-center gap-2 rounded px-2 py-1.5 text-sm transition-colors hover:bg-white dark:hover:bg-dark-700"
              >
                <input type="checkbox" :value="m" v-model="selectedModels" class="h-3.5 w-3.5 shrink-0 rounded border-gray-300 text-primary-500 focus:ring-primary-500 dark:border-dark-500" />
                <span class="truncate text-gray-800 dark:text-gray-200">{{ m }}</span>
              </label>
              <div v-if="filteredModels.length === 0" class="py-3 text-center text-xs text-gray-400">
                {{ t('admin.channels.display.noModels') }}
              </div>
            </div>
          </div>
          <p class="mt-1.5 text-xs text-gray-400 dark:text-gray-500">{{ t('admin.channels.display.emptyHint') }}</p>
        </div>
      </template>
    </div>

    <template #footer>
      <button type="button" class="btn btn-secondary" :disabled="saving" @click="close">{{ t('common.cancel') }}</button>
      <button type="button" class="btn btn-primary" :disabled="saving || loading" @click="save">
        <span v-if="saving" class="mr-2 inline-block h-4 w-4 animate-spin rounded-full border-2 border-white border-t-transparent align-[-2px]" />
        {{ t('common.save') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import GroupSelector from '@/components/common/GroupSelector.vue'
import Icon from '@/components/icons/Icon.vue'
import adminChannelsAPI from '@/api/admin/channels'
import adminGroupsAPI from '@/api/admin/groups'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { AdminGroup } from '@/types'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ 'update:show': [value: boolean]; saved: [] }>()

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const saving = ref(false)
const groups = ref<AdminGroup[]>([])
const candidateModels = ref<string[]>([])
const selectedGroupIds = ref<number[]>([])
const selectedModels = ref<string[]>([])
const modelSearch = ref('')

// 候选模型 = 选中的 + 渠道定价 / 精确映射里出现过的(去通配符、去重),保证已选项即便不在候选里也可见
const allModels = computed(() => {
  const set = new Set<string>([...candidateModels.value, ...selectedModels.value])
  return Array.from(set).sort((a, b) => a.localeCompare(b))
})
const filteredModels = computed(() => {
  const q = modelSearch.value.trim().toLowerCase()
  if (!q) return allModels.value
  return allModels.value.filter((m) => m.toLowerCase().includes(q))
})

async function load() {
  loading.value = true
  try {
    const [display, groupList, channelPage] = await Promise.all([
      adminChannelsAPI.getPricingDisplay(),
      adminGroupsAPI.getAllIncludingInactive(),
      adminChannelsAPI.list(1, 200),
    ])
    selectedGroupIds.value = [...(display.group_ids || [])]
    selectedModels.value = [...(display.models || [])]
    groups.value = groupList
    // 汇总渠道定价和精确映射的模型名(剔除通配符)作为候选。
    const models = new Set<string>()
    for (const ch of channelPage.items || []) {
      for (const p of ch.model_pricing || []) {
        for (const name of p.models || []) {
          if (name && !name.includes('*')) models.add(name)
        }
      }
      for (const mapping of Object.values(ch.model_mapping || {})) {
        for (const source of Object.keys(mapping || {})) {
          if (source && !source.includes('*')) models.add(source)
        }
      }
    }
    candidateModels.value = Array.from(models)
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  try {
    await adminChannelsAPI.updatePricingDisplay({
      group_ids: selectedGroupIds.value,
      models: selectedModels.value,
    })
    appStore.showSuccess(t('admin.channels.display.saved'))
    emit('saved')
    emit('update:show', false)
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  } finally {
    saving.value = false
  }
}

function close() {
  emit('update:show', false)
}

watch(
  () => props.show,
  (v) => {
    if (v) load()
    else modelSearch.value = ''
  },
)
</script>
