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
        <p class="text-xs leading-relaxed text-gray-600 dark:text-gray-400" data-test="reference-model">
          {{ t('keyFallback.editor.referenceModel', { model: chain.reference_model }) }}
        </p>
        <span
          class="shrink-0 text-xs text-gray-600 dark:text-gray-400"
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
                  class="inline-flex items-center gap-1 rounded bg-gray-900 px-1.5 py-0.5 text-[11px] font-medium text-white dark:bg-dark-600 dark:text-gray-100"
                  :title="t('keyFallback.editor.primaryHint')"
                >
                  <Icon name="lock" size="xs" />
                  {{ t('keyFallback.editor.primary') }}
                </span>
              </div>
              <StatusMark :item="primary" />
            </div>
            <PriceLine :item="primary" />
            <ReasonLine :item="primary" is-primary />
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
                    class="chain-icon-btn hover:!text-red-600 dark:hover:!text-red-400"
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
              <div
                v-if="!item.usable"
                class="mt-1.5"
              >
                <button
                  type="button"
                  class="text-xs font-medium text-amber-800 underline underline-offset-2 hover:text-red-600 disabled:cursor-not-allowed disabled:opacity-40 dark:text-amber-300 dark:hover:text-red-400"
                  :disabled="saving"
                  :aria-label="t('keyFallback.editor.remove', { name: item.name })"
                  data-test="remove-unusable"
                  @click="removeItem(item.group_id)"
                >
                  {{ t('keyFallback.editor.removeUnusable') }}
                </button>
              </div>
              <p
                v-if="itemErrors[item.group_id]"
                class="mt-1.5 rounded border border-red-200 bg-red-50 px-2 py-1 text-xs text-red-700 dark:border-red-900/50 dark:bg-red-900/20 dark:text-red-300"
                role="alert"
                data-test="item-error"
              >
                {{ itemErrors[item.group_id] }}
              </p>
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
              class="mt-1.5 text-xs text-gray-600 dark:text-gray-400"
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
              <p class="border-b border-gray-100 px-3 py-2 text-xs text-gray-600 dark:border-dark-700 dark:text-gray-400">
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

      <p v-if="localFallbacks.length > 1" class="mt-3 text-xs text-gray-600 dark:text-gray-400">
        {{ t('keyFallback.editor.orderHint') }}
      </p>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, onBeforeUnmount, onMounted, ref, watch, type PropType } from 'vue'
import { useI18n } from 'vue-i18n'
import { VueDraggable } from 'vue-draggable-plus'
import GroupBadge from '@/components/common/GroupBadge.vue'
import Icon from '@/components/icons/Icon.vue'
import { keyFallbackAPI } from '@/api/keyFallback'
import { useCurrencyDisplay } from '@/composables/useCurrencyDisplay'
import { extractApiErrorCode, extractApiErrorMetadata } from '@/utils/apiError'
import { fallbackErrorMessage, REFRESH_ON_ERROR } from '@/utils/keyFallbackError'
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
/** 后端指明了出错分组时，错误标在对应那一项上（key 为 group_id） */
const itemErrors = ref<Record<number, string>>({})
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
onBeforeUnmount(() => clearTimeout(savedTimer))
watch(
  () => props.keyId,
  () => {
    chain.value = null
    localFallbacks.value = []
    pickerOpen.value = false
    actionError.value = ''
    itemErrors.value = {}
    load()
  }
)

/** 失败后静默重新读取这把 Key 的真实链状态；不显示骨架屏，读取失败就保持当前界面 */
async function refreshSilently(): Promise<KeyFallbackChain | null> {
  const version = ++loadVersion
  try {
    const res = await keyFallbackAPI.getChain(props.keyId)
    if (version !== loadVersion) return null
    chain.value = res
    syncLocal(res)
    return res
  } catch {
    return null
  }
}

/**
 * 整条替换：照常提交整条链，不替用户删掉不可用项。
 * 失败时先回到本地快照，再向服务器重新拉一次真实状态（请求可能已落库，或主分组已被改）。
 */
async function persist(nextIds: number[]) {
  if (saving.value || !chain.value) return
  saving.value = true
  actionError.value = ''
  itemErrors.value = {}
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
    const fresh = await refreshSilently()
    if (fresh) emit('changed', fresh)
    const code = extractApiErrorCode(err)
    const message = fallbackErrorMessage(
      err,
      t,
      'keyFallback.editor.saveFailed',
      fresh && code && REFRESH_ON_ERROR.has(code) ? 'refreshed' : undefined
    )
    // 后端在 metadata 里带了 group_id：标在对应那一项上；那一项已不在链里（比如刚加的被拒）或没有 group_id，就用整体提示
    const raw = extractApiErrorMetadata(err)?.group_id
    const gid = raw == null ? NaN : Number(raw)
    if (Number.isFinite(gid) && localFallbacks.value.some((i) => i.group_id === gid)) {
      itemErrors.value = { [gid]: message }
    } else {
      actionError.value = message
    }
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
            class: ['h-1.5 w-1.5 rounded-full', ok ? 'bg-emerald-600 dark:bg-emerald-400' : 'bg-amber-600 dark:bg-amber-400'],
            'aria-hidden': 'true'
          }),
          t(`keyFallback.editor.status.${p.item.status}`)
        ]
      )
    }
  }
})

// 每百万 Token 单价的金额选项（见 utils/numberFormat 的 unitPrice）。
const UNIT_PRICE = { unitPrice: true } as const

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
      const cls = ['text-xs tabular-nums text-gray-600 dark:text-gray-400', p.bare ? 'block' : 'mt-1.5']
      // 人民币用服务端给的余额价口径（cny），不在前端自己乘倍率或汇率；
      // 全站切到美元（含 free 站）或没有 cny 时，显示同一价格的美元口径。
      // 每百万 Token 价格是报价，两条路径都按单价规则显示（不被四舍五入吞掉第三、四位小数），
      // 与模型目录、用量页的同类单价一致，不自己定小数位。
      // 哪条路径要的字段齐全才走哪条：人民币只看 cny，美元只看两个 usd 字段。
      let input: string | null = null
      let output: string | null = null
      if (price?.priced) {
        if (isFiat.value && price.cny) {
          input = formatFiat(price.cny.input_per_mtok, UNIT_PRICE)
          output = formatFiat(price.cny.output_per_mtok, UNIT_PRICE)
        } else if (typeof price.input_usd_per_mtok === 'number' && typeof price.output_usd_per_mtok === 'number') {
          input = formatUsd(price.input_usd_per_mtok, UNIT_PRICE)
          output = formatUsd(price.output_usd_per_mtok, UNIT_PRICE)
        }
      }
      if (input === null || output === null) {
        return h(tag, { class: cls, 'data-test': 'price' }, t('keyFallback.editor.unpriced'))
      }
      return h(tag, { class: cls, 'data-test': 'price' }, [
        t('keyFallback.editor.input', { price: input }),
        h('span', { class: 'text-gray-300 dark:text-dark-500', 'aria-hidden': 'true' }, ' / '),
        t('keyFallback.editor.output', { price: output })
      ])
    }
  }
})

const ReasonLine = defineComponent({
  props: {
    item: { type: Object as PropType<KeyFallbackChainItem>, required: true },
    // 主分组不能在这里移除，不能让它显示「建议移除」，改成指引去编辑密钥
    isPrimary: { type: Boolean, default: false }
  },
  setup(p) {
    return () => {
      if (p.item.usable && p.item.status === 'active') return null
      const key = p.isPrimary
        ? 'primary'
        : p.item.status === 'disabled'
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
/*
 * 深色规则一律写成 `.dark .x`。不要写 `:global(.dark) .x`：Vue 3 会把它编译成孤零零一条 `.dark`，
 * 规则落到 <html> 上，既改不了卡片和节点，还会把整页的底色和文字色一起改掉。
 */

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
.dark .chain-rail::before {
  background: theme('colors.dark.500');
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

/*
 * 节点：同样大小、同样粗细的圆环，填充色与卡片一致，只用颜色和填充区分状态。
 *   主分组 = 实心；可用的兜底 = 绿环；不可用的兜底 = 琥珀环；待添加 = 灰环
 */
.chain-node {
  position: relative;
  z-index: 1;
  flex: none;
  margin-top: 1rem;
  height: 15px;
  width: 15px;
  border-radius: 9999px;
  background: theme('colors.white');
  border: 2px solid theme('colors.gray.500');
}
.dark .chain-node {
  background: theme('colors.dark.800');
  border-color: theme('colors.dark.400');
}
.chain-node--primary {
  background: theme('colors.gray.900');
  border-color: theme('colors.gray.900');
}
.dark .chain-node--primary {
  background: theme('colors.gray.100');
  border-color: theme('colors.gray.100');
}
.chain-node--ok {
  border-color: theme('colors.emerald.600');
}
.dark .chain-node--ok {
  border-color: theme('colors.emerald.400');
}
.chain-node--off {
  border-color: theme('colors.amber.600');
}
.dark .chain-node--off {
  border-color: theme('colors.amber.400');
}
.chain-node--add {
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
.dark .chain-card {
  border-color: theme('colors.dark.600');
  background: theme('colors.dark.800');
}
.chain-card--off {
  border-style: dashed;
  background: theme('colors.gray.50');
}
.dark .chain-card--off {
  border-color: theme('colors.dark.500');
  background: theme('colors.dark.900');
}
.chain-icon-btn {
  display: inline-flex;
  height: 2.5rem;
  width: 2.5rem;
  align-items: center;
  justify-content: center;
  border-radius: 0.375rem;
  color: theme('colors.gray.500');
  transition: color 150ms, background-color 150ms;
}
.dark .chain-icon-btn {
  color: theme('colors.dark.300');
}
.chain-icon-btn:hover:not(:disabled) {
  background: theme('colors.gray.100');
  color: theme('colors.gray.700');
}
.dark .chain-icon-btn:hover:not(:disabled) {
  background: theme('colors.dark.700');
  color: theme('colors.dark.100');
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
.dark .add-btn {
  border-color: theme('colors.dark.500');
  color: theme('colors.dark.200');
}
.dark .add-btn:hover:not(:disabled) {
  border-color: theme('colors.dark.300');
  background: theme('colors.dark.800');
}
.dark .add-btn:disabled {
  color: theme('colors.dark.500');
  background: transparent;
}
@media (prefers-reduced-motion: reduce) {
  .chain-icon-btn,
  .add-btn {
    transition: none;
  }
}
</style>
