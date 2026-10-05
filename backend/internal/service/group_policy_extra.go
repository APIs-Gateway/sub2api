package service

import (
	"context"
	"math"
	"time"
)

// 额外倍率在成本函数内部、按「实际出价的那个模型」取值并乘入（设计 3.2、R2-BK-2）。
//
// 不在网关外层算出 multiplier / imageMultiplier 的地方乘：那时 OpenAI 网关的计费模型还没有定，
// 候选回退会拿 A 模型的额外倍率去乘 B 模型的价。所以：
//   - OpenAI 网关：token 路径在候选循环里每个候选各取一次；图片路径在 calculateOpenAIImageCost 的入口，
//     按它实际收到的模型取（带额外倍率的图片请求走的是 calculateOpenAIImageCost(首个候选)，
//     不是 firstOpenAIImageBillingModel 选出的模型，CHECK_OPUS_3 2.2）；
//   - Anthropic 网关：在 calculateRecordUsageCost 入口，按 billableModelWithFallback 选定的计费模型取一次。
//
// legacyPolicy 的 ExtraMultiplier 恒为 1，extra 为 1 时一律不乘，所以 legacy 分组的计费结果逐位不变。

// groupExtraMultiplier 取分组对 model 的额外倍率。没有策略、没有分组，或者取到的值不是有限的正数时返回 1。
// 分组取 apiKey.Group.ID：稳定优先兜底时影子 key 的分组就是实际服务的分组，价格与倍率都按它算。
func groupExtraMultiplier(ctx context.Context, policy GroupPolicy, apiKey *APIKey, model string, at time.Time) float64 {
	if apiKey == nil || apiKey.Group == nil {
		return 1
	}
	return extraMultiplierFor(ctx, policy, apiKey.Group.ID, model, at)
}

// extraMultiplierFor 是 groupExtraMultiplier 去掉 API Key 的形态：PriceQuoter 手上只有计价分组，没有 key。
// 校验规则与网关侧完全相同（同一个函数），所以报价与计费对「坏值按 1」的处理不会出现分歧。
func extraMultiplierFor(ctx context.Context, policy GroupPolicy, groupID int64, model string, at time.Time) float64 {
	if policy == nil {
		return 1
	}
	extra := policy.ExtraMultiplier(ctx, groupID, model, at)
	if math.IsNaN(extra) || extra <= 0 || math.IsInf(extra, 0) {
		return 1
	}
	return extra
}

// withExtraMultiplier 记下成本里实际乘上的额外倍率，供 RecordUsage 写进 usage_logs.rate_multiplier
// （写进去的倍率 = 原倍率 x 额外倍率，和 actual_cost = total_cost x rate_multiplier 的关系保持一致）。
// extra 为 1 时原样返回；否则返回带标记的副本，不改动成本函数返回的对象。
func withExtraMultiplier(cost *CostBreakdown, extra float64) *CostBreakdown {
	if cost == nil || extra == 1 {
		return cost
	}
	tagged := *cost
	tagged.extraMultiplier = extra
	return &tagged
}

// rateWithExtra 把成本里记下的额外倍率乘到要写进用量日志的倍率上；没有记录时原样返回。
func rateWithExtra(rate float64, cost *CostBreakdown) float64 {
	if cost == nil || cost.extraMultiplier == 0 || cost.extraMultiplier == 1 {
		return rate
	}
	return rate * cost.extraMultiplier
}
