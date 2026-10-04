<template>
  <BaseDialog
    :show="show"
    :title="`${t('keyFallback.admin.title')}${apiKey ? ' · ' + apiKey.name : ''}`"
    width="wide"
    @close="emit('close')"
  >
    <div v-if="loading && !chain" class="space-y-3 py-2" data-test="hidden-loading">
      <div class="h-12 animate-pulse rounded-lg bg-gray-100 dark:bg-dark-700" />
      <div class="h-12 animate-pulse rounded-lg bg-gray-100 dark:bg-dark-700" />
    </div>

    <div
      v-else-if="!chain"
      class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900/50 dark:bg-red-900/20 dark:text-red-300"
      role="alert"
      data-test="hidden-load-error"
    >
      {{ t('keyFallback.admin.loadFailed') }}
      <button type="button" class="ml-2 underline underline-offset-2" @click="load">
        {{ t('keyFallback.drawer.retry') }}
      </button>
    </div>

    <div v-else class="space-y-5">
      <p class="text-sm text-gray-600 dark:text-gray-400">{{ t('keyFallback.admin.intro') }}</p>

      <!-- 主分组与用户自己的兜底：只读 -->
      <div class="grid gap-3 sm:grid-cols-2">
        <div class="rounded-lg border border-gray-200 p-3 dark:border-dark-600">
          <p class="mb-1.5 text-xs text-gray-600 dark:text-gray-400">{{ t('keyFallback.admin.primary') }}</p>
          <p class="text-sm font-medium text-gray-900 dark:text-white" data-test="primary-name">
            {{ chain.primary_group.name }}
            <span class="ml-1 text-xs tabular-nums text-gray-600 dark:text-gray-400">{{ rateText(primaryRate) }}</span>
          </p>
        </div>
        <div class="rounded-lg border border-gray-200 p-3 dark:border-dark-600">
          <p class="mb-1.5 text-xs text-gray-600 dark:text-gray-400">{{ t('keyFallback.admin.userItems') }}</p>
          <p class="text-sm text-gray-900 dark:text-white">
            <template v-if="chain.user_items.length">
              {{ chain.user_items.map((i) => groupName(i.group_id)).join('、') }}
            </template>
            <template v-else>{{ t('keyFallback.admin.userItemsEmpty') }}</template>
          </p>
        </div>
      </div>

      <!-- 头部 / 尾部 -->
      <section v-for="part in parts" :key="part.name" :data-test="`part-${part.name}`">
        <h4 class="mb-2 text-sm font-semibold text-gray-800 dark:text-gray-100">
          {{ t(`keyFallback.admin.${part.name}`) }}
        </h4>
        <ul v-if="part.list.value.length" class="mb-2 space-y-1.5">
          <li
            v-for="(gid, idx) in part.list.value"
            :key="gid"
            class="flex items-center justify-between gap-2 rounded-lg border border-gray-200 px-3 py-2 dark:border-dark-600"
            :data-test="`${part.name}-item-${gid}`"
          >
            <span class="min-w-0 truncate text-sm text-gray-900 dark:text-white">
              {{ groupName(gid) }}
              <span class="ml-1 text-xs tabular-nums text-gray-600 dark:text-gray-400">{{ rateText(groupRate(gid)) }}</span>
            </span>
            <span class="flex shrink-0 items-center gap-0.5">
              <button
                type="button"
                class="hidden-icon-btn"
                :disabled="idx === 0 || saving"
                :aria-label="t('keyFallback.admin.moveUp', { name: groupName(gid) })"
                data-test="move-up"
                @click="move(part.list.value, idx, -1)"
              >
                <Icon name="chevronUp" size="sm" />
              </button>
              <button
                type="button"
                class="hidden-icon-btn"
                :disabled="idx === part.list.value.length - 1 || saving"
                :aria-label="t('keyFallback.admin.moveDown', { name: groupName(gid) })"
                data-test="move-down"
                @click="move(part.list.value, idx, 1)"
              >
                <Icon name="chevronDown" size="sm" />
              </button>
              <button
                type="button"
                class="hidden-icon-btn hover:!text-red-600 dark:hover:!text-red-400"
                :disabled="saving"
                :aria-label="t('keyFallback.admin.remove', { name: groupName(gid) })"
                data-test="remove"
                @click="remove(part.list.value, gid)"
              >
                <Icon name="x" size="sm" />
              </button>
            </span>
          </li>
        </ul>
        <p v-else class="mb-2 text-xs text-gray-600 dark:text-gray-400">{{ t(`keyFallback.admin.${part.name}Empty`) }}</p>

        <select
          class="input"
          :value="''"
          :disabled="saving || candidates.length === 0"
          :aria-label="t(part.name === 'head' ? 'keyFallback.admin.addHead' : 'keyFallback.admin.addTail')"
          :data-test="`add-${part.name}`"
          @change="onPick(part.list.value, $event)"
        >
          <option value="">
            {{
              candidates.length
                ? t(part.name === 'head' ? 'keyFallback.admin.addHead' : 'keyFallback.admin.addTail')
                : t('keyFallback.admin.noGroupToAdd')
            }}
          </option>
          <option v-for="g in candidates" :key="g.id" :value="g.id">
            {{ g.name }} ({{ rateText(g.rate_multiplier) }})
          </option>
        </select>
      </section>

      <!-- 写 head 时并排显示 head 与主分组的倍率：用户看到的价格与实际计费不一致是刻意的 -->
      <div
        v-if="head.length"
        class="rounded-lg border border-amber-300 bg-amber-50 px-3 py-2.5 text-sm text-amber-900 dark:border-amber-700/60 dark:bg-amber-900/20 dark:text-amber-200"
        data-test="rate-compare"
      >
        <p class="font-medium tabular-nums">
          {{
            t('keyFallback.admin.rateCompare', {
              head: groupRate(head[0]),
              primary: primaryRate
            })
          }}
        </p>
        <p class="mt-1 text-xs">{{ t('keyFallback.admin.rateWarn') }}</p>
      </div>

      <!-- 备注 -->
      <div>
        <label class="input-label" :for="noteId">{{ t('keyFallback.admin.note') }}</label>
        <textarea
          :id="noteId"
          v-model="note"
          rows="2"
          maxlength="200"
          :class="['input', noteInvalid && 'input-error']"
          :aria-invalid="noteInvalid"
          data-test="note"
        />
        <p
          :class="['mt-1 text-xs', noteInvalid ? 'text-red-600 dark:text-red-400' : 'text-gray-600 dark:text-gray-400']"
          data-test="note-hint"
        >
          {{ noteInvalid ? t('keyFallback.admin.noteRequired') : t('keyFallback.admin.noteHint') }}
        </p>
      </div>

      <!-- 实际顺序：服务端 dry-run -->
      <section v-if="chain.effective.length || chain.skipped?.length" data-test="effective">
        <h4 class="mb-2 text-sm font-semibold text-gray-800 dark:text-gray-100">
          {{ t('keyFallback.admin.effective') }}
        </h4>
        <ol class="space-y-1">
          <li
            v-for="hop in chain.effective"
            :key="hop.hop"
            class="flex flex-wrap items-center gap-x-2 text-xs text-gray-700 dark:text-gray-300"
          >
            <span class="text-gray-600 dark:text-gray-400">{{ t(`keyFallback.admin.hopSource.${hop.source}`) }}</span>
            <span>{{ groupName(hop.group_id) }}</span>
          </li>
        </ol>
        <ul v-if="chain.skipped?.length" class="mt-2 space-y-1" data-test="skipped">
          <li
            v-for="sk in chain.skipped"
            :key="`${sk.source}-${sk.group_id}`"
            class="text-xs text-amber-700 dark:text-amber-400"
          >
            {{ groupName(sk.group_id) }}
            {{ t('keyFallback.admin.skipped', { reason: skipReasonText(sk.skip_reason) }) }}
          </li>
        </ul>
      </section>

      <p
        v-if="errorText"
        class="rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-900/50 dark:bg-red-900/20 dark:text-red-300"
        role="alert"
        data-test="hidden-error"
      >
        {{ errorText }}
      </p>
    </div>

    <template #footer>
      <div v-if="chain" class="flex flex-wrap items-center justify-between gap-2">
        <button
          type="button"
          class="btn btn-secondary"
          :disabled="saving || !hasHidden"
          data-test="clear"
          @click="clearChain"
        >
          {{ t('keyFallback.admin.clear') }}
        </button>
        <button
          type="button"
          class="btn btn-primary"
          :disabled="saving"
          data-test="save"
          @click="save"
        >
          {{ t('keyFallback.admin.save') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { AdminGroup, AdminKeyFallbackChain, ApiKey } from '@/types'

// 管理端保持美元口径：倍率只显示 x 倍数，不做人民币换算
const NOTE_MIN_LENGTH = 4

const props = defineProps<{
  show: boolean
  apiKey: ApiKey | null
  groups: AdminGroup[]
}>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'saved'): void }>()

const { t } = useI18n()
const appStore = useAppStore()
const noteId = `hidden-chain-note-${Math.random().toString(36).slice(2, 8)}`

const chain = ref<AdminKeyFallbackChain | null>(null)
const loading = ref(false)
const saving = ref(false)
const errorText = ref('')
const head = ref<number[]>([])
const tail = ref<number[]>([])
const note = ref('')
const triedSave = ref(false)
let loadVersion = 0

const parts = [
  { name: 'head' as const, list: head },
  { name: 'tail' as const, list: tail }
]

const groupMap = computed(() => new Map(props.groups.map((g) => [g.id, g])))
const groupName = (id: number) =>
  groupMap.value.get(id)?.name ?? t('keyFallback.admin.unknownGroup', { id })
const groupRate = (id: number) => groupMap.value.get(id)?.rate_multiplier ?? 1
/** 后端 skip_reason 只有这四个值；其余（含将来新增的）一律显示「不可用」 */
const KNOWN_SKIP_REASONS = ['group_missing', 'group_inactive', 'invalid_platform', 'not_allowed']
const skipReasonText = (reason: string) =>
  t(`keyFallback.admin.skipReason.${KNOWN_SKIP_REASONS.includes(reason) ? reason : 'unknown'}`)
const rateText = (rate: number) => `${rate}x`

const primaryRate = computed(() =>
  chain.value ? groupRate(chain.value.primary_group.group_id) : 1
)

const hasHidden = computed(
  () => !!chain.value && (chain.value.hidden_head.length > 0 || chain.value.hidden_tail.length > 0)
)

/** 可加入隐藏链的分组：同平台、启用中、不是主分组、还没放进头部或尾部 */
const candidates = computed(() => {
  if (!chain.value) return []
  const used = new Set([chain.value.primary_group.group_id, ...head.value, ...tail.value])
  return props.groups.filter(
    (g) => g.platform === chain.value!.platform && g.status === 'active' && !used.has(g.id)
  )
})

const noteTooShort = computed(() => note.value.trim().length < NOTE_MIN_LENGTH)
/** 有 head 项必须写备注：head 让实际计费和用户看到的价格不一致，是刻意的，要写明原因 */
const noteNeeded = computed(() => head.value.length > 0)
const hasAny = computed(() => head.value.length > 0 || tail.value.length > 0)
const noteInvalid = computed(() => triedSave.value && noteNeeded.value && noteTooShort.value)

function applyChain(c: AdminKeyFallbackChain) {
  chain.value = c
  head.value = [...c.hidden_head].sort((a, b) => a.position - b.position).map((i) => i.group_id)
  tail.value = [...c.hidden_tail].sort((a, b) => a.position - b.position).map((i) => i.group_id)
  note.value = c.hidden_head[0]?.note ?? c.hidden_tail[0]?.note ?? ''
}

async function load() {
  if (!props.apiKey) return
  const version = ++loadVersion
  loading.value = true
  errorText.value = ''
  triedSave.value = false
  try {
    const res = await adminAPI.apiKeys.getFallbackChain(props.apiKey.id)
    if (version === loadVersion) applyChain(res)
  } catch (err) {
    if (version !== loadVersion) return
    chain.value = null
    console.error('Failed to load hidden fallback chain:', err)
  } finally {
    if (version === loadVersion) loading.value = false
  }
}

watch(
  () => [props.show, props.apiKey?.id] as const,
  ([show]) => {
    chain.value = null
    if (show) load()
    else loadVersion++
  },
  { immediate: true }
)

function move(list: number[], idx: number, delta: -1 | 1) {
  const target = idx + delta
  if (target < 0 || target >= list.length) return
  ;[list[idx], list[target]] = [list[target], list[idx]]
}
function remove(list: number[], gid: number) {
  const i = list.indexOf(gid)
  if (i >= 0) list.splice(i, 1)
}
function onPick(list: number[], e: Event) {
  const el = e.target as HTMLSelectElement
  const id = Number(el.value)
  el.value = ''
  if (id) list.push(id)
}

async function save() {
  if (!props.apiKey || saving.value) return
  triedSave.value = true
  errorText.value = ''
  if (noteNeeded.value && noteTooShort.value) return
  saving.value = true
  try {
    // 头部和尾部都空时等价于清空
    if (!hasAny.value) {
      await adminAPI.apiKeys.deleteHiddenFallbackChain(props.apiKey.id)
      appStore.showSuccess(t('keyFallback.admin.cleared'))
    } else {
      await adminAPI.apiKeys.putHiddenFallbackChain(props.apiKey.id, {
        head: [...head.value],
        tail: [...tail.value],
        ...(note.value.trim() ? { note: note.value.trim() } : {})
      })
      appStore.showSuccess(t('keyFallback.admin.saved'))
    }
    emit('saved')
    await load()
  } catch (err) {
    errorText.value = extractApiErrorMessage(err, t('keyFallback.admin.saveFailed'))
  } finally {
    saving.value = false
  }
}

async function clearChain() {
  if (!props.apiKey || saving.value) return
  if (typeof window !== 'undefined' && !window.confirm(t('keyFallback.admin.clearConfirm'))) return
  saving.value = true
  errorText.value = ''
  try {
    await adminAPI.apiKeys.deleteHiddenFallbackChain(props.apiKey.id)
    appStore.showSuccess(t('keyFallback.admin.cleared'))
    emit('saved')
    await load()
  } catch (err) {
    errorText.value = extractApiErrorMessage(err, t('keyFallback.admin.saveFailed'))
  } finally {
    saving.value = false
  }
}
</script>

<style scoped>
/* 深色规则写成 `.dark .x`，不要用 `:global(.dark) .x`（Vue 3 会把它编译成单独一条 `.dark`，规则落到 <html> 上） */
.hidden-icon-btn {
  display: inline-flex;
  height: 1.75rem;
  width: 1.75rem;
  align-items: center;
  justify-content: center;
  border-radius: 0.375rem;
  color: theme('colors.gray.500');
}
.dark .hidden-icon-btn {
  color: theme('colors.dark.300');
}
.hidden-icon-btn:hover:not(:disabled) {
  background: theme('colors.gray.100');
  color: theme('colors.gray.700');
}
.dark .hidden-icon-btn:hover:not(:disabled) {
  background: theme('colors.dark.700');
  color: theme('colors.dark.100');
}
.hidden-icon-btn:focus-visible {
  outline: 2px solid theme('colors.primary.500');
}
.hidden-icon-btn:disabled {
  cursor: not-allowed;
  opacity: 0.35;
}
</style>
