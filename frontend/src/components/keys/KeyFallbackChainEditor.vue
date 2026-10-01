<template>
  <div class="fallback-editor" :aria-busy="loading || saving">
    <!-- 加载中 -->
    <div v-if="loading && !chain" class="space-y-3 py-2" data-test="editor-loading">
      <div class="h-14 animate-pulse rounded-lg bg-gray-100 dark:bg-dark-700" />
      <div class="h-14 animate-pulse rounded-lg bg-gray-100 dark:bg-dark-700" />
    </div>

    <!-- 加载失败 -->
    <div
      v-else-if="!chain"
      class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900/50 dark:bg-red-900/20 dark:text-red-300"
      role="alert"
      data-test="editor-load-error"
    >
      <p>{{ loadError || t('keyFallback.editor.loadFailed') }}</p>
      <button type="button" class="mt-2 text-sm font-medium underline underline-offset-2" @click="load">
        {{ t('keyFallback.drawer.retry') }}
      </button>
    </div>

    <template v-else>
      <div class="mb-3 flex items-start justify-between gap-3">
        <p class="text-xs leading-relaxed text-gray-500 dark:text-gray-400" data-test="reference-model">
          {{ t('keyFallback.editor.referenceModel', { model: chain.reference_model }) }}
        </p>
        <span
          class="shrink-0 text-xs text-gray-400 dark:text-gray-500"
          role="status"
          data-test="save-status"
        >
          <template v-if="saving">{{ t('keyFallback.editor.saving') }}</template>
          <template v-else-if="justSaved">{{ t('keyFallback.editor.saved') }}</template>
        </span>
      </div>

      <div
        v-if="actionError"
        class="mb-3 rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-900/50 dark:bg-red-900/20 dark:text-red-300"
        role="alert"
        data-test="action-error"
      >
        {{ actionError }}
      </div>

      <!-- 链：左侧一条竖线把各项串起来，节点表示这一跳的状态 -->
      <div class="chain-rail relative">
        <!-- 主要项：固定，不可拖动、不可删除 -->
        <div v-if="primary" class="chain-row" data-test="primary-item">
          <span class="chain-node chain-node--primary" aria-hidden="true" />
          <div class="chain-card">
            <div class="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
              <div class="flex min-w-0 items-center gap-2">
                <GroupBadge
                  :name="primary.name"
                  :platform="chain.platform"
                  :rate-multiplier="primary.rate_multiplier"
                  :user-rate-multiplier="primary.user_rate_multiplier"
                />
                <span
                  class="inline-flex items-center gap-1 rounded bg-gray-900 px-1.5 py-0.5 text-[11px] font-medium text-white dark:bg-gray-100 dark:text-gray-900"
                  :title="t('keyFallback.editor.primaryHint')"
                >
                  <Icon name="lock" size="xs" />
                  {{ t('keyFallback.editor.primary') }}
                </span>
              </div>
              <StatusMark :item="primary" />
            </div>
            <PriceLine :item="primary" />
            <ReasonLine :item="primary" />
          </div>
        </div>

        <!-- 兜底项：可拖拽排序 -->
        <VueDraggable
          v-model="localFallbacks"
          :animation="180"
          handle=".drag-handle"
          :disabled="saving"
          class="chain-list"
          data-test="fallback-list"
          @end="onDragEnd"
        >
          <div
            v-for="(item, index) in localFallbacks"
            :key="item.group_id"
            class="chain-row"
            :data-test="`fallback-item-${item.group_id}`"
          >
            <span
              :class="['chain-node', item.usable ? 'chain-node--ok' : 'chain-node--off']"
              aria-hidden="true"
            />
            <div :class="['chain-card', !item.usable && 'chain-card--off']">
              <div class="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
                <div class="flex min-w-0 items-center gap-2">
                  <GroupBadge
                    :name="item.name"
                    :platform="chain.platform"
                    :rate-multiplier="item.rate_multiplier"
                    :user-rate-multiplier="item.user_rate_multiplier"
                  />
                </div>
                <div class="flex items-center gap-1">
                  <StatusMark :item="item" />
                  <button
                    type="button"
                    class="drag-handle chain-icon-btn cursor-grab active:cursor-grabbing"
                    :disabled="saving"
                    :aria-label="t('keyFallback.editor.dragHandle', { name: item.name })"
                    data-test="drag-handle"
                    @keydown.up.prevent="moveBy(index, -1)"
                    @keydown.down.prevent="moveBy(index, 1)"
                  >
                    <svg class="h-4 w-4" viewBox="0 0 20 20" fill="currentColor" aria-hidden="true">
                      <path
                        d="M7 3a1.5 1.5 0 1 0 0 3 1.5 1.5 0 0 0 0-3zM13 3a1.5 1.5 0 1 0 0 3 1.5 1.5 0 0 0 0-3zM7 8.5a1.5 1.5 0 1 0 0 3 1.5 1.5 0 0 0 0-3zM13 8.5a1.5 1.5 0 1 0 0 3 1.5 1.5 0 0 0 0-3zM7 14a1.5 1.5 0 1 0 0 3 1.5 1.5 0 0 0 0-3zM13 14a1.5 1.5 0 1 0 0 3 1.5 1.5 0 0 0 0-3z"
                      />
                    </svg>
                  </button>
                  <button
                    type="button"
                    class="chain-icon-btn hover:!text-red-600"
                    :disabled="saving"
                    :aria-label="t('keyFallback.editor.remove', { name: item.name })"
                    data-test="remove"
                    @click="removeItem(item.group_id)"
                  >
                    <Icon name="x" size="sm" />
                  </button>
                </div>
              </div>
              <PriceLine :item="item" />
              <ReasonLine :item="item" />
            </div>
          </div>
        </VueDraggable>

        <!-- 链的末尾：添加兜底分组 -->
        <div class="chain-row" data-test="add-row">
          <span class="chain-node chain-node--add" aria-hidden="true" />
          <div class="min-w-0 flex-1">
            <button
              type="button"
              :disabled="addDisabled"
              :aria-expanded="pickerOpen"
              :aria-describedby="addDisabled ? addHintId : undefined"
              class="add-btn"
              data-test="add-button"
              @click="pickerOpen = !pickerOpen"
            >
              {{ t('keyFallback.editor.add') }}
            </button>
            <p
              v-if="addDisabledReason"
              :id="addHintId"
              class="mt-1.5 text-xs text-gray-500 dark:text-gray-400"
              data-test="add-hint"
            >
              {{ addDisabledReason }}
            </p>

            <!-- 选择列表 -->
            <div
              v-if="pickerOpen && !addDisabled"
              class="picker mt-2 overflow-hidden rounded-lg border border-gray-200 bg-white shadow-sm dark:border-dark-600 dark:bg-dark-800"
              role="listbox"
              :aria-label="t('keyFallback.editor.pickTitle')"
              data-test="picker"
            >
              <p class="border-b border-gray-100 px-3 py-2 text-xs text-gray-500 dark:border-dark-700 dark:text-gray-400">
                {{ t('keyFallback.editor.pickTitle') }}
              </p>
              <ul class="max-h-64 divide-y divide-gray-100 overflow-y-auto dark:divide-dark-700">
                <li v-for="g in addableGroups" :key="g.group_id">
                  <button
                    type="button"
                    role="option"
                    class="flex w-full flex-col gap-1 px-3 py-2.5 text-left transition-colors hover:bg-gray-50 focus-visible:bg-gray-50 focus-visible:outline-none dark:hover:bg-dark-700 dark:focus-visible:bg-dark-700"
                    :data-test="`pick-${g.group_id}`"
                    @click="addGroup(g.group_id)"
                  >
                    <span class="flex flex-wrap items-center gap-2">
                      <GroupBadge
                        :name="g.name"
                        :platform="chain.platform"
                        :rate-multiplier="g.rate_multiplier"
                        :user-rate-multiplier="g.user_rate_multiplier ?? null"
                      />
                    </span>
                    <PriceLine :item="g" bare />
                  </button>
                </li>
              </ul>
            </div>
          </div>
        </div>
      </div>

      <p v-if="localFallbacks.length > 1" class="mt-3 text-xs text-gray-400 dark:text-gray-500">
        {{ t('keyFallback.editor.orderHint') }}
      </p>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, onMounted, ref, watch, type PropType } from 'vue'
import { useI18n } from 'vue-i18n'
import { VueDraggable } from 'vue-draggable-plus'
import GroupBadge from '@/components/common/GroupBadge.vue'
import Icon from '@/components/icons/Icon.vue'
import { keyFallbackAPI } from '@/api/keyFallback'
import { useCurrencyDisplay } from '@/composables/useCurrencyDisplay'
import { fallbackErrorMessage } from '@/utils/keyFallbackError'
import type {
  KeyFallbackAvailableGroup,
  KeyFallbackChain,
  KeyFallbackChainItem,
  KeyFallbackReferencePrice
} from '@/types'

const props = defineProps<{ keyId: number }>()
const emit = defineEmits<{
  (e: 'changed', chain: KeyFallbackChain): void
}>()

const { t } = useI18n()
const { isFiat, formatFiat, formatUsd } = useCurrencyDisplay()

const chain = ref<KeyFallbackChain | null>(null)
const loading = ref(false)
const loadError = ref('')
const saving = ref(false)
const justSaved = ref(false)
const actionError = ref('')
const pickerOpen = ref(false)
const localFallbacks = ref<KeyFallbackChainItem[]>([])
const addHintId = `fallback-add-hint-${props.keyId}`

let loadVersion = 0
let savedTimer: ReturnType<typeof setTimeout> | undefined

const primary = computed(() => chain.value?.items.find((i) => i.role === 'primary') ?? null)

function syncLocal(next: KeyFallbackChain | null) {
  localFallbacks.value = next
    ? next.items.filter((i) => i.role === 'fallback').sort((a, b) => a.position - b.position)
    : []
}

const maxFallbacks = computed(() => chain.value?.max_fallbacks ?? 0)
const limitReached = computed(
  () => maxFallbacks.value > 0 && localFallbacks.value.length >= maxFallbacks.value
)

/** 可添加的分组：服务端给的 available，再去掉本地已加入的 */
const addableGroups = computed<KeyFallbackAvailableGroup[]>(() => {
  const chosen = new Set(localFallbacks.value.map((i) => i.group_id))
  return (chain.value?.available ?? []).filter((g) => !chosen.has(g.group_id))
})

const addDisabledReason = computed(() => {
  if (limitReached.value) return t('keyFallback.editor.limitReached', { max: maxFallbacks.value })
  if (addableGroups.value.length === 0) return t('keyFallback.editor.pickEmpty')
  return ''
})
const addDisabled = computed(() => saving.value || addDisabledReason.value !== '')

watch(addDisabled, (v) => {
  if (v) pickerOpen.value = false
})

async function load() {
  const version = ++loadVersion
  loading.value = true
  loadError.value = ''
  try {
    const res = await keyFallbackAPI.getChain(props.keyId)
    if (version !== loadVersion) return
    chain.value = res
    syncLocal(res)
  } catch (err) {
    if (version !== loadVersion) return
    chain.value = null
    loadError.value = fallbackErrorMessage(err, t, 'keyFallback.editor.loadFailed')
  } finally {
    if (version === loadVersion) loading.value = false
  }
}

onMounted(load)
watch(
  () => props.keyId,
  () => {
    chain.value = null
    localFallbacks.value = []
    pickerOpen.value = false
    actionError.value = ''
    load()
  }
)

/** 整条替换；失败时恢复到上一次成功保存的状态并提示原因 */
async function persist(nextIds: number[]) {
  if (saving.value || !chain.value) return
  saving.value = true
  actionError.value = ''
  justSaved.value = false
  clearTimeout(savedTimer)
  try {
    const res = await keyFallbackAPI.replaceChain(props.keyId, nextIds)
    chain.value = res
    syncLocal(res)
    justSaved.value = true
    savedTimer = setTimeout(() => (justSaved.value = false), 2000)
    emit('changed', res)
  } catch (err) {
    syncLocal(chain.value)
    actionError.value = fallbackErrorMessage(err, t, 'keyFallback.editor.saveFailed')
  } finally {
    saving.value = false
  }
}

const savedIds = () =>
  (chain.value?.items ?? [])
    .filter((i) => i.role === 'fallback')
    .sort((a, b) => a.position - b.position)
    .map((i) => i.group_id)

function onDragEnd() {
  const next = localFallbacks.value.map((i) => i.group_id)
  const prev = savedIds()
  if (next.length === prev.length && next.every((id, i) => id === prev[i])) return
  persist(next)
}

/** 键盘排序：在拖动手柄上按上下方向键 */
function moveBy(index: number, delta: -1 | 1) {
  const target = index + delta
  if (saving.value || target < 0 || target >= localFallbacks.value.length) return
  const next = [...localFallbacks.value]
  ;[next[index], next[target]] = [next[target], next[index]]
  localFallbacks.value = next
  persist(next.map((i) => i.group_id))
}

function removeItem(groupId: number) {
  persist(localFallbacks.value.filter((i) => i.group_id !== groupId).map((i) => i.group_id))
}

function addGroup(groupId: number) {
  if (limitReached.value) return
  pickerOpen.value = false
  persist([...localFallbacks.value.map((i) => i.group_id), groupId])
}

// ---- 小组件：状态、价格、原因 ----

type PriceLike = { reference_price?: KeyFallbackReferencePrice }

const StatusMark = defineComponent({
  props: { item: { type: Object as PropType<KeyFallbackChainItem>, required: true } },
  setup(p) {
    return () => {
      const ok = p.item.usable && p.item.status === 'active'
      return h(
        'span',
        {
          class: [
            'inline-flex shrink-0 items-center gap-1.5 text-xs',
            ok ? 'text-emerald-700 dark:text-emerald-400' : 'text-amber-700 dark:text-amber-400'
          ],
          'data-test': 'status'
        },
        [
          h('span', {
            class: ['h-1.5 w-1.5 rounded-full', ok ? 'bg-emerald-500' : 'bg-amber-500'],
            'aria-hidden': 'true'
          }),
          t(`keyFallback.editor.status.${p.item.status}`)
        ]
      )
    }
  }
})

const PriceLine = defineComponent({
  props: {
    item: { type: Object as PropType<PriceLike>, required: true },
    bare: { type: Boolean, default: false }
  },
  setup(p) {
    return () => {
      const price = p.item.reference_price
      // 选择列表里整行是一个 button，里面只能放短语内容，所以用 span
      const tag = p.bare ? 'span' : 'p'
      const cls = ['text-xs tabular-nums text-gray-500 dark:text-gray-400', p.bare ? 'block' : 'mt-1.5']
      if (!price || !price.priced) {
        return h(tag, { class: cls, 'data-test': 'price' }, t('keyFallback.editor.unpriced'))
      }
      // 人民币用服务端给的余额价口径（cny），不在前端自己乘倍率或汇率；
      // 全站切到美元（含 free 站）或没有 cny 时，显示同一价格的美元口径。
      const useFiat = isFiat.value && !!price.cny
      const input = useFiat ? formatFiat(price.cny!.input_per_mtok) : formatUsd(price.input_usd_per_mtok, 3)
      const output = useFiat ? formatFiat(price.cny!.output_per_mtok) : formatUsd(price.output_usd_per_mtok, 3)
      return h(tag, { class: cls, 'data-test': 'price' }, [
        t('keyFallback.editor.input', { price: input }),
        h('span', { class: 'text-gray-300 dark:text-dark-500', 'aria-hidden': 'true' }, ' / '),
        t('keyFallback.editor.output', { price: output })
      ])
    }
  }
})

const ReasonLine = defineComponent({
  props: { item: { type: Object as PropType<KeyFallbackChainItem>, required: true } },
  setup(p) {
    return () => {
      if (p.item.usable && p.item.status === 'active') return null
      const key =
        p.item.status === 'disabled'
          ? 'disabled'
          : p.item.status === 'unavailable'
            ? 'unavailable'
            : 'notUsable'
      return h(
        'p',
        { class: 'mt-1 text-xs text-amber-700 dark:text-amber-400', 'data-test': 'reason' },
        t(`keyFallback.editor.reason.${key}`)
      )
    }
  }
})
</script>

<style scoped>
/* 竖线贯穿整条链；节点画在竖线上，节点样式表达这一跳是什么 */
.chain-rail::before {
  content: '';
  position: absolute;
  left: 7px;
  top: 18px;
  bottom: 18px;
  width: 1px;
  background: theme('colors.gray.300');
}
:global(.dark) .chain-rail::before {
  background: theme('colors.dark.600');
}
.chain-row {
  position: relative;
  display: flex;
  gap: 0.75rem;
  padding-bottom: 0.625rem;
}
.chain-list > .chain-row:last-child {
  padding-bottom: 0.625rem;
}
.chain-node {
  position: relative;
  z-index: 1;
  flex: none;
  margin-top: 1rem;
  height: 15px;
  width: 15px;
  border-radius: 9999px;
  background: theme('colors.white');
  border: 1.5px solid theme('colors.gray.400');
}
:global(.dark) .chain-node {
  background: theme('colors.dark.900');
  border-color: theme('colors.dark.400');
}
.chain-node--primary {
  background: theme('colors.gray.900');
  border-color: theme('colors.gray.900');
}
:global(.dark) .chain-node--primary {
  background: theme('colors.gray.100');
  border-color: theme('colors.gray.100');
}
.chain-node--ok {
  border-color: theme('colors.emerald.500');
}
.chain-node--off {
  border-style: dashed;
  border-color: theme('colors.amber.500');
}
.chain-node--add {
  border-style: dashed;
  margin-top: 0.625rem;
}
.chain-card {
  min-width: 0;
  flex: 1;
  border-radius: 0.5rem;
  border: 1px solid theme('colors.gray.200');
  background: theme('colors.white');
  padding: 0.625rem 0.75rem;
}
:global(.dark) .chain-card {
  border-color: theme('colors.dark.600');
  background: theme('colors.dark.800');
}
.chain-card--off {
  border-style: dashed;
  background: theme('colors.gray.50');
}
:global(.dark) .chain-card--off {
  background: theme('colors.dark.900');
}
.chain-icon-btn {
  display: inline-flex;
  height: 2rem;
  width: 2rem;
  align-items: center;
  justify-content: center;
  border-radius: 0.375rem;
  color: theme('colors.gray.400');
  transition: color 150ms, background-color 150ms;
}
.chain-icon-btn:hover:not(:disabled) {
  background: theme('colors.gray.100');
  color: theme('colors.gray.700');
}
:global(.dark) .chain-icon-btn:hover:not(:disabled) {
  background: theme('colors.dark.700');
  color: theme('colors.dark.200');
}
.chain-icon-btn:focus-visible,
.add-btn:focus-visible {
  outline: 2px solid theme('colors.primary.500');
  outline-offset: 1px;
}
.chain-icon-btn:disabled {
  cursor: not-allowed;
  opacity: 0.4;
}
.add-btn {
  width: 100%;
  border-radius: 0.5rem;
  border: 1px dashed theme('colors.gray.300');
  padding: 0.5rem 0.75rem;
  text-align: left;
  font-size: 0.875rem;
  font-weight: 500;
  color: theme('colors.gray.700');
  transition: border-color 150ms, background-color 150ms;
}
.add-btn:hover:not(:disabled) {
  border-color: theme('colors.gray.500');
  background: theme('colors.gray.50');
}
.add-btn:disabled {
  cursor: not-allowed;
  color: theme('colors.gray.400');
  background: transparent;
}
:global(.dark) .add-btn {
  border-color: theme('colors.dark.500');
  color: theme('colors.dark.200');
}
:global(.dark) .add-btn:hover:not(:disabled) {
  border-color: theme('colors.dark.300');
  background: theme('colors.dark.800');
}
:global(.dark) .add-btn:disabled {
  color: theme('colors.dark.500');
}
@media (prefers-reduced-motion: reduce) {
  .chain-icon-btn,
  .add-btn {
    transition: none;
  }
}
</style>
