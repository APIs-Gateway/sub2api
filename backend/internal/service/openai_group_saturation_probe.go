package service

import "context"

type openAIGroupSaturationProbeKey struct{}

// WithOpenAIGroupSaturationProbe 标记「本次选号发生在有回退链且非最后一跳」。
// 调度器据此在返回 WaitPlan 之前补试 top-K 之外的空闲候选，并只在全部失败时才把
// WaitPlan.GroupSaturated 置 true（设计复核 N1a）。无链请求和末跳不要调用它。
func WithOpenAIGroupSaturationProbe(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAIGroupSaturationProbeKey{}, true)
}

func openAIGroupSaturationProbeEnabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(openAIGroupSaturationProbeKey{}).(bool)
	return enabled
}

// HitPreviousResponse 报告本次选号是否命中了 previous_response_id 层（账号与上游响应绑定）。
// 规则 3 以选号结果为准，而不是请求里有没有带 previous_response_id：绑定没命中时请求走的是
// 普通负载选择，应按普通规则处理。
func (d OpenAIAccountScheduleDecision) HitPreviousResponse() bool {
	return d.StickyPreviousHit || d.Layer == openAIAccountScheduleLayerPreviousResponse
}
