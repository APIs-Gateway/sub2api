package service

import (
	"context"
	"math"
)

// W6 PR5：成本与账号成本的影子比对（设计 4.4、BK-3）。
//
// 比对点在两个网关各一处（gateway_pricing_shadow.go、openai_pricing_shadow.go），都是：
//
//	if sess := beginPricingShadow(ctx, policy, groupID); sess != nil { ...在 sess.run 里重算并比较... }
//
// 重算用 ctx 带 forceStage=v2 与影子标记，调用的是与结算相同的那个成本函数，所以网关分派
// （候选循环、图片选路、成本函数的选择）两边完全相同，差异只可能来自「渠道到矩阵的翻译」。
// 重算没有副作用：不写用量行、不计 PR1 的无价指标、不打 falling back 日志（影子标记）。

// pricingShadowSession 一次成本比对。
type pricingShadowSession struct {
	hub     *pricingShadowHub
	groupID int64
	// ctx 带影子标记、forceStage=v2，并且固定了「准入判断时看到的那份快照」。
	ctx context.Context
}

// beginPricingShadow 判断这次结算要不要做成本比对，要的话返回会话，不要返回 nil。
// 只有 stagedPolicy 且分组处于 shadow 阶段才比对；快照是兜底、沿用的旧数据、刚失效过、或超过限速时跳过并计数。
// 阶段与快照取自 ctx 里的固定器（调用方已经在计算入口固定过），所以与参与这次结算的快照是同一份。
func beginPricingShadow(ctx context.Context, policy GroupPolicy, groupID int64) *pricingShadowSession {
	sp, ok := policy.(*stagedPolicy)
	if !ok || sp.matrix == nil || groupID <= 0 {
		return nil
	}
	if isShadowRecompute(ctx) {
		// 影子重算里不再嵌套影子重算。
		return nil
	}
	snap := sp.matrix.cachedSnapshot(ctx, groupID)
	if snap == nil || snap.stage != PricingStageShadow {
		return nil
	}
	if !sp.shadowReady(snap, &sp.hub.costGate) {
		return nil
	}
	return &pricingShadowSession{
		hub:     sp.hub,
		groupID: groupID,
		ctx:     withPinnedSnapshot(withShadowRecompute(ctx, PricingStageV2), sp.matrix, groupID, snap),
	}
}

// run 在影子 ctx 上执行重算与比较。fn 里的 panic 被吞掉并计数，永不影响结算。fn 正常返回才计一次比对。
func (ss *pricingShadowSession) run(fn func(shadowCtx context.Context)) {
	ss.hub.guard("cost", func() {
		fn(ss.ctx)
		ss.hub.noteCompared(ss.groupID)
	})
}

// diff 登记一条差异。
func (ss *pricingShadowSession) diff(kind, class, model, usageRef string, legacyView, v2View any) {
	ss.hub.noteDiff(ss.groupID, kind, class, model, usageRef, legacyView, v2View)
}

// shadowCostView 成本比对的视图：既是判等的依据，也是写进样本的内容（只含金额与模式，不含任何用户信息）。
type shadowCostView struct {
	Model             string  `json:"model,omitempty"`
	Error             string  `json:"error,omitempty"`
	InputCost         float64 `json:"input_cost"`
	ImageInputCost    float64 `json:"image_input_cost"`
	OutputCost        float64 `json:"output_cost"`
	ImageOutputCost   float64 `json:"image_output_cost"`
	CacheCreationCost float64 `json:"cache_creation_cost"`
	CacheReadCost     float64 `json:"cache_read_cost"`
	TotalCost         float64 `json:"total_cost"`
	ActualCost        float64 `json:"actual_cost"`
	BillingMode       string  `json:"billing_mode"`
	ExtraMultiplier   float64 `json:"extra_multiplier"`
}

// newShadowCostView 由成本结果与错误构造视图。err 只区分「无价」与「其他错误」两类，不带错误文本
// （文本里可能有上游或内部细节，样本只给管理员看，但没有必要存）。
func newShadowCostView(model string, cost *CostBreakdown, err error) shadowCostView {
	v := shadowCostView{Model: model}
	if err != nil {
		if isUsagePricingUnavailableError(err) {
			v.Error = "pricing_unavailable"
		} else {
			v.Error = "calc_error"
		}
		return v
	}
	if cost == nil {
		v.Error = "nil_cost"
		return v
	}
	v.InputCost, v.ImageInputCost, v.OutputCost, v.ImageOutputCost = cost.InputCost, cost.ImageInputCost, cost.OutputCost, cost.ImageOutputCost
	v.CacheCreationCost, v.CacheReadCost = cost.CacheCreationCost, cost.CacheReadCost
	v.TotalCost, v.ActualCost = cost.TotalCost, cost.ActualCost
	v.BillingMode = cost.BillingMode
	v.ExtraMultiplier = cost.extraMultiplier
	return v
}

// equal 逐位比较（float64 位模式相同），设计 4.4：同一套代码、同一输入，期望逐位相等。
func (v shadowCostView) equal(o shadowCostView) bool {
	return v.Model == o.Model && v.Error == o.Error && v.BillingMode == o.BillingMode &&
		sameBits(v.InputCost, o.InputCost) && sameBits(v.ImageInputCost, o.ImageInputCost) &&
		sameBits(v.OutputCost, o.OutputCost) && sameBits(v.ImageOutputCost, o.ImageOutputCost) &&
		sameBits(v.CacheCreationCost, o.CacheCreationCost) && sameBits(v.CacheReadCost, o.CacheReadCost) &&
		sameBits(v.TotalCost, o.TotalCost) && sameBits(v.ActualCost, o.ActualCost) &&
		sameBits(v.ExtraMultiplier, o.ExtraMultiplier)
}

func sameBits(a, b float64) bool { return math.Float64bits(a) == math.Float64bits(b) }

// shadowAccountCostView 账号成本的视图：nil 表示用默认公式（total_cost x account_rate_multiplier）。
type shadowAccountCostView struct {
	Set  bool    `json:"set"`
	Cost float64 `json:"cost"`
}

func newShadowAccountCostView(cost *float64) shadowAccountCostView {
	if cost == nil {
		return shadowAccountCostView{}
	}
	return shadowAccountCostView{Set: true, Cost: *cost}
}

func (v shadowAccountCostView) equal(o shadowAccountCostView) bool {
	return v.Set == o.Set && sameBits(v.Cost, o.Cost)
}
