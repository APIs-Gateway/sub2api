package service

import "strings"

// QuoteOfficialReference 是模型的官方参考价：只看官方价这一层（动态价格目录、内置兜底价、DeepSeek 价卡），
// 不叠加渠道定价、分组倍率与额外倍率。管理端「模型」页用它回答「这个模型官方价是多少、价格从哪来」。
type QuoteOfficialReference struct {
	Model  string      `json:"model"`
	Priced bool        `json:"priced"`
	Source QuoteSource `json:"source"`
	// PerMTok 是标准档、参考上下文下的单价（USD / 百万 token）；没有价格时为 nil。
	PerMTok *QuoteUnitPrices `json:"per_mtok,omitempty"`
}

// OfficialReference 返回模型的官方参考价。取价与计费同一个入口（BillingService.GetModelPricing），
// 取不到价格（计费会得到 ErrModelPricingUnavailable）时 Priced 为 false、Source 为 none。只读，不改任何状态。
func (q *PriceQuoter) OfficialReference(model string) QuoteOfficialReference {
	model = strings.TrimSpace(model)
	ref := QuoteOfficialReference{Model: model, Source: QuoteSourceNone}
	if q == nil || q.billing == nil || model == "" {
		return ref
	}
	pricing, err := q.billing.GetModelPricing(model)
	if err != nil || pricing == nil {
		return ref
	}
	set := q.tokenPriceSet(pricing, "", true, 1)
	ref.Priced = true
	ref.Source = q.billing.quoteCatalogLayer(model)
	ref.PerMTok = &set.PerMTok
	return ref
}
