package service

import (
	"context"
	"time"
)

// W6 PR5：Anthropic 网关（含 Gemini、Antigravity 共用的 recordUsageCore）的影子比对点。
// 设计 4.4、REVIEW_OPUS_2 S-1、S-2。

// gatewayShadowBilling 比对点需要的全部输入。结算已经完成，这里只读。
type gatewayShadowBilling struct {
	input    *recordUsageCoreInput
	result   *ForwardResult
	apiKey   *APIKey
	account  *Account
	usageLog *UsageLog

	// selectedModel 是回退之前选定的计费模型，concreteModel 是实际出站模型；
	// billingModel 是结算最终用的计费模型，cost 是结算算出的成本。
	selectedModel   string
	concreteModel   string
	billingModel    string
	multiplier      float64 // 乘额外倍率之前的原值
	imageMultiplier float64
	opts            *recordUsageOpts
	pricingAt       time.Time
	cost            *CostBreakdown
}

// shadowCompareBilling 在分组处于 shadow 阶段时，用 forceStage=v2 把同一段计算（从 billableModelWithFallback
// 起、到 calculateRecordUsageCost 止，再加账号成本）重算一遍并与结算结果比较。
// 不处于 shadow 阶段、快照不可用、超过限速时什么也不做。重算里的 panic 被吞掉并计数，永不影响结算。
func (s *GatewayService) shadowCompareBilling(ctx context.Context, sb *gatewayShadowBilling) {
	if sb == nil || sb.apiKey == nil || sb.apiKey.Group == nil || sb.cost == nil {
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

		v2Model, v2Cost := s.resolveBillingModelAndCost(shadowCtx, sb.input, sb.result, sb.apiKey,
			sb.selectedModel, sb.concreteModel, sb.multiplier, sb.imageMultiplier, sb.opts, sb.pricingAt)
		legacyView := newShadowCostView(sb.billingModel, sb.cost, nil)
		v2View := newShadowCostView(v2Model, v2Cost, nil)
		if !legacyView.equal(v2View) {
			sess.diff(ShadowKindCost, ShadowClassTranslation, sb.billingModel, usageRef, legacyView, v2View)
		}

		// 账号成本：结算只在 API Key 有分组时才算，并且按 *GroupID 取分组；两个分组 id 不一致时
		// （稳定优先兜底的影子 Key 才会）不比，因为快照固定的是计价分组那一份。
		if sb.usageLog == nil || sb.account == nil || sb.apiKey.GroupID == nil || *sb.apiKey.GroupID != sb.apiKey.Group.ID {
			return
		}
		// applyAccountStatsCost 会改写传入的 usageLog，所以传副本，结算的 usageLog 一个字节都不动（S-2）。
		shadowLog := *sb.usageLog
		applyAccountStatsCost(shadowCtx, &shadowLog, s.groupPolicy(), s.billingService,
			sb.account.ID, *sb.apiKey.GroupID, sb.result.UpstreamModel, sb.result.Model,
			// 与 recordUsageCore 里结算那次调用的 UsageTokens 保持一致。
			UsageTokens{
				InputTokens:         sb.result.Usage.InputTokens,
				OutputTokens:        sb.result.Usage.OutputTokens,
				CacheCreationTokens: sb.result.Usage.CacheCreationInputTokens,
				CacheReadTokens:     sb.result.Usage.CacheReadInputTokens,
				ImageOutputTokens:   sb.result.Usage.ImageOutputTokens,
			},
			sb.cost.TotalCost, sb.pricingAt,
		)
		legacyAcct := newShadowAccountCostView(sb.usageLog.AccountStatsCost)
		v2Acct := newShadowAccountCostView(shadowLog.AccountStatsCost)
		if !legacyAcct.equal(v2Acct) {
			sess.diff(ShadowKindAccountCost, ShadowClassTranslation, sb.billingModel, usageRef, legacyAcct, v2Acct)
		}
	})
}
