import { useI18n } from 'vue-i18n'

import { useRateDisplay } from '@/composables/useRateDisplay'
import type { RateView } from '@/utils/rateDisplay'

/**
 * 分组徽标 / 下拉药丸用的倍率视图：useRateDisplay 的换算结果 + 悬停文案。
 * `tip` 是药丸的悬停提示；徽标只在有套餐倍率时才用它（窄屏会把「套餐低至」收起来，信息放进悬停）。
 */
export interface GroupRateView extends RateView {
  tip: string
}

/**
 * 用户端分组倍率的入口。GroupBadge / GroupOptionItem 与后台共用，所以它们的 `rateView`
 * 是可选入参：用户端页面用这里的 groupRateView 生成后传入，后台不传就是原来的样子。
 *
 * 悬停文案按站点和展示口径分三种：
 * - 充值倍率为 1 的站点（free 站，只有美元一个口径）：保持原来的 groups.rateMultiplierTip，一个字不变；
 * - 其余站点的人民币模式：每 $1 官方价的用量实付多少元，有套餐倍率时多一行套餐说明；
 * - 其余站点的美元模式：每 $1 官方价的用量扣多少额度。
 * 数字都取徽标上显示的同一个格式化结果，不另算。
 */
export function useGroupRateView() {
  const { t } = useI18n()
  const rateDisplay = useRateDisplay()

  function tipOf(view: RateView): string {
    const { isFiat, rechargeMultiplier } = rateDisplay.ctx.value
    if (rechargeMultiplier === 1) return t('groups.rateMultiplierTip')
    if (!isFiat) return t('groups.rateTipUsd', { rate: view.main })

    const lines = [t('groups.rateTipFiat', { rate: view.main })]
    if (view.plan !== undefined) lines.push(t('groups.planRateTip', { rate: view.plan }))
    if (view.yourPlan !== undefined) lines.push(t('groups.yourPlanRateTip', { rate: view.yourPlan }))
    return lines.join('\n')
  }

  /** 一个分组的倍率视图。group 传默认倍率或带 rate_multiplier 的分组对象，userRate 是用户专属倍率。 */
  function groupRateView(
    group: number | { rate_multiplier?: number | null },
    userRate?: number | null
  ): GroupRateView {
    const view = rateDisplay.rateView(group, userRate)
    return { ...view, tip: tipOf(view) }
  }

  return { groupRateView, isFiat: rateDisplay.isFiat }
}
