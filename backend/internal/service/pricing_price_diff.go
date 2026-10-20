package service

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// W6 PR4b-2a / 2b：PriceDiff（设计 3.3）——比较同一个（分组、模型）改动前后两份报价，给出价格方向。
// 直接返回 PriceDelta（PR4b-1 定义的类型），不另造一套枚举。
//
// 本函数只比较方向，不输出金额，所以与记账单位无关（人民币 R2/R3 的单位换算不影响它）。
// 报价由估算器（pricing_estimator.go 的 QuoteWith 与 BatchQuoteWith）产出；这里只依赖 Quote 里已有的字段，不做 I/O。
//
// 为什么要严：authorize 对 Delta == none 的涉价写入不要求交互式会话，所以把有变化的写入误报成 none，
// 就等于机器令牌可以改价。任何「拿不准」的差别一律报 unknown。

// priceDiffRelTol 相对容差：小于它的差别视为相等（浮点乘法的舍入噪声）。
const priceDiffRelTol = 1e-9

// PriceDiff 比较改动前后的报价（调用方对每一档 service tier、DeepSeek 的峰时与非峰时各报一次、各比一次）。规则：
//   - 任一份为 nil：unknown；
//   - 「形状」不同：unknown。形状是决定价格怎么算的那些因素：是否有价、价格是否来自渠道价（单元格的 custom）、
//     计费模式、计费路径（unified 或 legacy）、
//     Gemini 原生入口的长上下文加价、service tier 的计价方式、价卡自带的长上下文规则、价卡策略标记
//     （DeepSeek 官方价卡与峰时、GPT-5.x 的长上下文策略）。inherit 改成数值相同的 custom 会让其中几项变化，
//     不能因为单价碰巧相等就说价格没变；
//   - 价格点集合不同（区间或按次档位的边界、标签变了）：unknown；
//   - 所有价格点都相等：none；有涨无跌：up；有跌无涨：down；有涨有跌：unknown（最严，管理员必须交互式确认）。
//
// 价格点是用户实际按的单价：token 单价（FinalPrices，没有则 Prices 乘 EffectiveMultiplier）、
// 区间单价、按次与图片的单价（分别乘 EffectiveMultiplier、ImageMultiplier）、长上下文倍率与 service tier 倍率。
// 区间与按次档位的键里带着边界（和标签），所以只挪边界、只改标签也会被发现。
func PriceDiff(before, after *Quote) PriceDelta {
	if before == nil || after == nil {
		return PriceDeltaUnknown
	}
	if quoteShape(before) != quoteShape(after) {
		return PriceDeltaUnknown
	}
	// 没有价的报价也要比较价格点：没有渠道价时图片请求按张计费（CalculateImageCost，乘 ImageMultiplier），
	// 这条路径不要求模型有 token 价，所以「两边都没有价」不等于「价格没变」。
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

// CombinePriceDeltas 把多个价格方向合成一个：任一 unknown 或有涨有跌是 unknown，只涨是 up，只跌是 down，其余是 none。
// 没有任何输入是 none。估算器把各档 service tier、各个时刻、各个单元格的比较结果合在一起时用它。
func CombinePriceDeltas(deltas ...PriceDelta) PriceDelta {
	var up, down bool
	for _, d := range deltas {
		switch d {
		case PriceDeltaNone:
		case PriceDeltaUp:
			up = true
		case PriceDeltaDown:
			down = true
		default:
			return PriceDeltaUnknown
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

// quoteShape 把决定价格怎么算的因素摊成一个串：两份报价的形状不同，就不比较单价。
func quoteShape(q *Quote) string {
	var b strings.Builder
	fmt.Fprintf(&b, "priced=%t;channel=%t;mode=%s;path=%s;", q.Priced, q.Source == QuoteSourceChannel, q.BillingMode, q.PricingPath)
	if g := q.GatewayLongContext; g != nil {
		fmt.Fprintf(&b, "glc=%d/%g;", g.ThresholdTokens, g.ExtraMultiplier)
	} else {
		_, _ = b.WriteString("glc=-;")
	}
	if st := q.ServiceTier; st != nil {
		fmt.Fprintf(&b, "tier=%s;", st.Mode)
	} else {
		_, _ = b.WriteString("tier=-;")
	}
	if lc := q.LongContext; lc != nil {
		fmt.Fprintf(&b, "lc=%d/%t/%t;", lc.ThresholdTokens, lc.PriorityExcludesLongContext, lc.ExplicitPrices != nil)
	} else {
		_, _ = b.WriteString("lc=-;")
	}
	p := q.Policy
	fmt.Fprintf(&b, "policy=%t/%t/%t/%g/%s", p.DeepSeekOfficialCard, p.DeepSeekProBilledAsFlash, p.DeepSeekPeak,
		p.DeepSeekPeakMultiplier, p.LongContextPolicy)
	return b.String()
}

// quotePricePoints 把报价摊平成「名字到单价」的集合。区间与按次档位的名字里带边界（和标签）；
// 万一名字重复（两个档位的边界、标签完全相同），后来的加序号，保证每个档位都占一个点。
func quotePricePoints(q *Quote) map[string]float64 {
	pts := make(map[string]float64)
	put := func(key string, v float64) {
		if _, dup := pts[key]; dup {
			for i := 2; ; i++ {
				k := key + "#" + strconv.Itoa(i)
				if _, taken := pts[k]; !taken {
					key = k
					break
				}
			}
		}
		pts[key] = v
	}
	eff := q.EffectiveMultiplier
	if eff <= 0 {
		eff = 1
	}
	img := q.ImageMultiplier
	if img <= 0 {
		img = 1
	}
	// 没有渠道价时的图片请求按张计费，单价乘 ImageMultiplier（含额外倍率）；Quote 只在目录把模型标成图片模型时才有
	// ImageRequest，其余模型这条路径不进任何别的价格点，所以倍率本身单独作为一个点。
	put("img.mult", img)
	addUnit := func(prefix string, u QuoteUnitPrices, mult float64) {
		put(prefix+"input", u.Input*mult)
		put(prefix+"output", u.Output*mult)
		put(prefix+"cache_write", u.CacheWrite*mult)
		put(prefix+"cache_read", u.CacheRead*mult)
		put(prefix+"cache_write_5m", u.CacheWrite5m*mult)
		put(prefix+"cache_write_1h", u.CacheWrite1h*mult)
		put(prefix+"image_input", u.ImageInput*mult)
		put(prefix+"image_output", u.ImageOutput*mult)
	}
	switch {
	case q.FinalPrices != nil:
		addUnit("tok.", q.FinalPrices.PerToken, 1)
	case q.Prices != nil:
		addUnit("tok.", q.Prices.PerToken, eff)
	}
	for _, iv := range q.Intervals {
		addUnit("iv"+tierBounds(iv.MinTokens, iv.MaxTokens)+".", iv.Prices.PerToken, eff)
	}
	if lc := q.LongContext; lc != nil {
		put("lc.input_mult", lc.InputMultiplier)
		put("lc.output_mult", lc.OutputMultiplier)
		if lc.ExplicitPrices != nil {
			addUnit("lc.explicit.", lc.ExplicitPrices.PerToken, eff)
		}
	}
	if st := q.ServiceTier; st != nil {
		put("tier.mult", st.Multiplier)
	}
	if pr := q.PerRequest; pr != nil {
		put("req.default", pr.DefaultPrice*eff)
		for _, t := range pr.Tiers {
			put("req.tier["+t.TierLabel+"]"+tierBounds(t.MinTokens, t.MaxTokens), t.Price*eff)
		}
	}
	if ir := q.ImageRequest; ir != nil {
		for _, t := range ir.Tiers {
			put("img."+t.Tier, t.Price*img)
		}
	}
	return pts
}

// tierBounds 区间或档位的边界，形如 [0,200000] 与 [200000,inf]。
func tierBounds(minTokens int, maxTokens *int) string {
	hi := "inf"
	if maxTokens != nil {
		hi = strconv.Itoa(*maxTokens)
	}
	return "[" + strconv.Itoa(minTokens) + "," + hi + "]"
}
