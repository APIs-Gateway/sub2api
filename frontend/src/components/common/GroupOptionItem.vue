<template>
  <div class="flex min-w-0 flex-1 items-start justify-between gap-3">
    <!-- Left: name + description -->
    <div
      class="flex min-w-0 flex-1 flex-col items-start"
      :title="description || undefined"
    >
      <!-- Row 1: platform badge (name bold) -->
      <GroupBadge
        :name="name"
        :platform="platform"
        :subscription-type="subscriptionType"
        :show-rate="false"
        class="groupOptionItemBadge"
      />
      <!-- Row 2: description with top spacing -->
      <span
        v-if="description"
        class="mt-1.5 w-full whitespace-pre-line [overflow-wrap:anywhere] text-left text-xs leading-relaxed text-gray-500 dark:text-gray-400 line-clamp-3"
      >
        {{ description }}
      </span>
    </div>

    <!-- Right: rate pill (+ plan rate below it) + checkmark (aligned to the pill) -->
    <div class="flex shrink-0 items-start gap-2 pt-0.5">
      <div class="flex flex-col items-end gap-1">
        <!-- Rate pill (platform color) -->
        <span v-if="rateMultiplier !== undefined" :title="rateTip" :class="['inline-flex items-center whitespace-nowrap rounded-full px-3 py-1 text-xs font-semibold', ratePillClass]">
          <template v-if="hasCustomRate">
            <span class="mr-1 line-through opacity-50">{{ defaultRateText }}x</span>
            <span class="font-bold">{{ customRateText }}x</span>
          </template>
          <template v-else>
            {{ t('groups.rateMultiplierLabel', { rate: pillRateText }) }}
          </template>
        </span>
        <!-- 套餐低至（人民币模式，套餐可买时） -->
        <PlanRateText
          v-if="rateMultiplier !== undefined && rateView && rateView.plan !== undefined"
          :rate-view="rateView"
          class="text-xs"
        />
      </div>
      <!-- Checkmark -->
      <svg
        v-if="showCheckmark && selected"
        class="mt-1 h-4 w-4 shrink-0 text-primary-600 dark:text-primary-400"
        fill="none"
        stroke="currentColor"
        viewBox="0 0 24 24"
        stroke-width="2"
      >
        <path stroke-linecap="round" stroke-linejoin="round" d="M5 13l4 4L19 7" />
      </svg>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import GroupBadge from './GroupBadge.vue'
import PlanRateText from './PlanRateText.vue'
import type { GroupRateView } from '@/composables/useGroupRateView'
import type { SubscriptionType, GroupPlatform } from '@/types'

interface Props {
  name: string
  platform: GroupPlatform
  subscriptionType?: SubscriptionType
  rateMultiplier?: number
  userRateMultiplier?: number | null
  description?: string | null
  selected?: boolean
  showCheckmark?: boolean
  /**
   * 用户端的展示倍率（useGroupRateView().groupRateView 生成）：人民币模式显示等效倍率，
   * 药丸下面补「套餐低至」，悬停提示用新口径。不传就按 rateMultiplier / userRateMultiplier
   * 原样显示、悬停用 groups.rateMultiplierTip。
   */
  rateView?: GroupRateView
}

const props = withDefaults(defineProps<Props>(), {
  subscriptionType: 'standard',
  selected: false,
  showCheckmark: true,
  userRateMultiplier: null
})

const { t } = useI18n()

// Whether user has a custom rate different from default
const hasCustomRate = computed(() => {
  return (
    props.userRateMultiplier !== null &&
    props.userRateMultiplier !== undefined &&
    props.rateMultiplier !== undefined &&
    props.userRateMultiplier !== props.rateMultiplier
  )
})

// 药丸文字：有 rateView 用换算好的；专属倍率时 mainStruck 是划掉的默认倍率
const hasCustomView = computed(() => props.rateView?.mainStruck !== undefined)
const defaultRateText = computed(() =>
  hasCustomView.value ? props.rateView!.mainStruck : String(props.rateMultiplier)
)
const customRateText = computed(() =>
  hasCustomView.value ? props.rateView!.main : String(props.userRateMultiplier)
)
const pillRateText = computed(() => (props.rateView ? props.rateView.main : props.rateMultiplier))
const rateTip = computed(() => (props.rateView ? props.rateView.tip : t('groups.rateMultiplierTip')))

// Rate pill color matches platform badge color
const ratePillClass = computed(() => {
  switch (props.platform) {
    case 'anthropic':
      return 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-400'
    case 'openai':
      return 'bg-green-50 text-green-700 dark:bg-green-900/20 dark:text-green-400'
    case 'gemini':
      return 'bg-sky-50 text-sky-700 dark:bg-sky-900/20 dark:text-sky-400'
    default: // antigravity and others
      return 'bg-violet-50 text-violet-700 dark:bg-violet-900/20 dark:text-violet-400'
  }
})
</script>

<style scoped>
/* Bold the group name inside GroupBadge when used in dropdown option */
.groupOptionItemBadge :deep(span.truncate) {
  font-weight: 600;
}
</style>
