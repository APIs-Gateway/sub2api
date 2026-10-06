<template>
  <div class="note-signal" role="alert" data-test="write-error">
    <p class="font-medium">{{ t(`admin.pricingConfig.write.error.${info.key}`, info.params) }}</p>
    <ul v-if="issues.length" class="mt-1 space-y-0.5 text-sm">
      <li v-for="(i, n) in issues" :key="n">
        {{ groupName(i.groupId) }} · {{ i.model }}：{{ t(`admin.pricingConfig.write.issue.${i.reason}`, { target: i.target ?? '' }) }}
      </li>
    </ul>
    <p v-if="info.action !== 'none'" class="mt-2">
      <button type="button" class="btn btn-secondary btn-sm" data-test="write-error-action" @click="emit('action', info.action)">
        {{ info.action === 'refresh' ? t('admin.pricingConfig.write.refreshData') : t('admin.pricingConfig.write.previewAgain') }}
      </button>
    </p>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { errorInfo, parseIssues, type ErrorAction, type WriteError } from '../pricingErrors'
import { usePricingData } from '../usePricingData'

const props = defineProps<{ error: WriteError }>()
const emit = defineEmits<{ (e: 'action', action: ErrorAction): void }>()

const { t } = useI18n()
const { state } = usePricingData()

const info = computed(() => errorInfo(props.error))
const issues = computed(() =>
  parseIssues(props.error.metadata.violations ?? props.error.metadata.issues)
)

function groupName(id: number | null): string {
  return state.groups.find((g) => g.id === id)?.name ?? (id === null ? '' : `#${id}`)
}
</script>

<style scoped>
.note-signal {
  @apply rounded-md border-l-2 border-primary-600 bg-primary-50 px-3 py-2.5 text-sm text-primary-900;
  @apply dark:border-primary-500 dark:bg-primary-950/40 dark:text-primary-100;
}
</style>
