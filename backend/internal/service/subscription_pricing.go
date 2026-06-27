package service

import (
	"fmt"
	"math"

	"github.com/shopspring/decimal"
)

// 订阅「量大优惠」线性定价（per-day redesign 第 3 节）。
//
// 每刀额度单价 u 随每日额度 D 线性递减——D 越大单价越低、越划算：
//
//	u(D) = u_max + (u_min − u_max) × (D − D_min) / (D_max − D_min)，clamp 到 [u_min, u_max]
//	套餐售价 P = D × T × u(D)
//
// 注意：u 只用于「售价 ↔ 额度」换算。套餐余额消费 1 官方刀扣 1 刀、不乘 u；
// u 仅在下单算价/冻结快照时使用。

// SubscriptionFormulaVersion 标识当前定价公式版本，下单时冻结进订单快照，
// 供日后审计/排查「这单是按哪版公式成交的」。改公式形态时 +1。
const SubscriptionFormulaVersion = 1

// SubscriptionPricingConfig 定价端点与自定义范围（规格第 3/10 节，均「可调」）。
// 当前用默认值；后续可由管理员配置覆盖（下单时仍按订单冻结快照发卡，改配置不影响已下单）。
type SubscriptionPricingConfig struct {
	DMin float64 // 最小档每日额度（官方刀/天）
	DMax float64 // 最大档每日额度
	UMax float64 // 最小档单价（最贵）
	UMin float64 // 最大档单价（最便宜）
	TMin int     // 自定义最短天数（默认 30 天起买，挤掉短期大 D 套利空间）
	TMax int     // 自定义最长天数
}

// DefaultSubscriptionPricingConfig 规格默认值：D∈[1,50]、u∈[1.0,2.0]、T∈[30,90]。
func DefaultSubscriptionPricingConfig() SubscriptionPricingConfig {
	return SubscriptionPricingConfig{
		DMin: 1,
		DMax: 50,
		UMax: 2.0,
		UMin: 1.0,
		TMin: 30,
		TMax: 90,
	}
}

// UnitPrice 计算每刀额度单价 u(D)：随 D 线性递减，clamp 到 [UMin, UMax]。
// 退化场景 DMax==DMin（无梯度）按最贵档 UMax。
func (c SubscriptionPricingConfig) UnitPrice(d float64) float64 {
	span := c.DMax - c.DMin
	var u float64
	if span <= 0 {
		u = c.UMax
	} else {
		u = c.UMax + (c.UMin-c.UMax)*(d-c.DMin)/span
	}
	// clamp 到 [min,max]，对 UMin<UMax（默认）与异常配置都容错。
	lo, hi := math.Min(c.UMin, c.UMax), math.Max(c.UMin, c.UMax)
	return math.Min(math.Max(u, lo), hi)
}

// Price 计算套餐售价 P = D × T × u(D)，按站点本币 2 位小数四舍五入。
func (c SubscriptionPricingConfig) Price(d float64, t int) float64 {
	u := c.UnitPrice(d)
	return decimal.NewFromFloat(d).
		Mul(decimal.NewFromInt(int64(t))).
		Mul(decimal.NewFromFloat(u)).
		Round(2).
		InexactFloat64()
}

// ValidateCustom 校验自定义 D、T 是否在允许范围（防滥用 + 防前端篡改价格）。
func (c SubscriptionPricingConfig) ValidateCustom(d float64, t int) error {
	if math.IsNaN(d) || math.IsInf(d, 0) || d < c.DMin || d > c.DMax {
		return fmt.Errorf("每日额度 D=%v 超出允许范围 [%v, %v]", d, c.DMin, c.DMax)
	}
	if t < c.TMin || t > c.TMax {
		return fmt.Errorf("有效天数 T=%d 超出允许范围 [%d, %d]", t, c.TMin, c.TMax)
	}
	return nil
}

// SubscriptionPriceQuote 下单时冻结进订单快照的定价快照（D/T/u/price/formula_version）。
// 支付回调严格按本快照发卡，绝不按回调时的当前公式/配置重算（防下单后管理员改价导致不一致）。
// currency 在订单创建层补齐（来自支付实例/站点本币），不在定价层。
type SubscriptionPriceQuote struct {
	DailyAmountUSD float64 `json:"daily_amount_usd"` // D
	ValidityDays   int     `json:"validity_days"`    // T
	UnitPrice      float64 `json:"unit_price"`       // u(D)
	Price          float64 `json:"price"`            // P = D×T×u(D)
	FormulaVersion int     `json:"formula_version"`  // 定价公式版本
}

// Quote 校验并产出定价快照：自定义套餐下单走它，金额完全由后端公式决定、不信前端。
func (c SubscriptionPricingConfig) Quote(d float64, t int) (SubscriptionPriceQuote, error) {
	if err := c.ValidateCustom(d, t); err != nil {
		return SubscriptionPriceQuote{}, err
	}
	return SubscriptionPriceQuote{
		DailyAmountUSD: d,
		ValidityDays:   t,
		UnitPrice:      c.UnitPrice(d),
		Price:          c.Price(d, t),
		FormulaVersion: SubscriptionFormulaVersion,
	}, nil
}

// DeriveWindowCaps 由每日额度 D 与有效期 T 派生「周封顶 W / 月封顶 M」（规格第 2 节默认）：
//
//	W = 7 × D       （一周 7 天的日额度之和，作为周窗口上限）
//	M = min(T,30) × D（不足 30 天的卡按实际天数封顶，避免月封顶超过整期可用额度）
//
// W/M 是「连透支也不能突破」的硬上限，开卡时写进卡的 weekly_limit_usd / monthly_limit_usd。
// 用 decimal 乘，避免 7×1.1 这类浮点尾差。
func DeriveWindowCaps(d float64, t int) (weekly, monthly float64) {
	days := t
	if days > 30 {
		days = 30
	}
	weekly = decimal.NewFromFloat(d).Mul(decimal.NewFromInt(7)).InexactFloat64()
	monthly = decimal.NewFromFloat(d).Mul(decimal.NewFromInt(int64(days))).InexactFloat64()
	return weekly, monthly
}
