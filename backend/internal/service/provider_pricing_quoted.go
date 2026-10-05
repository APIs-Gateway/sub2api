package service

import (
	"context"
	"math"
	"time"
)

// BuildHvoyProviderPricingQuoted 与 BuildHvoyProviderPricing 输出同一份报价单，区别是每个模型的价格改由
// PriceQuoter 给出（W6 PR8a，设计 3.4、Q7）：分组 codex plus 里的渠道覆盖、官方价卡策略、硬编码兜底价和（v2 阶段的）
// 单元格覆盖与额外倍率，都和计费同源。展示价 = 报价器的最终单价（已含倍率）÷ 支付倍率，换算函数与旧口径同一个。
//
// 以下情形保留旧口径，输出与接管前逐位相同：
//   - quoter 为 nil；
//   - 没有名为 codex plus 的活跃分组，或该分组倍率无效（旧口径按 1 倍出价并带说明，报价器无从取价）；
//   - 报价本身出错（读分组失败等）：保留旧口径的价格，不因为一次读取失败把已发布的价格撤掉。
//
// 清单仍是 hvoyProviderPricingModels 里硬编码的 7 个模型（Q7(b)）。
func (s *PricingService) BuildHvoyProviderPricingQuoted(ctx context.Context, quoter *PriceQuoter, lister HvoyProviderGroupLister, paymentMultiplier float64, siteName, frontendURL string, now time.Time) (HvoyProviderPricingResponse, error) {
	groupsByName := make(map[string]Group)
	multipliers := map[string]float64{}
	if lister != nil {
		groups, err := lister.ListActive(ctx)
		if err != nil {
			return HvoyProviderPricingResponse{}, err
		}
		for _, group := range groups {
			groupsByName[normalizeHvoyGroupName(group.Name)] = group
		}
		for _, model := range hvoyProviderPricingModels {
			if group, ok := groupsByName[normalizeHvoyGroupName(model.groupName)]; ok {
				multipliers[model.groupName] = group.RateMultiplier
			}
		}
	}

	resp := s.BuildHvoyProviderPricing(paymentMultiplier, multipliers, siteName, frontendURL, now)
	if quoter == nil {
		return resp, nil
	}

	payment := normalizeBalanceRechargeMultiplier(paymentMultiplier)
	for i, model := range hvoyProviderPricingModels {
		group, ok := groupsByName[normalizeHvoyGroupName(model.groupName)]
		if !ok || math.IsNaN(group.RateMultiplier) || math.IsInf(group.RateMultiplier, 0) || group.RateMultiplier <= 0 {
			continue
		}
		quoted, ok := quoteHvoyModel(ctx, quoter, model, group.ID, payment)
		if !ok {
			continue
		}
		resp.Data.Models[i] = quoted
	}
	return resp, nil
}

// quoteHvoyModel 用报价器给一个模型出价。第二个返回值为 false 表示报价出错，调用方保留旧口径。
func quoteHvoyModel(ctx context.Context, quoter *PriceQuoter, model hvoyProviderPricingModelRef, groupID int64, payment float64) (HvoyProviderPricingModel, bool) {
	quote, err := quoter.Quote(ctx, QuoteRequest{Model: model.modelName, GroupID: groupID})
	if err != nil || quote == nil {
		return HvoyProviderPricingModel{}, false
	}
	out := HvoyProviderPricingModel{ModelName: model.modelName, GroupName: model.groupName}
	if !quote.Priced || quote.FinalPrices == nil {
		out.Note = "pricing unavailable"
		return out, true
	}
	if !quote.Access.OK {
		out.Note = "model not available in group"
		return out, true
	}
	perToken := quote.FinalPrices.PerToken
	out.InputPrice = usdPerTokenToCNYPerMTok(perToken.Input, payment)
	out.OutputPrice = optionalUSDPerTokenToCNYPerMTok(perToken.Output, payment)
	out.CacheInputPrice = optionalUSDPerTokenToCNYPerMTok(perToken.CacheRead, payment)
	out.CacheCreatePrice = optionalUSDPerTokenToCNYPerMTok(perToken.CacheWrite, payment)
	out.CacheCreatePrice1H = optionalUSDPerTokenToCNYPerMTok(perToken.CacheWrite1h, payment)
	out.Enabled = true
	return out, true
}
