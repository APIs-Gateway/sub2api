package service

import (
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// SubscriptionPricingBounds 暴露给前端的自定义购买区间（滑块/输入框范围）。
// 当前为 DefaultSubscriptionPricingConfig 的默认值；后续若支持管理员配置，仅需在此换源。
type SubscriptionPricingBounds struct {
	DMin float64 `json:"d_min"` // 每日额度下限
	DMax float64 `json:"d_max"` // 每日额度上限
	UMin float64 `json:"u_min"` // 最便宜单价（最大档）
	UMax float64 `json:"u_max"` // 最贵单价（最小档）
	TMin  int `json:"t_min"`  // 最短有效天数
	TMax  int `json:"t_max"`  // 最长有效天数
	TStep int `json:"t_step"` // 天数步长：T 必须为该值整数倍（默认 30，按整月购买）
}

// SubscriptionQuoteResult 自定义购买报价（实时预览与下单冻结同源）：D/T/u/售价 + 派生周/月封顶。
type SubscriptionQuoteResult struct {
	DailyAmountUSD float64 `json:"daily_amount_usd"` // D
	ValidityDays   int     `json:"validity_days"`    // T
	UnitPrice      float64 `json:"unit_price"`       // u(D)
	Price          float64 `json:"price"`            // P = D×T×u(D)
	WeeklyCapUSD   float64 `json:"weekly_cap_usd"`   // W = 7×D（派生封顶）
	MonthlyCapUSD  float64 `json:"monthly_cap_usd"`  // M = min(T,30)×D（派生封顶）
	FormulaVersion int     `json:"formula_version"`  // 定价公式版本（下单时冻结进订单快照）
}

// PricingBounds 返回自定义购买区间（当前为默认配置）。
func (s *SubscriptionService) PricingBounds() SubscriptionPricingBounds {
	c := DefaultSubscriptionPricingConfig()
	return SubscriptionPricingBounds{
		DMin: c.DMin, DMax: c.DMax,
		UMin: c.UMin, UMax: c.UMax,
		TMin: c.TMin, TMax: c.TMax, TStep: c.TStep,
	}
}

// QuoteSubscription 校验自定义 D/T 并产出报价（含派生周/月封顶）；金额完全由后端公式决定、不信前端。
// 校验失败（D/T 超范围）返回带码 INVALID_SUBSCRIPTION_PARAMS 的 BadRequest。
func (s *SubscriptionService) QuoteSubscription(d float64, t int) (*SubscriptionQuoteResult, error) {
	cfg := DefaultSubscriptionPricingConfig()
	q, err := cfg.Quote(d, t)
	if err != nil {
		return nil, infraerrors.BadRequest("INVALID_SUBSCRIPTION_PARAMS", err.Error())
	}
	weekly, monthly := DeriveWindowCaps(d, t)
	return &SubscriptionQuoteResult{
		DailyAmountUSD: q.DailyAmountUSD,
		ValidityDays:   q.ValidityDays,
		UnitPrice:      q.UnitPrice,
		Price:          q.Price,
		WeeklyCapUSD:   weekly,
		MonthlyCapUSD:  monthly,
		FormulaVersion: q.FormulaVersion,
	}, nil
}
