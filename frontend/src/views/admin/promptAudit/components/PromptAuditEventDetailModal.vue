<template>
  <BaseDialog :show="show" :title="title" width="wide" @close="close">
    <div v-if="loading" class="flex items-center justify-center py-16">
      <div class="flex flex-col items-center gap-3">
        <div class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary-600"></div>
        <div class="text-sm font-medium text-gray-500 dark:text-gray-400">{{ t('admin.promptAudit.detail.loading') }}</div>
      </div>
    </div>

    <div v-else-if="!event" class="py-10 text-center text-sm text-gray-500 dark:text-gray-400">
      {{ t('admin.promptAudit.detail.notFound') }}
    </div>

    <div v-else class="space-y-6">
      <!-- Summary -->
      <div>
        <h4 class="mb-2 text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.summary') }}</h4>
        <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
            <div class="text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.time') }}</div>
            <div class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{ formatDateTime(event.created_at) }}</div>
          </div>
          <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
            <div class="text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.decision') }}</div>
            <div class="mt-1">
              <span class="rounded px-2 py-0.5 text-xs font-medium" :class="decisionClass(event.decision)">
                {{ decisionLabel(t, event.decision) }}
              </span>
            </div>
          </div>
          <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
            <div class="text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.riskLevel') }}</div>
            <div class="mt-1">
              <span class="rounded px-2 py-0.5 text-xs font-medium" :class="riskLevelClass(event.risk_level)">
                {{ riskLevelLabel(t, event.risk_level) }}
              </span>
            </div>
          </div>
          <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
            <div class="text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.action') }}</div>
            <div class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{ event.action || '-' }}</div>
          </div>
          <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
            <div class="text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.policy') }}</div>
            <div class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{ event.policy_id || '-' }}</div>
            <div class="text-xs text-gray-500 dark:text-dark-400">
              {{ t('admin.promptAudit.detail.policyVersion', { version: event.policy_version }) }}
            </div>
          </div>
          <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
            <div class="text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.guardEndpoint') }}</div>
            <div class="mt-1 break-all text-sm font-medium text-gray-900 dark:text-white">{{ event.guard_endpoint_id || '-' }}</div>
          </div>
          <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
            <div class="text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.scannerBackend') }}</div>
            <div class="mt-1 text-sm font-medium text-gray-900 dark:text-white">
              {{ event.scanner_backend || '-' }}
              <span v-if="event.scanner_version" class="text-gray-400"> · {{ event.scanner_version }}</span>
            </div>
          </div>
          <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
            <div class="text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.latency') }}</div>
            <div class="mt-1 text-sm font-medium text-gray-900 dark:text-white">
              {{ t('admin.promptAudit.detail.latencyMs', { ms: event.latency_ms }) }}
            </div>
          </div>
        </div>
      </div>

      <!-- Request context -->
      <div>
        <h4 class="mb-2 text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.request') }}</h4>
        <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
            <div class="text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.requestId') }}</div>
            <div class="mt-1 break-all font-mono text-sm font-medium text-gray-900 dark:text-white">
              {{ event.snapshot.request_id || '-' }}
            </div>
          </div>
          <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
            <div class="text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.user') }}</div>
            <div class="mt-1 text-sm font-medium text-gray-900 dark:text-white">#{{ event.snapshot.user_id }}</div>
            <div class="text-xs text-gray-500 dark:text-dark-400">
              {{ event.snapshot.user_email || event.snapshot.username || '-' }}
            </div>
          </div>
          <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
            <div class="text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.apiKey') }}</div>
            <div class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{ event.snapshot.api_key_name || '-' }}</div>
          </div>
          <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
            <div class="text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.group') }}</div>
            <div class="mt-1 text-sm font-medium text-gray-900 dark:text-white">
              {{ event.snapshot.group_name || (event.snapshot.group_id != null ? String(event.snapshot.group_id) : '-') }}
            </div>
          </div>
          <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
            <div class="text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.provider') }}</div>
            <div class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{ event.snapshot.provider || '-' }}</div>
          </div>
          <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
            <div class="text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.endpoint') }}</div>
            <div class="mt-1 break-all text-sm font-medium text-gray-900 dark:text-white">{{ event.snapshot.endpoint || '-' }}</div>
          </div>
          <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
            <div class="text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.model') }}</div>
            <div class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{ event.snapshot.model || '-' }}</div>
          </div>
          <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
            <div class="text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.stage') }}</div>
            <div class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{ event.snapshot.stage || '-' }}</div>
          </div>
          <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
            <div class="text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.promptLength') }}</div>
            <div class="mt-1 text-sm font-medium text-gray-900 dark:text-white">
              {{ t('admin.promptAudit.detail.promptLengthChars', { n: event.snapshot.prompt_length }) }}
            </div>
          </div>
          <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
            <div class="text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.messageCount') }}</div>
            <div class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{ event.snapshot.message_count }}</div>
          </div>
          <div class="rounded-xl bg-gray-50 p-4 dark:bg-dark-900">
            <div class="text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.promptHash') }}</div>
            <div class="mt-1 break-all font-mono text-xs font-medium text-gray-900 dark:text-white">
              {{ event.snapshot.prompt_hash || '-' }}
            </div>
          </div>
        </div>
      </div>

      <!-- Redacted preview (never full_prompt: this contract has no such field) -->
      <div>
        <h4 class="mb-2 text-xs font-bold uppercase tracking-wider text-gray-400">
          {{ t('admin.promptAudit.detail.redactedPreview') }}
        </h4>
        <div class="rounded-xl border border-gray-200 bg-gray-50 p-4 text-sm text-gray-700 dark:border-dark-700 dark:bg-dark-900 dark:text-gray-300">
          <p class="whitespace-pre-wrap break-words">{{ event.snapshot.redacted_preview || '-' }}</p>
        </div>
        <p class="mt-1 text-xs text-gray-400">{{ t('admin.promptAudit.detail.redactedPreviewHint') }}</p>
      </div>

      <!-- Categories / matched scanners -->
      <div v-if="event.categories?.length || event.matched_scanners?.length" class="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div v-if="event.categories?.length">
          <h4 class="mb-2 text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.categories') }}</h4>
          <div class="flex flex-wrap gap-1.5">
            <span
              v-for="c in event.categories"
              :key="c"
              class="rounded-full bg-gray-100 px-2.5 py-1 text-xs font-medium text-gray-700 dark:bg-dark-700 dark:text-gray-300"
            >{{ c }}</span>
          </div>
        </div>
        <div v-if="event.matched_scanners?.length">
          <h4 class="mb-2 text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.matchedScanners') }}</h4>
          <div class="flex flex-wrap gap-1.5">
            <span
              v-for="s in event.matched_scanners"
              :key="s"
              class="rounded-full bg-gray-100 px-2.5 py-1 text-xs font-medium text-gray-700 dark:bg-dark-700 dark:text-gray-300"
            >{{ s }}</span>
          </div>
        </div>
      </div>

      <!-- Scanner scores -->
      <div v-if="scannerScoreEntries.length">
        <h4 class="mb-2 text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.scannerScores') }}</h4>
        <table class="w-full text-sm">
          <tbody>
            <tr v-for="[scanner, score] in scannerScoreEntries" :key="scanner" class="border-b border-gray-100 dark:border-dark-800">
              <td class="py-1.5 pr-4 font-medium text-gray-700 dark:text-gray-300">{{ scanner }}</td>
              <td class="py-1.5 font-mono text-gray-900 dark:text-white">{{ score }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Issue summaries -->
      <div>
        <h4 class="mb-2 text-xs font-bold uppercase tracking-wider text-gray-400">{{ t('admin.promptAudit.detail.issues') }}</h4>
        <p v-if="!event.issue_summaries?.length" class="text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.promptAudit.detail.issuesEmpty') }}
        </p>
        <div v-else class="space-y-3">
          <div
            v-for="(issue, index) in event.issue_summaries"
            :key="`${issue.scanner_id}-${index}`"
            class="rounded-xl border border-gray-200 p-4 dark:border-dark-700"
          >
            <div class="flex flex-wrap items-center justify-between gap-2">
              <div class="text-sm font-semibold text-gray-900 dark:text-white">{{ issue.title || issue.category }}</div>
              <span class="rounded px-2 py-0.5 text-xs font-medium" :class="riskLevelClass(issue.severity)">
                {{ issue.severity_label || issue.severity }}
              </span>
            </div>
            <p v-if="issue.description" class="mt-1 text-sm text-gray-600 dark:text-gray-400">{{ issue.description }}</p>
            <div class="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-gray-500 dark:text-dark-400">
              <span>{{ t('admin.promptAudit.detail.issueAction') }}: {{ issue.action_label || issue.action || '-' }}</span>
              <span>{{ t('admin.promptAudit.detail.issueScore') }}: {{ issue.score }}</span>
            </div>
            <div v-if="issue.evidence" class="mt-2 rounded-lg bg-gray-50 p-2 text-xs text-gray-600 dark:bg-dark-900 dark:text-gray-400">
              <span class="font-semibold">{{ t('admin.promptAudit.detail.issueEvidence') }}:</span> {{ issue.evidence }}
            </div>
          </div>
        </div>
      </div>
    </div>

    <template #footer>
      <button class="btn btn-secondary" @click="close">{{ t('admin.promptAudit.detail.close') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { getPromptAuditEvent } from '@/api/admin/promptAudit'
import type { PromptAuditEvent } from '@/api/admin/promptAudit'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatDateTime } from '@/utils/format'
import { decisionClass, decisionLabel, riskLevelClass, riskLevelLabel } from '../promptAuditPresentation'

interface Props {
  show: boolean
  eventId: number | null
}

const props = defineProps<Props>()
const emit = defineEmits<{
  (e: 'close'): void
}>()

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const event = ref<PromptAuditEvent | null>(null)

const title = computed(() => t('admin.promptAudit.detail.title', { id: props.eventId ?? '' }))

const scannerScoreEntries = computed(() => Object.entries(event.value?.scanner_scores ?? {}))

function close(): void {
  emit('close')
}

async function fetchEvent(): Promise<void> {
  if (!props.eventId) {
    event.value = null
    return
  }
  loading.value = true
  event.value = null
  try {
    event.value = await getPromptAuditEvent(props.eventId)
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('admin.promptAudit.loadEventFailed')))
  } finally {
    loading.value = false
  }
}

watch(
  () => [props.show, props.eventId] as const,
  ([show]) => {
    if (show) void fetchEvent()
  },
)
</script>
