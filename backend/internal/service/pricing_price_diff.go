package service

import (
	"fmt"
	"math"
)

// W6 PR4b-2a：PriceDiff（设计 3.3）——比较同一个（分组、模型）改动前后两份报价，给出价格方向。
// 直接返回 PriceDelta（PR4b-1 定义的类型），不另造一套枚举。
//
// 本函数只比较方向，不输出金额，所以与记账单位无关（人民币 R2/R3 的单位换算不影响它）。
// 报价由 PR4b-2b 的估算器（QuoteWith）产出；这里只依赖 Quote 里已有的字段，不做 I/O。

// priceDiffRelTol 相对容差：小于它的差别视为相等（浮点乘法的舍入噪声）。
const priceDiffRelTol = 1e-9

// PriceDiff 比较改动前后的报价。规则：
//   - 任一份为 nil：unknown；
//   - 两份都没有价（Priced=false）：none；只有一份没有价，或计费模式、价格点集合不同：unknown；
//   - 所有价格点都相等：none；
//   - 有涨无跌：up；有跌无涨：down；有涨有跌：unknown（最严，管理员必须交互式确认）。
//
// 价格点是用户实际按的单价：token 单价（FinalPrices，没有则 Prices 乘 EffectiveMultiplier）、
// 区间单价、按次与图片的单价（分别乘 EffectiveMultiplier、ImageMultiplier）、长上下文倍率与 service tier 倍率。
func PriceDiff(before, after *Quote) PriceDelta {
	if before == nil || after == nil {
		return PriceDeltaUnknown
	}
	if !before.Priced && !after.Priced {
		return PriceDeltaNone
	}
	if before.Priced != after.Priced || before.BillingMode != after.BillingMode {
		return PriceDeltaUnknown
	}
	a, b := quotePricePoints(before), quotePricePoints(after)
	if len(a) != len(b) {
		return PriceDeltaUnknown
	}
	var up, down bool
	for k, av := range a {
		bv, ok := b[k]
		if !ok {
			return PriceDeltaUnknown
		}
		switch {
		case priceDiffEqual(av, bv):
		case bv > av:
			up = true
		default:
			down = true
		}
	}
	switch {
	case up && down:
		return PriceDeltaUnknown
	case up:
		return PriceDeltaUp
	case down:
		return PriceDeltaDown
	}
	return PriceDeltaNone
}

func priceDiffEqual(a, b float64) bool {
	if a == b {
		return true
	}
	return math.Abs(a-b) <= priceDiffRelTol*math.Max(math.Abs(a), math.Abs(b))
}

// quotePricePoints 把报价摊平成「名字到单价」的集合。
func quotePricePoints(q *Quote) map[string]float64 {
	pts := make(map[string]float64)
	eff := q.EffectiveMultiplier
	if eff <= 0 {
		eff = 1
	}
	img := q.ImageMultiplier
	if img <= 0 {
		img = 1
	}
	addUnit := func(prefix string, u QuoteUnitPrices, mult float64) {
		pts[prefix+"input"] = u.Input * mult
		pts[prefix+"output"] = u.Output * mult
		pts[prefix+"cache_write"] = u.CacheWrite * mult
		pts[prefix+"cache_read"] = u.CacheRead * mult
		pts[prefix+"cache_write_5m"] = u.CacheWrite5m * mult
		pts[prefix+"cache_write_1h"] = u.CacheWrite1h * mult
		pts[prefix+"image_input"] = u.ImageInput * mult
		pts[prefix+"image_output"] = u.ImageOutput * mult
	}
	switch {
	case q.FinalPrices != nil:
		addUnit("tok.", q.FinalPrices.PerToken, 1)
	case q.Prices != nil:
		addUnit("tok.", q.Prices.PerToken, eff)
	}
	for i, iv := range q.Intervals {
		addUnit(fmt.Sprintf("iv%d.", i), iv.Prices.PerToken, eff)
	}
	if lc := q.LongContext; lc != nil {
		pts["lc.input_mult"] = lc.InputMultiplier
		pts["lc.output_mult"] = lc.OutputMultiplier
		if lc.ExplicitPrices != nil {
			addUnit("lc.explicit.", lc.ExplicitPrices.PerToken, eff)
		}
	}
	if st := q.ServiceTier; st != nil {
		pts["tier.mult"] = st.Multiplier
	}
	if pr := q.PerRequest; pr != nil {
		pts["req.default"] = pr.DefaultPrice * eff
		for i, t := range pr.Tiers {
			pts[fmt.Sprintf("req.tier%d", i)] = t.Price * eff
		}
	}
	if ir := q.ImageRequest; ir != nil {
		for _, t := range ir.Tiers {
			pts["img."+t.Tier] = t.Price * img
		}
	}
	return pts
}
