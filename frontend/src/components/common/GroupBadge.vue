<template>
  <span
    :class="[
      'inline-flex items-center gap-1.5 rounded-md px-2 py-0.5 text-xs font-medium transition-colors',
      badgeClass
    ]"
    :title="planRateView ? planRateView.tip : undefined"
  >
    <!-- Platform logo -->
    <PlatformIcon v-if="platform" :platform="platform" size="sm" />
    <!-- Group name -->
    <span class="truncate">{{ name }}</span>
    <!-- Right side label -->
    <span v-if="showLabel" :class="labelClass">
      <template v-if="hasCustomRate">
        <!-- 原倍率删除线 + 专属倍率高亮 -->
        <span class="line-through opacity-50 mr-0.5">{{ defaultRateText }}x</span>
        <span class="font-bold">{{ customRateText }}x</span>
      </template>
      <template v-else>
        {{ labelText }}
      </template>
    </span>
    <!-- 套餐低至：窄屏收起，信息在悬停提示里 -->
    <PlanRateText
      v-if="planRateView && inlinePlan"
      :rate-view="planRateView"
      class="hidden text-[10px] font-medium sm:inline"
    />
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { SubscriptionType, GroupPlatform } from '@/types'
import PlatformIcon from './PlatformIcon.vue'
import PlanRateText from './PlanRateText.vue'
import type { GroupRateView } from '@/composables/useGroupRateView'

interface Props {
  name: string
  platform?: GroupPlatform
  subscriptionType?: SubscriptionType
  rateMultiplier?: number
  userRateMultiplier?: number | null // 用户专属倍率
  showRate?: boolean
  daysRemaining?: number | null // 剩余天数（订阅类型时使用）
  /**
   * 订阅分组默认在右侧 label 展示"订阅"或剩余天数；
   * 开启后订阅分组也改为显示倍率（保留订阅主题色 label，配合可用渠道这类
   * 只关心费率、不关心有效期的场景）。
   */
  alwaysShowRate?: boolean
  /**
   * 用户端的展示倍率（useGroupRateView().groupRateView 生成）：人民币模式显示等效倍率，
   * 并在右侧补「套餐低至」。不传就按 rateMultiplier / userRateMultiplier 原样显示（后台用）。
   */
  rateView?: GroupRateView
  /**
   * 「套餐低至」是否放在徽标里面（默认是）。表格行里横向放不下（密钥列表在 1440 宽下已经要
   * 横向滚动），传 false 由使用处把它放在徽标下一行；悬停提示不受影响。
   */
  inlinePlan?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  subscriptionType: 'standard',
  showRate: true,
  daysRemaining: null,
  userRateMultiplier: null,
  alwaysShowRate: false,
  inlinePlan: true
})

const { t } = useI18n()

const isSubscription = computed(() => props.subscriptionType === 'subscription')

// 是否有专属倍率（且与默认倍率不同）
const hasCustomRate = computed(() => {
  return (
    props.userRateMultiplier !== null &&
    props.userRateMultiplier !== undefined &&
    props.rateMultiplier !== undefined &&
    props.userRateMultiplier !== props.rateMultiplier
  )
})

// 标签里显示的是倍率（而不是订阅天数）：专属倍率、标准分组，或订阅分组开了 alwaysShowRate
const labelShowsRate = computed(
  () => hasCustomRate.value || !isSubscription.value || props.alwaysShowRate
)

// 标签显示倍率时才用 rateView；订阅分组显示「订阅」/ 剩余天数，不受影响
const shownRateView = computed(() => (labelShowsRate.value ? props.rateView : undefined))

// 专属倍率：划掉的默认倍率和高亮的专属倍率。有 rateView 用它换算好的，否则用原始值
const hasCustomView = computed(() => shownRateView.value?.mainStruck !== undefined)
const defaultRateText = computed(() =>
  hasCustomView.value ? shownRateView.value!.mainStruck : String(props.rateMultiplier)
)
const customRateText = computed(() =>
  hasCustomView.value ? shownRateView.value!.main : String(props.userRateMultiplier)
)

// 是否显示右侧标签
const showLabel = computed(() => {
  if (!props.showRate) return false
  // 订阅类型：显示天数或"订阅"
  if (isSubscription.value) return true
  // 标准类型：显示倍率（包括专属倍率）
  return props.rateMultiplier !== undefined || hasCustomRate.value
})

// 有「套餐低至」才显示第二段，悬停提示也只在这时加（其余情况徽标与原来逐字相同）
const planRateView = computed(() =>
  showLabel.value && shownRateView.value?.plan !== undefined ? shownRateView.value : undefined
)

// Label text
const labelText = computed(() => {
  const rateLabel = shownRateView.value
    ? `${shownRateView.value.main}x`
    : props.rateMultiplier !== undefined
      ? `${props.rateMultiplier}x`
      : ''
  if (isSubscription.value && !props.alwaysShowRate) {
    // 如果有剩余天数，显示天数
    if (props.daysRemaining !== null && props.daysRemaining !== undefined) {
      if (props.daysRemaining <= 0) {
        return t('admin.users.expired')
      }
      return t('admin.users.daysRemaining', { days: props.daysRemaining })
    }
    // 否则显示"订阅"
    return t('groups.subscription')
  }
  return rateLabel
})

// Label style based on type and days remaining
const labelClass = computed(() => {
  const base = 'px-1.5 py-0.5 rounded text-[10px] font-semibold'

  if (!isSubscription.value) {
    // Standard: subtle background (不再为专属倍率使用不同的背景色)
    return `${base} bg-black/10 dark:bg-white/10`
  }

  // 订阅类型：根据剩余天数显示不同颜色
  if (props.daysRemaining !== null && props.daysRemaining !== undefined) {
    if (props.daysRemaining <= 0 || props.daysRemaining <= 3) {
      // 已过期或紧急（<=3天）：红色
      return `${base} bg-red-200/80 text-red-800 dark:bg-red-800/50 dark:text-red-300`
    }
    if (props.daysRemaining <= 7) {
      // 警告（<=7天）：橙色
      return `${base} bg-amber-200/80 text-amber-800 dark:bg-amber-800/50 dark:text-amber-300`
    }
  }

  // 正常状态或无天数：根据平台显示主题色
  if (props.platform === 'anthropic') {
    return `${base} bg-orange-200/60 text-orange-800 dark:bg-orange-800/40 dark:text-orange-300`
  }
  if (props.platform === 'openai') {
    return `${base} bg-emerald-200/60 text-emerald-800 dark:bg-emerald-800/40 dark:text-emerald-300`
  }
  if (props.platform === 'gemini') {
    return `${base} bg-blue-200/60 text-blue-800 dark:bg-blue-800/40 dark:text-blue-300`
  }
  return `${base} bg-violet-200/60 text-violet-800 dark:bg-violet-800/40 dark:text-violet-300`
})

// Badge color based on platform and subscription type
const badgeClass = computed(() => {
  if (props.platform === 'anthropic') {
    // Claude: orange theme
    return isSubscription.value
      ? 'bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400'
      : 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-400'
  } else if (props.platform === 'openai') {
    // OpenAI: green theme
    return isSubscription.value
      ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400'
      : 'bg-green-50 text-green-700 dark:bg-green-900/20 dark:text-green-400'
  }
  if (props.platform === 'gemini') {
    return isSubscription.value
      ? 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400'
      : 'bg-sky-50 text-sky-700 dark:bg-sky-900/20 dark:text-sky-400'
  }
  // Fallback: original colors
  return isSubscription.value
    ? 'bg-violet-100 text-violet-700 dark:bg-violet-900/30 dark:text-violet-400'
    : 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400'
})
</script>
