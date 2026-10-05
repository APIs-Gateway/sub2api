package service

import (
	"context"
	"time"
)

// W6 PR5：OpenAI 网关（含 Grok）的影子比对点。设计 4.4、REVIEW_OPUS_2 S-1、S-2。

// openAIShadowBilling 比对点需要的全部输入。结算已经完成，这里只读。
type openAIShadowBilling struct {
	result   *OpenAIForwardResult
	apiKey   *APIKey // 结算用的 Key：稳定优先兜底时是影子 Key，其 Group 指向实际服务的分组
	account  *Account
	usageLog *UsageLog

	billingModels   []string
	multiplier      float64 // 乘额外倍率之前的原值
	imageMultiplier float64
	tokens          UsageTokens
	serviceTier     string
	pricingAt       time.Time

	// legacyCost、legacyErr 是 calculateOpenAIRecordUsageCost 的原始返回；cost 是结算最终用的成本
	// （无价、审计行等情况下已被改写），账号成本按它算。
	legacyCost *CostBreakdown
	legacyErr  error
	cost       *CostBreakdown
}

// shadowCompareBilling 在分组处于 shadow 阶段时，用 forceStage=v2 把 calculateOpenAIRecordUsageCost
// 与账号成本重算一遍并与结算结果比较。不处于 shadow 阶段、快照不可用、超过限速时什么也不做。
// 重算里的 panic 被吞掉并计数，永不影响结算。
func (s *OpenAIGatewayService) shadowCompareBilling(ctx context.Context, sb *openAIShadowBilling) {
	if sb == nil || sb.apiKey == nil || sb.apiKey.Group == nil || sb.result == nil {
		return
	}
	sess := beginPricingShadow(ctx, s.groupPolicy(), sb.apiKey.Group.ID)
	if sess == nil {
		return
	}
	sess.run(func(shadowCtx context.Context) {
		usageRef := ""
		if sb.usageLog != nil {
			usageRef = sb.usageLog.RequestID
		}
		model := firstUsageBillingModel(sb.billingModels)

		v2Cost, v2Err := s.calculateOpenAIRecordUsageCost(shadowCtx, sb.result, sb.apiKey, sb.billingModels,
			sb.multiplier, sb.imageMultiplier, sb.tokens, sb.serviceTier, sb.pricingAt)
		legacyView := newShadowCostView(model, sb.legacyCost, sb.legacyErr)
		v2View := newShadowCostView(model, v2Cost, v2Err)
		if !legacyView.equal(v2View) {
			sess.diff(ShadowKindCost, ShadowClassTranslation, model, usageRef, legacyView, v2View)
		}

		if sb.usageLog == nil || sb.account == nil || sb.cost == nil || sb.apiKey.GroupID == nil || *sb.apiKey.GroupID != sb.apiKey.Group.ID {
			return
		}
		// applyAccountStatsCost 会改写传入的 usageLog，所以传副本，结算的 usageLog 一个字节都不动（S-2）。
		shadowLog := *sb.usageLog
		applyAccountStatsCost(shadowCtx, &shadowLog, s.groupPolicy(), s.billingService,
			sb.account.ID, *sb.apiKey.GroupID, sb.result.UpstreamModel, sb.result.Model,
			sb.tokens, sb.cost.TotalCost, sb.pricingAt,
		)
		legacyAcct := newShadowAccountCostView(sb.usageLog.AccountStatsCost)
		v2Acct := newShadowAccountCostView(shadowLog.AccountStatsCost)
		if !legacyAcct.equal(v2Acct) {
			sess.diff(ShadowKindAccountCost, ShadowClassTranslation, model, usageRef, legacyAcct, v2Acct)
		}
	})
}
