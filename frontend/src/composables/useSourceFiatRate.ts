import { computed } from 'vue'

import { useCurrencyDisplay, type MoneyDigits } from '@/composables/useCurrencyDisplay'
import { useSubscriptionStore } from '@/stores/subscriptions'

/**
 * 「当前扣费来源」的额度单价，给 Key 额度上限、5h/1d/7d 限额、平台限额这类
 * 「额度计数器」做人民币展示用（方案 K2）。
 *
 * 这些计数器累加的是扣掉的额度，钱包扣的和订阅卡扣的混在一起，单价不同，
 * 所以上限没有精确的人民币值。这里按用户此刻会被扣的来源折算：
 * 有生效中的订阅卡（同一时间只有一张）就按该卡 u(D)，否则按钱包 1/m。
 * 订阅到期后同一个上限的人民币显示会变大，这是 K2 的已知取舍。
 * 界面上金额前不加「≈」（cxw 要求）。
 *
 * 订阅卡列表由 App.vue 登录后拉取并缓存，这里只读 store，不额外发请求。
 */
export function useSourceFiatRate() {
  const subscriptionStore = useSubscriptionStore()
  const { isFiat, walletFiatPerCredit, formatFiat, formatUsd, creditsFromFiat, fiatFromCredits } =
    useCurrencyDisplay()

  const activeCardRate = computed(() => {
    for (const sub of subscriptionStore.activeSubscriptions) {
      const rate = sub.fiat_per_credit
      if (sub.status === 'active' && typeof rate === 'number' && Number.isFinite(rate) && rate > 0) {
        return rate
      }
    }
    return null
  })

  /** 1 个额度按当前扣费来源值多少人民币。 */
  const sourceFiatPerCredit = computed(() => activeCardRate.value ?? walletFiatPerCredit.value)

  /** 当前按订阅卡单价折算（文案提示用）。 */
  const usesSubscriptionRate = computed(() => activeCardRate.value !== null)

  /**
   * 按当前扣费来源单价展示一笔金额：人民币模式下是「¥x」，美元模式下是原始额度。
   * 给签到「今日消费」这类单个金额的展示用；它们同样是钱包与订阅卡混扣的额度。
   */
  function formatSourceAmount(credits: number | null | undefined, digits?: MoneyDigits): string {
    if (!isFiat.value) return formatUsd(credits, digits)
    return formatFiat(fiatFromCredits(credits, sourceFiatPerCredit.value), digits)
  }

  /** 额度上限的展示：人民币模式下是「¥x」，美元模式下是原始额度。与 formatSourceAmount 同口径。 */
  function formatLimit(credits: number | null | undefined, digits?: MoneyDigits): string {
    return formatSourceAmount(credits, digits)
  }

  /** 输入框：用户填的人民币按当前来源单价换算回额度。 */
  function limitCreditsFromFiat(fiat: number): number {
    return creditsFromFiat(fiat, sourceFiatPerCredit.value)
  }

  /** 输入框回显：额度按当前来源单价换算成人民币。 */
  function limitFiatFromCredits(credits: number | null | undefined): number {
    return fiatFromCredits(credits, sourceFiatPerCredit.value)
  }

  return {
    sourceFiatPerCredit,
    usesSubscriptionRate,
    formatLimit,
    formatSourceAmount,
    limitCreditsFromFiat,
    limitFiatFromCredits
  }
}
