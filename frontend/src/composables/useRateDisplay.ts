import { computed, ref, watch } from 'vue'

import subscriptionsAPI, { type SubscriptionPricingBounds } from '@/api/subscriptions'
import { useCurrencyDisplay } from '@/composables/useCurrencyDisplay'
import { useSubscriptionStore } from '@/stores/subscriptions'
import {
  activeCardUnit,
  balanceRate as balanceRateOf,
  buildRateView,
  formatRate,
  formatRawRate,
  planLowestRate as planLowestRateOf,
  planRateAvailable,
  planRateForUnit as planRateForUnitOf,
  resolvePlanUnitMin,
  usageRowRate as usageRowRateOf,
  type RateContext,
  type RateView,
  type UsageRateRow
} from '@/utils/rateDisplay'
import { FeatureFlags, isFeatureFlagEnabled } from '@/utils/featureFlags'

/**
 * 套餐定价区间（/subscriptions/pricing）的共享缓存。
 *
 * 模块级单例：价格页、密钥分组下拉、购买面板都要「套餐低至」，不能各请求一次。
 * 成功和失败都只请求一次——失败时静默（套餐倍率只是展示增强，取不到就只显示主倍率），
 * 也不自动重试，避免列表里每一行挂载都再发一次请求。接口需要登录，只给登录后的用户端页面用。
 */
const planPricing = ref<SubscriptionPricingBounds | null>(null)
let planPricingRequest: Promise<void> | null = null

/** 请求一次套餐定价区间；已经请求过（不论成败）直接返回同一个 promise。 */
export function loadPlanPricing(): Promise<void> {
  if (!planPricingRequest) {
    planPricingRequest = (async () => {
      try {
        planPricing.value = (await subscriptionsAPI.getSubscriptionPricing()) ?? null
      } catch {
        // 静默：取不到就不出现套餐倍率。
      }
    })()
  }
  return planPricingRequest
}

/** 仅供测试复位。 */
export function resetPlanPricingForTest() {
  planPricing.value = null
  planPricingRequest = null
}

/**
 * 用户端展示倍率的入口：把 useCurrencyDisplay（isFiat、充值倍率）、公开设置
 * （payment_enabled）、生效套餐卡和共享的套餐定价区间接起来，换算全部交给
 * utils/rateDisplay.ts。组件只管把结果放进 i18n 模板，不要自己除 m / 乘 u。
 *
 * 套餐定价区间只在 m ≠ 1 且 payment_enabled !== false 时请求（一次，失败静默）。
 * 所有函数读的是响应式上下文，放进模板或 computed 里会随模式切换、设置加载自动更新。
 */
export function useRateDisplay() {
  const { isFiat, rechargeMultiplier } = useCurrencyDisplay()
  const subscriptionStore = useSubscriptionStore()

  const paymentEnabled = computed(() => isFeatureFlagEnabled(FeatureFlags.payment))
  const planUnitMin = computed(() => resolvePlanUnitMin(planPricing.value))
  const cardUnit = computed(() => activeCardUnit(subscriptionStore.activeSubscriptions))

  const ctx = computed<RateContext>(() => ({
    isFiat: isFiat.value,
    rechargeMultiplier: rechargeMultiplier.value,
    planUnitMin: planUnitMin.value,
    paymentEnabled: paymentEnabled.value,
    cardUnit: cardUnit.value
  }))

  const wantPlanPricing = computed(() => rechargeMultiplier.value !== 1 && paymentEnabled.value)
  watch(
    wantPlanPricing,
    (want) => {
      if (want) void loadPlanPricing()
    },
    { immediate: true }
  )

  /** 套餐倍率是否出现（R2 全部条件）。价格页的「套餐倍率」列据此整列显示 / 隐藏。 */
  const planAvailable = computed(() => planRateAvailable(ctx.value))

  /** R1 余额倍率：人民币模式 r ÷ m，美元模式原样 r。 */
  function balanceRate(r: number): number {
    return balanceRateOf(r, ctx.value)
  }

  /** R2 套餐低至 r × u_min；任一抑制条件成立为 null。 */
  function planLowestRate(r: number): number | null {
    return planLowestRateOf(r, ctx.value)
  }

  /** 套餐倍率 r × u（卡的精确单价、报价里的 unit_price）；不满足 R2 的 a、c、d 为 null。 */
  function planRateForUnit(r: number, u: number | null | undefined): number | null {
    return planRateForUnitOf(r, u, ctx.value)
  }

  /** R7 用量行倍率；null 表示显示 `-`（美元模式下取不到原始倍率时同样为 null）。 */
  function usageRowRate(row: UsageRateRow): number | null {
    return usageRowRateOf(row, ctx.value)
  }

  /**
   * 一个分组的倍率视图。group 可以直接传默认倍率，也可以传带 rate_multiplier 的分组对象；
   * userRate 是用户专属倍率。
   */
  function rateView(
    group: number | { rate_multiplier?: number | null },
    userRate?: number | null
  ): RateView {
    const base = typeof group === 'number' ? group : (group.rate_multiplier ?? NaN)
    return buildRateView(base, userRate, ctx.value)
  }

  return {
    ctx,
    isFiat,
    planUnitMin,
    cardUnit,
    planAvailable,
    balanceRate,
    planLowestRate,
    planRateForUnit,
    usageRowRate,
    rateView,
    formatRate,
    formatRawRate
  }
}
