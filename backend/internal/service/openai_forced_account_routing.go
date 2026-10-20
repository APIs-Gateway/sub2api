package service

import "context"

type openAIForcedAccountRoutingContextKey struct{}

func WithOpenAIForcedAccountRouting(ctx context.Context, accountID int64) context.Context {
	if ctx == nil || accountID <= 0 {
		return ctx
	}
	return context.WithValue(ctx, openAIForcedAccountRoutingContextKey{}, accountID)
}

func openAIForcedAccountRoutingID(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	accountID, _ := ctx.Value(openAIForcedAccountRoutingContextKey{}).(int64)
	if accountID <= 0 {
		return 0
	}
	return accountID
}

// ClearOpenAIForcedAccountRouting 去掉 ctx 里的 forced 账号路由。
// Key 配置了管理员隐藏链时，forced 路由让位给链（设计 6.3）：入口在确认走链之后调用它，
// 让每一跳都走标准调度，而不是被钉回同一个账号。
func ClearOpenAIForcedAccountRouting(ctx context.Context) context.Context {
	if ctx == nil || openAIForcedAccountRoutingID(ctx) == 0 {
		return ctx
	}
	return context.WithValue(ctx, openAIForcedAccountRoutingContextKey{}, int64(0))
}
